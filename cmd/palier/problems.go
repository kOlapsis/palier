package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notify"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
	"github.com/kolapsis/palier/internal/web"
)

// sync opens and resolves the incidents of what the last full scan shows.
func (a *app) sync(ctx context.Context, snap web.Snapshot) {
	if snap.At.IsZero() {
		return
	}
	last := map[string]run.View{}
	runs, err := run.List(ctx, a.db, "", 500)
	if err != nil {
		slog.Error("incidents", "err", err)
		return
	}
	for _, r := range runs {
		if _, ok := last[r.Stack]; !ok && !r.DryRun && r.Status != run.Running {
			last[r.Stack] = r
		}
	}
	plans := map[string]*plan.Plan{}
	for _, st := range snap.Stacks {
		if rec, err := a.storedPlan(ctx, st.Name); err == nil && rec != nil {
			plans[st.Name] = rec.Plan
		}
	}
	if err := a.notifier.Sync(ctx, problems(snap, last, plans, a.cfg.Auto)); err != nil {
		slog.Error("incidents", "err", err)
	}
}

// problems lists what is wrong now: alerts when Palier can no longer vouch for a stack, information when an update did not go through.
func problems(snap web.Snapshot, last map[string]run.View, plans map[string]*plan.Plan, auto string) []notify.Problem {
	var out []notify.Problem
	if snap.Err != "" {
		out = append(out, notify.Problem{Key: "palier:scan", Level: notify.Alert, Title: "Palier cannot read the Docker engine", Detail: snap.Err,
			Action: "Check that Docker runs and that Palier can reach its socket. Palier tries again at every scan.", Flaky: true})
	}
	for _, st := range snap.Stacks {
		if st.Observed {
			continue
		}
		page := "/stacks/" + st.Name
		p := plans[st.Name]
		pending := p != nil && len(p.Steps) > 0
		if r, ok := last[st.Name]; ok {
			log := fmt.Sprintf(" Details: palier log %d.", r.ID)
			path := fmt.Sprintf("/runs/%d", r.ID)
			cause := r.Error
			switch {
			case r.Status == run.Failed && (pending || !healthy(st)):
				out = append(out, notify.Problem{Key: st.Name + ":failed", Stack: st.Name, Level: notify.Alert, Title: st.Name + ": rollback failed",
					Detail: cause, Action: "Palier could not put the stack back as it was, and does not touch it any more. The run log says what is left in the modified state." + log, Path: path})
			case r.Status == run.RolledBack && pending:
				out = append(out, notify.Problem{Key: st.Name + ":rolled_back", Stack: st.Name, Level: notify.Info, Title: st.Name + ": update rolled back",
					Detail: cause, Action: "Nothing to do now: the stack is back on its last verified step and runs. Palier tries again in 24 hours, or as soon as the plan changes." + log, Path: path})
			case r.Status == run.Abandoned && pending:
				out = append(out, notify.Problem{Key: st.Name + ":abandoned", Stack: st.Name, Level: notify.Info, Title: st.Name + ": update not started",
					Detail: cause, Action: "Nothing was changed. Palier tries again in 24 hours, or as soon as the plan changes." + log, Path: path})
			case r.Status == run.Stopped && pending:
				out = append(out, notify.Problem{Key: st.Name + ":stopped", Stack: st.Name, Level: notify.Info, Title: st.Name + ": update stopped after a verified step",
					Detail: cause, Action: "The stack runs on the last verified step. The next steps start once the check passes." + log, Path: path})
			}
		}
		automatic := false
		if pending && len(p.Suspicious) > 0 {
			out = append(out, notify.Problem{Key: st.Name + ":injection", Stack: st.Name, Level: notify.Alert, Title: st.Name + ": release notes try to steer the AI",
				Detail: "“" + p.Suspicious[0] + "”", Action: fmt.Sprintf("Palier will not apply this plan on its own. Read the notes and the plan with palier show %s before you apply it.", st.Name), Path: page})
		} else if pending {
			ok, why := plan.Automatic(p, auto)
			automatic = ok
			if !ok {
				out = append(out, notify.Problem{Key: st.Name + ":decision", Stack: st.Name, Level: notify.Info, Title: st.Name + ": a decision is needed",
					Detail: "Palier will not apply this plan on its own: " + why + ".", Action: fmt.Sprintf("Read the plan with palier show %s, then palier apply %s, or ignore a version with palier ignore.", st.Name, st.Name), Path: page})
			}
		}
		var severe []string
		for _, s := range st.Services {
			for _, adv := range s.Advisories {
				if adv.Severity == "critical" || adv.Severity == "high" {
					severe = append(severe, fmt.Sprintf("%s %s (%s): %s", s.Name, cmp.Or(adv.CVE, adv.ID), adv.Severity, adv.Summary))
				}
			}
			if s.DetectionError != "" {
				out = append(out, notify.Problem{Key: st.Name + "/" + s.Name + ":detection", Stack: st.Name, Level: notify.Info, Title: st.Name + ": versions of " + s.Name + " unknown",
					Detail: s.DetectionError, Action: "Palier cannot update this service until it reads its registry again. Credentials and request limits are the usual causes.", Path: page, Flaky: true})
			}
		}
		if len(severe) > 0 && !automatic {
			detail := strings.Join(severe[:min(len(severe), 3)], "; ")
			if len(severe) > 3 {
				detail += fmt.Sprintf("; and %d more", len(severe)-3)
			}
			title := fmt.Sprintf("%s: %d critical or high security advisories", st.Name, len(severe))
			if len(severe) == 1 {
				title = st.Name + ": 1 critical or high security advisory"
			}
			out = append(out, notify.Problem{Key: st.Name + ":vulnerable", Stack: st.Name, Level: notify.Alert, Title: title,
				Detail: detail, Action: fmt.Sprintf("Palier cannot close them on its own. Read palier show %s.", st.Name), Path: page})
		}
	}
	return out
}

