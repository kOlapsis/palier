// Package inventory builds the stacks from the Docker engine and their Compose files, and finds newer versions.
package inventory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kolapsis/palier/internal/catalog"
	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/registry"
	"github.com/kolapsis/palier/internal/version"
)

// Stack is a Compose project, or a container started outside Compose, which Palier only observes.
type Stack struct {
	Name        string     `json:"name"`
	Observed    bool       `json:"observed_only,omitempty"`
	WorkingDir  string     `json:"working_dir,omitempty"`
	ConfigFiles []string   `json:"config_files,omitempty"`
	EnvFiles    []string   `json:"env_files,omitempty"`
	Services    []*Service `json:"services"`
	Errors      []string   `json:"errors,omitempty"`
}

// Service is one service of a stack, as it runs.
type Service struct {
	Name           string           `json:"name"`
	Container      string           `json:"container"`
	Image          string           `json:"image"`
	Tag            string           `json:"tag"`
	Digest         string           `json:"digest,omitempty"`
	Version        string           `json:"version,omitempty"`
	VersionSource  string           `json:"version_source,omitempty"`
	Republished    bool             `json:"republished,omitempty"`
	State          string           `json:"state"`
	Healthcheck    bool             `json:"healthcheck"`
	Health         string           `json:"health,omitempty"`
	Restarts       int              `json:"restarts"`
	Mounts         []Mount          `json:"mounts,omitempty"`
	DependsOn      []string         `json:"depends_on,omitempty"`
	Role           string           `json:"role"`
	Available      []Release        `json:"available,omitempty"`
	Maturing       []Release        `json:"maturing,omitempty"`
	DetectionError string           `json:"detection_error,omitempty"`
	ImageID        string           `json:"image_id"`
	Source         string           `json:"source,omitempty"`
	BackupHook     string           `json:"backup_hook,omitempty"`
	Notes          []notes.Note     `json:"notes,omitempty"`
	Advisories     []notes.Advisory `json:"advisories,omitempty"`
	NotesError     string           `json:"notes_error,omitempty"`
	Hidden         []string         `json:"hidden,omitempty"`
	BuiltLocally   bool             `json:"built_locally,omitempty"`
	NotInRegistry  bool             `json:"not_in_registry,omitempty"`

	current      version.V
	digests      []string
	labelVersion string
}

// Mount is a volume or a host directory a service mounts read-write.
type Mount struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// Release is a newer tag with its date: the publication of its release note, else the first push Palier saw.
type Release struct {
	Tag    string    `json:"tag"`
	Pushed time.Time `json:"pushed,omitzero"`
}

// Roles of a service in its stack.
const (
	RoleApplication = "application"
	RoleDependency  = "dependency"
	RoleObserved    = "observed"
)

const (
	labelProject     = "com.docker.compose.project"
	labelWorkingDir  = "com.docker.compose.project.working_dir"
	labelConfigFiles = "com.docker.compose.project.config_files"
	labelService     = "com.docker.compose.service"
	labelEnvFiles    = "com.docker.compose.project.environment_file"
	labelDependsOn   = "palier.depends_on"
)

