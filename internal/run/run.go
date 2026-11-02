// Package run applies a plan to a stack with a checkpoint, verifies it, and rolls the whole stack back on failure.
package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kolapsis/palier/internal/catalog"
	"github.com/kolapsis/palier/internal/compose"
	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/registry"
)

// Statuses of a run.
const (
	Running    = "running"
	Done       = "done"
	RolledBack = "rolled_back"
	Failed     = "failed"
	Abandoned  = "abandoned"
)

var results = map[string]string{Done: "completed", RolledBack: "rolled back", Failed: "failed", Abandoned: "abandoned"}

// Engine applies plans on the local engine.
type Engine struct {
	DB         *sql.DB
	Docker     *docker.Client
	DockerHost string
	Registry   *registry.Client
	Stable     time.Duration
	DataDir    string
	Finished   func(stack string, id int64, status, cause string)
}

// Checkpoint is the state of a stack before its first modification, then after each verified step.
type Checkpoint struct {
	Step           int                 `json:"step"`
	Project        compose.Project     `json:"project"`
	Files          []compose.FileState `json:"files"`
	Services       []Saved             `json:"services"`
	Snapshots      []Snapshot          `json:"snapshots"`
	SnapshotsDone  bool                `json:"snapshots_done"`
	CreatedVolumes []string            `json:"created_volumes"`
}

// Saved is a service as it ran before the run.
type Saved struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Tag     string            `json:"tag"`
	ImageID string            `json:"image_id"`
	Digest  string            `json:"digest"`
	Mounts  []inventory.Mount `json:"mounts"`
}

// ErrBusy means a run is already in progress on the stack.
var ErrBusy = errors.New("a run is already in progress on this stack")

// Begin records a new run of p on stack.
func (e *Engine) Begin(ctx context.Context, stack string, p *plan.Plan, author string, dryRun bool) (int64, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	res, err := e.DB.ExecContext(ctx, `INSERT INTO runs (stack, status, dry_run, author, plan, started)
		SELECT ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM runs WHERE stack = ? AND status = ?)`,
		stack, Running, dryRun, author, string(body), now(), stack, Running)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrBusy
	}
	return res.LastInsertId()
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (e *Engine) event(id int64, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	slog.Info("run", "run", id, "event", msg)
	if _, err := e.DB.Exec(`INSERT INTO run_events (run_id, at, message) VALUES (?, ?, ?)`, id, now(), msg); err != nil {
		slog.Error("run log", "run", id, "err", err)
	}
}

func (e *Engine) finish(id int64, status string, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if _, err := e.DB.Exec(`UPDATE runs SET status = ?, error = ?, finished = ? WHERE id = ?`, status, msg, now(), id); err != nil {
		slog.Error("run end", "run", id, "err", err)
	}
	e.event(id, "result: %s", results[status])
	var stack string
	var dryRun bool
	if e.Finished != nil && e.DB.QueryRow(`SELECT stack, dry_run FROM runs WHERE id = ?`, id).Scan(&stack, &dryRun) == nil && !dryRun {
		e.Finished(stack, id, status, msg)
	}
}

// ProjectOf returns the Compose project of a stack.
func ProjectOf(st *inventory.Stack) compose.Project {
	return compose.Project{Name: st.Name, WorkingDir: st.WorkingDir, ConfigFiles: st.ConfigFiles, EnvFiles: st.EnvFiles}
}

