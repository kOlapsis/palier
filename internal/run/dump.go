package run

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	svc := service(st, op.Service)
	if svc == nil {
		return fmt.Errorf("service %s not found", op.Service)
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
	m, err := e.method(ctx, cp.Project, svc.Name, op.Engine, detail.Config.Env)
	if err != nil {
		return err
	}

	clients := dependencyOrder(st, clientsOf(st, svc.Name))
	slices.Reverse(clients)
	if len(clients) > 0 {
		e.event(id, "stopping %s during the export of %s", strings.Join(clients, ", "), svc.Name)
		if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"stop"}, clients...)...); err != nil {
			return err
		}
	}
	if err := e.waitReady(ctx, cp.Project, m); err != nil {
		return err
	}
	before, err := m.summary(ctx)
	if err != nil {
		return fmt.Errorf("reading the database before the export: %w", err)
	}
	dumpPath := filepath.Join(e.DataDir, "dumps", fmt.Sprintf("run%d-%s.sql", id, svc.Name))
	if err := os.MkdirAll(filepath.Dir(dumpPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	dump, err := m.dump(ctx)
	if err == nil {
		err = compose.RunIO(ctx, e.DockerHost, cp.Project, nil, f, m.exec(dump)...)
	}
	f.Close()
	if err != nil {
		return fmt.Errorf("export of %s: %w", svc.Name, err)
	}
	size := int64(0)
	if fi, err := os.Stat(dumpPath); err == nil {
		size = fi.Size()
	}
	e.event(id, "export of %s done: %d bytes, %s", svc.Name, size, summaryLine(before))
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
		newVolume = newVolumeName(old, svc.Name, op.ToTag)
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
		return fmt.Errorf("volume %s already exists: Palier does not write over data it did not create", full)
	}
	cp.CreatedVolumes = append(cp.CreatedVolumes, full)
	if err := e.save(id, cp); err != nil {
		return err
	}
	fs.Data = string(data)
	if err := compose.Write(fs); err != nil {
		return err
	}
	e.event(id, "%s: %s set to %s:%s on the new volume %s mounted on %s; the old volume is no longer mounted", file, svc.Name, repo, op.ToTag, newVolume, newPath)
	if _, err := compose.Run(ctx, e.DockerHost, cp.Project, "up", "-d", "--no-deps", svc.Name); err != nil {
		return err
	}
	if err := e.waitReady(ctx, cp.Project, m); err != nil {
		return err
	}
	in, err := os.Open(dumpPath)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	err = compose.RunIO(ctx, e.DockerHost, cp.Project, in, &out, m.exec(m.restore)...)
	in.Close()
	if err != nil {
		return fmt.Errorf("reimport into %s: %w", svc.Name, err)
	}
	after, err := m.summary(ctx)
	if err != nil {
		return fmt.Errorf("reading the database after the reimport: %w", err)
	}
	if diff := compareSummaries(before, after); diff != "" {
		return fmt.Errorf("the reimported database differs from the export: %s", diff)
	}
	e.event(id, "reimport verified, identical to the export: %s", summaryLine(after))
	os.Remove(dumpPath)
	if len(clients) > 0 {
		slices.Reverse(clients)
		e.event(id, "starting %s", strings.Join(clients, ", "))
		if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"up", "-d", "--no-deps"}, clients...)...); err != nil {
			return err
		}
	}
	return nil
}

var volumeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// newVolumeName names the volume of a new major: the old named volume with the major, or one named after the service when the data sat in a host directory.
func newVolumeName(old, service, tag string) string {
	major := strings.SplitN(tag, ".", 2)[0]
	if volumeName.MatchString(old) {
		return old + "-" + major
	}
	return service + "-data-" + major
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
		return fmt.Errorf("%s:%s not found after the pull: %w", repo, tag, err)
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
		return "", fmt.Errorf("image %s declares %d volumes, Palier expects a single data directory", image, len(img.Config.Volumes))
	}
	for path := range img.Config.Volumes {
		return path, nil
	}
	return "", nil
}

// dbMethod holds the commands of one database engine, run in its container through docker compose exec.
type dbMethod struct {
	service string
	env     []string
	restore []string
	ready   []string
	dump    func(ctx context.Context) ([]string, error)
	summary func(ctx context.Context) ([]string, error)
}

