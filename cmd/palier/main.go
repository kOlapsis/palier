// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kolapsis/palier/internal/ai"
	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/notify"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/registry"
	"github.com/kolapsis/palier/internal/run"
	"github.com/kolapsis/palier/internal/store"
	"github.com/kolapsis/palier/internal/version"
	"github.com/kolapsis/palier/internal/web"
)

func main() {
	args := os.Args[1:]
	cmd := "status"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	logOut := os.Stderr
	if cmd == "serve" {
		logOut = os.Stdout
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(logOut, nil)))
	var flags []string
	var pos []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			pos = append(pos, a)
		}
	}
	has := func(names ...string) bool {
		return slices.ContainsFunc(names, func(n string) bool { return slices.Contains(flags, n) })
	}
	known := func(names ...string) bool {
		return !slices.ContainsFunc(flags, func(f string) bool { return !slices.Contains(names, f) })
	}
	var err error
	switch {
	case cmd == "serve" && len(args) == 0:
		if err = serve(); err != nil {
			slog.Error("fatal error", "err", err)
			os.Exit(1)
		}
		return
	case cmd == "help" || cmd == "-h" || cmd == "--help":
		printUsage(os.Stdout)
		return
	case cmd == "scan" && has("--once") && known("--once", "--json") && len(pos) == 0:
		err = withApp(func(ctx context.Context, a *app) error {
			stacks, err := a.scan(ctx, "")
			if err != nil {
				return err
			}
			return printJSON(stacks)
		})
	case cmd == "plan" && has("--once") && known("--once", "--json") && len(pos) == 1:
		err = withApp(func(ctx context.Context, a *app) error {
			st, err := a.stack(ctx, pos[0])
			if err != nil {
				return err
			}
			rec, err := a.planFor(ctx, st)
			if err != nil {
				return err
			}
			return printJSON(rec)
		})
	default:
		var cfg config.Config
		if cfg, err = config.Load(os.Getenv); err != nil {
			break
		}
		c := newCLI(cfg, has("--json"))
		switch {
		case cmd == "apply" && len(pos) == 1 && known("--json", "--dry-run", "--yes", "-y", "--detach"):
			err = c.apply(pos[0], has("--dry-run"), has("--yes", "-y"), has("--detach"))
		case cmd == "log" && len(pos) == 1 && known("--json", "-f", "--follow"):
			err = c.log(pos[0], has("-f", "--follow"))
		case known("--json"):
			err = c.run(cmd, pos)
		default:
			err = errUsage
		}
	}
	switch {
	case errors.Is(err, errUsage):
		printUsage(os.Stderr)
		os.Exit(2)
	case errors.Is(err, errSilent):
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "palier: "+err.Error())
		os.Exit(1)
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

type app struct {
	cfg config.Config
	db  *sql.DB
	dc  *docker.Client
	reg *registry.Client
	nc  *notes.Client
	eng *run.Engine

	notifier  *notify.Notifier
	analyzing sync.Map
}

