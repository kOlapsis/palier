package run

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kolapsis/palier/internal/compose"
	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/registry"
)

const (
	pgIndexes     = `SELECT schemaname || '.' || indexname FROM pg_indexes WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY 1`
	pgConstraints = `SELECT replace(conrelid::regclass::text || '.' || conname, ' ', '_') FROM pg_constraint WHERE contype <> 'n' AND connamespace::regnamespace::text NOT IN ('pg_catalog', 'information_schema') ORDER BY 1`
)

const pgTables = `SELECT table_schema || '.' || table_name || '=' || (xpath('/row/c/text()', query_to_xml(format('select count(*) as c from %I.%I', table_schema, table_name), false, true, '')))[1]::text FROM information_schema.tables WHERE table_type = 'BASE TABLE' AND table_schema NOT IN ('pg_catalog', 'information_schema') ORDER BY 1`

// dumpRestore moves a PostgreSQL service to a new major: logical export from the running container, new volume,
// new major started on it, import, comparison of roles, databases, tables, row counts, indexes and constraints.
// The old volume is left as it was.
func (e *Engine) dumpRestore(ctx context.Context, id int64, st *inventory.Stack, cp *Checkpoint, op plan.Operation) error {
	if op.Engine != "postgres" {
		return fmt.Errorf("pas de méthode d'export et de réimport pour le moteur %q", op.Engine)
	}
	svc := service(st, op.Service)
	if svc == nil {
		return fmt.Errorf("service %s introuvable", op.Service)
	}
	repo := svc.Image
	if op.ToImage != "" {
		repo = registry.ParseRef(op.ToImage).Name()
	}
	if err := e.pull(ctx, repo, op.ToTag); err != nil {
		return err
	}
	newPath, err := e.dataPath(ctx, repo+":"+op.ToTag)
	if err != nil {
		return err
	}
	oldPath, err := e.dataPath(ctx, svc.ImageID)
	if err != nil {
		return err
	}
	var detail docker.ContainerDetail
	if err := e.Docker.Get(ctx, "/containers/"+svc.Container+"/json", &detail); err != nil {
		return err
	}
	user := "postgres"
	for _, env := range detail.Config.Env {
		if v, ok := strings.CutPrefix(env, "POSTGRES_USER="); ok && v != "" {
			user = v
		}
	}

	clients := dependencyOrder(st, clientsOf(st, svc.Name))
	slices.Reverse(clients)
	if len(clients) > 0 {
		e.event(id, "arrêt de %s pendant l'export de %s", strings.Join(clients, ", "), svc.Name)
		if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"stop"}, clients...)...); err != nil {
			return err
		}
	}
	before, err := e.pgSummary(ctx, cp.Project, svc.Name, user)
	if err != nil {
		return fmt.Errorf("lecture de la base avant l'export : %w", err)
	}
	dumpPath := filepath.Join(e.DataDir, "dumps", fmt.Sprintf("run%d-%s.sql", id, svc.Name))
	if err := os.MkdirAll(filepath.Dir(dumpPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = compose.RunIO(ctx, e.DockerHost, cp.Project, nil, f, "exec", "-T", svc.Name, "pg_dumpall", "-U", user)
	f.Close()
	if err != nil {
		return fmt.Errorf("export de %s : %w", svc.Name, err)
	}
	size := int64(0)
	if fi, err := os.Stat(dumpPath); err == nil {
		size = fi.Size()
	}
	e.event(id, "export de %s terminé : %d octets, %s", svc.Name, size, summaryLine(before))
	if _, err := compose.Run(ctx, e.DockerHost, cp.Project, "stop", svc.Name); err != nil {
		return err
	}

	file, err := compose.ImageFile(cp.Project.ConfigFiles, svc.Name)
	if err != nil {
		return err
	}
	fs, err := compose.ReadState(file)
	if err != nil {
		return err
	}
	var newVolume string
	data, err := compose.Edit([]byte(fs.Data), func(root *yaml.Node) error {
		old, err := compose.MountSource(root, svc.Name, oldPath)
		if err != nil {
			return err
		}
		newVolume = fmt.Sprintf("%s-%s", old, strings.SplitN(op.ToTag, ".", 2)[0])
		return compose.ReplaceMount(root, svc.Name, oldPath, newVolume, newPath)
	})
	if err == nil {
		data, err = compose.SetImage(data, svc.Name, repo+":"+op.ToTag)
	}
	if err != nil {
		return err
	}
	full := cp.Project.Name + "_" + newVolume
	if e.Docker.Get(ctx, "/volumes/"+url.PathEscape(full), &struct{}{}) == nil {
		return fmt.Errorf("le volume %s existe déjà : Palier n'écrit pas sur des données qu'il n'a pas créées", full)
	}
	cp.CreatedVolumes = append(cp.CreatedVolumes, full)
	if err := e.save(id, cp); err != nil {
		return err
	}
	fs.Data = string(data)
	if err := compose.Write(fs); err != nil {
		return err
	}
	e.event(id, "%s : %s passe en %s:%s sur le nouveau volume %s monté sur %s ; l'ancien volume n'est plus monté", file, svc.Name, repo, op.ToTag, newVolume, newPath)
	if _, err := compose.Run(ctx, e.DockerHost, cp.Project, "up", "-d", "--no-deps", svc.Name); err != nil {
		return err
	}
	if err := e.verify(ctx, id, cp.Project.Name, []string{svc.Name}, 5*time.Second); err != nil {
		return err
	}
	in, err := os.Open(dumpPath)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	err = compose.RunIO(ctx, e.DockerHost, cp.Project, in, &out, "exec", "-T", svc.Name, "psql", "-q", "-v", "ON_ERROR_STOP=0", "-U", user, "-d", "postgres")
	in.Close()
	if err != nil {
		return fmt.Errorf("réimport dans %s : %w", svc.Name, err)
	}
	after, err := e.pgSummary(ctx, cp.Project, svc.Name, user)
	if err != nil {
		return fmt.Errorf("lecture de la base après le réimport : %w", err)
	}
	if diff := compareSummaries(before, after); diff != "" {
		return fmt.Errorf("la base réimportée diffère de l'export : %s", diff)
	}
	e.event(id, "réimport vérifié, identique à l'export : %s", summaryLine(after))
	os.Remove(dumpPath)
	if len(clients) > 0 {
		slices.Reverse(clients)
		e.event(id, "démarrage de %s", strings.Join(clients, ", "))
		if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"up", "-d", "--no-deps"}, clients...)...); err != nil {
			return err
		}
	}
	return nil
}