// Execute applies the plan of run id to st, then finishes the run done, rolled_back, failed or abandoned.
func (e *Engine) Execute(ctx context.Context, id int64, st *inventory.Stack, p *plan.Plan, dryRun bool) {
	for _, s := range st.Services {
		running := s.Version
		if running == "" {
			running = s.Tag
		}
		for _, c := range firstChanges(p, s.Name) {
			if c.From != running {
				e.finish(id, Abandoned, fmt.Errorf("the stack changed since the plan was computed: %s is at %s, the plan starts from %s", s.Name, running, c.From))
				return
			}
		}
	}
	if dryRun {
		e.dryRun(ctx, id, st, p)
		return
	}
	for _, s := range st.Services {
		if s.BackupHook == "" {
			continue
		}
		e.event(id, "backup hook of %s: %s", s.Name, s.BackupHook)
		if _, err := compose.Run(ctx, e.DockerHost, ProjectOf(st), "exec", "-T", s.Name, "sh", "-c", s.BackupHook); err != nil {
			e.finish(id, Abandoned, fmt.Errorf("the backup hook of %s failed, nothing is applied: %w", s.Name, err))
			return
		}
	}
	cp, err := checkpoint(st)
	if err == nil {
		err = e.save(id, cp)
	}
	if err != nil {
		e.finish(id, Abandoned, fmt.Errorf("cannot create checkpoint: %w", err))
		return
	}
	if err := e.modified(id); err != nil {
		e.finish(id, Abandoned, err)
		return
	}
	if err := e.snapshot(ctx, id, st, cp, changedServices(st, p)); err != nil {
		e.event(id, "cannot copy volumes: %v", err)
		e.rollback(context.WithoutCancel(ctx), id, cp, err)
		return
	}
	e.event(id, "checkpoint: %d Compose files, %d services, %d volume copies", len(cp.Files), len(cp.Services), len(cp.Snapshots))
	for i, step := range p.Steps {
		e.event(id, "step %d of %d: %s", i+1, len(p.Steps), describe(step))
		if err := e.step(ctx, id, st, cp, step); err != nil {
			e.event(id, "step %d failed: %v", i+1, err)
			e.rollback(context.WithoutCancel(ctx), id, cp, err)
			return
		}
		e.event(id, "step %d verified", i+1)
		if i+1 < len(p.Steps) {
			next, err := e.advance(ctx, id, cp, &plan.Plan{Steps: p.Steps[i+1:]}, i+1)
			if err != nil {
				e.event(id, "cannot create checkpoint after step %d: %v", i+1, err)
				e.rollback(context.WithoutCancel(ctx), id, cp, err)
				return
			}
			cp = next
		}
	}
	e.dropSnapshots(ctx, id, cp)
	e.finish(id, Done, nil)
}

// advance replaces prev by a checkpoint of the stack as the first done steps left it, with fresh copies of the volumes rest changes.
func (e *Engine) advance(ctx context.Context, id int64, prev *Checkpoint, rest *plan.Plan, done int) (*Checkpoint, error) {
	stacks, err := inventory.Scan(ctx, e.Docker)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(stacks, func(st *inventory.Stack) bool { return st.Name == prev.Project.Name && !st.Observed })
	if i < 0 {
		return nil, fmt.Errorf("stack %s not found", prev.Project.Name)
	}
	now := stacks[i]
	cp, err := checkpoint(now)
	if err != nil {
		return nil, err
	}
	cp.Step = done
	if err := e.snapshot(ctx, id, now, cp, changedServices(now, rest)); err != nil {
		e.dropSnapshots(context.WithoutCancel(ctx), id, cp)
		return nil, err
	}
	if err := e.save(id, cp); err != nil {
		e.dropSnapshots(context.WithoutCancel(ctx), id, cp)
		return nil, err
	}
	e.dropSnapshots(ctx, id, prev)
	e.event(id, "checkpoint after step %d: %d Compose files, %d services, %d volume copies", done, len(cp.Files), len(cp.Services), len(cp.Snapshots))
	return cp, nil
}

// changedServices returns the services the plan changes and, since an application migrates the data of its
// dependencies, every service they depend on.
func changedServices(st *inventory.Stack, p *plan.Plan) []string {
	var out []string
	var add func(string)
	add = func(name string) {
		if slices.Contains(out, name) {
			return
		}
		out = append(out, name)
		if s := service(st, name); s != nil {
			for _, d := range s.DependsOn {
				add(d)
			}
		}
	}
	for _, s := range p.Steps {
		for _, c := range s.Changes {
			add(c.Service)
		}
	}
	return out
}

func firstChanges(p *plan.Plan, service string) []plan.Change {
	for _, s := range p.Steps {
		for _, c := range s.Changes {
			if c.Service == service {
				return []plan.Change{c}
			}
		}
	}
	return nil
}

func describe(s plan.Step) string {
	var parts []string
	for _, c := range s.Changes {
		parts = append(parts, fmt.Sprintf("%s %s to %s", c.Service, c.From, c.To))
	}
	if len(s.StopDuring) > 0 {
		parts = append(parts, "stop "+strings.Join(s.StopDuring, ", "))
	}
	return strings.Join(parts, "; ")
}

