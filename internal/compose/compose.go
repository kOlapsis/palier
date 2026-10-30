// Package compose edits Compose files in place and runs docker compose on a project.
package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	full := []string{"-p", p.Name, "--project-directory", p.WorkingDir}
	for _, f := range p.ConfigFiles {
		full = append(full, "-f", f)
	}
	for _, f := range p.EnvFiles {
		full = append(full, "--env-file", f)
	}
	cmd := exec.CommandContext(ctx, "docker-compose", append(full, args...)...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+dockerHost)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker compose %s : %w : %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
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
	return os.Rename(tmp, fs.Path)
}
