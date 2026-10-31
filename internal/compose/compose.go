// Package compose edits Compose files in place and runs docker compose on a project.
package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Project is a Compose project as its labels describe it.
type Project struct {
	Name        string   `json:"name"`
	WorkingDir  string   `json:"working_dir"`
	ConfigFiles []string `json:"config_files"`
	EnvFiles    []string `json:"env_files,omitempty"`
}

// Run executes docker compose with args on the project and returns its combined output.
func Run(ctx context.Context, dockerHost string, p Project, args ...string) (string, error) {
	var out bytes.Buffer
	err := RunIO(ctx, dockerHost, p, nil, &out, args...)
	return out.String(), err
}

// RunIO executes docker compose with args, reading stdin and writing its standard output to stdout.
func RunIO(ctx context.Context, dockerHost string, p Project, stdin io.Reader, stdout io.Writer, args ...string) error {
	full := []string{"-p", p.Name, "--project-directory", p.WorkingDir}
	for _, f := range p.ConfigFiles {
		full = append(full, "-f", f)
	}
	for _, f := range p.EnvFiles {
		full = append(full, "--env-file", f)
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker-compose", append(full, args...)...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+dockerHost)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s : %w : %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ImageFile returns the last of the files that sets the image of service.
func ImageFile(files []string, service string) (string, error) {
	found := ""
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		if _, err := imageNode(data, service); err == nil {
			found = f
		}
	}
	if found == "" {
		return "", fmt.Errorf("aucun fichier Compose ne donne l'image du service %s", service)
	}
	return found, nil
}

func imageNode(data []byte, service string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("fichier vide")
	}
	n := child(doc.Content[0], "services")
	n = child(n, service)
	n = child(n, "image")
	if n == nil || n.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("pas d'image pour le service %s", service)
	}
	return n, nil
}

func child(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// SetImage replaces the image of service in data, leaving every other byte as it was.
func SetImage(data []byte, service, image string) ([]byte, error) {
	n, err := imageNode(data, service)
	if err != nil {
		return nil, err
	}
	if strings.Contains(n.Value, "$") {
		return nil, fmt.Errorf("l'image du service %s passe par une variable (%s), Palier ne sait pas encore la changer", service, n.Value)
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if n.Line < 1 || n.Line > len(lines) {
		return nil, fmt.Errorf("position de l'image du service %s introuvable", service)
	}
	start := 0
	for _, l := range lines[:n.Line-1] {
		start += len(l)
	}
	line := []rune(string(lines[n.Line-1]))
	col := n.Column - 1
	start += len(string(line[:col]))
	rest := string(data[start:])
	var length int
	var replacement string
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		end := strings.IndexByte(rest[1:], '"')
		length, replacement = end+2, `"`+image+`"`
	case n.Style&yaml.SingleQuotedStyle != 0:
		end := strings.IndexByte(rest[1:], '\'')
		length, replacement = end+2, "'"+image+"'"
	default:
		if !strings.HasPrefix(rest, n.Value) {
			return nil, fmt.Errorf("image du service %s écrite sur plusieurs lignes, non prise en charge", service)
		}
		length, replacement = len(n.Value), image
	}
	out := append([]byte{}, data[:start]...)
	out = append(out, replacement...)
	return append(out, data[start+length:]...), nil
}

// FileState is a file with its content, mode and owner.
type FileState struct {
	Path string      `json:"path"`
	Data string      `json:"data"`
	Mode os.FileMode `json:"mode"`
	UID  int         `json:"uid"`
	GID  int         `json:"gid"`
}

// ReadState reads a file with its mode and owner.
func ReadState(path string) (FileState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileState{}, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return FileState{}, err
	}
	st := fi.Sys().(*syscall.Stat_t)
	return FileState{Path: path, Data: string(data), Mode: fi.Mode().Perm(), UID: int(st.Uid), GID: int(st.Gid)}, nil
}

