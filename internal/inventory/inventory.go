// Package inventory builds the stacks from the Docker engine and their Compose files, and finds newer versions.
package inventory

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kolapsis/palier/internal/docker"
	"github.com/kolapsis/palier/internal/registry"
	"github.com/kolapsis/palier/internal/version"
)

// Stack is a Compose project, or a container started outside Compose, which Palier only observes.
type Stack struct {
	Name        string     `json:"name"`
	Observed    bool       `json:"observed_only,omitempty"`
	WorkingDir  string     `json:"working_dir,omitempty"`
	ConfigFiles []string   `json:"config_files,omitempty"`
	Services    []*Service `json:"services"`
	Errors      []string   `json:"errors,omitempty"`
}

// Service is one service of a stack, as it runs.
type Service struct {
	Name           string    `json:"name"`
	Container      string    `json:"container"`
	Image          string    `json:"image"`
	Tag            string    `json:"tag"`
	Digest         string    `json:"digest,omitempty"`
	Version        string    `json:"version,omitempty"`
	State          string    `json:"state"`
	Healthcheck    bool      `json:"healthcheck"`
	Health         string    `json:"health,omitempty"`
	Restarts       int       `json:"restarts"`
	Volumes        []string  `json:"volumes,omitempty"`
	DependsOn      []string  `json:"depends_on,omitempty"`
	Role           string    `json:"role"`
	Available      []Release `json:"available,omitempty"`
	Maturing       []Release `json:"maturing,omitempty"`
	DetectionError string    `json:"detection_error,omitempty"`

	current version.V
}

// Release is a newer tag with the date Palier first saw it pushed.
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
	labelDependsOn   = "palier.depends_on"
)

// Scan reads every container of the engine and groups them into stacks.
func Scan(ctx context.Context, dc *docker.Client) ([]*Stack, error) {
	var containers []docker.Container
	if err := dc.Get(ctx, "/containers/json?all=1", &containers); err != nil {
		return nil, err
	}
	stacks := map[string]*Stack{}
	for _, c := range containers {
		svc, err := inspect(ctx, dc, c)
		if err != nil {
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
			deps, err := readDependsOn(st.ConfigFiles)
			if err != nil {
				st.Errors = append(st.Errors, err.Error())
			}
			for _, s := range st.Services {
				s.DependsOn = append(s.DependsOn, deps[s.Name]...)
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
	if err := dc.Get(ctx, "/images/"+c.ImageID+"/json", &img); err != nil {
		return nil, err
	}
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
		if _, digest, ok := strings.Cut(rd, "@"); ok {
			svc.Digest = digest
			if registry.ParseRef(rd).Name() == ref.Name() {
				break
			}
		}
	}
	for _, m := range d.Mounts {
		if m.Type == "volume" {
			svc.Volumes = append(svc.Volumes, m.Name+":"+m.Destination)
		} else {
			svc.Volumes = append(svc.Volumes, m.Source+":"+m.Destination)
		}
	}
	if v, ok := version.Parse(ref.Tag); ok && v.Precise() {
		svc.current = v
	} else if v, ok := version.Parse(img.Config.Labels["org.opencontainers.image.version"]); ok && v.Precise() {
		svc.current = v
	}
	svc.Version = svc.current.Tag
	return svc, nil
}

func readDependsOn(files []string) (map[string][]string, error) {
	deps := map[string][]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return deps, fmt.Errorf("lecture du fichier Compose : %w", err)
		}
		d, err := parseDependsOn(data)
		if err != nil {
			return deps, fmt.Errorf("%s : %w", f, err)
		}
		for s, list := range d {
			deps[s] = append(deps[s], list...)
		}
	}
	return deps, nil
}

func parseDependsOn(data []byte) (map[string][]string, error) {
	var doc struct {
		Services map[string]struct {
			DependsOn yaml.Node `yaml:"depends_on"`
		}
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	deps := map[string][]string{}
	for name, s := range doc.Services {
		n := s.DependsOn
		step := 1
		if n.Kind == yaml.MappingNode {
			step = 2
		}
		for i := 0; i < len(n.Content); i += step {
			deps[name] = append(deps[name], n.Content[i].Value)
		}
	}
	return deps, nil
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

// Detect fills the newer versions of every service, holding back those pushed less than maturation ago.
func Detect(ctx context.Context, stacks []*Stack, reg *registry.Client, maturation time.Duration) {
	var wg sync.WaitGroup
	for _, st := range stacks {
		for _, s := range st.Services {
			wg.Go(func() { detect(ctx, s, reg, maturation) })
		}
	}
	wg.Wait()
}

func detect(ctx context.Context, s *Service, reg *registry.Client, maturation time.Duration) {
	if s.current.Tag == "" {
		s.DetectionError = "version en service inconnue : ni le tag ni le label org.opencontainers.image.version ne donnent une version précise"
		return
	}
	ref := registry.ParseRef(s.Image + ":" + s.Tag)
	tags, err := reg.Tags(ctx, ref)
	if err != nil {
		s.DetectionError = err.Error()
		return
	}
	for _, v := range version.Newer(s.current, tags) {
		pushed, err := reg.Pushed(ctx, ref, v.Tag)
		if err != nil {
			s.DetectionError = err.Error()
		}
		r := Release{Tag: v.Tag, Pushed: pushed}
		if !pushed.IsZero() && time.Since(pushed) < maturation {
			s.Maturing = append(s.Maturing, r)
		} else {
			s.Available = append(s.Available, r)
		}
	}
}
