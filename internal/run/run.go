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

var results = map[string]string{Done: "terminée", RolledBack: "revenue en arrière", Failed: "échec", Abandoned: "abandonnée"}

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

// Checkpoint is the state of a stack before its first modification.
type Checkpoint struct {
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
var ErrBusy = errors.New("une exécution est déjà en cours sur cette stack")

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
	slog.Info("exécution", "run", id, "event", msg)
	if _, err := e.DB.Exec(`INSERT INTO run_events (run_id, at, message) VALUES (?, ?, ?)`, id, now(), msg); err != nil {
		slog.Error("journal d'exécution", "run", id, "err", err)
	}
}

func (e *Engine) finish(id int64, status string, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if _, err := e.DB.Exec(`UPDATE runs SET status = ?, error = ?, finished = ? WHERE id = ?`, status, msg, now(), id); err != nil {
		slog.Error("fin d'exécution", "run", id, "err", err)
	}
	e.event(id, "résultat : %s", results[status])
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
				e.finish(id, Abandoned, fmt.Errorf("la stack a changé depuis le calcul du plan : %s est en %s, le plan part de %s", s.Name, running, c.From))
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
		e.event(id, "hook de sauvegarde de %s : %s", s.Name, s.BackupHook)
		if _, err := compose.Run(ctx, e.DockerHost, ProjectOf(st), "exec", "-T", s.Name, "sh", "-c", s.BackupHook); err != nil {
			e.finish(id, Abandoned, fmt.Errorf("le hook de sauvegarde de %s a échoué, rien n'est appliqué : %w", s.Name, err))
			return
		}
	}
	cp, err := checkpoint(st)
	if err == nil {
		err = e.save(id, cp)
	}
	if err != nil {
		e.finish(id, Abandoned, fmt.Errorf("point de reprise impossible : %w", err))
		return
	}
	if err := e.modified(id); err != nil {
		e.finish(id, Abandoned, err)
		return
	}
	if err := e.snapshot(ctx, id, st, cp, changedServices(st, p)); err != nil {
		e.event(id, "copie des volumes impossible : %v", err)
		e.rollback(context.WithoutCancel(ctx), id, cp, err)
		return
	}
	e.event(id, "point de reprise : fichiers Compose %d, services %d, copies de volumes %d", len(cp.Files), len(cp.Services), len(cp.Snapshots))
	for i, step := range p.Steps {
		e.event(id, "étape %d sur %d : %s", i+1, len(p.Steps), describe(step))
		if err := e.step(ctx, id, st, cp, step); err != nil {
			e.event(id, "échec de l'étape %d : %v", i+1, err)
			e.rollback(context.WithoutCancel(ctx), id, cp, err)
			return
		}
		e.event(id, "étape %d vérifiée", i+1)
	}
	e.dropSnapshots(ctx, id, cp)
	e.finish(id, Done, nil)
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
		parts = append(parts, fmt.Sprintf("%s %s vers %s", c.Service, c.From, c.To))
	}
	if len(s.StopDuring) > 0 {
		parts = append(parts, "arrêt de "+strings.Join(s.StopDuring, ", "))
	}
	return strings.Join(parts, " ; ")
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
		e.event(id, "arrêt de %s", name)
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
		e.event(id, "démarrage de %s", strings.Join(stopped, ", "))
		if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"up", "-d", "--no-deps"}, stopped...)...); err != nil {
			return err
		}
	}
	if err := e.verify(ctx, id, cp.Project.Name, present(cp), e.Stable); err != nil {
		return err
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
	e.event(id, "attente de %s sur %s", g.CheckRef, g.Service)
	deadline := time.Now().Add(gateTimeout)
	for tries := 1; ; tries++ {
		err := e.runCheck(ctx, st, cp, g)
		if err == nil {
			e.event(id, "%s réussi sur %s après %d essai(s)", g.CheckRef, g.Service, tries)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s toujours en échec sur %s après %s : %w", g.CheckRef, g.Service, gateTimeout, err)
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
		return fmt.Errorf("service %s introuvable", op.Service)
	}
	card := catalog.For(svc.Image)
	if card == nil || card.Checks[op.CheckRef] == nil {
		return fmt.Errorf("pas de commande %s dans la fiche de %s", op.CheckRef, op.Service)
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
		e.event(id, "recreate de %s", strings.Join(services, ", "))
		_, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"up", "-d", "--no-deps"}, services...)...)
		return err
	case "stop", "start":
		e.event(id, "%s de %s", op.Op, strings.Join(services, ", "))
		_, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{op.Op}, services...)...)
		return err
	case "wait_stable":
		return e.verify(ctx, id, cp.Project.Name, services, time.Duration(op.Seconds)*time.Second)
	case "db_dump_restore":
		return e.dumpRestore(ctx, id, st, cp, op)
	case "exec_check":
		e.event(id, "exec_check %s sur %s", op.CheckRef, op.Service)
		return e.runCheck(ctx, st, cp, op)
	}
	return fmt.Errorf("opération %s non prise en charge", op.Op)
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
			return fmt.Errorf("l'image de %s passe par la variable %s : Palier ne change pas son dépôt", op.Service, variable)
		}
		env, err := compose.ReadState(envFile(cp.Project))
		if err != nil {
			return err
		}
		if current, _ := compose.EnvVar([]byte(env.Data), variable); current == op.Tag {
			e.event(id, "%s : %s vaut déjà %s", env.Path, variable, op.Tag)
			return nil
		}
		env.Data = string(compose.SetEnvVar([]byte(env.Data), variable, op.Tag))
		e.event(id, "%s : %s passe à %s (image de %s)", env.Path, variable, op.Tag, op.Service)
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
	e.event(id, "%s : image de %s passée à %s:%s", file, op.Service, ref, op.Tag)
	return compose.Write(fs)
}