// Scan reads every container of the engine and groups them into stacks.
func Scan(ctx context.Context, dc *docker.Client) ([]*Stack, error) {
	var containers []docker.Container
	if err := dc.Get(ctx, "/containers/json?all=1", &containers); err != nil {
		return nil, err
	}
	self := selfID()
	skip := ""
	for _, c := range containers {
		if self != "" && c.ID == self {
			skip = c.Labels[labelProject]
			if skip == "" {
				skip = "\x00" + c.ID
			}
		}
	}
	stacks := map[string]*Stack{}
	for _, c := range containers {
		if key := c.Labels[labelProject]; key == skip || "\x00"+c.ID == skip {
			continue
		}
		svc, err := inspect(ctx, dc, c)
		if err != nil {
			if strings.Contains(err.Error(), "404") {
				continue
			}
			return nil, err
		}
		project := c.Labels[labelProject]
		key := project
		if project == "" {
			key = "\x00" + svc.Container
		}
		st := stacks[key]
		if st == nil {
			st = &Stack{Name: project, WorkingDir: c.Labels[labelWorkingDir]}
			if project == "" {
				st.Name, st.Observed = svc.Container, true
			} else if files := c.Labels[labelConfigFiles]; files != "" {
				st.ConfigFiles = strings.Split(files, ",")
			}
			if files := c.Labels[labelEnvFiles]; files != "" {
				st.EnvFiles = strings.Split(files, ",")
			}
			stacks[key] = st
		}
		if extra := c.Labels[labelDependsOn]; extra != "" {
			for _, d := range strings.Split(extra, ",") {
				svc.DependsOn = append(svc.DependsOn, strings.TrimSpace(d))
			}
		}
		st.Services = append(st.Services, svc)
	}
	var out []*Stack
	for _, st := range stacks {
		if !st.Observed {
			deps, local, err := readDependsOn(st.ConfigFiles)
			if err != nil {
				st.Errors = append(st.Errors, err.Error())
			}
			for _, s := range st.Services {
				s.DependsOn = append(s.DependsOn, deps[s.Name]...)
				s.BuiltLocally = s.BuiltLocally || local[s.Name]
			}
		}
		assignRoles(st)
		slices.SortFunc(st.Services, func(a, b *Service) int { return cmp.Compare(a.Name, b.Name) })
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b *Stack) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func inspect(ctx context.Context, dc *docker.Client, c docker.Container) (*Service, error) {
	var d docker.ContainerDetail
	if err := dc.Get(ctx, "/containers/"+c.ID+"/json", &d); err != nil {
		return nil, err
	}
	var img docker.Image
	// A container being recreated may point to an image that is already gone; its service keeps no image details.
	imgErr := dc.Get(ctx, "/images/"+c.ImageID+"/json", &img)
	ref := registry.ParseRef(d.Config.Image)
	svc := &Service{
		Name:        c.Labels[labelService],
		Container:   strings.TrimPrefix(c.Names[0], "/"),
		Image:       ref.Name(),
		Tag:         ref.Tag,
		State:       d.State.Status,
		Healthcheck: d.Config.Healthcheck != nil && len(d.Config.Healthcheck.Test) > 0 && d.Config.Healthcheck.Test[0] != "NONE",
		Restarts:    d.RestartCount,
	}
	if svc.Name == "" {
		svc.Name = svc.Container
	}
	if d.State.Health != nil {
		svc.Health = d.State.Health.Status
	}
	for _, rd := range img.RepoDigests {
		if name, digest, ok := strings.Cut(rd, "@"); ok && registry.ParseRef(name).Name() == ref.Name() {
			svc.digests = append(svc.digests, digest)
		}
	}
	if len(svc.digests) > 0 {
		svc.Digest = svc.digests[0]
	}
	svc.BuiltLocally = imgErr == nil && len(img.RepoDigests) == 0
	svc.labelVersion = img.Config.Labels["org.opencontainers.image.version"]
	svc.Source = img.Config.Labels["org.opencontainers.image.source"]
	svc.BackupHook = c.Labels["palier.hook.backup"]
	svc.ImageID = c.ImageID
	for _, m := range d.Mounts {
		switch {
		case !m.RW:
		case m.Type == "volume":
			svc.Mounts = append(svc.Mounts, Mount{Type: m.Type, Source: m.Name, Destination: m.Destination})
		case m.Type == "bind":
			svc.Mounts = append(svc.Mounts, Mount{Type: m.Type, Source: m.Source, Destination: m.Destination})
		}
	}
	return svc, nil
}

var containerPath = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// selfID returns the ID of the container Palier runs in, read from the mounts Docker gives it, or "" outside a container.
func selfID() string {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	if m := containerPath.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// readDependsOn returns the depends_on of each service, and the services built on the machine rather than pulled.
func readDependsOn(files []string) (map[string][]string, map[string]bool, error) {
	deps, local := map[string][]string{}, map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return deps, local, fmt.Errorf("reading Compose file: %w", err)
		}
		d, l, err := parseDependsOn(data)
		if err != nil {
			return deps, local, fmt.Errorf("%s: %w", f, err)
		}
		for s, list := range d {
			deps[s] = append(deps[s], list...)
		}
		for s := range l {
			local[s] = true
		}
	}
	return deps, local, nil
}