func checkpoint(st *inventory.Stack) (*Checkpoint, error) {
	cp := &Checkpoint{Project: ProjectOf(st)}
	files := slices.Clone(st.ConfigFiles)
	if env := envFile(cp.Project); !slices.Contains(files, env) {
		if _, err := os.Stat(env); err == nil {
			files = append(files, env)
		}
	}
	for _, f := range files {
		fs, err := compose.ReadState(f)
		if err != nil {
			return nil, err
		}
		cp.Files = append(cp.Files, fs)
	}
	for _, s := range st.Services {
		cp.Services = append(cp.Services, Saved{Name: s.Name, Image: s.Image, Tag: s.Tag, ImageID: s.ImageID, Digest: s.Digest, Mounts: s.Mounts})
	}
	return cp, nil
}

func (e *Engine) save(id int64, cp *Checkpoint) error {
	body, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	_, err = e.DB.Exec(`UPDATE runs SET checkpoint = ? WHERE id = ?`, string(body), id)
	return err
}

func (e *Engine) modified(id int64) error {
	_, err := e.DB.Exec(`UPDATE runs SET modified = 1 WHERE id = ?`, id)
	return err
}

func (e *Engine) step(ctx context.Context, id int64, st *inventory.Stack, cp *Checkpoint, step plan.Step) error {
	stopped := dependencyOrder(st, step.StopDuring)
	slices.Reverse(stopped)
	for _, name := range stopped {
		e.event(id, "stopping %s", name)
		if _, err := compose.Run(ctx, e.DockerHost, cp.Project, "stop", name); err != nil {
			return err
		}
	}
	for _, op := range step.Operations {
		if err := e.operation(ctx, id, st, cp, op); err != nil {
			return err
		}
	}
	if len(stopped) > 0 {
		slices.Reverse(stopped)
		e.event(id, "starting %s", strings.Join(stopped, ", "))
		if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"up", "-d", "--no-deps"}, stopped...)...); err != nil {
			return err
		}
	}
	if err := e.verify(ctx, id, cp.Project.Name, present(cp), e.Stable); err != nil {
		return err
	}
	for _, c := range step.Changes {
		if svc := service(st, c.Service); svc != nil && plan.Level(c.From, c.To) != "major" && strings.HasPrefix(svc.Image[strings.LastIndex(svc.Image, "/")+1:], "postgres") {
			if err := e.refreshCollations(ctx, id, cp, svc); err != nil {
				return err
			}
		}
	}
	if g := step.Gate; g != nil && g.Op == "exec_check" {
		return e.gate(ctx, id, st, cp, *g)
	}
	return nil
}

const (
	gateInterval = 30 * time.Second
	gateTimeout  = 2 * time.Hour
)

// gate runs the check command of the card until it succeeds.
func (e *Engine) gate(ctx context.Context, id int64, st *inventory.Stack, cp *Checkpoint, g plan.Operation) error {
	e.event(id, "waiting for %s on %s", g.CheckRef, g.Service)
	deadline := time.Now().Add(gateTimeout)
	for tries := 1; ; tries++ {
		err := e.runCheck(ctx, st, cp, g)
		if err == nil {
			e.event(id, "%s passed on %s after %d attempt(s)", g.CheckRef, g.Service, tries)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s still failing on %s after %s: %w", g.CheckRef, g.Service, gateTimeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gateInterval):
		}
	}
}

func (e *Engine) runCheck(ctx context.Context, st *inventory.Stack, cp *Checkpoint, op plan.Operation) error {
	svc := service(st, op.Service)
	if svc == nil {
		return fmt.Errorf("service %s not found", op.Service)
	}
	card := catalog.For(svc.Image)
	if card == nil || card.Checks[op.CheckRef] == nil {
		return fmt.Errorf("no command %s in the card of %s", op.CheckRef, op.Service)
	}
	_, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"exec", "-T", op.Service}, card.Checks[op.CheckRef]...)...)
	return err
}

func names(cp *Checkpoint) []string {
	var out []string
	for _, s := range cp.Services {
		out = append(out, s.Name)
	}
	return out
}