// composeEdit applies the structured edits of one service; a removed service is stopped and deleted first.
func (e *Engine) composeEdit(ctx context.Context, id int64, cp *Checkpoint, op plan.Operation) error {
	for _, ed := range op.Edits {
		if ed.Action == "remove_service" {
			e.event(id, "arrêt et suppression du conteneur de %s", op.Service)
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
		e.event(id, "%s : %s", file, strings.Join(done, " ; "))
		return nil
	}
	return fmt.Errorf("aucun fichier Compose ne déclare le service %s", op.Service)
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
	e.event(id, "vérification de %s pendant %d s", strings.Join(services, ", "), int(stable.Seconds()))
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
			return fmt.Errorf("pas stable après %d s : %s", int((stable + grace).Seconds()), waiting)
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
			return name + " n'a pas de conteneur", nil
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
			fail = fmt.Sprintf("%s redémarre en boucle", name)
		case d.State.Status == "exited" || d.State.Status == "dead":
			fail = fmt.Sprintf("%s s'est arrêté", name)
		case d.State.Health != nil && d.State.Health.Status == "unhealthy":
			fail = fmt.Sprintf("%s est unhealthy", name)
		}
		if fail != "" {
			logs, _ := e.Docker.Logs(ctx, d.ID, 20)
			return "", fmt.Errorf("%s ; dernières lignes du journal :\n%s", fail, strings.TrimSpace(logs))
		}
		if d.State.Status != "running" {
			return name + " est " + d.State.Status, nil
		}
		if d.State.Health != nil && d.State.Health.Status != "healthy" {
			return name + " est " + d.State.Health.Status, nil
		}
	}
	return "", nil
}