func setup() (*app, error) {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	dc, err := docker.New(cfg.DockerHost)
	if err != nil {
		db.Close()
		return nil, err
	}
	auths, err := registry.LoadAuths(cfg.DockerConfig)
	if err != nil {
		db.Close()
		return nil, err
	}
	reg := registry.New(db, auths, cfg.RegistryMirror, cfg.GitHubToken)
	if auths != nil {
		for host, h := range auths.Helpers {
			slog.Info("credential helper", "registry", host, "helper", "docker-credential-"+h)
		}
		if auths.Store != "" {
			slog.Info("credential store", "helper", "docker-credential-"+auths.Store)
		}
	}
	for _, domain := range []string{"docker.io", "ghcr.io"} {
		if reg.UsesCredentials(domain) {
			slog.Info("registry credentials loaded", "registry", domain, "docker_config", cfg.DockerConfig)
		}
	}
	a := &app{}
	eng := &run.Engine{DB: db, Docker: dc, DockerHost: cfg.DockerHost, Registry: reg, Stable: time.Duration(cfg.StableSeconds) * time.Second, DataDir: cfg.DataDir,
		BackupTo: cfg.BackupTo, BackupKeep: cfg.BackupKeep}
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, "RCLONE_") {
			eng.Rclone = append(eng.Rclone, v)
		}
	}
	eng.Finished = func(stack string, id int64, status, cause string) {
		events := map[string]string{run.Done: "run_done", run.RolledBack: "run_rolled_back", run.Failed: "run_failed", run.Abandoned: "run_abandoned", run.Stopped: "run_stopped"}
		a.notify(map[string]any{"event": events[status], "stack": stack, "run": id, "status": status, "error": cause})
	}
	eng.BackedUp = func(stack string, id int64, b run.Archive) {
		event := map[string]any{"event": "backup_done", "stack": stack, "run": id, "service": b.Service, "kind": b.Kind, "engine": b.Engine, "databases": b.Databases,
			"location": b.Location, "size": b.Size, "sha256": b.SHA256, "seconds": b.Seconds}
		if b.Error != "" {
			event = map[string]any{"event": "backup_failed", "stack": stack, "run": id, "service": b.Service, "kind": b.Kind, "location": b.Location, "error": b.Error}
		}
		a.notify(event)
	}
	var senders []notify.Sender
	var info struct{ Name string }
	if cfg.SMTPHost != "" || len(cfg.Chats) > 0 {
		dc.Get(context.Background(), "/info", &info)
	}
	if cfg.SMTPHost != "" {
		senders = append(senders, &notify.Email{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Password: cfg.SMTPPassword, From: cfg.SMTPFrom, To: cfg.EmailTo, Level: cfg.EmailLevel, Machine: info.Name})
	}
	for _, c := range cfg.Chats {
		c.Machine = info.Name
		senders = append(senders, &c)
	}
	*a = app{cfg: cfg, db: db, dc: dc, reg: reg, nc: notes.New(db, cfg.GitHubToken, cfg.GitLabToken), eng: eng, notifier: notify.New(db, senders, cfg.PublicURL)}
	return a, nil
}

func withApp(f func(context.Context, *app) error) error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return f(ctx, a)
}

