package run

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/kolapsis/palier/internal/compose"
	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/plan"
)

const (
	rcloneRepo  = "docker.io/rclone/rclone"
	rcloneTag   = "1.75.2"
	stampLayout = "20060102T150405Z"
)

// Archive is one backup sent off the machine before a run, or the reason it could not be.
type Archive struct {
	Service   string   `json:"service"`
	Kind      string   `json:"kind"`
	Engine    string   `json:"engine,omitempty"`
	Databases []string `json:"databases,omitempty"`
	Location  string   `json:"location,omitempty"`
	Size      int64    `json:"size"`
	SHA256    string   `json:"sha256,omitempty"`
	Seconds   int      `json:"seconds"`
	Error     string   `json:"error,omitempty"`
	Run       int64    `json:"run"`
	At        string   `json:"at"`
}

// backups makes and sends off the machine the backups the palier.backup labels of st ask for, then keeps the BackupKeep latest of each service.
func (e *Engine) backups(ctx context.Context, id int64, st *inventory.Stack) error {
	stamp := time.Now().UTC().Format(stampLayout)
	var done [][2]string
	for _, s := range st.Services {
		to := cmp.Or(s.BackupTo, e.BackupTo)
		for _, kind := range strings.Split(s.Backup, ",") {
			if kind = strings.TrimSpace(kind); kind == "" {
				continue
			}
			a := Archive{Service: s.Name, Kind: kind}
			var err error
			switch {
			case kind != "dump" && kind != "volumes":
				err = fmt.Errorf("palier.backup is %q, expected dump, volumes, or both separated by a comma", s.Backup)
			case s.BackupTo != "" && !config.Destination.MatchString(s.BackupTo):
				err = fmt.Errorf("palier.backup.to is %q, expected an rclone remote and a path such as nas:backups", s.BackupTo)
			case to == "":
				err = errors.New("palier.backup asks for a backup, but neither palier.backup.to nor PALIER_BACKUP_TO is set")
			case kind == "dump":
				a.Engine, a.Location = plan.EngineOf(s.Image), remotePath(to, st.Name, s.Name, stamp, "dump.sql")
				err = e.dump(ctx, ProjectOf(st), s, &a)
			default:
				a.Location = remotePath(to, st.Name, s.Name, stamp, "volumes.tar")
				err = e.archive(ctx, id, st, s, &a)
			}
			if err != nil {
				a.Error = err.Error()
				e.event(id, "backup of %s (%s) failed: %v", s.Name, kind, err)
			} else {
				dbs := ""
				if len(a.Databases) > 0 {
					dbs = ", databases " + strings.Join(a.Databases, ", ")
				}
				e.event(id, "backup of %s (%s%s) sent to %s: %d bytes, sha256 %s, %d s", s.Name, kind, dbs, a.Location, a.Size, a.SHA256, a.Seconds)
			}
			a.Run, a.At = id, now()
			body, _ := json.Marshal(a)
			if _, err := e.DB.Exec(`INSERT INTO backups (stack, service, kind, ok, body) VALUES (?, ?, ?, ?, ?)`, st.Name, s.Name, kind, a.Error == "", string(body)); err != nil {
				slog.Error("backup record", "run", id, "err", err)
			}
			if e.BackedUp != nil {
				e.BackedUp(st.Name, id, a)
			}
			if err != nil {
				return fmt.Errorf("the backup of %s failed, nothing is applied: %w", s.Name, err)
			}
			if d := [2]string{s.Name, to}; !slices.Contains(done, d) {
				done = append(done, d)
			}
		}
	}
	for _, d := range done {
		e.rotate(ctx, id, remotePath(d[1], st.Name, d[0]))
	}
	return nil
}

