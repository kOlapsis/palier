package run

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kolapsis/palier/internal/catalog"
	"github.com/kolapsis/palier/internal/compose"
	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/inventory"
)

const (
	helperRepo  = "docker.io/library/busybox"
	helperTag   = "1.37.0"
	helperImage = helperRepo + ":" + helperTag
	copyTimeout = 2 * time.Hour
	copyLimit   = 20 << 20 // KiB
	minFree     = 2 << 20  // KiB
)

// Snapshot is a copy, in a volume of its own, of what a service had mounted before the run.
type Snapshot struct {
	Service string          `json:"service"`
	Mount   inventory.Mount `json:"mount"`
	Volume  string          `json:"volume"`
	Exclude []string        `json:"exclude,omitempty"`
}

// snapshot stops the services the plan changes, copies their volumes and host directories, and starts them again.
func (e *Engine) snapshot(ctx context.Context, id int64, st *inventory.Stack, cp *Checkpoint, changed []string) error {
	var targets []*inventory.Service
	for _, s := range st.Services {
		if !slices.Contains(changed, s.Name) {
			continue
		}
		copies, bulk := dataMounts(s)
		for _, b := range bulk {
			e.event(id, "%s: %s holds data the upgrade does not rewrite, not copied", s.Name, b)
		}
		if len(copies) > 0 {
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
		copies, _ := dataMounts(s)
		for i, snap := range copies {
			snap.Volume = fmt.Sprintf("palier-run%d-step%d-%s-%d", id, cp.Step, s.Name, i)
			if err := e.Docker.Post(ctx, "/volumes/create", map[string]any{"Name": snap.Volume, "Labels": map[string]string{"palier.run": fmt.Sprint(id)}}, nil); err != nil {
				return err
			}
			cp.Snapshots = append(cp.Snapshots, snap)
			skip := ""
			out, err := e.helper(ctx, id, measureScript, snap, snap.Mount.Type, snap.Mount.Source, "volume", snap.Volume)
			if err == nil {
				skip, err = copyVerdict(out, s.Role == inventory.RoleDependency || catalog.For(s.Image) != nil)
			}
			if err == nil && skip == "" {
				_, err = e.helper(ctx, id, copyScript, snap, snap.Mount.Type, snap.Mount.Source, "volume", snap.Volume)
			}
			single := errors.Is(err, errNotDir)
			if single {
				err, skip = nil, "single file"
			}
			if err != nil {
				return fmt.Errorf("copying %s (%s): %w", s.Name, snap.Mount.Destination, err)
			}
			if skip != "" {
				cp.Snapshots = cp.Snapshots[:len(cp.Snapshots)-1]
				if err := e.Docker.Delete(ctx, "/volumes/"+snap.Volume); err != nil {
					return err
				}
				if single {
					e.event(id, "%s: %s is a single file, not copied", s.Name, snap.Mount.Destination)
				} else {
					e.event(id, "%s: %s not copied, a rollback will not restore it: %s", s.Name, snap.Mount.Destination, skip)
				}
				continue
			}
			without := ""
			if len(snap.Exclude) > 0 {
				without = ", without " + strings.Join(snap.Exclude, ", ")
			}
			e.event(id, "%s: %s copied to volume %s%s", s.Name, snap.Mount.Destination, snap.Volume, without)
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

// dataMounts returns the copies to make of the mounts of s, without host system paths, logs and the bulk data of its catalog card,
// and the destinations left out because they hold only bulk data.
func dataMounts(s *inventory.Service) (copies []Snapshot, bulk []string) {
	var paths []string
	if card := catalog.For(s.Image); card != nil {
		paths = card.Bulk
	}
	for _, m := range s.Mounts {
		if m.Type == "bind" && (m.Source == "/" || under(m.Source, hostSystemPaths)) || under(m.Destination, logPaths) {
			continue
		}
		if under(m.Destination, paths) {
			bulk = append(bulk, m.Destination)
			continue
		}
		snap := Snapshot{Service: s.Name, Mount: m}
		for _, p := range paths {
			if rel, ok := strings.CutPrefix(p, strings.TrimSuffix(m.Destination, "/")+"/"); ok {
				snap.Exclude = append(snap.Exclude, rel)
			}
		}
		copies = append(copies, snap)
	}
	return copies, bulk
}

// ancestors returns the directories that lead to the excluded paths, which a copy walks into instead of copying whole.
func ancestors(exclude []string) []string {
	var out []string
	for _, x := range exclude {
		for i := range len(x) {
			if x[i] == '/' && !slices.Contains(out, x[:i]) {
				out = append(out, x[:i])
			}
		}
	}
	return out
}

// copyVerdict reads the last "size available total" line, in KiB, of measureScript and returns why the copy is not made, or "".
func copyVerdict(out string, state bool) (string, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	f := strings.Fields(lines[len(lines)-1])
	if len(f) != 3 {
		return "", fmt.Errorf("unexpected measure: %q", out)
	}
	var n [3]int64
	for i, s := range f {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return "", fmt.Errorf("unexpected measure: %q", out)
		}
		n[i] = v
	}
	need, avail, total := n[0], n[1], n[2]
	switch {
	case !state && need > copyLimit:
		return fmt.Sprintf("%s to copy, more than the %s copied for an application without a catalog card", gib(need), gib(copyLimit)), nil
	case avail-need < max(total/10, minFree):
		return fmt.Sprintf("%s to copy, %s free on the Docker disk", gib(need), gib(avail)), nil
	}
	return "", nil
}

func gib(kib int64) string { return fmt.Sprintf("%.1f GiB", float64(kib)/(1<<20)) }

var errNotDir = errors.New("not a directory")

const walkScript = `walk() {
	local e n r
	for e in "$1"/* "$1"/.[!.]* "$1"/..?*; do
		[ -e "$e" ] || [ -L "$e" ] || continue
		n=${e##*/}
		r=$3$n
		case " $EX " in *" $r "*) continue ;; esac
		case " $ANC " in *" $r "*)
			if [ "$M" = copy ]; then
				mkdir -p "$2/$n" && chown "$(stat -c %u:%g "$e")" "$2/$n" && chmod "$(stat -c %a "$e")" "$2/$n" || exit 1
			fi
			walk "$e" "$2/$n" "$r/"
			continue ;;
		esac
		case $M in
		size) SIZE=$((SIZE + $(du -sk "$e" | cut -f1))) ;;
		wipe) rm -rf "$e" || exit 1 ;;
		copy) cp -a "$e" "$2/" || exit 1 ;;
		esac
	done
}
[ -d /from ] || exit 3
`

const (
	measureScript = walkScript + `SIZE=0 M=size
walk /from /to ""
df -Pk /to | tail -n 1 | { read -r _ total _ avail _; echo "$SIZE $avail $total"; }`
	copyScript = walkScript + `M=copy
walk /from /to ""`
	restoreScript = walkScript + `M=wipe
walk /to /to ""
M=copy
walk /from /to ""`
)

// helper runs script in a throwaway container with /from and /to mounted, the exclusions of snap in its environment, and returns its output.
func (e *Engine) helper(ctx context.Context, id int64, script string, snap Snapshot, fromType, from, toType, to string) (string, error) {
	body := map[string]any{
		"Image":  helperImage,
		"Cmd":    []string{"sh", "-c", script},
		"Env":    []string{"EX=" + strings.Join(snap.Exclude, " "), "ANC=" + strings.Join(ancestors(snap.Exclude), " ")},
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
		return "", err
	}
	defer e.Docker.Delete(context.WithoutCancel(ctx), "/containers/"+created.ID+"?force=1")
	if err := e.Docker.Post(ctx, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		return "", err
	}
	began := time.Now()
	reported := began
	for {
		var d docker.ContainerDetail
		if err := e.Docker.Get(ctx, "/containers/"+created.ID+"/json", &d); err != nil {
			return "", err
		}
		if d.State.Status == "exited" || d.State.Status == "dead" {
			logs, _ := e.Docker.Logs(ctx, created.ID, 20)
			switch d.State.ExitCode {
			case 0:
				return strings.TrimSpace(logs), nil
			case 3:
				return "", errNotDir
			}
			return "", fmt.Errorf("copy failed (code %d): %s", d.State.ExitCode, strings.TrimSpace(logs))
		}
		if time.Since(began) > copyTimeout {
			return "", fmt.Errorf("copy still running after %s", copyTimeout)
		}
		if time.Since(reported) >= time.Minute {
			reported = time.Now()
			e.event(id, "%s: working on %s, %d min so far", snap.Service, snap.Mount.Destination, int(time.Since(began).Minutes()))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
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
		if _, err := e.helper(ctx, id, restoreScript, s, "volume", s.Volume, s.Mount.Type, s.Mount.Source); err != nil {
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
