package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/notify"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
	"github.com/kolapsis/palier/internal/web"
)

const usage = `Palier updates Docker Compose stacks step by step, with a checkpoint and a rollback.

Usage:
  palier                                   state of every stack (same as palier status)
  palier show <stack>                      services, advisories, upgrade path and plan of a stack
  palier apply <stack> [--dry-run] [--yes] [--detach]
                                           apply the plan and follow the run
  palier runs [stack]                      last runs
  palier log <run> [-f]                    log of a run, -f to follow it
  palier ignore <stack> <service> <version>
                                           never propose this version again
  palier postpone <stack> <YYYY-MM-DD>     no automatic apply before this date
  palier scan                              scan now and show the state
  palier notify                            channels, last message sent, open problems
  palier notify test                       send a test message on every channel
  palier notify report                     send the report of what happened since the last one
  palier serve                             run Palier (the container does it)

Every command takes --json for scripts.
Scripting without the running Palier:
  palier scan --once                       scan in this process and print the inventory as JSON
  palier plan <stack> --once               compute the plan in this process and print it as JSON
`

type cli struct {
	http  *http.Client
	sock  string
	json  bool
	color bool
	out   io.Writer
}

func newCLI(cfg config.Config, asJSON bool) *cli {
	sock := socketPath(cfg)
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}
	return &cli{
		http:  &http.Client{Transport: tr, Timeout: 10 * time.Minute},
		sock:  sock,
		json:  asJSON,
		color: terminal(os.Stdout) && os.Getenv("NO_COLOR") == "",
		out:   os.Stdout,
	}
}

func terminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// call sends a request to the running Palier and decodes its JSON answer into out.
func (c *cli) call(method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, "http://palier"+path, body)
	if err != nil {
		return err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("Palier is not running here (no socket at %s): run this command in the Palier container, for example docker exec palier palier", c.sock)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		return errors.New(cmp.Or(e.Error, resp.Status))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (c *cli) printJSON(v any) error {
	enc := json.NewEncoder(c.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// run executes one command of the command line.
func (c *cli) run(cmd string, args []string) error {
	switch {
	case cmd == "status" && len(args) == 0:
		return c.status()
	case cmd == "show" && len(args) == 1:
		return c.show(args[0])
	case cmd == "runs" && len(args) <= 1:
		return c.runs(strings.Join(args, ""))
	case cmd == "ignore" && len(args) == 3:
		return c.decide(args[0], "ignore", url.Values{"service": {args[1]}, "version": {args[2]}},
			fmt.Sprintf("%s %s of %s will not be proposed again.", args[1], args[2], args[0]))
	case cmd == "postpone" && len(args) == 2:
		return c.decide(args[0], "postpone", url.Values{"until": {args[1]}},
			fmt.Sprintf("No automatic apply of %s before %s.", args[0], args[1]))
	case cmd == "scan" && len(args) == 0:
		return c.scan()
	case cmd == "notify" && len(args) == 0:
		return c.notifications()
	case cmd == "notify" && len(args) == 1 && args[0] == "test":
		if err := c.call(http.MethodPost, "/notify/test", url.Values{}, &map[string]bool{}); err != nil {
			return err
		}
		c.line(c.paint("ok", "Test message accepted by every channel."))
		return nil
	case cmd == "notify" && len(args) == 1 && args[0] == "report":
		var r struct{ Queued bool }
		if err := c.call(http.MethodPost, "/notify/report", url.Values{}, &r); err != nil {
			return err
		}
		if r.Queued {
			c.line("Report queued; palier notify shows when it is sent.")
		} else {
			c.line("Nothing happened since the last report: nothing sent.")
		}
		return nil
	}
	return errUsage
}

var errUsage = errors.New("usage")

func printUsage(f *os.File) {
	c := &cli{color: terminal(f) && os.Getenv("NO_COLOR") == "", out: f}
	fmt.Fprint(f, c.usage())
}

// usage returns the help with the title and sections in bold and the commands in color.
func (c *cli) usage() string {
	lines := strings.Split(usage, "\n")
	for i, l := range lines {
		switch {
		case i == 0 || strings.HasSuffix(l, ":"):
			lines[i] = c.bold(l)
		case strings.HasPrefix(l, "  palier"):
			cmd, desc, found := strings.Cut(strings.TrimPrefix(l, "  "), "   ")
			lines[i] = "  " + c.paint("accent", cmd)
			if found {
				lines[i] += "   " + desc
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (c *cli) status() error {
	var st web.Status
	if err := c.call(http.MethodGet, "/status", nil, &st); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(st)
	}
	if st.At.IsZero() {
		c.line(c.paint("run", "First scan in progress: versions are being checked."))
	} else {
		c.line(c.dim("Last scan " + web.Since(st.At) + "."))
	}
	if st.Error != "" {
		c.line(c.paint("bad", clean(st.Error)))
	}
	c.incidents(st.Incidents)
	if len(st.Stacks) == 0 {
		c.line("No container on this Docker engine yet.")
		return nil
	}
	rows := [][]cell{}
	for _, l := range st.Stacks {
		rows = append(rows, []cell{{text: l.Name, bold: true}, {text: l.State, tone: web.Tone(l.State)}, {text: l.Running}, {text: l.Target, tone: "accent"}, {text: l.Note, tone: "muted"}})
	}
	c.line("")
	c.table([]string{"STACK", "STATE", "RUNNING", "TARGET", "NOTE"}, rows)
	return nil
}

func (c *cli) incidents(list []notify.Incident) {
	for _, i := range list {
		tone, label := "warn", "info"
		if i.Level == notify.Alert {
			tone, label = "bad", "alert"
		}
		c.line(c.paint(tone, c.bold(label+": ")+clean(i.Title)) + c.dim(" since "+when(i.Opened)))
	}
}

func (c *cli) notifications() error {
	var n struct {
		Channels  []notify.Channel
		Incidents []notify.Incident
	}
	if err := c.call(http.MethodGet, "/notify", nil, &n); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(n)
	}
	if len(n.Channels) == 0 {
		c.line("No channel: set PALIER_SMTP_HOST and PALIER_EMAIL_TO to receive emails. Problems are still listed below.")
	}
	for _, ch := range n.Channels {
		c.line(c.bold(ch.Name) + "  " + clean(ch.Describe))
		switch d := ch.Last; {
		case d == nil:
			c.line(c.dim("  nothing sent yet"))
		case d.Sent != "":
			c.line(c.paint("ok", "  sent "+when(d.Sent)) + "  " + clean(d.Subject))
		case d.Error != "":
			c.line(c.paint("bad", fmt.Sprintf("  failed %d time%s: %s", d.Attempts, plural(d.Attempts), clean(d.Error))) + "  " + clean(d.Subject))
		default:
			c.line(c.paint("run", "  waiting to be sent") + "  " + clean(d.Subject))
		}
	}
	c.line("")
	if len(n.Incidents) == 0 {
		c.line(c.paint("ok", "No open problem."))
		return nil
	}
	c.line(c.bold("Open problems"))
	c.incidents(n.Incidents)
	return nil
}

func (c *cli) view(stack string) (web.StackView, error) {
	var v web.StackView
	return v, c.call(http.MethodGet, "/stacks/"+url.PathEscape(stack), nil, &v)
}

func (c *cli) show(stack string) error {
	v, err := c.view(stack)
	if err != nil {
		return err
	}
	if c.json {
		return c.printJSON(v)
	}
	st := v.Stack
	c.line(c.bold(clean(st.Name)) + "  " + c.paint(web.Tone(v.State), v.State))
	scanned := "first scan in progress"
	if !v.At.IsZero() {
		scanned = "last scan " + web.Since(v.At)
	}
	c.line(c.dim(strings.TrimPrefix(clean(st.WorkingDir)+" · "+scanned, " · ")))
	for _, e := range append(st.Errors, v.Error) {
		if e != "" {
			c.line(c.paint("bad", clean(e)))
		}
	}
	if v.Postponed != "" {
		c.line("Automatic apply postponed until " + v.Postponed + ".")
	}
	c.line("")
	rows := [][]cell{}
	stops := false
	for _, s := range st.Services {
		version := clean(cmp.Or(s.Version, s.Tag))
		running := version
		if s.Version != "" && s.Version != s.Tag {
			running += c.dim(" (" + clean(s.Tag) + ")")
		}
		state := s.State
		if s.Health != "" {
			state += " " + s.Health
		}
		if s.Restarts > 0 {
			state += fmt.Sprintf(", %d restart%s", s.Restarts, plural(s.Restarts))
		}
		var path cell
		switch crumbs := v.Paths[s.Name]; {
		case s.BuiltLocally:
			path = cell{text: "built locally: no registry to check", tone: "muted"}
		case v.At.IsZero():
			path = cell{text: "checking versions", tone: "run"}
		case len(crumbs) > 1:
			var parts []string
			for i, cr := range crumbs {
				tag := clean(cr.Tag)
				switch {
				case i == 0:
				case cr.Stop:
					tag, stops = c.paint("warn", tag+"*"), true
				case cr.Last:
					tag = c.paint("accent", tag)
				}
				parts = append(parts, tag)
			}
			path = cell{text: strings.Join(parts, " → "), raw: true}
		case len(s.Available) > 0 && v.Record != nil:
			path = cell{text: "stays on " + version + ": the plan does not change it", tone: "muted"}
		default:
			path = cell{text: "up to date", tone: "ok"}
		}
		rows = append(rows, []cell{{text: s.Name, bold: true}, {text: s.Role, tone: "muted"}, {text: running, raw: true}, {text: state, tone: stateTone(s.State, s.Health)}, path})
	}
	c.table([]string{"SERVICE", "ROLE", "RUNNING", "STATE", "UPGRADE PATH"}, rows)
	if stops {
		c.line(c.dim("* required stop from the catalog card"))
	}
	for _, s := range st.Services {
		for _, e := range []string{s.DetectionError, s.NotesError} {
			if e != "" {
				c.line(c.paint("bad", clean(s.Name+": "+e)))
			}
		}
		if len(s.Maturing) > 0 {
			var tags []string
			for _, r := range s.Maturing {
				tags = append(tags, clean(r.Tag))
			}
			c.line(c.dim(clean(s.Name) + ": maturing " + strings.Join(tags, ", ")))
		}
		if s.Republished {
			c.line(c.dim(clean(s.Name) + ": tag republished in the registry with different content, not a new version"))
		}
	}
	for _, s := range st.Services {
		if len(s.Advisories) > 0 {
			c.advisories(s.Name, s.Advisories)
		}
	}
	c.plan(v)
	if len(v.Runs) > 0 {
		c.line("")
		c.line(c.bold("Runs"))
		c.runTable(v.Runs[:min(len(v.Runs), 5)])
	}
	return nil
}

func stateTone(state, health string) string {
	switch {
	case state != "running" || health == "unhealthy":
		return "bad"
	case health == "starting":
		return "warn"
	}
	return ""
}

func (c *cli) advisories(service string, list []notes.Advisory) {
	c.line("")
	c.line(c.bold(clean(service)+": ") + c.paint("bad", clean(web.Severities(list))))
	rows := [][]cell{}
	for _, a := range web.WorstFirst(list) {
		tone := "muted"
		switch a.Severity {
		case "critical", "high":
			tone = "bad"
		case "medium":
			tone = "warn"
		}
		rows = append(rows, []cell{{text: cmp.Or(a.Severity, "unknown"), tone: tone}, {text: cmp.Or(a.CVE, a.ID)}, {text: a.Summary}})
	}
	c.indented(rows)
}

func (c *cli) plan(v web.StackView) {
	if v.Record == nil || v.Record.Plan == nil {
		return
	}
	p := v.Record.Plan
	source := "AI plan"
	if v.Record.Source != "ai" {
		source = "Plan computed from versions only"
	}
	c.line("")
	c.line(c.bold("Plan") + c.dim(fmt.Sprintf(" · %s, confirmed %s · %d step%s", source, web.Since(v.Record.Created), len(p.Steps), plural(len(p.Steps)))))
	switch {
	case len(p.Steps) == 0:
		c.line("Nothing to apply.")
	case v.Auto:
		c.line("Automatic apply will pick it up.")
	case v.AutoWhy != "":
		c.line("No automatic apply: " + clean(v.AutoWhy) + ".")
	}
	if len(p.Incomplete) > 0 {
		c.line(c.paint("bad", "Incomplete plan, it will not be applied:"))
		for _, r := range p.Incomplete {
			c.line(c.paint("bad", "  - "+clean(r)))
		}
	}
	if len(p.Withheld) > 0 {
		c.line("Versions left out:")
		for _, r := range p.Withheld {
			c.line("  - " + clean(r))
		}
	}
	for i, s := range p.Steps {
		c.line("")
		var changes []string
		for _, ch := range s.Changes {
			changes = append(changes, fmt.Sprintf("%s %s → %s", clean(ch.Service), clean(ch.From), clean(ch.To)))
		}
		c.line(fmt.Sprintf("%s  %s   %s %s", c.bold(fmt.Sprintf("%2d", i+1)), strings.Join(changes, ", "),
			c.paint(web.Tone(s.Risk), "risk "+s.Risk), c.paint(web.Tone(s.Reversible), "reversible "+s.Reversible)))
		if s.Why != "" {
			c.line("    " + clean(s.Why))
		}
		if len(s.StopDuring) > 0 {
			c.line(c.dim("    stopped during the step: " + clean(strings.Join(s.StopDuring, ", "))))
		}
		var ops []string
		for _, op := range s.Operations {
			ops = append(ops, describeOp(op))
		}
		c.line(c.dim("    " + clean(strings.Join(ops, " · "))))
		if g := s.Gate; g != nil && g.Op == "exec_check" {
			c.line(c.dim("    then waits for " + clean(g.CheckRef+" on "+g.Service)))
		}
		for _, f := range s.Facts {
			quote, _, _ := strings.Cut(clean(f.Quote), "\n")
			if utf8.RuneCountInString(quote) > 110 {
				quote = string([]rune(quote)[:110]) + "…"
			}
			c.line(fmt.Sprintf("    %s %s %s", c.paint("accent", clean(f.Category)), c.dim(clean(f.Service+" "+f.Version)), "“"+quote+"”"))
			c.line(c.dim("      " + clean(f.SourceURL)))
		}
	}
	if len(p.Steps) > 0 && len(p.Incomplete) == 0 {
		c.line("")
		c.line(c.dim(fmt.Sprintf("palier apply %s  ·  palier apply %s --dry-run  ·  palier ignore %s <service> <version>", v.Stack.Name, v.Stack.Name, v.Stack.Name)))
	}
}

func describeOp(op plan.Operation) string {
	parts := append([]string{op.Op}, op.Targets()...)
	switch {
	case op.Image != "":
		parts = append(parts, op.Image+":"+op.Tag)
	case op.Tag != "":
		parts = append(parts, op.Tag)
	}
	if op.ToTag != "" {
		parts = append(parts, "to "+strings.TrimPrefix(op.ToImage+":", ":")+op.ToTag)
	}
	if op.CheckRef != "" {
		parts = append(parts, op.CheckRef)
	}
	if op.Seconds > 0 {
		parts = append(parts, fmt.Sprintf("%d s", op.Seconds))
	}
	if n := len(op.Edits); n > 0 {
		parts = append(parts, fmt.Sprintf("%d edit%s", n, plural(n)))
	}
	return strings.Join(parts, " ")
}

func (c *cli) runs(stack string) error {
	var runs []run.View
	if err := c.call(http.MethodGet, "/runs?stack="+url.QueryEscape(stack), nil, &runs); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(runs)
	}
	if len(runs) == 0 {
		c.line("No run yet.")
		return nil
	}
	c.runTable(runs)
	return nil
}

func (c *cli) runTable(runs []run.View) {
	rows := [][]cell{}
	for _, r := range runs {
		result := cell{text: resultLabel(r.Status), tone: web.Tone(r.Status)}
		if r.DryRun {
			result.text = "dry run, " + result.text
		}
		rows = append(rows, []cell{{text: fmt.Sprint(r.ID), bold: true}, {text: r.Stack}, {text: when(r.Started)}, {text: r.Author, tone: "muted"}, result})
	}
	c.table([]string{"RUN", "STACK", "STARTED", "BY", "RESULT"}, rows)
}

func resultLabel(status string) string {
	switch status {
	case run.Done:
		return "completed"
	case run.RolledBack:
		return "rolled back"
	}
	return status
}

func when(v string) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	return t.Local().Format("Jan 2 15:04")
}

func (c *cli) decide(stack, kind string, form url.Values, done string) error {
	var ok map[string]bool
	if err := c.call(http.MethodPost, "/stacks/"+url.PathEscape(stack)+"/"+kind, form, &ok); err != nil {
		return err
	}
	if c.json {
		return c.printJSON(ok)
	}
	c.line(done)
	return nil
}

func (c *cli) scan() error {
	var started struct{ Asked time.Time }
	if err := c.call(http.MethodPost, "/scan", url.Values{}, &started); err != nil {
		return err
	}
	if terminal(os.Stderr) && !c.json {
		fmt.Fprintln(os.Stderr, c.dim("Scanning: registries, release notes and advisories of every image…"))
	}
	for {
		var st web.Status
		if err := c.call(http.MethodGet, "/status", nil, &st); err != nil {
			return err
		}
		if !st.Began.Before(started.Asked) {
			return c.status()
		}
		time.Sleep(2 * time.Second)
	}
}

// apply shows the plan, asks for confirmation in a terminal, starts the run in Palier and follows it.
func (c *cli) apply(stack string, dryRun, yes, detach bool) error {
	v, err := c.view(stack)
	if err != nil {
		return err
	}
	if v.Record == nil || len(v.Record.Plan.Steps) == 0 {
		return fmt.Errorf("%s has no plan to apply", stack)
	}
	if !c.json {
		verb := "Apply"
		if dryRun {
			verb = "Dry run of"
		}
		c.line(fmt.Sprintf("%s the plan of %s, %d step%s:", verb, c.bold(stack), len(v.Record.Plan.Steps), plural(len(v.Record.Plan.Steps))))
		for i, s := range v.Record.Plan.Steps {
			var changes []string
			for _, ch := range s.Changes {
				changes = append(changes, fmt.Sprintf("%s %s → %s", clean(ch.Service), clean(ch.From), clean(ch.To)))
			}
			c.line(fmt.Sprintf("  %2d  %s  %s", i+1, strings.Join(changes, ", "), c.paint(web.Tone(s.Risk), "risk "+s.Risk)))
		}
	}
	if !dryRun && !yes {
		if !terminal(os.Stdin) {
			return errors.New("no terminal to confirm: add --yes to apply without confirmation")
		}
		fmt.Fprint(c.out, "Apply it now? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			c.line("Nothing applied.")
			return nil
		}
	}
	form := url.Values{}
	if dryRun {
		form.Set("dry_run", "1")
	}
	var started struct{ Run int64 }
	if err := c.call(http.MethodPost, "/stacks/"+url.PathEscape(stack)+"/apply", form, &started); err != nil {
		return err
	}
	if detach {
		if c.json {
			return c.printJSON(started)
		}
		c.line(fmt.Sprintf("Run %d started. Follow it with: palier log %d -f", started.Run, started.Run))
		return nil
	}
	return c.follow(started.Run, true)
}

func (c *cli) log(id string, follow bool) error {
	var n int64
	if _, err := fmt.Sscan(id, &n); err != nil {
		return fmt.Errorf("invalid run number %q", id)
	}
	return c.follow(n, follow)
}

// follow prints the events of run id, waiting for new ones while it runs when wait is set.
func (c *cli) follow(id int64, wait bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	printed := 0
	for first := true; ; first = false {
		var v run.View
		if err := c.call(http.MethodGet, fmt.Sprintf("/runs/%d", id), nil, &v); err != nil {
			return err
		}
		if !c.json {
			if first {
				c.line(c.dim(fmt.Sprintf("Run %d · %s · by %s · started %s", v.ID, clean(v.Stack), clean(v.Author), when(v.Started))))
			}
			for _, e := range v.Events[printed:] {
				c.event(e)
			}
		}
		printed = len(v.Events)
		if v.Status != run.Running || !wait {
			if c.json {
				return c.printJSON(v)
			}
			if v.Status == run.Running {
				c.line(c.paint("run", "still running"))
				return nil
			}
			label := resultLabel(v.Status)
			if v.DryRun {
				label = "dry run " + label
			}
			c.line(c.bold(c.paint(web.Tone(v.Status), "Result: "+label)))
			if v.Status != run.Done {
				return errSilent
			}
			return nil
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "\nThe run goes on in Palier. Follow it again with: palier log %d -f\n", id)
			return errSilent
		case <-time.After(2 * time.Second):
		}
	}
}

var errSilent = errors.New("")

func (c *cli) event(e run.Event) {
	at := e.At
	if t, err := time.Parse(time.RFC3339, e.At); err == nil {
		at = t.Local().Format("15:04:05")
	}
	lines := strings.Split(clean(e.Message), "\n")
	c.line(c.dim(at) + "  " + lines[0])
	for _, l := range lines[1:] {
		c.line("          " + l)
	}
}

// clean removes the control characters of untrusted text, such as terminal escape sequences in release notes or logs.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
}

type cell struct {
	text string
	tone string
	bold bool
	raw  bool
}

var ansi = map[string]string{"ok": "32", "accent": "36", "run": "36", "warn": "33", "bad": "31", "muted": "2"}

func (c *cli) paint(tone, s string) string {
	if code := ansi[tone]; c.color && code != "" {
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	return s
}

func (c *cli) dim(s string) string { return c.paint("muted", s) }

func (c *cli) bold(s string) string {
	if c.color {
		return "\x1b[1m" + s + "\x1b[0m"
	}
	return s
}

func (c *cli) line(s string) { fmt.Fprintln(c.out, s) }

// width counts the runes of s that a terminal shows, leaving out the color sequences.
func width(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc:
			esc = r != 'm'
		default:
			n++
		}
	}
	return n
}

func (c *cli) table(header []string, rows [][]cell) {
	hdr := make([]cell, len(header))
	for i, h := range header {
		hdr[i] = cell{text: h, tone: "muted"}
	}
	c.grid(append([][]cell{hdr}, rows...), "")
}

func (c *cli) indented(rows [][]cell) { c.grid(rows, "  ") }

func (c *cli) grid(rows [][]cell, indent string) {
	shown := make([][]string, len(rows))
	widths := map[int]int{}
	for i, row := range rows {
		for j, cl := range row {
			text := strings.ReplaceAll(cl.text, "\n", " ")
			if !cl.raw {
				text = clean(text)
			}
			switch {
			case cl.bold:
				text = c.bold(text)
			case cl.tone != "":
				text = c.paint(cl.tone, text)
			}
			shown[i] = append(shown[i], text)
			widths[j] = max(widths[j], width(text))
		}
	}
	for _, row := range shown {
		var b strings.Builder
		b.WriteString(indent)
		for j, text := range row {
			b.WriteString(text)
			if j < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[j]-width(text)+2))
			}
		}
		c.line(strings.TrimRight(b.String(), " "))
	}
}