func service(st *inventory.Stack, name string) *inventory.Service {
	for _, s := range st.Services {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func clientsOf(st *inventory.Stack, name string) []string {
	var out []string
	for _, s := range st.Services {
		if slices.Contains(s.DependsOn, name) {
			out = append(out, s.Name)
		}
	}
	return out
}

func (e *Engine) pull(ctx context.Context, repo, tag string) error {
	if e.Docker.Get(ctx, "/images/"+repo+":"+tag+"/json", &struct{}{}) == nil {
		return nil
	}
	if err := e.Docker.Post(ctx, "/images/create?"+url.Values{"fromImage": {repo}, "tag": {tag}}.Encode(), nil, nil); err != nil {
		return err
	}
	if err := e.Docker.Get(ctx, "/images/"+repo+":"+tag+"/json", &struct{}{}); err != nil {
		return fmt.Errorf("%s:%s introuvable après le téléchargement : %w", repo, tag, err)
	}
	return nil
}

// dataPath returns the single VOLUME an image declares, where its data lives.
func (e *Engine) dataPath(ctx context.Context, image string) (string, error) {
	var img struct {
		Config struct{ Volumes map[string]struct{} }
	}
	if err := e.Docker.Get(ctx, "/images/"+image+"/json", &img); err != nil {
		return "", err
	}
	if len(img.Config.Volumes) != 1 {
		return "", fmt.Errorf("l'image %s déclare %d volumes, Palier attend un seul répertoire de données", image, len(img.Config.Volumes))
	}
	for path := range img.Config.Volumes {
		return path, nil
	}
	return "", nil
}

// pgSummary lists roles, databases, and the tables of each database with their row counts.
func (e *Engine) pgSummary(ctx context.Context, p compose.Project, svc, user string) ([]string, error) {
	query := func(db, sql string) ([]string, error) {
		var out bytes.Buffer
		if err := compose.RunIO(ctx, e.DockerHost, p, nil, &out, "exec", "-T", svc, "psql", "-U", user, "-d", db, "-At", "-c", sql); err != nil {
			return nil, err
		}
		return strings.Fields(out.String()), nil
	}
	roles, err := query("postgres", `SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	dbs, err := query("postgres", `SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, r := range roles {
		lines = append(lines, "role "+r)
	}
	for _, db := range dbs {
		lines = append(lines, "database "+db)
		tables, err := query(db, pgTables)
		if err != nil {
			return nil, err
		}
		for _, t := range tables {
			lines = append(lines, "table "+db+"."+t)
		}
		for kind, sql := range map[string]string{"index": pgIndexes, "constraint": pgConstraints} {
			names, err := query(db, sql)
			if err != nil {
				return nil, err
			}
			for _, n := range names {
				lines = append(lines, kind+" "+db+"."+n)
			}
		}
	}
	return lines, nil
}

func summaryLine(lines []string) string {
	var roles, dbs, tables, rows, indexes, constraints int
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "role "):
			roles++
		case strings.HasPrefix(l, "database "):
			dbs++
		case strings.HasPrefix(l, "index "):
			indexes++
		case strings.HasPrefix(l, "constraint "):
			constraints++
		case strings.HasPrefix(l, "table "):
			tables++
			var n int
			fmt.Sscanf(l[strings.LastIndex(l, "=")+1:], "%d", &n)
			rows += n
		}
	}
	return fmt.Sprintf("rôles %d, bases %d, tables %d, lignes %d, index %d, contraintes %d", roles, dbs, tables, rows, indexes, constraints)
}

func compareSummaries(before, after []string) string {
	var diffs []string
	for _, l := range before {
		if !slices.Contains(after, l) {
			diffs = append(diffs, "manque "+l)
		}
	}
	for _, l := range after {
		if !slices.Contains(before, l) {
			diffs = append(diffs, "en trop "+l)
		}
	}
	if len(diffs) > 5 {
		diffs = append(diffs[:5], fmt.Sprintf("et %d autres différences", len(diffs)-5))
	}
	return strings.Join(diffs, ", ")
}