func parseDependsOn(data []byte) (map[string][]string, map[string]bool, error) {
	var doc struct {
		Services map[string]struct {
			DependsOn  yaml.Node `yaml:"depends_on"`
			Build      yaml.Node `yaml:"build"`
			PullPolicy string    `yaml:"pull_policy"`
		}
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	deps, local := map[string][]string{}, map[string]bool{}
	for name, s := range doc.Services {
		if s.Build.Kind != 0 || s.PullPolicy == "never" || s.PullPolicy == "build" {
			local[name] = true
		}
		n := s.DependsOn
		step := 1
		if n.Kind == yaml.MappingNode {
			step = 2
		}
		for i := 0; i < len(n.Content); i += step {
			deps[name] = append(deps[name], n.Content[i].Value)
		}
	}
	return deps, local, nil
}

func assignRoles(st *Stack) {
	needed := map[string]bool{}
	for _, s := range st.Services {
		slices.Sort(s.DependsOn)
		s.DependsOn = slices.Compact(s.DependsOn)
		for _, d := range s.DependsOn {
			needed[d] = true
		}
	}
	for _, s := range st.Services {
		switch {
		case st.Observed:
			s.Role = RoleObserved
		case needed[s.Name]:
			s.Role = RoleDependency
		default:
			s.Role = RoleApplication
		}
	}
}

const maxDigestChecks = 30

// resolve reads the version from the tag when no tag is more precise, else from the digest, else from the OCI label.
func resolve(ctx context.Context, s *Service, ref registry.Ref, tags []string, reg *registry.Client) error {
	running, parsed := version.Parse(s.Tag)
	var candidates []version.V
	for _, t := range tags {
		if w, ok := version.Parse(t); ok && (!parsed || running.Extends(w)) {
			candidates = append(candidates, w)
		}
	}
	if parsed && len(candidates) == 0 {
		s.current, s.VersionSource, s.Version = running, "tag", running.Tag
		d, err := reg.Digest(ctx, ref, s.Tag)
		if err != nil || slices.Contains(s.digests, d) || len(s.digests) == 0 {
			return err
		}
		platforms, err := reg.PlatformDigests(ctx, ref, d)
		if err != nil {
			return err
		}
		s.Republished = !slices.ContainsFunc(platforms, func(p string) bool { return slices.Contains(s.digests, p) })
		return nil
	}
	slices.SortFunc(candidates, func(a, b version.V) int {
		switch {
		case a.Less(b):
			return 1
		case b.Less(a):
			return -1
		}
		return 0
	})
	// shortcut: only the 30 highest candidates are compared by digest, enough unless the running image is far behind its moving tag.
	for _, w := range candidates[:min(len(candidates), maxDigestChecks)] {
		d, err := reg.Digest(ctx, ref, w.Tag)
		if err != nil {
			return err
		}
		if slices.Contains(s.digests, d) {
			s.current, s.VersionSource, s.Version = w, "digest", w.Tag
			return nil
		}
	}
	for _, d := range s.digests {
		if a, err := reg.AnnotatedVersion(ctx, ref, d); err == nil {
			if v, ok := version.Parse(a); ok {
				s.current, s.VersionSource, s.Version = v, "annotation", v.Tag
				return nil
			}
		}
	}
	if v, ok := version.Parse(s.labelVersion); ok {
		s.current, s.VersionSource, s.Version = v, "label", v.Tag
		return nil
	}
	return fmt.Errorf("running version unknown: tag %q is not a precise version, no precise registry tag has its digest, and neither the registry nor the image gives org.opencontainers.image.version", s.Tag)
}

// Detect fills the newer versions of every service, holding back those pushed less than maturation ago.
func Detect(ctx context.Context, stacks []*Stack, reg *registry.Client, nc *notes.Client, maturation time.Duration) {
	var wg sync.WaitGroup
	for _, st := range stacks {
		for _, s := range st.Services {
			wg.Go(func() { detect(ctx, s, reg, nc, maturation) })
		}
	}
	wg.Wait()
}

func detect(ctx context.Context, s *Service, reg *registry.Client, nc *notes.Client, maturation time.Duration) {
	if s.BuiltLocally {
		return
	}
	ref := registry.ParseRef(s.Image + ":" + s.Tag)
	tags, err := reg.Tags(ctx, ref)
	var refused *registry.StatusError
	if errors.As(err, &refused) && (refused.Code == 401 || refused.Code == 403 || refused.Code == 404) && !reg.UsesCredentials(ref.Domain) {
		s.NotInRegistry = true
		return
	}
	if err != nil {
		s.DetectionError = err.Error()
		return
	}
	if err := resolve(ctx, s, ref, tags, reg); err != nil {
		s.DetectionError = err.Error()
		return
	}
	newer := version.Newer(s.current, tags)
	repo := notes.Repo(s.Source)
	if repo != "" {
		list, err := nc.Advisories(ctx, repo)
		if err != nil {
			s.NotesError = err.Error()
		}
		for _, a := range list {
			if a.Affects(s.current) {
				s.Advisories = append(s.Advisories, a)
				s.Notes = append(s.Notes, notes.Note{URL: a.URL, Published: a.Published, Body: a.Body})
			}
		}
	}
	fixed, urgent := notes.FixedFrom(s.Advisories, s.current)
	if repo != "" && len(newer) > 0 {
		releases, err := nc.Releases(ctx, repo, s.current)
		if err != nil {
			s.NotesError = strings.TrimPrefix(s.NotesError+" ; "+err.Error(), " ; ")
		}
		last := newer[len(newer)-1]
		for _, n := range releases {
			if v, ok := version.Parse(strings.TrimPrefix(n.Tag, "v")); ok && !notes.Less(last, v) {
				s.Notes = append(s.Notes, n)
			}
		}
	}
	if card := catalog.For(s.Image); card != nil {
		for _, ex := range card.Excerpts {
			s.Notes = append(s.Notes, notes.Note{URL: ex.URL, Body: ex.Text})
		}
	}
	for i := range s.Notes {
		var removed []string
		s.Notes[i].Body, removed = notes.Clean(s.Notes[i].Body)
		for _, r := range removed {
			s.Hidden = append(s.Hidden, cmp.Or(s.Notes[i].Tag, "documentation")+": "+r)
		}
	}
	for _, v := range newer {
		pushed, err := reg.Pushed(ctx, ref, v.Tag)
		if err != nil {
			s.DetectionError = err.Error()
		}
		for _, n := range s.Notes {
			if notes.Same(n.Tag, v) && !n.Published.IsZero() {
				pushed = n.Published
			}
		}
		r := Release{Tag: v.Tag, Pushed: pushed}
		if !pushed.IsZero() && time.Since(pushed) < maturation && !(urgent && !notes.Less(v, fixed)) {
			s.Maturing = append(s.Maturing, r)
		} else {
			s.Available = append(s.Available, r)
		}
	}
}