func (e *Engine) rollback(ctx context.Context, id int64, cp *Checkpoint, cause error) {
	e.event(id, "retour arrière de toute la stack au point de reprise")
	err := e.restore(ctx, id, cp)
	if err == nil {
		err = e.verify(ctx, id, cp.Project.Name, names(cp), e.Stable)
	}
	if err != nil {
		e.event(id, "le retour arrière a échoué : %v", err)
		e.finish(id, Failed, fmt.Errorf("%v ; retour arrière : %w", cause, err))
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
		e.event(id, "%s restauré", f.Path)
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
	e.event(id, "images d'origine retaguées, recreate de la stack")
	if _, err := compose.Run(ctx, e.DockerHost, cp.Project, "up", "-d"); err != nil {
		return err
	}
	for _, v := range cp.CreatedVolumes {
		if err := e.Docker.Delete(ctx, "/volumes/"+url.PathEscape(v)); err != nil {
			e.event(id, "volume %s créé pendant l'exécution non supprimé : %v", v, err)
		} else {
			e.event(id, "volume %s créé pendant l'exécution supprimé", v)
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
		cause := errors.New("Palier s'est arrêté pendant l'exécution")
		if !p.modified {
			e.event(p.id, "reprise au démarrage : aucune modification faite")
			e.finish(p.id, Abandoned, cause)
			continue
		}
		var cp Checkpoint
		if err := json.Unmarshal([]byte(p.cp), &cp); err != nil {
			e.finish(p.id, Failed, fmt.Errorf("%v ; point de reprise illisible : %w", cause, err))
			continue
		}
		e.event(p.id, "reprise au démarrage : la stack avait été modifiée")
		e.rollback(ctx, p.id, &cp, cause)
	}
	return nil
}

func (e *Engine) dryRun(ctx context.Context, id int64, st *inventory.Stack, p *plan.Plan) {
	files := map[string][]byte{}
	var problems []string
	for i, step := range p.Steps {
		e.event(id, "étape %d sur %d : %s", i+1, len(p.Steps), describe(step))
		for _, op := range step.Operations {
			if op.Op != "set_image" {
				e.event(id, "ferait : %s %s", op.Op, strings.Join(op.Targets(), " "))
				continue
			}
			svc := st.Services[slices.IndexFunc(st.Services, func(s *inventory.Service) bool { return s.Name == op.Service })]
			repo := svc.Image
			if op.Image != "" {
				repo = registry.ParseRef(op.Image).Name()
			}
			if _, err := e.Registry.Digest(ctx, registry.ParseRef(repo+":"+op.Tag), op.Tag); err != nil {
				problems = append(problems, fmt.Sprintf("%s:%s absente du registre : %v", repo, op.Tag, err))
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
					e.event(id, "ferait : %s passe à %s (image de %s, présente au registre)", variable, op.Tag, op.Service)
					continue
				}
				files[file], err = compose.SetImage(files[file], op.Service, ref+":"+op.Tag)
			}
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			e.event(id, "ferait : %s, image de %s passée à %s:%s (présente au registre)", file, op.Service, ref, op.Tag)
		}
	}
	if len(problems) > 0 {
		e.finish(id, Failed, errors.New(strings.Join(problems, " ; ")))
		return
	}
	e.finish(id, Done, nil)
}

// View is a run as the interface and the command line show it.
type View struct {
	ID       int64   `json:"id"`
	Stack    string  `json:"stack"`
	Status   string  `json:"status"`
	DryRun   bool    `json:"dry_run"`
	Author   string  `json:"author"`
	Error    string  `json:"error,omitempty"`
	Started  string  `json:"started"`
	Finished string  `json:"finished,omitempty"`
	Events   []Event `json:"events,omitempty"`
}

// Event is one line of the journal of a run.
type Event struct {
	At      string `json:"at"`
	Message string `json:"message"`
}

// Load returns run id with its journal.
func Load(ctx context.Context, db *sql.DB, id int64) (*View, error) {
	v := &View{}
	err := db.QueryRowContext(ctx, `SELECT id, stack, status, dry_run, author, error, started, finished FROM runs WHERE id = ?`, id).
		Scan(&v.ID, &v.Stack, &v.Status, &v.DryRun, &v.Author, &v.Error, &v.Started, &v.Finished)
	if err != nil {
		return nil, err
	}
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
	rows, err := db.QueryContext(ctx, `SELECT id, stack, status, dry_run, author, error, started, finished FROM runs
		WHERE ? = '' OR stack = ? ORDER BY id DESC LIMIT ?`, stack, stack, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []View
	for rows.Next() {
		var v View
		if err := rows.Scan(&v.ID, &v.Stack, &v.Status, &v.DryRun, &v.Author, &v.Error, &v.Started, &v.Finished); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