// Write replaces the file at fs.Path with fs.Data, giving it fs.Mode and fs.UID:fs.GID.
func Write(fs FileState) error {
	tmp := filepath.Join(filepath.Dir(fs.Path), "."+filepath.Base(fs.Path)+".palier")
	if err := os.WriteFile(tmp, []byte(fs.Data), fs.Mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, fs.Mode); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chown(tmp, fs.UID, fs.GID); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, fs.Path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Edit applies fn to the root mapping of a Compose file and writes the document again, keeping its comments.
func Edit(data []byte, fn func(root *yaml.Node) error) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("fichier Compose sans contenu")
	}
	if err := fn(doc.Content[0]); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

// MountSource returns what service mounts at target, as written in the file.
func MountSource(root *yaml.Node, service, target string) (string, error) {
	vols := child(child(child(root, "services"), service), "volumes")
	if vols != nil {
		for _, item := range vols.Content {
			if parts := strings.Split(item.Value, ":"); item.Kind == yaml.ScalarNode && len(parts) >= 2 && parts[1] == target {
				return parts[0], nil
			}
			if t := child(item, "target"); item.Kind == yaml.MappingNode && t != nil && t.Value == target {
				if src := child(item, "source"); src != nil {
					return src.Value, nil
				}
			}
		}
	}
	return "", fmt.Errorf("le service %s ne monte rien sur %s", service, target)
}

// ReplaceMount mounts the named volume at target in place of what service mounted at oldTarget, and declares the volume.
func ReplaceMount(root *yaml.Node, service, oldTarget, volume, target string) error {
	vols := child(child(child(root, "services"), service), "volumes")
	found := false
	if vols != nil {
		for _, item := range vols.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				parts := strings.Split(item.Value, ":")
				if len(parts) >= 2 && parts[1] == oldTarget {
					parts[0], parts[1] = volume, target
					item.Value, found = strings.Join(parts, ":"), true
				}
			case yaml.MappingNode:
				if t := child(item, "target"); t != nil && t.Value == oldTarget {
					t.Value = target
					if src := child(item, "source"); src != nil {
						src.Value = volume
					} else {
						item.Content = append(item.Content, scalar("source"), scalar(volume))
					}
					if typ := child(item, "type"); typ != nil {
						typ.Value = "volume"
					}
					found = true
				}
			}
		}
	}
	if !found {
		return fmt.Errorf("le service %s ne monte rien sur %s", service, oldTarget)
	}
	top := child(root, "volumes")
	if top == nil {
		top = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, scalar("volumes"), top)
	}
	if child(top, volume) == nil {
		top.Content = append(top.Content, scalar(volume), &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle})
	}
	return nil
}

var imageVar = regexp.MustCompile(`^([^$]+):\$\{(\w+)(?::?-[^}]*)?\}$`)

// ImageVariable returns the repository and the variable that holds the tag of an image written repo:${VAR}.
func ImageVariable(image string) (repo, variable string, ok bool) {
	m := imageVar.FindStringSubmatch(image)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ImageValue returns the image of service as written in the file.
func ImageValue(data []byte, service string) (string, error) {
	n, err := imageNode(data, service)
	if err != nil {
		return "", err
	}
	return n.Value, nil
}

// SetEnvVar sets NAME=value in the content of an env file, replacing its line or appending one.
func SetEnvVar(data []byte, name, value string) []byte {
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if k, _, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.TrimSpace(k) == name {
			lines[i] = name + "=" + value
			return []byte(strings.Join(lines, "\n"))
		}
	}
	out := strings.TrimRight(string(data), "\n")
	if out != "" {
		out += "\n"
	}
	return []byte(out + name + "=" + value + "\n")
}