func dependencyOrder(st *inventory.Stack, selected []string) []string {
	deps := map[string][]string{}
	for _, s := range st.Services {
		deps[s.Name] = s.DependsOn
	}
	var depth func(string, int) int
	depth = func(name string, guard int) int {
		d := 0
		for _, dep := range deps[name] {
			if guard < len(deps) {
				d = max(d, 1+depth(dep, guard+1))
			}
		}
		return d
	}
	out := slices.Clone(selected)
	slices.SortStableFunc(out, func(a, b string) int { return depth(a, 0) - depth(b, 0) })
	return out
}

func (e *Engine) operation(ctx context.Context, id int64, st *inventory.Stack, cp *Checkpoint, op plan.Operation) error {
	services := op.Targets()
	switch op.Op {
	case "set_image":
		return e.setImage(id, cp, op)
	case "compose_edit":
		return e.composeEdit(ctx, id, cp, op)
	case "recreate":
		e.event(id, "recreate %s", strings.Join(services, ", "))
		_, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"up", "-d", "--no-deps"}, services...)...)
		return err
	case "stop", "start":
		e.event(id, "%s %s", op.Op, strings.Join(services, ", "))
		_, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{op.Op}, services...)...)
		return err
	case "wait_stable":
		return e.verify(ctx, id, cp.Project.Name, services, time.Duration(op.Seconds)*time.Second)
	case "db_dump_restore":
		return e.dumpRestore(ctx, id, st, cp, op)
	case "exec_check":
		e.event(id, "exec_check %s on %s", op.CheckRef, op.Service)
		return e.runCheck(ctx, st, cp, op)
	}
	return fmt.Errorf("unsupported operation %s", op.Op)
}

func currentImage(data []byte, service string) (string, error) {
	raw, err := compose.ImageValue(data, service)
	if err != nil {
		return "", err
	}
	if repo, _, ok := compose.ImageVariable(raw); ok {
		return repo, nil
	}
	image, _, _ := strings.Cut(raw, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image, nil
}

// envFile is the file Compose reads its variables from: the one given to the project, else .env in its directory.
func envFile(p compose.Project) string {
	if len(p.EnvFiles) > 0 {
		return p.EnvFiles[0]
	}
	return filepath.Join(p.WorkingDir, ".env")
}

// setImage writes the new tag where the Compose file takes it: the image itself, or the variable that holds its tag.
func (e *Engine) setImage(id int64, cp *Checkpoint, op plan.Operation) error {
	file, err := compose.ImageFile(cp.Project.ConfigFiles, op.Service)
	if err != nil {
		return err
	}
	fs, err := compose.ReadState(file)
	if err != nil {
		return err
	}
	raw, err := compose.ImageValue([]byte(fs.Data), op.Service)
	if err != nil {
		return err
	}
	if repo, variable, ok := compose.ImageVariable(raw); ok {
		if op.Image != "" && registry.ParseRef(op.Image).Name() != registry.ParseRef(repo).Name() {
			return fmt.Errorf("the image of %s uses the variable %s: Palier does not change its repository", op.Service, variable)
		}
		env, err := compose.ReadState(envFile(cp.Project))
		if err != nil {
			return err
		}
		if current, _ := compose.EnvVar([]byte(env.Data), variable); current == op.Tag {
			e.event(id, "%s: %s is already %s", env.Path, variable, op.Tag)
			return nil
		}
		env.Data = string(compose.SetEnvVar([]byte(env.Data), variable, op.Tag))
		e.event(id, "%s: %s set to %s (image of %s)", env.Path, variable, op.Tag, op.Service)
		return compose.Write(env)
	}
	ref, err := currentImage([]byte(fs.Data), op.Service)
	if err != nil {
		return err
	}
	if op.Image != "" && registry.ParseRef(op.Image).Name() != registry.ParseRef(ref).Name() {
		ref, _, _ = strings.Cut(op.Image, "@")
		if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
			ref = ref[:i]
		}
	}
	data, err := compose.SetImage([]byte(fs.Data), op.Service, ref+":"+op.Tag)
	if err != nil {
		return err
	}
	fs.Data = string(data)
	e.event(id, "%s: image of %s set to %s:%s", file, op.Service, ref, op.Tag)
	return compose.Write(fs)
}

