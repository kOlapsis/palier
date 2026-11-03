package main

import (
	"bytes"
	"context"
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

	notifier *notify.Notifier
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
	reg := registry.New(db, auths, cfg.RegistryMirror)
	for _, domain := range []string{"docker.io", "ghcr.io"} {
		if reg.UsesCredentials(domain) {
			slog.Info("registry credentials loaded", "registry", domain, "docker_config", cfg.DockerConfig)
		}
	}
	a := &app{}
	eng := &run.Engine{DB: db, Docker: dc, DockerHost: cfg.DockerHost, Registry: reg, Stable: time.Duration(cfg.StableSeconds) * time.Second, DataDir: cfg.DataDir}
	eng.Finished = func(stack string, id int64, status, cause string) {
		events := map[string]string{run.Done: "run_done", run.RolledBack: "run_rolled_back", run.Failed: "run_failed", run.Abandoned: "run_abandoned", run.Stopped: "run_stopped"}
		a.notify(map[string]any{"event": events[status], "stack": stack, "run": id, "status": status, "error": cause})
	}
	var senders []notify.Sender
	if cfg.SMTPHost != "" {
		var info struct{ Name string }
		dc.Get(context.Background(), "/info", &info)
		senders = append(senders, &notify.Email{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Password: cfg.SMTPPassword, From: cfg.SMTPFrom, To: cfg.EmailTo, Level: cfg.EmailLevel, Machine: info.Name})
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
	rows, err := a.db.QueryContext(ctx, `SELECT stack, service, version FROM decisions WHERE kind = 'ignore'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ignored := map[[3]string]bool{}
	for rows.Next() {
		var k [3]string
		if err := rows.Scan(&k[0], &k[1], &k[2]); err != nil {
			return nil, err
		}
		ignored[k] = true
	}
	for _, st := range stacks {
		for _, s := range st.Services {
			drop := func(r inventory.Release) bool { return ignored[[3]string{st.Name, s.Name, r.Tag}] }
			s.Available = slices.DeleteFunc(s.Available, drop)
			s.Maturing = slices.DeleteFunc(s.Maturing, drop)
		}
	}
	return stacks, rows.Err()
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
		Name, Version string
		Available     []inventory.Release
		Notes         []string
	}
	in := struct {
		Model    string
		Prompt   string
		Build    string
		AI       bool
		Files    map[string]string
		Services []svc
	}{Model: cfg.AIModel, Prompt: ai.Fingerprint, Build: build, AI: cfg.AIKey != "", Files: map[string]string{}}
	for _, f := range st.ConfigFiles {
		data, _ := os.ReadFile(f)
		in.Files[f] = string(data)
	}
	for _, s := range st.Services {
		v := svc{Name: s.Name, Version: s.Version, Available: s.Available}
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
	if !slices.ContainsFunc(st.Services, func(s *inventory.Service) bool { return len(s.Available) > 0 }) {
		_, err := a.db.ExecContext(ctx, `DELETE FROM plans WHERE stack = ?`, st.Name)
		return nil, err
	}
	hash := inputHash(st, a.cfg)
	if rec, err := a.storedPlan(ctx, st.Name); err != nil || rec != nil && rec.InputHash == hash {
		if rec != nil {
			rec.Created = time.Now().UTC()
			_, err = a.db.ExecContext(ctx, `UPDATE plans SET created = ? WHERE stack = ?`, rec.Created.Format(time.RFC3339), st.Name)
		}
		return rec, err
	}
	rec := &plan.Record{Stack: st.Name, Source: "versions", Created: time.Now().UTC(), InputHash: hash}
	var cachedSource, cachedBody, cachedAt string
	if a.db.QueryRowContext(ctx, `SELECT source, body, created FROM plan_cache WHERE input_hash = ?`, hash).Scan(&cachedSource, &cachedBody, &cachedAt) == nil {
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
	hasNotes := slices.ContainsFunc(st.Services, func(s *inventory.Service) bool { return len(s.Notes) > 0 })
	switch {
	case a.cfg.AIKey == "":
		rec.Plan = plan.Fallback(st, "No AI key: plan computed from versions only.")
	case !hasNotes:
		rec.Plan = plan.Fallback(st, "No release notes found: plan computed from versions only.")
	default:
		slog.Info("computing plan with AI", "stack", st.Name, "model", a.cfg.AIModel)
		p, err := ai.Propose(ctx, a.cfg.AIKey, a.cfg.AIModel, st)
		if err != nil {
			rec.Plan = plan.Fallback(st, "Plan computed from versions only.")
			rec.Plan.Incomplete = append(rec.Plan.Incomplete, "the AI returned no plan: "+err.Error())
			plan.Validate(rec.Plan, st)
			return rec, nil
		}
		rec.Plan, rec.Source = p, "ai"
	}
	plan.Validate(rec.Plan, st)
	body, err := json.Marshal(rec.Plan)
	if err != nil {
		return nil, err
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO plans (stack, input_hash, source, body, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (stack) DO UPDATE SET input_hash = excluded.input_hash, source = excluded.source, body = excluded.body, created = excluded.created`,
		st.Name, hash, rec.Source, string(body), rec.Created.Format(time.RFC3339))
	if err == nil {
		_, err = a.db.ExecContext(ctx, `INSERT OR IGNORE INTO plan_cache (input_hash, source, body, created) VALUES (?, ?, ?, ?)`,
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

func (a *app) decide(ctx context.Context, stack, kind, service, version, until, author string) error {
	_, err := a.db.ExecContext(ctx, `INSERT INTO decisions (stack, kind, service, version, until, author, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		stack, kind, service, version, until, author, time.Now().UTC().Format(time.RFC3339))
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

const retryAfter = 24 * time.Hour

var startedAt = time.Now()

// notRetried keeps the automatic loop from applying again, within a day and the same Palier process, the plan whose last run did not finish done.
func (a *app) notRetried(ctx context.Context, stack string, p *plan.Plan) (bool, string) {
	var id int64
	var body, status, finished string
	if a.db.QueryRowContext(ctx, `SELECT id, plan, status, finished FROM runs WHERE stack = ? AND dry_run = 0 ORDER BY id DESC LIMIT 1`, stack).Scan(&id, &body, &status, &finished) != nil {
		return true, ""
	}
	current, _ := json.Marshal(p)
	at, _ := time.Parse(time.RFC3339, finished)
	if status == run.Done || status == run.Running || body != string(current) || time.Since(at) > retryAfter || at.Before(startedAt) {
		return true, ""
	}
	return false, fmt.Sprintf("the same plan ended \"%s\" in run %d; retry on %s, or sooner if the plan changes", status, id, at.Add(retryAfter).Format("Jan 2 3:04 PM"))
}

// waitForScan returns at the next scan interval, on a rescan request, or as soon as a run started by another process has finished.
func (a *app) waitForScan(ctx context.Context, rescan <-chan struct{}, last time.Time) {
	deadline := time.After(a.cfg.ScanInterval)
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
			var finished sql.NullString
			a.db.QueryRowContext(ctx, `SELECT max(finished) FROM runs`).Scan(&finished)
			if t, err := time.Parse(time.RFC3339, finished.String); err == nil && t.After(last) {
				return
			}
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
	start := func(st *inventory.Stack, author string, dryRun bool) (int64, error) {
		id, p, err := a.begin(ctx, st, author, dryRun)
		if err != nil {
			return 0, err
		}
		go func() {
			a.eng.Execute(ctx, id, st, p, dryRun)
			select {
			case rescan <- struct{}{}:
			default:
			}
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
			began := time.Now()
			stacks, err := a.scan(ctx, "")
			next := web.Snapshot{Stacks: stacks, Began: began, At: time.Now()}
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
				if rec == nil || a.cfg.Auto == "off" {
					continue
				}
				ok, why := plan.Automatic(rec.Plan, a.cfg.Auto)
				if until := a.postponed(ctx, st.Name); ok && time.Now().Before(until) {
					ok, why = false, "postponed until "+until.Format("2006-01-02")
				}
				if ok {
					ok, why = a.notRetried(ctx, st.Name, rec.Plan)
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
				if fresh, err := a.scan(ctx, ""); err == nil {
					mu.Lock()
					snap = web.Snapshot{Stacks: fresh, Began: rescanned, At: time.Now()}
					mu.Unlock()
					for _, now := range fresh {
						if now.Name == st.Name && !now.Observed {
							if _, err := a.planFor(ctx, now); err != nil {
								slog.Error("plan", "stack", now.Name, "err", err)
							}
						}
					}
					a.sync(ctx, current())
				}
			}
			a.sync(ctx, current())
			a.waitForScan(ctx, rescan, next.At)
			if ctx.Err() != nil {
				return
			}
		}
	}()

	deps := web.Deps{
		Current: current,
		Plan:    func(stack string) (*plan.Record, error) { return a.storedPlan(ctx, stack) },
		Apply: func(stack, author string, dryRun bool) (int64, error) {
			st, err := a.stack(ctx, stack)
			if err != nil {
				return 0, err
			}
			return start(st, author, dryRun)
		},
		Decide: func(stack, author, kind, service, version, until string) error {
			if err := a.decide(ctx, stack, kind, service, version, until, author); err != nil {
				return err
			}
			select {
			case rescan <- struct{}{}:
			default:
			}
			return nil
		},
		Postponed: func(stack string) time.Time { return a.postponed(ctx, stack) },
		Automatic: func(stack string, p *plan.Plan) (bool, string) {
			ok, why := plan.Automatic(p, a.cfg.Auto)
			if until := a.postponed(ctx, stack); ok && time.Now().Before(until) {
				ok, why = false, "postponed until "+until.Format("2006-01-02")
			}
			if ok {
				ok, why = a.notRetried(ctx, stack, p)
			}
			return ok, why
		},
		Run:  func(id int64) (*run.View, error) { return run.Load(ctx, a.db, id) },
		Runs: func(stack string) ([]run.View, error) { return run.List(ctx, a.db, stack, 20) },
		Rescan: func() {
			select {
			case rescan <- struct{}{}:
			default:
			}
		},
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
	slog.Info("palier started", "socket", sock, "web", a.cfg.Listen, "data_dir", a.cfg.DataDir, "ai", a.cfg.AIKey != "", "auto", a.cfg.Auto)
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