// EnvVar returns the value of NAME in the content of an env file.
func EnvVar(data []byte, name string) (string, bool) {
	for _, l := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.TrimSpace(k) == name {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// Services returns the names of the services a Compose file declares.
func Services(data []byte) ([]string, error) {
	var doc struct{ Services map[string]yaml.Node }
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var out []string
	for name := range doc.Services {
		out = append(out, name)
	}
	return out, nil
}

// ApplyEdit applies one structured edit to service and says what it did.
func ApplyEdit(root *yaml.Node, service, action, name, value, newName string) (string, error) {
	services := child(root, "services")
	svc := child(services, service)
	if svc == nil {
		return "", fmt.Errorf("pas de service %s dans le fichier", service)
	}
	switch action {
	case "remove_service":
		removeKey(services, service)
		for i := 1; i < len(services.Content); i += 2 {
			deps := child(services.Content[i], "depends_on")
			removeKey(deps, service)
			if deps != nil && deps.Kind == yaml.SequenceNode {
				deps.Content = slicesDeleteScalar(deps.Content, service)
			}
		}
		return "service " + service + " retiré", nil
	case "set_env", "unset_env", "rename_env":
		env := child(svc, "environment")
		if env == nil {
			if action != "set_env" {
				return name + " absent de l'environnement de " + service + ", rien à faire", nil
			}
			env = &yaml.Node{Kind: yaml.MappingNode}
			svc.Content = append(svc.Content, scalar("environment"), env)
		}
		return editEnv(env, service, action, name, value, newName)
	case "set_command", "set_healthcheck":
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(value), &n); err != nil || len(n.Content) == 0 {
			return "", fmt.Errorf("valeur illisible pour %s : %q", action, value)
		}
		key, node := "command", n.Content[0]
		if action == "set_healthcheck" {
			key, node = "healthcheck", &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("test"), n.Content[0]}}
		}
		removeKey(svc, key)
		svc.Content = append(svc.Content, scalar(key), node)
		return key + " de " + service + " fixé à " + value, nil
	case "unset_command", "unset_healthcheck":
		key := strings.TrimPrefix(action, "unset_")
		if !removeKey(svc, key) {
			return key + " absent de " + service + ", rien à faire", nil
		}
		return key + " retiré de " + service, nil
	case "add_volume", "remove_volume":
		vols := child(svc, "volumes")
		if action == "add_volume" {
			if vols == nil {
				vols = &yaml.Node{Kind: yaml.SequenceNode}
				svc.Content = append(svc.Content, scalar("volumes"), vols)
			}
			vols.Content = append(vols.Content, scalar(value))
			return "volume " + value + " ajouté à " + service, nil
		}
		if vols != nil {
			for i, item := range vols.Content {
				parts := strings.Split(item.Value, ":")
				if item.Value == value || len(parts) >= 2 && parts[1] == value || child(item, "target") != nil && child(item, "target").Value == value {
					vols.Content = append(vols.Content[:i], vols.Content[i+1:]...)
					return "volume " + value + " retiré de " + service, nil
				}
			}
		}
		return "volume " + value + " absent de " + service + ", rien à faire", nil
	}
	return "", fmt.Errorf("modification %q inconnue", action)
}

func editEnv(env *yaml.Node, service, action, name, value, newName string) (string, error) {
	if env.Kind == yaml.SequenceNode {
		for i, item := range env.Content {
			k, v, _ := strings.Cut(item.Value, "=")
			if k != name {
				continue
			}
			switch action {
			case "set_env":
				item.Value = name + "=" + value
			case "unset_env":
				env.Content = append(env.Content[:i], env.Content[i+1:]...)
			case "rename_env":
				item.Value = newName + "=" + v
			}
			return action + " " + name + " sur " + service, nil
		}
		if action == "set_env" {
			env.Content = append(env.Content, scalar(name+"="+value))
			return name + " ajouté à " + service, nil
		}
		return name + " absent de l'environnement de " + service + ", rien à faire", nil
	}
	for i := 0; i+1 < len(env.Content); i += 2 {
		if env.Content[i].Value != name {
			continue
		}
		switch action {
		case "set_env":
			env.Content[i+1] = scalar(value)
		case "unset_env":
			env.Content = append(env.Content[:i], env.Content[i+2:]...)
		case "rename_env":
			env.Content[i].Value = newName
		}
		return action + " " + name + " sur " + service, nil
	}
	if action == "set_env" {
		env.Content = append(env.Content, scalar(name), scalar(value))
		return name + " ajouté à " + service, nil
	}
	return name + " absent de l'environnement de " + service + ", rien à faire", nil
}

func removeKey(m *yaml.Node, key string) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

func slicesDeleteScalar(nodes []*yaml.Node, v string) []*yaml.Node {
	out := nodes[:0]
	for _, n := range nodes {
		if n.Value != v {
			out = append(out, n)
		}
	}
	return out
}