// composeEdit applies the structured edits of one service; a removed service is stopped and deleted first.
func (e *Engine) composeEdit(ctx context.Context, id int64, cp *Checkpoint, op plan.Operation) error {
	for _, ed := range op.Edits {
		if ed.Action == "remove_service" {
			e.event(id, "stopping and removing the container of %s", op.Service)
			if _, err := compose.Run(ctx, e.DockerHost, cp.Project, "rm", "-s", "-f", op.Service); err != nil {
				return err
			}
		}
	}
	for _, file := range cp.Project.ConfigFiles {
		fs, err := compose.ReadState(file)
		if err != nil {
			return err
		}
		names, err := compose.Services([]byte(fs.Data))
		if err != nil || !slices.Contains(names, op.Service) {
			continue
		}
		var done []string
		data, err := compose.Edit([]byte(fs.Data), func(root *yaml.Node) error {
			for _, ed := range op.Edits {
				what, err := compose.ApplyEdit(root, op.Service, ed.Action, ed.Name, ed.Value, ed.NewName)
				if err != nil {
					return err
				}
				done = append(done, what)
			}
			return nil
		})
		if err != nil {
			return err
		}
		fs.Data = string(data)
		if err := compose.Write(fs); err != nil {
			return err
		}
		e.event(id, "%s: %s", file, strings.Join(done, "; "))
		return nil
	}
	return fmt.Errorf("no Compose file declares service %s", op.Service)
}

// present returns the services of the checkpoint the Compose files still declare.
func present(cp *Checkpoint) []string {
	var declared []string
	for _, f := range cp.Project.ConfigFiles {
		if data, err := os.ReadFile(f); err == nil {
			names, _ := compose.Services(data)
			declared = append(declared, names...)
		}
	}
	var out []string
	for _, n := range names(cp) {
		if slices.Contains(declared, n) {
			out = append(out, n)
		}
	}
	return out
}

const minHealthGrace = 2 * time.Minute

// healthGrace is the time Docker itself gives the services to become healthy: start period, then retries of the check.
func (e *Engine) healthGrace(ctx context.Context, project string, services []string) time.Duration {
	grace := minHealthGrace
	filters := url.QueryEscape(fmt.Sprintf(`{"label":["com.docker.compose.project=%s"]}`, project))
	var list []docker.Container
	if e.Docker.Get(ctx, "/containers/json?all=1&filters="+filters, &list) != nil {
		return grace
	}
	for _, c := range list {
		if !slices.Contains(services, c.Labels["com.docker.compose.service"]) {
			continue
		}
		var d docker.ContainerDetail
		if e.Docker.Get(ctx, "/containers/"+c.ID+"/json", &d) != nil || d.Config.Healthcheck == nil {
			continue
		}
		h := d.Config.Healthcheck
		interval := time.Duration(h.Interval)
		if interval == 0 {
			interval = 30 * time.Second
		}
		retries := h.Retries
		if retries == 0 {
			retries = 3
		}
		grace = max(grace, time.Duration(h.StartPeriod)+(interval+time.Duration(h.Timeout))*time.Duration(retries+1))
	}
	return grace
}