func healthy(st *inventory.Stack) bool {
	for _, s := range st.Services {
		if s.State != "running" || s.Health == "unhealthy" {
			return false
		}
	}
	return true
}

// report sums up, stack by stack, the versions the runs between since and until moved, and the runs that did not go through.
func (a *app) report(ctx context.Context, since, until time.Time) notify.Message {
	runs, err := run.List(ctx, a.db, "", 1000)
	if err != nil {
		slog.Error("weekly report", "err", err)
	}
	slices.Reverse(runs)
	return summarize(runs, since, until)
}

var outcomes = map[string]string{run.RolledBack: "rolled back", run.Abandoned: "not started", run.Failed: "rollback failed", run.Stopped: "stopped after a verified step"}

func summarize(runs []run.View, since, until time.Time) notify.Message {
	type move struct{ from, to string }
	type miss struct {
		count int
		last  time.Time
		cause string
	}
	moved := map[string]map[string]*move{}
	var order []string
	services := map[string][]string{}
	missed := map[[2]string]*miss{}
	var missOrder [][2]string
	for _, r := range runs {
		end, err := time.Parse(time.RFC3339, r.Finished)
		if err != nil || r.DryRun || end.Before(since) || end.After(until) {
			continue
		}
		if r.Status != run.Done {
			k := [2]string{r.Stack, r.Status}
			if missed[k] == nil {
				missed[k] = &miss{}
				missOrder = append(missOrder, k)
			}
			cause, _, _ := strings.Cut(r.Error, "\n")
			missed[k].count++
			missed[k].last, missed[k].cause = end, cause
			continue
		}
		if moved[r.Stack] == nil {
			moved[r.Stack] = map[string]*move{}
			order = append(order, r.Stack)
		}
		for _, c := range r.Changes {
			if m := moved[r.Stack][c.Service]; m != nil {
				m.to = c.To
				continue
			}
			moved[r.Stack][c.Service] = &move{c.From, c.To}
			services[r.Stack] = append(services[r.Stack], c.Service)
		}
	}
	var applied, other []string
	for _, st := range order {
		var parts []string
		for _, svc := range services[st] {
			m := moved[st][svc]
			switch {
			case m.from == m.to:
			case svc == st && len(services[st]) == 1:
				parts = append(parts, fmt.Sprintf("%s → %s", m.from, m.to))
			default:
				parts = append(parts, fmt.Sprintf("%s %s → %s", svc, m.from, m.to))
			}
		}
		if len(parts) > 0 {
			applied = append(applied, st+": "+strings.Join(parts, ", "))
		}
	}
	for _, k := range missOrder {
		m := missed[k]
		times := ""
		if m.count > 1 {
			times = fmt.Sprintf(" %d times, last", m.count)
		}
		line := fmt.Sprintf("%s: %s%s on %s", k[0], cmp.Or(outcomes[k[1]], k[1]), times, m.last.Local().Format("Mon Jan 2"))
		if m.cause != "" {
			line += ": " + truncate(m.cause, 140)
		}
		other = append(other, line)
	}
	m := notify.Message{Subject: "Weekly report", Title: "What Palier did since " + since.Local().Format("Mon Jan 2")}
	for _, l := range []notify.List{{Title: "Updated", Items: applied}, {Title: "Did not go through", Items: other}} {
		if len(l.Items) > 0 {
			m.Lists = append(m.Lists, l)
		}
	}
	stuck := map[string]bool{}
	for _, k := range missOrder {
		stuck[k[0]] = true
	}
	m.Summary = fmt.Sprintf("%d stack%s updated, %d with an update that did not go through.", len(applied), plural(len(applied)), len(stuck))
	if len(applied) > 0 {
		m.Subject = fmt.Sprintf("Weekly report: %d stack%s updated", len(applied), plural(len(applied)))
	}
	return m
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
