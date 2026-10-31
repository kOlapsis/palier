package run

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/kolapsis/palier/internal/compose"
	"github.com/kolapsis/palier/internal/inventory"
)

const (
	helperRepo  = "docker.io/library/busybox"
	helperTag   = "1.37.0"
	helperImage = helperRepo + ":" + helperTag
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
		if slices.Contains(changed, s.Name) && len(s.Mounts) > 0 {
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
	e.event(id, "arrêt de %s pour copier leurs volumes", strings.Join(reversed, ", "))
	if _, err := compose.Run(ctx, e.DockerHost, cp.Project, append([]string{"stop"}, reversed...)...); err != nil {
		return err
	}
	for _, s := range targets {
		for i, m := range s.Mounts {
			snap := Snapshot{Service: s.Name, Mount: m, Volume: fmt.Sprintf("palier-run%d-%s-%d", id, s.Name, i)}
			if err := e.Docker.Post(ctx, "/volumes/create", map[string]any{"Name": snap.Volume, "Labels": map[string]string{"palier.run": fmt.Sprint(id)}}, nil); err != nil {
				return err
			}
			if err := e.copy(ctx, m.Type, m.Source, "volume", snap.Volume, false); err != nil {
				return fmt.Errorf("copie de %s (%s) : %w", s.Name, m.Destination, err)
			}
			cp.Snapshots = append(cp.Snapshots, snap)
			e.event(id, "%s : %s copié dans le volume %s", s.Name, m.Destination, snap.Volume)
		}
	}
	cp.SnapshotsDone = true
	if err := e.save(id, cp); err != nil {
		return err
	}
	e.event(id, "démarrage de %s", strings.Join(ordered, ", "))
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

// copy copies the content of one mount into another with a throwaway container, emptying the target first when wipe is set.
func (e *Engine) copy(ctx context.Context, fromType, from, toType, to string, wipe bool) error {
	script := "cp -a /from/. /to/"
	if wipe {
		script = "find /to -mindepth 1 -maxdepth 1 -exec rm -rf {} + && " + script
	}
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
	var waited struct{ StatusCode int }
	if err := e.Docker.Post(ctx, "/containers/"+created.ID+"/wait", nil, &waited); err != nil {
		return err
	}
	if waited.StatusCode != 0 {
		logs, _ := e.Docker.Logs(ctx, created.ID, 20)
		return fmt.Errorf("la copie a échoué (code %d) : %s", waited.StatusCode, strings.TrimSpace(logs))
	}
	return nil
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
		if err := e.copy(ctx, "volume", s.Volume, s.Mount.Type, s.Mount.Source, true); err != nil {
			return fmt.Errorf("restauration de %s (%s) : %w", s.Service, s.Mount.Destination, err)
		}
		e.event(id, "%s : %s restauré depuis %s", s.Service, s.Mount.Destination, s.Volume)
	}
	return nil
}

func (e *Engine) dropSnapshots(ctx context.Context, id int64, cp *Checkpoint) {
	for _, s := range cp.Snapshots {
		if err := e.Docker.Delete(ctx, "/volumes/"+s.Volume); err != nil {
			e.event(id, "volume de copie %s non supprimé : %v", s.Volume, err)
		}
	}
}