// verify waits until every service runs, is healthy when it has a healthcheck, and stays so for stable without restarting.
func (e *Engine) verify(ctx context.Context, id int64, project string, services []string, stable time.Duration) error {
	e.event(id, "verifying %s for %d s", strings.Join(services, ", "), int(stable.Seconds()))
	grace := e.healthGrace(ctx, project, services)
	deadline := time.Now().Add(stable + grace)
	restarts := map[string]int{}
	var okSince time.Time
	for {
		waiting, err := e.check(ctx, project, services, restarts)
		if err != nil {
			return err
		}
		switch {
		case waiting != "":
			okSince = time.Time{}
		case okSince.IsZero():
			okSince = time.Now()
		case time.Since(okSince) >= stable:
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not stable after %d s: %s", int((stable + grace).Seconds()), waiting)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (e *Engine) check(ctx context.Context, project string, services []string, restarts map[string]int) (string, error) {
	filters := url.QueryEscape(fmt.Sprintf(`{"label":["com.docker.compose.project=%s"]}`, project))
	var list []docker.Container
	if err := e.Docker.Get(ctx, "/containers/json?all=1&filters="+filters, &list); err != nil {
		return "", err
	}
	for _, name := range services {
		i := slices.IndexFunc(list, func(c docker.Container) bool { return c.Labels["com.docker.compose.service"] == name })
		if i < 0 {
			return name + " has no container", nil
		}
		var d docker.ContainerDetail
		if err := e.Docker.Get(ctx, "/containers/"+list[i].ID+"/json", &d); err != nil {
			return "", err
		}
		base, seen := restarts[name]
		if !seen {
			restarts[name] = d.RestartCount
			base = d.RestartCount
		}
		fail := ""
		switch {
		case d.RestartCount > base || d.State.Status == "restarting":
			fail = fmt.Sprintf("%s keeps restarting", name)
		case d.State.Status == "exited" || d.State.Status == "dead":
			fail = fmt.Sprintf("%s stopped", name)
		case d.State.Health != nil && d.State.Health.Status == "unhealthy":
			fail = fmt.Sprintf("%s is unhealthy", name)
		}
		if fail != "" {
			logs, _ := e.Docker.Logs(ctx, d.ID, 20)
			return "", fmt.Errorf("%s; last log lines:\n%s", fail, strings.TrimSpace(logs))
		}
		if d.State.Status != "running" {
			return name + " is " + d.State.Status, nil
		}
		if d.State.Health != nil && d.State.Health.Status != "healthy" {
			return name + " is " + d.State.Health.Status, nil
		}
	}
	return "", nil
}

func (e *Engine) rollback(ctx context.Context, id int64, cp *Checkpoint, cause error) {
	if cp.Step > 0 {
		e.event(id, "rolling back the whole stack to the last verified step, after step %d", cp.Step)
		cause = fmt.Errorf("%v; the stack stays at the last verified step, after step %d", cause, cp.Step)
	} else {
		e.event(id, "rolling back the whole stack to the checkpoint")
	}
	err := e.restore(ctx, id, cp)
	if err == nil {
		err = e.verify(ctx, id, cp.Project.Name, names(cp), e.Stable)
	}
	if err != nil {
		e.event(id, "rollback failed: %v", err)
		e.finish(id, Failed, fmt.Errorf("%v; rollback: %w", cause, err))
		return
	}
	e.dropSnapshots(ctx, id, cp)
	e.finish(id, RolledBack, cause)
}

func (e *Engine) restore(ctx context.Context, id int64, cp *Checkpoint) error {
	for _, f := range cp.Files {
		if err := compose.Write(f); err != nil {
			return err
		}
		e.event(id, "%s restored", f.Path)
	}
	if cp.SnapshotsDone {
		if err := e.restoreSnapshots(ctx, id, cp); err != nil {
			return err
		}
	}
	for _, s := range cp.Services {
		q := url.Values{"repo": {s.Image}, "tag": {s.Tag}}
		if err := e.Docker.Post(ctx, "/images/"+s.ImageID+"/tag?"+q.Encode(), nil, nil); err != nil {
			return err
		}
	}
	e.event(id, "original images retagged, recreating the stack")
	if _, err := compose.Run(ctx, e.DockerHost, cp.Project, "up", "-d"); err != nil {
		return err
	}
	for _, v := range cp.CreatedVolumes {
		if err := e.Docker.Delete(ctx, "/volumes/"+url.PathEscape(v)); err != nil {
			e.event(id, "volume %s created during the run was not removed: %v", v, err)
		} else {
			e.event(id, "volume %s created during the run removed", v)
		}
	}
	return nil
}

// Recover finishes the runs a stopped Palier left running: rolled back when they had modified the stack, abandoned otherwise.
func (e *Engine) Recover(ctx context.Context) error {
	rows, err := e.DB.QueryContext(ctx, `SELECT id, modified, checkpoint FROM runs WHERE status = ?`, Running)
	if err != nil {
		return err
	}
	type pending struct {
		id       int64
		modified bool
		cp       string
	}
	var list []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.modified, &p.cp); err != nil {
			rows.Close()
			return err
		}
		list = append(list, p)
	}
	rows.Close()
	for _, p := range list {
		cause := errors.New("Palier stopped during the run")
		if !p.modified {
			e.event(p.id, "recovery at startup: no changes made")
			e.finish(p.id, Abandoned, cause)
			continue
		}
		var cp Checkpoint
		if err := json.Unmarshal([]byte(p.cp), &cp); err != nil {
			e.finish(p.id, Failed, fmt.Errorf("%v; unreadable checkpoint: %w", cause, err))
			continue
		}
		e.event(p.id, "recovery at startup: the stack had been modified")
		e.rollback(ctx, p.id, &cp, cause)
	}
	return nil
}

