package plan

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kolapsis/palier/internal/inventory"
)

var (
	secretName = regexp.MustCompile(`(?i)pass|secret|token|key|credential|private|cert`)
	envRef     = regexp.MustCompile(`\$\{?(\w+)`)
	envPair    = regexp.MustCompile(`(?m)^[ \t]*-?[ \t]*(\w+)[ \t]*[=:][ \t]*["']?([^"'\s#]+)`)
	shellMeta  = regexp.MustCompile("[|;&<>`\n]|\\$\\(")
	urlHost    = regexp.MustCompile(`://([^/:\s]+)`)
	fakeTag    = regexp.MustCompile(`(?i)</?\s*(release_notes|compose_file|card|services|system|instructions?|untrusted[\w-]*)\b[^>\n]*>?`)
	volumeMode = []string{"", "ro", "rw", "z", "Z", "ro,z", "ro,Z", "rw,z", "rw,Z"}
)

// checkFreeValues refuses the values of a compose_edit that come neither from the notes of its service nor from the Compose
// files, that run shell, reach outside the stack directory or copy a secret, and records where each accepted value comes from.
func checkFreeValues(p *Plan, n int, op *Operation, st *inventory.Stack) {
	var svc *inventory.Service
	for _, s := range st.Services {
		if s.Name == op.Service {
			svc = s
		}
	}
	if svc == nil {
		return
	}
	var files, envs strings.Builder
	for _, f := range st.ConfigFiles {
		b, _ := os.ReadFile(f)
		files.Write(b)
	}
	for _, f := range st.EnvFiles {
		b, _ := os.ReadFile(f)
		envs.Write(b)
	}
	compose := files.String()
	var secrets []string
	for _, m := range envPair.FindAllStringSubmatch(compose+"\n"+envs.String(), -1) {
		if secretName.MatchString(m[1]) && len(m[2]) >= 6 && !strings.HasPrefix(m[2], "$") {
			secrets = append(secrets, m[2])
		}
	}
	source := func(v string) string {
		for _, note := range svc.Notes {
			if strings.Contains(note.Body, v) {
				return note.URL
			}
		}
		if strings.Contains(compose, v) {
			return "the Compose file"
		}
		return ""
	}
	for i := range op.Edits {
		e := &op.Edits[i]
		e.Source = ""
		refuse := func(format string, args ...any) {
			p.incomplete("step %d: edit %s of %s refused: "+format, append([]any{n, e.Action, op.Service}, args...)...)
		}
		var cited []string
		switch e.Action {
		case "set_env":
			if m := envRef.FindStringSubmatch(e.Value); m != nil && secretName.MatchString(m[1]) && m[1] != e.Name {
				refuse("its value reads the secret %s", m[1])
				continue
			}
			if slices.ContainsFunc(secrets, func(s string) bool { return strings.Contains(e.Value, s) }) {
				refuse("its value copies a secret of the stack")
				continue
			}
			cited = []string{e.Name, e.Value}
		case "rename_env":
			cited = []string{e.NewName}
		case "set_command", "set_healthcheck":
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(e.Value), &node); err != nil {
				refuse("unreadable value %q", e.Value)
				continue
			}
			for _, s := range scalars(&node) {
				if strings.EqualFold(s, "CMD-SHELL") || strings.EqualFold(s, "CMD") || strings.EqualFold(s, "NONE") {
					continue
				}
				if shellMeta.MatchString(strings.TrimSuffix(strings.TrimSpace(s), "|| exit 1")) {
					refuse("%q runs shell", s)
					cited = nil
					break
				}
				if m := urlHost.FindStringSubmatch(s); m != nil && e.Action == "set_healthcheck" && !slices.Contains([]string{"localhost", "127.0.0.1", "0.0.0.0", "[::1]"}, m[1]) {
					refuse("the healthcheck reaches %s, outside the container", m[1])
					cited = nil
					break
				}
				cited = append(cited, s)
			}
			if cited == nil {
				continue
			}
		case "add_volume":
			src, dst, mode := splitVolume(e.Value)
			if !slices.Contains(volumeMode, mode) {
				refuse("unknown mount option %q", mode)
				continue
			}
			if strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~") {
				path := filepath.Clean(src)
				if !filepath.IsAbs(path) {
					path = filepath.Join(st.WorkingDir, src)
				}
				if st.WorkingDir == "" || strings.HasPrefix(src, "~") || path != filepath.Clean(st.WorkingDir) && !strings.HasPrefix(path, filepath.Clean(st.WorkingDir)+string(filepath.Separator)) {
					refuse("it mounts %s, outside the directory of the stack", src)
					continue
				}
				if strings.HasSuffix(path, ".sock") {
					refuse("it mounts the socket %s", src)
					continue
				}
			}
			cited = []string{dst}
		default:
			continue
		}
		for _, v := range cited {
			if v == "" {
				continue
			}
			if s := source(v); s == "" {
				refuse("%q is written neither in the notes of %s nor in the Compose file", v, op.Service)
				e.Source = ""
				break
			} else if e.Source == "" || e.Source == "the Compose file" {
				e.Source = s
			}
		}
	}
}

func scalars(n *yaml.Node) []string {
	if n.Kind == yaml.ScalarNode {
		return []string{n.Value}
	}
	var out []string
	for _, c := range n.Content {
		out = append(out, scalars(c)...)
	}
	return out
}

// splitVolume reads source:target[:mode], or a target alone for an anonymous volume.
func splitVolume(v string) (src, dst, mode string) {
	parts := strings.Split(v, ":")
	switch len(parts) {
	case 1:
		return "", parts[0], ""
	case 2:
		return parts[0], parts[1], ""
	}
	return parts[0], parts[1], strings.Join(parts[2:], ":")
}

// checkSuspicious keeps the passages flagged as addressed to an AI that are really in the notes, and adds the notes that
// imitate the delimiters of the prompt.
func checkSuspicious(p *Plan, st *inventory.Stack) {
	var kept []string
	in := func(v string) bool {
		for _, s := range st.Services {
			for _, note := range s.Notes {
				if quoted(note.Body, v) {
					return true
				}
			}
		}
		return false
	}
	for _, q := range p.Suspicious {
		if q = strings.TrimSpace(q); len(q) >= 10 && in(q) && !slices.Contains(kept, q) {
			kept = append(kept, q)
		}
	}
	for _, s := range st.Services {
		for _, note := range s.Notes {
			if m := fakeTag.FindString(note.Body); m != "" && !slices.Contains(kept, m) {
				kept = append(kept, m)
			}
		}
	}
	p.Suspicious = kept
}