// scan reads every stack, or only the Compose stack only when it is set, and finds their newer versions.
func (a *app) scan(ctx context.Context, only string) ([]*inventory.Stack, error) {
	stacks, err := inventory.Scan(ctx, a.dc)
	if err != nil {
		return nil, err
	}
	if only != "" {
		stacks = slices.DeleteFunc(stacks, func(st *inventory.Stack) bool { return st.Name != only || st.Observed })
	}
	inventory.Detect(ctx, stacks, a.reg, a.nc, a.cfg.Maturation)
	for _, st := range stacks {
		st.Scanned = time.Now()
	}
	rows, err := a.db.QueryContext(ctx, `SELECT kind, stack, service, version FROM decisions WHERE kind IN ('ignore', 'accept', 'via') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	decided := map[[4]string]bool{}
	var ignored [][4]string
	for rows.Next() {
		var k [4]string
		if err := rows.Scan(&k[0], &k[1], &k[2], &k[3]); err != nil {
			return nil, err
		}
		if k[0] == "ignore" && !decided[k] {
			ignored = append(ignored, k)
		}
		decided[k] = true
	}
	for _, st := range stacks {
		for _, s := range st.Services {
			drop := func(r inventory.Release) bool { return decided[[4]string{"ignore", st.Name, s.Name, r.Tag}] }
			s.Available = slices.DeleteFunc(s.Available, drop)
			s.Maturing = slices.DeleteFunc(s.Maturing, drop)
			s.Available, s.Maturing = accept(s.Available, s.Maturing, func(tag string) bool { return decided[[4]string{"accept", st.Name, s.Name, tag}] })
			for _, k := range ignored {
				if k[1] == st.Name && k[2] == s.Name {
					s.Ignored = append(s.Ignored, k[3])
				}
			}
			for _, r := range s.Available {
				if decided[[4]string{"via", st.Name, s.Name, r.Tag}] {
					s.Via = append(s.Via, r.Tag)
				}
			}
		}
	}
	return stacks, rows.Err()
}

// accept moves to available the maturing versions that accepted names, and keeps available in ascending order.
func accept(available, maturing []inventory.Release, accepted func(tag string) bool) ([]inventory.Release, []inventory.Release) {
	var waiting []inventory.Release
	for _, r := range maturing {
		if accepted(r.Tag) {
			available = append(available, r)
		} else {
			waiting = append(waiting, r)
		}
	}
	slices.SortStableFunc(available, func(a, b inventory.Release) int {
		v, _ := version.Parse(a.Tag)
		w, _ := version.Parse(b.Tag)
		switch {
		case v.Less(w):
			return -1
		case w.Less(v):
			return 1
		}
		return 0
	})
	return available, waiting
}

// merge returns old with the stacks of names replaced by their fresh scan: a Compose stack that is gone leaves the list.
func merge(old []*inventory.Stack, names []string, fresh []*inventory.Stack) []*inventory.Stack {
	out := slices.Clone(fresh)
	for _, st := range old {
		if !slices.Contains(names, st.Name) || st.Observed {
			out = append(out, st)
		}
	}
	slices.SortFunc(out, func(a, b *inventory.Stack) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func (a *app) stack(ctx context.Context, name string) (*inventory.Stack, error) {
	stacks, err := a.scan(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(stacks) == 0 {
		return nil, fmt.Errorf("no Compose stack %q", name)
	}
	return stacks[0], nil
}

// build identifies this binary, so that a new version of the rules recomputes the stored plans.
var build = func() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}()

func inputHash(st *inventory.Stack, cfg config.Config) string {
	type svc struct {
		Name, Version, Role string
		DependsOn           []string
		Available           []inventory.Release
		Via                 []string
		Notes               []string
	}
	in := struct {
		Provider string
		URL      string
		Model    string
		Prompt   string
		Build    string
		AI       bool
		Files    map[string]string
		Services []svc
	}{Provider: cfg.AIProvider, URL: cfg.AIURL, Model: cfg.AIModel, Prompt: ai.Fingerprint, Build: build, AI: cfg.AI, Files: map[string]string{}}
	for _, f := range st.ConfigFiles {
		data, _ := os.ReadFile(f)
		in.Files[f] = string(data)
	}
	for _, s := range st.Services {
		v := svc{Name: s.Name, Version: s.Version, Role: s.Role, DependsOn: s.DependsOn, Available: s.Available, Via: s.Via}
		for _, n := range s.Notes {
			v.Notes = append(v.Notes, n.Tag+"\x00"+n.Body)
		}
		in.Services = append(in.Services, v)
	}
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (a *app) storedPlan(ctx context.Context, stack string) (*plan.Record, error) {
	rec := &plan.Record{Stack: stack}
	var body, created string
	err := a.db.QueryRowContext(ctx, `SELECT input_hash, source, body, created FROM plans WHERE stack = ?`, stack).Scan(&rec.InputHash, &rec.Source, &body, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec.Created, _ = time.Parse(time.RFC3339, created)
	return rec, json.Unmarshal([]byte(body), &rec.Plan)
}

// planFor returns the plan of st, computed again only when what it depends on changed.
func (a *app) planFor(ctx context.Context, st *inventory.Stack) (*plan.Record, error) {
	began := time.Now()
	if !slices.ContainsFunc(st.Services, func(s *inventory.Service) bool { return len(s.Available) > 0 }) {
		_, err := a.db.ExecContext(ctx, `DELETE FROM plans WHERE stack = ?`, st.Name)
		return nil, err
	}
	hash := inputHash(st, a.cfg)
	stored, err := a.storedPlan(ctx, st.Name)
	if err != nil {
		return nil, err
	}
	var failed *plan.Plan
	var blamed []int
	why := ""
	if stored != nil && stored.InputHash == hash {
		step, cause, twice := a.rolledBackTwice(ctx, stored)
		if !twice {
			stored.Created = time.Now().UTC()
			_, err = a.db.ExecContext(ctx, `UPDATE plans SET created = ? WHERE stack = ?`, stored.Created.Format(time.RFC3339), st.Name)
			return stored, err
		}
		failed, blamed, why = stored.Plan, []int{step}, cause
	}
	rec := &plan.Record{Stack: st.Name, Source: "versions", Created: time.Now().UTC(), InputHash: hash}
	var cachedSource, cachedBody, cachedAt string
	if failed == nil && a.db.QueryRowContext(ctx, `SELECT source, body, created FROM plan_cache WHERE input_hash = ?`, hash).Scan(&cachedSource, &cachedBody, &cachedAt) == nil {
		rec.Source = cachedSource
		cachedAt = rec.Created.Format(time.RFC3339)
		if err := json.Unmarshal([]byte(cachedBody), &rec.Plan); err != nil {
			return nil, err
		}
		_, err := a.db.ExecContext(ctx, `INSERT INTO plans (stack, input_hash, source, body, created) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (stack) DO UPDATE SET input_hash = excluded.input_hash, source = excluded.source, body = excluded.body, created = excluded.created`,
			st.Name, hash, rec.Source, cachedBody, cachedAt)
		return rec, err
	}
	check := func(p *plan.Plan) {
		before := len(p.Steps)
		plan.SecurityFirst(p, st)
		if added := len(p.Steps) - before; added > 0 {
			slog.Info("security fix first", "stack", st.Name, "steps added", added, "first", p.Steps[0].Why)
		}
		plan.Validate(p, st)
	}
	hasNotes := slices.ContainsFunc(st.Services, func(s *inventory.Service) bool { return len(s.Notes) > 0 })
	switch {
	case !a.cfg.AI:
		rec.Plan = plan.Fallback(st, "No AI key: plan computed from versions only.")
		check(rec.Plan)
	case !hasNotes:
		rec.Plan = plan.Fallback(st, "No release notes found: plan computed from versions only.")
		check(rec.Plan)
	default:
		slog.Info("computing plan with AI", "stack", st.Name, "provider", a.cfg.AIProvider, "model", a.cfg.AIModel)
		a.analyzing.Store(st.Name, true)
		defer a.analyzing.Delete(st.Name)
		model := ai.Endpoint{Provider: a.cfg.AIProvider, URL: a.cfg.AIURL, Key: a.cfg.AIKey, Model: a.cfg.AIModel, Timeout: a.cfg.AITimeout}
		digests, err := a.digests(ctx, model, st)
		propose := func(full []notes.Note, why string) (*plan.Plan, error) {
			p, err := ai.Propose(ctx, model, st, digests, full, why)
			if err != nil {
				return nil, err
			}
			for _, d := range digests {
				p.Suspicious = append(p.Suspicious, d.Passages...)
			}
			check(p)
			return p, nil
		}
		p := failed
		if err == nil && p == nil {
			if p, err = propose(nil, ""); err == nil {
				blamed, why = plan.Blamed(p), strings.Join(p.Incomplete, "; ")
			}
		}
		if err != nil {
			rec.Plan = plan.Fallback(st, "Plan computed from versions only.")
			rec.Plan.Incomplete = append(rec.Plan.Incomplete, aiFailure(err))
			rec.InputHash = ""
			check(rec.Plan)
			break
		}
		if full := ai.Suspects(st, model, digests, p, blamed); why != "" && len(full) > 0 {
			slog.Info("computing plan again with full notes", "stack", st.Name, "notes", len(full), "why", why)
			if again, err := propose(full, why); err != nil {
				slog.Warn("plan not computed again", "stack", st.Name, "error", err)
			} else {
				again.Refined = refined(full, why)
				p = again
			}
		}
		rec.Plan, rec.Source = p, "ai"
	}
	slog.Info("plan computed", "stack", st.Name, "source", rec.Source, "steps", len(rec.Plan.Steps), "incomplete", len(rec.Plan.Incomplete), "duration", time.Since(began).Round(time.Second).String())
	body, err := json.Marshal(rec.Plan)
	if err != nil {
		return nil, err
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO plans (stack, input_hash, source, body, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (stack) DO UPDATE SET input_hash = excluded.input_hash, source = excluded.source, body = excluded.body, created = excluded.created`,
		st.Name, rec.InputHash, rec.Source, string(body), rec.Created.Format(time.RFC3339))
	if err == nil && rec.InputHash != "" {
		_, err = a.db.ExecContext(ctx, `INSERT OR REPLACE INTO plan_cache (input_hash, source, body, created) VALUES (?, ?, ?, ?)`,
			hash, rec.Source, string(body), rec.Created.Format(time.RFC3339))
	}
	if err == nil && len(rec.Plan.Steps) > 0 && len(rec.Plan.Incomplete) == 0 {
		var changes []string
		for _, s := range rec.Plan.Steps {
			for _, c := range s.Changes {
				changes = append(changes, c.Service+" "+c.From+" → "+c.To)
			}
		}
		a.notify(map[string]any{"event": "plan_ready", "stack": st.Name, "steps": len(rec.Plan.Steps), "changes": changes})
	}
	return rec, err
}