// LastBackups returns, for each service and kind of backup of stack, the last one sent, then the last failure when it came after.
func LastBackups(ctx context.Context, db *sql.DB, stack string) ([]Archive, error) {
	rows, err := db.QueryContext(ctx, `SELECT body FROM backups WHERE id IN (SELECT max(id) FROM backups WHERE stack = ? GROUP BY service, kind, ok) ORDER BY service, kind, id`, stack)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Archive
	for rows.Next() {
		var body string
		var a Archive
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &a); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].Service == a.Service && out[n-1].Kind == a.Kind && out[n-1].Error != "" {
			out = out[:n-1]
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// remotePath joins parts under the rclone destination root.
func remotePath(root string, parts ...string) string {
	if !strings.HasSuffix(root, ":") {
		root = strings.TrimSuffix(root, "/") + "/"
	}
	return root + strings.Join(parts, "/")
}

// dump streams the logical export of the database of s, from its running container, to a.Location.
func (e *Engine) dump(ctx context.Context, p compose.Project, s *inventory.Service, a *Archive) error {
	if a.Engine == "" {
		return fmt.Errorf("palier.backup: dump on %s, whose image is not a PostgreSQL, MySQL or MariaDB database", s.Name)
	}
	var detail docker.ContainerDetail
	if err := e.Docker.Get(ctx, "/containers/"+s.Container+"/json", &detail); err != nil {
		return err
	}
	m, err := e.method(ctx, p, s.Name, a.Engine, detail.Config.Env)
	if err != nil {
		return err
	}
	if a.Databases, err = m.databases(); err != nil {
		return fmt.Errorf("database list of %s: %w", s.Name, err)
	}
	cmd, err := m.dump(ctx)
	if err != nil {
		return err
	}
	return e.send(ctx, a, func(w io.Writer) error { return compose.RunIO(ctx, e.DockerHost, p, m.env, nil, w, m.exec(cmd)...) })
}

// archive stops s and its clients, streams a tar of its data mounts to a.Location, and starts them again.
func (e *Engine) archive(ctx context.Context, id int64, st *inventory.Stack, s *inventory.Service, a *Archive) error {
	copies, bulk := dataMounts(s)
	for _, b := range bulk {
		e.event(id, "%s: %s holds data the upgrade does not rewrite, not archived", s.Name, b)
	}
	if len(copies) == 0 {
		return fmt.Errorf("palier.backup: volumes on %s, which mounts no data", s.Name)
	}
	if err := e.ensureHelper(ctx); err != nil {
		return err
	}
	if err := e.modified(id); err != nil {
		return err
	}
	stopped := dependencyOrder(st, append([]string{s.Name}, clientsOf(st, s.Name)...))
	slices.Reverse(stopped)
	e.event(id, "stopping %s to archive the volumes of %s", strings.Join(stopped, ", "), s.Name)
	p := ProjectOf(st)
	if _, err := compose.Run(ctx, e.DockerHost, p, append([]string{"stop"}, stopped...)...); err != nil {
		return err
	}
	err := e.send(ctx, a, func(w io.Writer) error { return e.tar(ctx, copies, w) })
	slices.Reverse(stopped)
	e.event(id, "starting %s", strings.Join(stopped, ", "))
	if _, serr := compose.Run(context.WithoutCancel(ctx), e.DockerHost, p, append([]string{"start"}, stopped...)...); serr != nil {
		return errors.Join(err, serr)
	}
	return err
}

// tar writes to w an archive of the mounts of copies, each under its path in the container, without their exclusions.
func (e *Engine) tar(ctx context.Context, copies []Snapshot, w io.Writer) error {
	args := []string{"tar", "-C", "/backup", "-cf", "-"}
	var mounts []map[string]any
	var paths []string
	for _, c := range copies {
		rel := strings.TrimPrefix(c.Mount.Destination, "/")
		mounts = append(mounts, map[string]any{"Type": c.Mount.Type, "Source": c.Mount.Source, "Target": "/backup/" + rel, "ReadOnly": true})
		for _, x := range c.Exclude {
			args = append(args, "--exclude", rel+"/"+x)
		}
		paths = append(paths, rel)
	}
	return e.throwaway(ctx, map[string]any{
		"Image":      helperImage,
		"Cmd":        append(args, paths...),
		"Labels":     map[string]string{"palier.helper": "backup"},
		"HostConfig": map[string]any{"Mounts": mounts},
	}, nil, w)
}

// send uploads what write produces to a.Location, then its .sha256 file, and records size, SHA-256 and duration in a.
func (e *Engine) send(ctx context.Context, a *Archive, write func(io.Writer) error) error {
	began := time.Now()
	size, sum, err := e.upload(ctx, a.Location, write)
	if err == nil {
		line := sum + "  " + path.Base(a.Location) + "\n"
		_, _, err = e.upload(ctx, a.Location+".sha256", func(w io.Writer) error { _, err := io.WriteString(w, line); return err })
	}
	a.Size, a.SHA256, a.Seconds = size, sum, int(time.Since(began).Seconds())
	return err
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// upload streams what write produces to dest through rclone rcat, without touching a disk, and returns its size and SHA-256.
func (e *Engine) upload(ctx context.Context, dest string, write func(io.Writer) error) (int64, string, error) {
	pr, pw := io.Pipe()
	wrote := make(chan error, 1)
	go func() {
		err := write(pw)
		pw.CloseWithError(err)
		wrote <- err
	}()
	h, n := sha256.New(), &counter{}
	err := e.rclone(ctx, io.TeeReader(pr, io.MultiWriter(h, n)), nil, "rcat", dest)
	pr.CloseWithError(errors.New("upload stopped"))
	werr := <-wrote
	if err == nil && werr == nil {
		return n.n, hex.EncodeToString(h.Sum(nil)), nil
	}
	if werr == nil || err != nil && !errors.Is(err, werr) {
		return 0, "", err
	}
	if derr := e.rclone(context.WithoutCancel(ctx), nil, nil, "deletefile", dest); derr != nil {
		return 0, "", fmt.Errorf("%w; the partial file %s may remain: %v", werr, dest, derr)
	}
	return 0, "", fmt.Errorf("%w; the partial file %s was deleted", werr, dest)
}

// rclone runs rclone with args in a throwaway container that gets the RCLONE_ variables of Palier.
func (e *Engine) rclone(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	if err := e.pull(ctx, rcloneRepo, rcloneTag); err != nil {
		return err
	}
	return e.throwaway(ctx, map[string]any{
		"Image":       rcloneRepo + ":" + rcloneTag,
		"Cmd":         append(args, "-q"),
		"Env":         e.Rclone,
		"Labels":      map[string]string{"palier.helper": "backup"},
		"AttachStdin": stdin != nil,
		"OpenStdin":   stdin != nil,
		"StdinOnce":   stdin != nil,
	}, stdin, stdout)
}

// throwaway runs a container of body to its end, fed with stdin or with its output copied to stdout, and removes it.
func (e *Engine) throwaway(ctx context.Context, body map[string]any, stdin io.Reader, stdout io.Writer) error {
	var created struct {
		ID string `json:"Id"`
	}
	if err := e.Docker.Post(ctx, "/containers/create", body, &created); err != nil {
		return err
	}
	defer e.Docker.Delete(context.WithoutCancel(ctx), "/containers/"+created.ID+"?force=1")
	if stdin == nil && stdout == nil {
		stdout = io.Discard
	}
	streamErr := e.Docker.Stream(ctx, created.ID, stdin, stdout)
	var exit struct{ StatusCode int }
	if err := e.Docker.Post(ctx, "/containers/"+created.ID+"/wait", nil, &exit); err != nil {
		return errors.Join(streamErr, err)
	}
	if exit.StatusCode != 0 {
		logs, _ := e.Docker.Logs(ctx, created.ID, 20)
		return fmt.Errorf("%s exited with code %d: %s", body["Image"], exit.StatusCode, strings.TrimSpace(logs))
	}
	return streamErr
}

// rotate keeps the BackupKeep latest backups under dir and deletes the older ones.
func (e *Engine) rotate(ctx context.Context, id int64, dir string) {
	var out bytes.Buffer
	if err := e.rclone(ctx, nil, &out, "lsf", "--dirs-only", dir); err != nil {
		e.event(id, "old backups under %s not listed: %v", dir, err)
		return
	}
	for _, name := range expired(strings.Fields(out.String()), e.BackupKeep) {
		if err := e.rclone(ctx, nil, nil, "purge", dir+"/"+name); err != nil {
			e.event(id, "old backup %s/%s not removed: %v", dir, name, err)
			continue
		}
		e.event(id, "old backup %s/%s removed, the %d latest kept", dir, name, e.BackupKeep)
	}
}

// expired returns the backup directories, as rclone lsf lists them, beyond the keep latest, oldest first.
func expired(names []string, keep int) []string {
	var stamps []string
	for _, n := range names {
		n = strings.TrimSuffix(n, "/")
		if _, err := time.Parse(stampLayout, n); err == nil {
			stamps = append(stamps, n)
		}
	}
	slices.Sort(stamps)
	if len(stamps) <= keep {
		return nil
	}
	return stamps[:len(stamps)-keep]
}
