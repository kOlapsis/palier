package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/plan"
)

func TestCleanStripsTerminalSequences(t *testing.T) {
	if got := clean("v1.2\x1b]0;pwned\x07\x1b[31m red\x9b2J\nnext\tline"); got != "v1.2]0;pwned[31m red2J\nnext\tline" {
		t.Errorf("clean = %q", got)
	}
}

func TestGridAlignsColoredCells(t *testing.T) {
	var out bytes.Buffer
	c := &cli{color: true, out: &out}
	c.table([]string{"STACK", "STATE"}, [][]cell{
		{{text: "gitlab", bold: true}, {text: "stopped", tone: "warn"}},
		{{text: "a\x1b[2Jb"}, {text: "up to date", tone: "ok"}},
	})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%q", out.String())
	}
	for _, l := range lines {
		if strings.Contains(l, "\x1b[2J") {
			t.Errorf("escape sequence of the data printed: %q", l)
		}
	}
	col := func(l string) int { return width(l[:strings.LastIndex(l, "  ")+2]) }
	if col(lines[0]) != 8 || col(lines[1]) != 8 || col(lines[2]) != 8 {
		t.Errorf("second column not aligned at 8: %q", lines)
	}
}

func TestDescribeOp(t *testing.T) {
	for op, want := range map[*plan.Operation]string{
		{Op: "set_image", Service: "gitlab", Image: "gitlab/gitlab-ce", Tag: "18.5.3-ce.0"}:                         "set_image gitlab gitlab/gitlab-ce:18.5.3-ce.0",
		{Op: "recreate", Services: []string{"web", "worker"}}:                                                       "recreate web worker",
		{Op: "db_dump_restore", Service: "postgres", ToImage: "postgres", ToTag: "17.6"}:                            "db_dump_restore postgres to postgres:17.6",
		{Op: "compose_edit", Service: "immich", Edits: []plan.Edit{{Action: "rename_env"}, {Action: "remove_env"}}}: "compose_edit immich 2 edits",
		{Op: "wait_stable", Services: []string{"gitlab"}, Seconds: 300}:                                             "wait_stable gitlab 300 s",
	} {
		if got := describeOp(*op); got != want {
			t.Errorf("describeOp = %q, want %q", got, want)
		}
	}
}

func TestColoredUsageKeepsTheText(t *testing.T) {
	plain := (&cli{}).usage()
	colored := (&cli{color: true}).usage()
	if plain != usage || colored == usage {
		t.Fatal("usage changed without color, or not colored with it")
	}
	strip := func(s string) string {
		var b strings.Builder
		esc := false
		for _, r := range s {
			switch {
			case r == '\x1b':
				esc = true
			case esc:
				esc = r != 'm'
			default:
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	if strip(colored) != usage {
		t.Errorf("colored usage differs from the plain one:\n%s", strip(colored))
	}
}

func TestInputHashFollowsRoles(t *testing.T) {
	stack := func(role string) *inventory.Stack {
		return &inventory.Stack{Services: []*inventory.Service{{Name: "n8n", Version: "2.45.7", Role: role}}}
	}
	if inputHash(stack(inventory.RoleApplication), config.Config{}) == inputHash(stack(inventory.RoleDependency), config.Config{}) {
		t.Error("a role change keeps the stored plan")
	}
}

func TestAIFailureSaysWhatToDo(t *testing.T) {
	for msg, want := range map[string]string{
		`Anthropic API: 401 Unauthorized: {"type":"authentication_error"}`:                                   "check PALIER_AI_KEY",
		`Anthropic API: 400 Bad Request: {"message":"prompt is too long: 1175981 tokens > 1000000 maximum"}`: "larger context",
		`AI API http://ollama:11434/v1: 404 Not Found: model "qwen" not found`:                               "check PALIER_AI_MODEL",
		`Anthropic API: 529 : {"type":"overloaded_error"}`:                                                   "palier scan asks now",
	} {
		if got := aiFailure(errors.New(msg)); !strings.Contains(got, want) || !strings.Contains(got, msg) {
			t.Errorf("%s: %s", msg, got)
		}
	}
}

func TestRetryStopsLoops(t *testing.T) {
	now := time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)
	hour, twoDays := now.Add(-time.Hour), now.Add(-48*time.Hour)
	for _, c := range []struct {
		name     string
		statuses []string
		at       time.Time
		ok       bool
		why      string
	}{
		{"no run yet", nil, now, true, ""},
		{"last run passed", []string{"done", "rolled_back"}, hour, true, ""},
		{"rolled back an hour ago", []string{"rolled_back", "done"}, hour, false, "next automatic try on"},
		{"rolled back two days ago", []string{"rolled_back", "done"}, twoDays, true, ""},
		{"abandoned an hour ago", []string{"abandoned"}, hour, false, "next automatic try on"},
		{"three rollbacks in a row, even long ago", []string{"rolled_back", "rolled_back", "rolled_back"}, twoDays, false, "last 3 runs were rolled back"},
		{"a rollback that failed, even long ago", []string{"failed", "done"}, twoDays, false, "its rollback did not pass"},
	} {
		ok, why := retry(7, c.statuses, c.at, now)
		if ok != c.ok || !strings.Contains(why, c.why) {
			t.Errorf("%s: %v %q", c.name, ok, why)
		}
	}
}