// refined says which notes a plan computed again read in full, and why.
func refined(full []notes.Note, why string) string {
	var tags []string
	for _, n := range full {
		tags = append(tags, cmp.Or(n.Tag, "documentation"))
	}
	if len(tags) > 8 {
		tags = append(tags[:8], fmt.Sprintf("and %d more", len(tags)-8))
	}
	if r := []rune(why); len(r) > 300 {
		why = string(r[:300]) + "…"
	}
	return fmt.Sprintf("Computed again with the full notes of %s, because a first plan did not hold: %s", strings.Join(tags, ", "), why)
}

// rolledBackTwice reports whether the last two runs of the plan of rec both ended rolled back or failed, with the step
// that did not pass and what Palier saw of it, never the logs.
func (a *app) rolledBackTwice(ctx context.Context, rec *plan.Record) (int, string, bool) {
	if rec.Source != "ai" || rec.Plan.Refined != "" {
		return 0, "", false
	}
	current, _ := json.Marshal(rec.Plan)
	rows, err := a.db.QueryContext(ctx, `SELECT plan, status, checkpoint, error FROM runs WHERE stack = ? AND dry_run = 0 ORDER BY id DESC LIMIT 2`, rec.Stack)
	if err != nil {
		return 0, "", false
	}
	defer rows.Close()
	runs, step, seen := 0, 0, ""
	for rows.Next() {
		var body, status, checkpoint, msg string
		if rows.Scan(&body, &status, &checkpoint, &msg) != nil || body != string(current) || status != run.RolledBack && status != run.Failed {
			return 0, "", false
		}
		if runs == 0 {
			var cp run.Checkpoint
			json.Unmarshal([]byte(checkpoint), &cp)
			step = cp.Step
			if before, _, found := strings.Cut(msg, "; last log lines"); found {
				seen = ": " + before
			}
		}
		runs++
	}
	if runs < 2 || step >= len(rec.Plan.Steps) {
		return 0, "", false
	}
	return step, fmt.Sprintf("the run of this plan was rolled back twice at step %d%s", step+1, seen), true
}