func (e *Engine) dryRun(ctx context.Context, id int64, st *inventory.Stack, p *plan.Plan) {
	files := map[string][]byte{}
	var problems []string
	for i, step := range p.Steps {
		e.event(id, "step %d of %d: %s", i+1, len(p.Steps), describe(step))
		for _, op := range step.Operations {
			if op.Op != "set_image" {
				e.event(id, "would do: %s %s", op.Op, strings.Join(op.Targets(), " "))
				continue
			}
			svc := st.Services[slices.IndexFunc(st.Services, func(s *inventory.Service) bool { return s.Name == op.Service })]
			repo := svc.Image
			if op.Image != "" {
				repo = registry.ParseRef(op.Image).Name()
			}
			if _, err := e.Registry.Digest(ctx, registry.ParseRef(repo+":"+op.Tag), op.Tag); err != nil {
				problems = append(problems, fmt.Sprintf("%s:%s is not in the registry: %v", repo, op.Tag, err))
			}
			file, err := compose.ImageFile(st.ConfigFiles, op.Service)
			if err == nil && files[file] == nil {
				var fs compose.FileState
				fs, err = compose.ReadState(file)
				files[file] = []byte(fs.Data)
			}
			var ref string
			if err == nil {
				ref, err = currentImage(files[file], op.Service)
			}
			if raw, _ := compose.ImageValue(files[file], op.Service); err == nil {
				if _, variable, ok := compose.ImageVariable(raw); ok {
					e.event(id, "would do: %s set to %s (image of %s, present in the registry)", variable, op.Tag, op.Service)
					continue
				}
				files[file], err = compose.SetImage(files[file], op.Service, ref+":"+op.Tag)
			}
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			e.event(id, "would do: %s, image of %s set to %s:%s (present in the registry)", file, op.Service, ref, op.Tag)
		}
	}
	if len(problems) > 0 {
		e.finish(id, Failed, errors.New(strings.Join(problems, "; ")))
		return
	}
	e.finish(id, Done, nil)
}

// View is a run as the interface and the command line show it.
type View struct {
	ID        int64    `json:"id"`
	Stack     string   `json:"stack"`
	Status    string   `json:"status"`
	DryRun    bool     `json:"dry_run"`
	Author    string   `json:"author"`
	Error     string   `json:"error,omitempty"`
	Started   string   `json:"started"`
	Finished  string   `json:"finished,omitempty"`
	Steps     []string `json:"steps,omitempty"`
	StepsDone int      `json:"steps_done"`
	Events    []Event  `json:"events,omitempty"`
}

const viewColumns = `id, stack, status, dry_run, author, error, started, finished, plan,
	CASE WHEN json_valid(checkpoint) THEN coalesce(json_extract(checkpoint, '$.step'), 0) ELSE 0 END`

func scanView(row interface{ Scan(...any) error }) (View, error) {
	var v View
	var body string
	if err := row.Scan(&v.ID, &v.Stack, &v.Status, &v.DryRun, &v.Author, &v.Error, &v.Started, &v.Finished, &body, &v.StepsDone); err != nil {
		return v, err
	}
	var p plan.Plan
	if json.Unmarshal([]byte(body), &p) == nil {
		for _, s := range p.Steps {
			v.Steps = append(v.Steps, describe(s))
		}
	}
	if v.Status == Done {
		v.StepsDone = len(v.Steps)
	}
	return v, nil
}

// Event is one line of the journal of a run.
type Event struct {
	At      string `json:"at"`
	Message string `json:"message"`
}

// Load returns run id with its journal.
func Load(ctx context.Context, db *sql.DB, id int64) (*View, error) {
	view, err := scanView(db.QueryRowContext(ctx, `SELECT `+viewColumns+` FROM runs WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	v := &view
	rows, err := db.QueryContext(ctx, `SELECT at, message FROM run_events WHERE run_id = ? ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.At, &ev.Message); err != nil {
			return nil, err
		}
		v.Events = append(v.Events, ev)
	}
	return v, rows.Err()
}

// List returns the latest runs, of one stack when stack is not empty, newest first.
func List(ctx context.Context, db *sql.DB, stack string, limit int) ([]View, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+viewColumns+` FROM runs
		WHERE ? = '' OR stack = ? ORDER BY id DESC LIMIT ?`, stack, stack, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []View
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