func (m *dbMethod) exec(cmd []string) []string {
	args := []string{"exec", "-T"}
	for _, e := range m.env {
		args = append(args, "-e", e)
	}
	return append(append(args, m.service), cmd...)
}

func envValue(env []string, names ...string) string {
	for _, name := range names {
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, name+"="); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

const mysqlSystem = "('mysql', 'information_schema', 'performance_schema', 'sys')"

// method returns the export, import, readiness and comparison commands of engine for the service in its container.
func (e *Engine) method(ctx context.Context, p compose.Project, service, engine string, env []string) (*dbMethod, error) {
	m := &dbMethod{service: service}
	run := func(cmd ...string) ([]string, error) {
		var out bytes.Buffer
		if err := compose.RunIO(ctx, e.DockerHost, p, nil, &out, m.exec(cmd)...); err != nil {
			return nil, err
		}
		return strings.Fields(out.String()), nil
	}
	switch engine {
	case "postgres":
		user := envValue(env, "POSTGRES_USER")
		if user == "" {
			user = "postgres"
		}
		m.restore = []string{"psql", "-q", "-v", "ON_ERROR_STOP=0", "-U", user, "-d", "postgres"}
		m.ready = []string{"pg_isready", "-U", user, "-h", "127.0.0.1"}
		m.dump = func(context.Context) ([]string, error) { return []string{"pg_dumpall", "-U", user}, nil }
		m.summary = func(context.Context) ([]string, error) {
			return summarize(func(db, sql string) ([]string, error) { return run("psql", "-U", user, "-d", db, "-At", "-c", sql) },
				`SELECT rolname FROM pg_roles WHERE rolname !~ '^pg_' ORDER BY 1`,
				`SELECT datname FROM pg_database WHERE NOT datistemplate ORDER BY 1`, "postgres",
				func(db string) []string { return []string{pgTables, pgIndexes, pgConstraints} })
		}
	case "mysql", "mariadb":
		password := envValue(env, "MARIADB_ROOT_PASSWORD", "MYSQL_ROOT_PASSWORD")
		if password == "" {
			return nil, fmt.Errorf("%s has no root password in its environment: Palier cannot export it", service)
		}
		client, dumper := "mysql", "mysqldump"
		if engine == "mariadb" {
			client, dumper = "mariadb", "mariadb-dump"
		}
		m.env = []string{"MYSQL_PWD=" + password}
		m.restore = []string{client, "-uroot"}
		m.ready = []string{client, "-uroot", "-h", "127.0.0.1", "--protocol=TCP", "-e", "SELECT 1"}
		sqlQuery := func(_, sql string) ([]string, error) { return run(client, "-uroot", "-N", "-B", "-e", sql) }
		databases := "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN " + mysqlSystem + " ORDER BY 1"
		m.dump = func(context.Context) ([]string, error) {
			dbs, err := sqlQuery("", databases)
			if err != nil || len(dbs) == 0 {
				return nil, fmt.Errorf("database list of %s: %v", service, err)
			}
			return append([]string{dumper, "-uroot", "--single-transaction", "--routines", "--events", "--triggers", "--databases"}, dbs...), nil
		}
		m.summary = func(context.Context) ([]string, error) {
			return summarize(sqlQuery,
				"SELECT CONCAT(user, '@', host) FROM mysql.user WHERE user NOT IN ('root', 'mysql.sys', 'mysql.session', 'mysql.infoschema', 'mariadb.sys', 'healthcheck') ORDER BY 1",
				databases, "",
				func(db string) []string {
					return []string{
						"",
						"SELECT DISTINCT CONCAT(table_name, '.', index_name) FROM information_schema.statistics WHERE table_schema = '" + db + "' ORDER BY 1",
						"SELECT CONCAT(table_name, '.', constraint_name, '.', constraint_type) FROM information_schema.table_constraints WHERE table_schema = '" + db + "' ORDER BY 1",
					}
				}, func(db string) ([]string, error) {
					tables, err := sqlQuery("", "SELECT table_name FROM information_schema.tables WHERE table_type = 'BASE TABLE' AND table_schema = '"+db+"' ORDER BY 1")
					if err != nil {
						return nil, err
					}
					var out []string
					for _, t := range tables {
						n, err := sqlQuery("", "SELECT COUNT(*) FROM `"+db+"`.`"+t+"`")
						if err != nil || len(n) == 0 {
							return nil, fmt.Errorf("counting %s.%s: %v", db, t, err)
						}
						out = append(out, t+"="+n[0])
					}
					return out, nil
				})
		}
	default:
		return nil, fmt.Errorf("no export and reimport method for engine %q", engine)
	}
	return m, nil
}

// summarize lists roles, databases and, for each database, the result of each query of perDB; counted, when given,
// returns the tables of a database with their exact row counts instead of the first query.
func summarize(query func(db, sql string) ([]string, error), roles, databases, adminDB string, perDB func(db string) []string, counted ...func(db string) ([]string, error)) ([]string, error) {
	names, err := query(adminDB, roles)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, r := range names {
		lines = append(lines, "role "+r)
	}
	dbs, err := query(adminDB, databases)
	if err != nil {
		return nil, err
	}
	for _, db := range dbs {
		lines = append(lines, "database "+db)
		for i, sql := range perDB(db) {
			var rows []string
			if i == 0 && len(counted) > 0 {
				rows, err = counted[0](db)
			} else {
				rows, err = query(db, sql)
			}
			if err != nil {
				return nil, err
			}
			kind := []string{"table", "index", "constraint"}[i]
			for _, r := range rows {
				lines = append(lines, kind+" "+db+"."+r)
			}
		}
	}
	return lines, nil
}

// waitReady waits until the database of the new container accepts connections over TCP, which its first-start
// initialisation server does not.
func (e *Engine) waitReady(ctx context.Context, p compose.Project, m *dbMethod) error {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		err := compose.RunIO(ctx, e.DockerHost, p, nil, &bytes.Buffer{}, m.exec(m.ready)...)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s does not accept connections after 5 min: %w", m.service, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
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
	return fmt.Sprintf("roles %d, databases %d, tables %d, rows %d, indexes %d, constraints %d", roles, dbs, tables, rows, indexes, constraints)
}

func compareSummaries(before, after []string) string {
	var diffs []string
	for _, l := range before {
		if !slices.Contains(after, l) {
			diffs = append(diffs, "missing "+l)
		}
	}
	for _, l := range after {
		if !slices.Contains(before, l) {
			diffs = append(diffs, "extra "+l)
		}
	}
	if len(diffs) > 5 {
		diffs = append(diffs[:5], fmt.Sprintf("and %d more differences", len(diffs)-5))
	}
	return strings.Join(diffs, ", ")
}

const pgStaleCollations = `SELECT datname FROM pg_database WHERE datallowconn AND datcollversion IS NOT NULL AND datcollversion <> pg_database_collation_actual_version(oid) ORDER BY 1`

// refreshCollations rebuilds the indexes of every database whose collation version no longer matches the C library of
// the image, then records the new version, as PostgreSQL asks after such a change.
func (e *Engine) refreshCollations(ctx context.Context, id int64, cp *Checkpoint, svc *inventory.Service) error {
	var detail docker.ContainerDetail
	if err := e.Docker.Get(ctx, "/containers/"+svc.Container+"/json", &detail); err != nil {
		return nil
	}
	user := "postgres"
	for _, env := range detail.Config.Env {
		if v, ok := strings.CutPrefix(env, "POSTGRES_USER="); ok && v != "" {
			user = v
		}
	}
	var out bytes.Buffer
	if err := compose.RunIO(ctx, e.DockerHost, cp.Project, nil, &out, "exec", "-T", svc.Name, "psql", "-U", user, "-d", "postgres", "-At", "-c", pgStaleCollations); err != nil {
		return nil
	}
	for _, db := range strings.Fields(out.String()) {
		e.event(id, "%s: database %s has a stale collation version, REINDEX then REFRESH COLLATION VERSION", svc.Name, db)
		quoted := `"` + strings.ReplaceAll(db, `"`, `""`) + `"`
		for _, sql := range []string{"REINDEX DATABASE " + quoted, "ALTER DATABASE " + quoted + " REFRESH COLLATION VERSION"} {
			if err := compose.RunIO(ctx, e.DockerHost, cp.Project, nil, &bytes.Buffer{}, "exec", "-T", svc.Name, "psql", "-v", "ON_ERROR_STOP=1", "-U", user, "-d", db, "-c", sql); err != nil {
				return fmt.Errorf("%s on %s: %w", sql, db, err)
			}
		}
	}
	return nil
}
