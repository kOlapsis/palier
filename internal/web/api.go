package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/run"
)

// Line is one stack of the status list.
type Line struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Running string `json:"running,omitempty"`
	Target  string `json:"target,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Status is the state of every stack at the last scan.
type Status struct {
	Began  time.Time `json:"began"`
	At     time.Time `json:"at"`
	Error  string    `json:"error,omitempty"`
	Stacks []Line    `json:"stacks"`
}

// API returns the JSON handler the command line reaches on the local socket.
func API(deps Deps) http.Handler {
	s := &server{author: "cli", deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		snap := s.deps.Current()
		out := Status{Began: snap.Began, At: snap.At, Error: snap.Err, Stacks: []Line{}}
		for _, st := range snap.Stacks {
			out.Stacks = append(out.Stacks, s.line(s.view(st, snap.At)))
		}
		reply(w, out, nil)
	})
	mux.HandleFunc("GET /stacks/{name}", func(w http.ResponseWriter, r *http.Request) {
		st, at := s.find(r.PathValue("name"))
		if st == nil {
			reply(w, nil, fmt.Errorf("no stack %q", r.PathValue("name")))
			return
		}
		reply(w, s.view(st, at), nil)
	})
	mux.HandleFunc("POST /stacks/{name}/apply", func(w http.ResponseWriter, r *http.Request) {
		id, err := s.deps.Apply(r.PathValue("name"), s.author, r.FormValue("dry_run") == "1")
		reply(w, map[string]int64{"run": id}, err)
	})
	for _, kind := range []string{"ignore", "postpone"} {
		mux.HandleFunc("POST /stacks/{name}/"+kind, func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]bool{"ok": true}, s.record(s.author, r.PathValue("name"), kind, r.FormValue("service"), r.FormValue("version"), r.FormValue("until")))
		})
	}
	mux.HandleFunc("GET /runs", func(w http.ResponseWriter, r *http.Request) {
		runs, err := s.deps.Runs(r.FormValue("stack"))
		reply(w, runs, err)
	})
	mux.HandleFunc("GET /runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			reply(w, nil, fmt.Errorf("invalid run number %q", r.PathValue("id")))
			return
		}
		v, err := s.deps.Run(id)
		reply(w, v, err)
	})
	mux.HandleFunc("POST /scan", func(w http.ResponseWriter, r *http.Request) {
		asked := time.Now()
		s.deps.Rescan()
		reply(w, map[string]time.Time{"asked": asked}, nil)
	})
	return mux
}

func reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		v = map[string]string{"error": err.Error()}
	}
	json.NewEncoder(w).Encode(v)
}

// line sums up a stack: its application, the target of its plan, and what needs attention.
func (s *server) line(v StackView) Line {
	l := Line{Name: v.Stack.Name, State: v.State}
	if len(v.Stack.Services) == 0 {
		return l
	}
	app := v.Stack.Services[0]
	for _, svc := range v.Stack.Services {
		if svc.Role == inventory.RoleApplication {
			app = svc
			break
		}
	}
	l.Running = app.Version
	if l.Running == "" {
		l.Running = app.Tag
	}
	if path := v.Paths[app.Name]; len(path) > 1 {
		l.Target = path[len(path)-1].Tag
	}
	var notes []string
	advisories, severity := 0, map[string]int{}
	for _, svc := range v.Stack.Services {
		for _, a := range svc.Advisories {
			advisories++
			severity[a.Severity]++
		}
		if svc.DetectionError != "" {
			notes = append(notes, "versions of "+svc.Name+" unknown")
		}
	}
	if advisories > 0 {
		n := fmt.Sprintf("%d security advisories", advisories)
		if advisories == 1 {
			n = "1 security advisory"
		}
		for _, sev := range []string{"critical", "high"} {
			if severity[sev] > 0 {
				n += fmt.Sprintf(", %d %s", severity[sev], sev)
				break
			}
		}
		notes = append(notes, n)
	}
	if v.Record != nil && len(v.Record.Plan.Steps) > 0 {
		notes = append(notes, fmt.Sprintf("%d step%s", len(v.Record.Plan.Steps), plural(len(v.Record.Plan.Steps))))
	}
	if v.Postponed != "" {
		notes = append(notes, "postponed until "+v.Postponed)
	}
	if len(v.Runs) > 0 && !v.Runs[0].DryRun && v.Runs[0].Status != run.Done && v.Runs[0].Error != "" {
		cause, _, _ := strings.Cut(v.Runs[0].Error, "\n")
		if r := []rune(cause); len(r) > 80 {
			cause = string(r[:80]) + "…"
		}
		notes = append(notes, fmt.Sprintf("run %d: %s", v.Runs[0].ID, cause))
	}
	l.Note = strings.Join(notes, "; ")
	return l
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