// digests returns the digest of every note of st, read by the model only for the notes it has not read yet.
func (a *app) digests(ctx context.Context, model ai.Endpoint, st *inventory.Stack) (map[string]ai.Digest, error) {
	out := map[string]ai.Digest{}
	var todo []notes.Note
	unique, _ := ai.Unique(st)
	for _, n := range unique {
		var body string
		var d ai.Digest
		if a.db.QueryRowContext(ctx, `SELECT body FROM digests WHERE key = ?`, ai.DigestKey(model, n)).Scan(&body) == nil && json.Unmarshal([]byte(body), &d) == nil {
			out[ai.DigestKey(model, n)] = d
		} else {
			todo = append(todo, n)
		}
	}
	if len(todo) > 0 {
		slog.Info("reading release notes", "stack", st.Name, "new", len(todo), "known", len(out))
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var failed error
	slots := make(chan struct{}, 4)
	for _, n := range todo {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			began := time.Now()
			d, err := ai.Read(ctx, model, n)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				slog.Warn("release note not read", "stack", st.Name, "note", n.URL, "duration", time.Since(began).Round(time.Second).String(), "error", err)
				failed = cmp.Or(failed, err)
				return
			}
			slog.Info("release note read", "stack", st.Name, "note", n.URL, "facts", len(d.Facts), "duration", time.Since(began).Round(time.Second).String())
			out[ai.DigestKey(model, n)] = d
			body, _ := json.Marshal(d)
			if _, err := a.db.ExecContext(ctx, `INSERT OR REPLACE INTO digests (key, body, created) VALUES (?, ?, ?)`, ai.DigestKey(model, n), string(body), time.Now().UTC().Format(time.RFC3339)); err != nil {
				failed = cmp.Or(failed, err)
			}
		})
	}
	wg.Wait()
	return out, failed
}

// aiFailure says why the AI gave no plan and what to do about it.
func aiFailure(err error) string {
	msg := err.Error()
	todo := "Palier asks again at the next scan, and palier scan asks now"
	switch {
	case strings.Contains(msg, " 401 ") || strings.Contains(msg, " 403 ") || strings.Contains(msg, "authentication_error") || strings.Contains(msg, "permission_error"):
		todo = "check PALIER_AI_KEY"
	case strings.Contains(msg, " 404 ") || strings.Contains(msg, "not_found_error"):
		todo = "check PALIER_AI_MODEL and PALIER_AI_URL"
	case strings.Contains(msg, "too long") || strings.Contains(msg, "context length") || strings.Contains(msg, "context window"):
		todo = "a request does not fit in the context of this model, most often one very long release note read whole; raise the context of the model or set PALIER_AI_MODEL to one with a larger context"
	}
	return fmt.Sprintf("the AI returned no plan, %s. Its answer: %s", todo, msg)
}

