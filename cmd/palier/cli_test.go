package main

import (
	"bytes"
	"strings"
	"testing"

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
