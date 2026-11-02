package run

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/kolapsis/palier/internal/compose"
	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/inventory"
)

const (
	helperRepo  = "docker.io/library/busybox"
	helperTag   = "1.37.0"
	helperImage = helperRepo + ":" + helperTag
	copyTimeout = 2 * time.Hour
)

// Snapshot is a copy, in a volume of its own, of what a service had mounted before the run.
type Snapshot struct {
	Service string          `json:"service"`
	Mount   inventory.Mount `json:"mount"`
	Volume  string          `json:"volume"`
}

// snapshot stops the services the plan changes, copies their volumes and host directories, and starts them again.
func (e *Engine) snapshot(ctx context.Context, id int64, st *inventory.Stack, cp *Checkpoint, changed []string) error {
	var targets []*inventory.Service
	for _, s := range st.Services {
		if slices.Contains(changed, s.Name) && len(dataMounts(s)) > 0 {
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		cp.SnapshotsDone = true
		return nil
	}
	if err := e.ensureHelper(ctx); err != nil {
		return err
	}
	var names []string
	for _, s := range targets {
		names = append(names, s.Name)
	}
	ordered := dependencyOrder(st, names)
	reversed := slices.Clone(ordered)
	slices.Reverse(reversed)
	e.event(id, "stopping %s to copy their volumes", strings.Join(reversed, ", "))
	if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"stop"}, reversed...)...); err != nil {
		return err
	}
	for _, s := range targets {
		for i, m := range dataMounts(s) {
			snap := Snapshot{Service: s.Name, Mount: m, Volume: fmt.Sprintf("palier-run%d-step%d-%s-%d", id, cp.Step, s.Name, i)}
			if err := e.Docker.Post(ctx, "/volumes/create", map[string]any{"Name": snap.Volume, "Labels": map[string]string{"palier.run": fmt.Sprint(id)}}, nil); err != nil {
				return err
			}
			cp.Snapshots = append(cp.Snapshots, snap)
			err := e.copy(ctx, id, m.Type, m.Source, "volume", snap.Volume, false)
			if errors.Is(err, errNotDir) {
				cp.Snapshots = cp.Snapshots[:len(cp.Snapshots)-1]
				if err := e.Docker.Delete(ctx, "/volumes/"+snap.Volume); err != nil {
					return err
				}
				e.event(id, "%s: %s is a single file, not copied", s.Name, m.Destination)
				continue
			}
			if err != nil {
				return fmt.Errorf("copying %s (%s): %w", s.Name, m.Destination, err)
			}
			e.event(id, "%s: %s copied to volume %s", s.Name, m.Destination, snap.Volume)
		}
	}
	cp.SnapshotsDone = true
	if err := e.save(id, cp); err != nil {
		return err
	}
	e.event(id, "starting %s", strings.Join(ordered, ", "))
	_, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"start"}, ordered...)...)
	return err
}

func (e *Engine) ensureHelper(ctx context.Context) error {
	if e.Docker.Get(ctx, "/images/"+helperImage+"/json", &struct{}{}) == nil {
		return nil
	}
	q := url.Values{"fromImage": {helperRepo}, "tag": {helperTag}}
	if err := e.Docker.Post(ctx, "/images/create?"+q.Encode(), nil, nil); err != nil {
		return err
	}
	return e.Docker.Get(ctx, "/images/"+helperImage+"/json", &struct{}{})
}

var (
	hostSystemPaths = []string{"/proc", "/sys", "/dev", "/run", "/var/run", "/tmp", "/usr/share/zoneinfo", "/lib/modules", "/var/lib/docker"}
	logPaths        = []string{"/var/log"}
)

func under(path string, roots []string) bool {
	return slices.ContainsFunc(roots, func(r string) bool { return path == r || strings.HasPrefix(path, r+"/") })
}

// dataMounts returns the mounts of s that may hold application data, leaving out host system paths and logs.
func dataMounts(s *inventory.Service) []inventory.Mount {
	var out []inventory.Mount
	for _, m := range s.Mounts {
		if m.Type == "bind" && (m.Source == "/" || under(m.Source, hostSystemPaths)) || under(m.Destination, logPaths) {
			continue
		}
		out = append(out, m)
	}
	return out
}

var errNotDir = errors.New("not a directory")

// copy copies the content of one mount into another with a throwaway container, emptying the target first when wipe is set.
func (e *Engine) copy(ctx context.Context, id int64, fromType, from, toType, to string, wipe bool) error {
	script := "cp -a /from/. /to/"
	if wipe {
		script = "find /to -mindepth 1 -maxdepth 1 -exec rm -rf {} + && " + script
	}
	script = "[ -d /from ] || exit 3; " + script
	body := map[string]any{
		"Image":  helperImage,
		"Cmd":    []string{"sh", "-c", script},
		"Labels": map[string]string{"palier.helper": "copy"},
		"HostConfig": map[string]any{"Mounts": []map[string]any{
			{"Type": fromType, "Source": from, "Target": "/from"},
			{"Type": toType, "Source": to, "Target": "/to"},
		}},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := e.Docker.Post(ctx, "/containers/create", body, &created); err != nil {
		return err
	}
	defer e.Docker.Delete(context.WithoutCancel(ctx), "/containers/"+created.ID+"?force=1")
	if err := e.Docker.Post(ctx, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		return err
	}
	began := time.Now()
	reported := began
	for {
		var d docker.ContainerDetail
		if err := e.Docker.Get(ctx, "/containers/"+created.ID+"/json", &d); err != nil {
			return err
		}
		if d.State.Status == "exited" || d.State.Status == "dead" {
			if d.State.ExitCode == 3 {
				return errNotDir
			}
			if d.State.ExitCode != 0 {
				logs, _ := e.Docker.Logs(ctx, created.ID, 20)
				return fmt.Errorf("copy failed (code %d): %s", d.State.ExitCode, strings.TrimSpace(logs))
			}
			return nil
		}
		if time.Since(began) > copyTimeout {
			return fmt.Errorf("copy still running after %s", copyTimeout)
		}
		if time.Since(reported) >= time.Minute {
			reported = time.Now()
			e.event(id, "copying %s to %s: %d min so far", from, to, int(time.Since(began).Minutes()))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func (e *Engine) restoreSnapshots(ctx context.Context, id int64, cp *Checkpoint) error {
	if len(cp.Snapshots) == 0 {
		return nil
	}
	var names []string
	for _, s := range cp.Snapshots {
		if !slices.Contains(names, s.Service) {
			names = append(names, s.Service)
		}
	}
	if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"stop"}, names...)...); err != nil {
		return err
	}
	for _, s := range cp.Snapshots {
		if err := e.copy(ctx, id, "volume", s.Volume, s.Mount.Type, s.Mount.Source, true); err != nil {
			return fmt.Errorf("restoring %s (%s): %w", s.Service, s.Mount.Destination, err)
		}
		e.event(id, "%s: %s restored from %s", s.Service, s.Mount.Destination, s.Volume)
	}
	return nil
}

func (e *Engine) dropSnapshots(ctx context.Context, id int64, cp *Checkpoint) {
	for _, s := range cp.Snapshots {
		if err := e.Docker.Delete(ctx, "/volumes/"+s.Volume); err != nil {
			e.event(id, "copy volume %s was not removed: %v", s.Volume, err)
		}
	}
}