func (a *app) decide(ctx context.Context, stack, kind, service, version, until, author string) error {
	_, err := a.db.ExecContext(ctx, `INSERT INTO decisions (stack, kind, service, version, until, author, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		stack, kind, service, version, until, author, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (a *app) forget(ctx context.Context, stack, service, version string) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM decisions WHERE stack = ? AND service = ? AND version = ? AND kind IN ('ignore', 'accept', 'via')`, stack, service, version)
	return err
}

func (a *app) postponed(ctx context.Context, stack string) time.Time {
	var until sql.NullString
	a.db.QueryRowContext(ctx, `SELECT max(until) FROM decisions WHERE kind = 'postpone' AND stack = ?`, stack).Scan(&until)
	t, _ := time.Parse("2006-01-02", until.String)
	return t
}

// begin records the decision and a new run of the stored plan of st.
func (a *app) begin(ctx context.Context, st *inventory.Stack, author string, dryRun bool) (int64, *plan.Plan, error) {
	rec, err := a.planFor(ctx, st)
	if err != nil {
		return 0, nil, err
	}
	if rec == nil || len(rec.Plan.Steps) == 0 {
		return 0, nil, fmt.Errorf("%s is up to date", st.Name)
	}
	if len(rec.Plan.Incomplete) > 0 {
		return 0, nil, fmt.Errorf("the plan for %s is incomplete and cannot be applied", st.Name)
	}
	kind := "apply"
	if dryRun {
		kind = "dry_run"
	}
	if err := a.decide(ctx, st.Name, kind, "", "", "", author); err != nil {
		return 0, nil, err
	}
	id, err := a.eng.Begin(ctx, st.Name, rec.Plan, author, dryRun)
	return id, rec.Plan, err
}

const (
	retryAfter = 24 * time.Hour
	strikes    = 3
)

// notRetried keeps the automatic loop from applying again on a stack whose last run did not pass.
func (a *app) notRetried(ctx context.Context, stack string) (bool, string) {
	rows, err := a.db.QueryContext(ctx, `SELECT id, status, finished FROM runs WHERE stack = ? AND dry_run = 0 ORDER BY id DESC LIMIT ?`, stack, strikes)
	if err != nil {
		return true, ""
	}
	defer rows.Close()
	var last int64
	var finished string
	var statuses []string
	for rows.Next() {
		var id int64
		var status, at string
		if rows.Scan(&id, &status, &at) != nil {
			break
		}
		if len(statuses) == 0 {
			last, finished = id, at
		}
		statuses = append(statuses, status)
	}
	at, _ := time.Parse(time.RFC3339, finished)
	return retry(last, statuses, at, time.Now())
}

// retry decides from the last runs of a stack, newest first: a day of wait after a run that did not pass, whatever the
// plan has become, and a full stop after a failed rollback or three rollbacks in a row.
func retry(last int64, statuses []string, at, now time.Time) (bool, string) {
	if len(statuses) == 0 || statuses[0] == run.Done || statuses[0] == run.Running {
		return true, ""
	}
	rolledBack := 0
	for _, s := range statuses {
		if s != run.RolledBack && s != run.Failed {
			break
		}
		rolledBack++
	}
	switch {
	case statuses[0] == run.Failed:
		return false, fmt.Sprintf("run %d failed and its rollback did not pass: Palier applies nothing on this stack on its own until a run passes", last)
	case rolledBack >= strikes:
		return false, fmt.Sprintf("the last %d runs were rolled back: Palier applies nothing on this stack on its own until a run passes", rolledBack)
	case now.Sub(at) <= retryAfter:
		return false, fmt.Sprintf("run %d ended \"%s\"; next automatic try on %s", last, statuses[0], at.Add(retryAfter).Format("Jan 2 3:04 PM"))
	}
	return true, ""
}

// waitForScan returns at the next scan interval or on a rescan request, and asks for one for the stack of each run finished since, which another process may have started.
func (a *app) waitForScan(ctx context.Context, rescan <-chan struct{}, last, since time.Time, ask func(string)) {
	deadline := time.After(time.Until(last.Add(a.cfg.ScanInterval)))
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rescan:
			return
		case <-deadline:
			return
		case <-tick.C:
			rows, err := a.db.QueryContext(ctx, `SELECT DISTINCT stack FROM runs WHERE finished > ? AND author != 'auto'`, since.UTC().Format(time.RFC3339))
			if err != nil {
				continue
			}
			for rows.Next() {
				var stack string
				if rows.Scan(&stack) == nil {
					ask(stack)
				}
			}
			rows.Close()
		}
	}
}

// notify posts an event to PALIER_WEBHOOK_URL when it is set.
func (a *app) notify(event map[string]any) {
	if a.cfg.WebhookURL == "" {
		return
	}
	event["at"] = time.Now().UTC().Format(time.RFC3339)
	body, _ := json.Marshal(event)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		slog.Error("webhook", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.WebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(a.cfg.WebhookSecret))
		mac.Write(body)
		req.Header.Set("X-Palier-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("webhook", "event", event["event"], "err", err)
		return
	}
	resp.Body.Close()
	slog.Info("webhook sent", "event", event["event"], "stack", event["stack"], "status", resp.StatusCode)
}

func serve() error {
	a, err := setup()
	if err != nil {
		return err
	}
	defer a.db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.eng.Recover(ctx); err != nil {
		return err
	}
	report := func(since, until time.Time) notify.Message { return a.report(ctx, since, until) }
	go a.notifier.Run(ctx, report)

	var mu sync.Mutex
	var snap web.Snapshot
	current := func() web.Snapshot {
		mu.Lock()
		defer mu.Unlock()
		return snap
	}
	rescan := make(chan struct{}, 1)
	var all bool
	var only []string
	ask := func(stack string) {
		mu.Lock()
		if stack == "" {
			all = true
		} else if !slices.Contains(only, stack) {
			only = append(only, stack)
		}
		mu.Unlock()
		select {
		case rescan <- struct{}{}:
		default:
		}
	}
	var scanning atomic.Bool
	start := func(st *inventory.Stack, author string, dryRun bool) (int64, error) {
		id, p, err := a.begin(ctx, st, author, dryRun)
		if err != nil {
			return 0, err
		}
		go func() {
			a.eng.Execute(ctx, id, st, p, dryRun)
			ask(st.Name)
		}()
		return id, nil
	}
	go func() {
		if stacks, err := inventory.Scan(ctx, a.dc); err == nil {
			mu.Lock()
			snap = web.Snapshot{Stacks: stacks}
			mu.Unlock()
		}
		for {
			mu.Lock()
			names := only
			if all {
				names = nil
			}
			all, only = false, nil
			mu.Unlock()
			began := time.Now()
			scanning.Store(true)
			slog.Info("scan started", "stacks", cmp.Or(strings.Join(names, ", "), "all"))
			var stacks []*inventory.Stack
			var err error
			for _, name := range names {
				one, err := a.scan(ctx, name)
				if err != nil {
					names = nil
					break
				}
				stacks = append(stacks, one...)
			}
			if names == nil {
				stacks, err = a.scan(ctx, "")
			}
			scanning.Store(false)
			next := web.Snapshot{Stacks: stacks, Began: began, At: time.Now()}
			if names != nil {
				prev := current()
				next = web.Snapshot{Stacks: merge(prev.Stacks, names, stacks), Began: began, At: prev.At, Err: prev.Err}
			}
			if err != nil {
				next.Err = err.Error()
				slog.Error("scan", "err", err)
			} else {
				slog.Info("scan finished", "stacks", len(stacks), "duration", time.Since(began).Round(time.Millisecond).String())
			}
			mu.Lock()
			snap = next
			mu.Unlock()
			for _, st := range stacks {
				if st.Observed {
					continue
				}
				rec, err := a.planFor(ctx, st)
				if err != nil {
					slog.Error("plan", "stack", st.Name, "err", err)
				}
				if rec == nil {
					continue
				}
				ok, why := plan.Automatic(rec.Plan, a.cfg.Auto)
				if until := a.postponed(ctx, st.Name); ok && time.Now().Before(until) {
					ok, why = false, "postponed until "+until.Format("2006-01-02")
				}
				if ok {
					ok, why = a.notRetried(ctx, st.Name)
				}
				if !ok {
					slog.Info("automatic apply skipped", "stack", st.Name, "auto", a.cfg.Auto, "reason", why)
					continue
				}
				id, p, err := a.begin(ctx, st, "auto", false)
				if err != nil {
					slog.Error("automatic apply", "stack", st.Name, "err", err)
					continue
				}
				slog.Info("automatic apply", "stack", st.Name, "run", id)
				a.eng.Execute(ctx, id, st, p, false)
				rescanned := time.Now()
				scanning.Store(true)
				fresh, err := a.scan(ctx, st.Name)
				scanning.Store(false)
				if err == nil {
					mu.Lock()
					snap = web.Snapshot{Stacks: merge(snap.Stacks, []string{st.Name}, fresh), Began: rescanned, At: snap.At, Err: snap.Err}
					mu.Unlock()
					for _, now := range fresh {
						if _, err := a.planFor(ctx, now); err != nil {
							slog.Error("plan", "stack", now.Name, "err", err)
						}
					}
					a.sync(ctx, current())
				}
			}
			a.sync(ctx, current())
			a.waitForScan(ctx, rescan, next.At, began, ask)
			if ctx.Err() != nil {
				return
			}
		}
	}()

	deps := web.Deps{
		Current: current,
		Plan:    func(stack string) (*plan.Record, error) { return a.storedPlan(ctx, stack) },
		Apply: func(stack, author string, dryRun bool) (int64, error) {
			slog.Info("apply requested", "stack", stack, "author", author, "dry_run", dryRun)
			st, err := a.stack(ctx, stack)
			if err != nil {
				return 0, err
			}
			return start(st, author, dryRun)
		},
		Decide: func(stack, author, kind, service, version, until string) error {
			slog.Info("decision", "stack", stack, "author", author, "kind", kind, "service", service, "version", version, "until", until)
			if err := a.decide(ctx, stack, kind, service, version, until, author); err != nil {
				return err
			}
			ask(stack)
			return nil
		},
		Forget: func(stack, author, service, version string) error {
			slog.Info("decisions removed", "stack", stack, "author", author, "service", service, "version", version)
			if err := a.forget(ctx, stack, service, version); err != nil {
				return err
			}
			ask(stack)
			return nil
		},
		Postponed: func(stack string) time.Time { return a.postponed(ctx, stack) },
		Automatic: func(stack string, p *plan.Plan) (bool, string) {
			ok, why := plan.Automatic(p, a.cfg.Auto)
			if until := a.postponed(ctx, stack); ok && time.Now().Before(until) {
				ok, why = false, "postponed until "+until.Format("2006-01-02")
			}
			if ok {
				ok, why = a.notRetried(ctx, stack)
			}
			return ok, why
		},
		Run:      func(id int64) (*run.View, error) { return run.Load(ctx, a.db, id) },
		Runs:     func(stack string) ([]run.View, error) { return run.List(ctx, a.db, stack, 20) },
		Backups:  func(stack string) ([]run.Archive, error) { return run.LastBackups(ctx, a.db, stack) },
		Scanning: scanning.Load,
		Analyzing: func(stack string) bool {
			_, ok := a.analyzing.Load(stack)
			return ok
		},
		Rescan: ask,
		Incidents: func() []notify.Incident {
			list, err := a.notifier.Incidents(ctx, true)
			if err != nil {
				slog.Error("incidents", "err", err)
			}
			return list
		},
		Channels:     func() []notify.Channel { return a.notifier.Channels(ctx) },
		NotifyTest:   func() error { return a.notifier.Test(ctx) },
		NotifyReport: func() (bool, error) { return a.notifier.SendReport(ctx, report, true) },
		Config:       a.cfg,
	}
	sock := socketPath(a.cfg)
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	servers := []*http.Server{{Handler: web.API(deps), ReadHeaderTimeout: 10 * time.Second}}
	errc := make(chan error, 2)
	go func() { errc <- servers[0].Serve(ln) }()
	if a.cfg.Listen != "" {
		srv := &http.Server{Addr: a.cfg.Listen, Handler: web.New(deps), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv)
		go func() { errc <- srv.ListenAndServe() }()
	}
	slog.Info("palier started", "socket", sock, "web", a.cfg.Listen, "data_dir", a.cfg.DataDir, "ai", a.cfg.AI, "auto", a.cfg.Auto)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutdown requested")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

func socketPath(cfg config.Config) string { return filepath.Join(cfg.DataDir, "palier.sock") }
