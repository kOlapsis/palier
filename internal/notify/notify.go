// Package notify tells people what Palier needs or did: an alert or a piece of information when a problem opens and when it is resolved, and a weekly report.
package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Levels of a problem.
const (
	Alert = "alert"
	Info  = "info"
)

// Kinds of message.
const (
	Opened   = "opened"
	Resolved = "resolved"
	Report   = "report"
	Test     = "test"
)

// Problem is something wrong that Palier sees now, identified by Key across scans.
type Problem struct {
	Key    string
	Stack  string
	Level  string
	Title  string
	Detail string
	Action string
	Path   string
	Flaky  bool
}

// Field is one line of the table of a message.
type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Message is what a channel sends.
type Message struct {
	Kind    string  `json:"kind"`
	Level   string  `json:"level"`
	Subject string  `json:"subject"`
	Title   string  `json:"title"`
	Summary string  `json:"summary"`
	Fields  []Field `json:"fields,omitempty"`
	Action  string  `json:"action,omitempty"`
	Link    string  `json:"link,omitempty"`
	Lists   []List  `json:"lists,omitempty"`
}

// List is one section of a report.
type List struct {
	Title string   `json:"title"`
	Items []string `json:"items"`
}

// Sender is a channel: email today, others later.
type Sender interface {
	Name() string
	Describe() string
	Wants(m Message) bool
	Send(ctx context.Context, m Message) error
}

// Incident is a problem that was opened, and maybe resolved.
type Incident struct {
	Key      string `json:"key"`
	Stack    string `json:"stack"`
	Level    string `json:"level"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Opened   string `json:"opened"`
	Resolved string `json:"resolved,omitempty"`
}

// Delivery is the last message a channel sent or tried to send.
type Delivery struct {
	Subject  string `json:"subject"`
	Created  string `json:"created"`
	Sent     string `json:"sent,omitempty"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error,omitempty"`
}

// Channel is a sender as the command line and the web interface show it.
type Channel struct {
	Name     string    `json:"name"`
	Describe string    `json:"describe"`
	Last     *Delivery `json:"last,omitempty"`
}

// Notifier keeps the incidents, queues the messages and sends them.
type Notifier struct {
	db      *sql.DB
	senders []Sender
	public  string

	mu      sync.Mutex
	pending map[string]int
}

// New returns a notifier that sends through senders, with links to public when it is set.
func New(db *sql.DB, senders []Sender, public string) *Notifier {
	return &Notifier{db: db, senders: senders, public: strings.TrimSuffix(public, "/"), pending: map[string]int{}}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Sync opens the incidents of the problems seen now and resolves those no longer seen; a flaky problem must be seen twice in a row.
func (n *Notifier) Sync(ctx context.Context, problems []Problem) error {
	open, err := n.Incidents(ctx, true)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	seen := map[string]bool{}
	for _, p := range problems {
		seen[p.Key] = true
		if slices.ContainsFunc(open, func(i Incident) bool { return i.Key == p.Key }) {
			continue
		}
		if p.Flaky {
			if n.pending[p.Key]++; n.pending[p.Key] < 2 {
				continue
			}
		}
		delete(n.pending, p.Key)
		if _, err := n.db.ExecContext(ctx, `INSERT INTO incidents (key, stack, level, title, detail, opened) VALUES (?, ?, ?, ?, ?, ?)`,
			p.Key, p.Stack, p.Level, p.Title, p.Detail, now()); err != nil {
			return err
		}
		n.queue(ctx, n.message(Opened, p.Level, p.Stack, p.Title, p.Detail, p.Action, p.Path, now()))
	}
	for key := range n.pending {
		if !seen[key] {
			delete(n.pending, key)
		}
	}
	for _, i := range open {
		if seen[i.Key] {
			continue
		}
		if _, err := n.db.ExecContext(ctx, `UPDATE incidents SET resolved = ? WHERE key = ? AND resolved = ''`, now(), i.Key); err != nil {
			return err
		}
		n.queue(ctx, n.message(Resolved, i.Level, i.Stack, i.Title, i.Detail, "", "", i.Opened))
	}
	return nil
}

func (n *Notifier) message(kind, level, stack, title, detail, action, path, opened string) Message {
	m := Message{Kind: kind, Level: level, Title: title, Summary: truncate(detail, 500), Action: action}
	m.Fields = []Field{{"Stack", stack}}
	if kind == Resolved {
		m.Subject = "Resolved: " + title
		m.Summary = "This is over. It was: " + truncate(detail, 300)
		m.Fields = append(m.Fields, Field{"Opened", when(opened)}, Field{"Resolved", when(now())})
	} else {
		m.Subject = title
		if level == Alert {
			m.Subject = "Alert: " + title
		}
		m.Fields = append(m.Fields, Field{"Seen", when(opened)})
	}
	if n.public != "" && path != "" {
		m.Link = n.public + path
	}
	return m
}

func when(v string) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	return t.Local().Format("Mon Jan 2, 15:04 MST")
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (n *Notifier) queue(ctx context.Context, m Message) {
	body, _ := json.Marshal(m)
	for _, s := range n.senders {
		if !s.Wants(m) {
			continue
		}
		if _, err := n.db.ExecContext(ctx, `INSERT INTO outbox (channel, subject, message, created, next_try) VALUES (?, ?, ?, ?, ?)`, s.Name(), m.Subject, string(body), now(), now()); err != nil {
			slog.Error("notification queue", "channel", s.Name(), "err", err)
		}
	}
}

// Incidents returns the open incidents, or every incident when open is false.
func (n *Notifier) Incidents(ctx context.Context, open bool) ([]Incident, error) {
	rows, err := n.db.QueryContext(ctx, `SELECT key, stack, level, title, detail, opened, resolved FROM incidents WHERE NOT ? OR resolved = '' ORDER BY id`, open)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var i Incident
		if err := rows.Scan(&i.Key, &i.Stack, &i.Level, &i.Title, &i.Detail, &i.Opened, &i.Resolved); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Channels returns the senders with their last delivery.
func (n *Notifier) Channels(ctx context.Context) []Channel {
	var out []Channel
	for _, s := range n.senders {
		c := Channel{Name: s.Name(), Describe: s.Describe()}
		var d Delivery
		if n.db.QueryRowContext(ctx, `SELECT subject, created, sent, attempts, error FROM outbox WHERE channel = ? ORDER BY id DESC LIMIT 1`, s.Name()).
			Scan(&d.Subject, &d.Created, &d.Sent, &d.Attempts, &d.Error) == nil {
			c.Last = &d
		}
		out = append(out, c)
	}
	return out
}

// Test sends a test message through every channel at once and returns their errors.
func (n *Notifier) Test(ctx context.Context) error {
	if len(n.senders) == 0 {
		return errors.New("no notification channel: set PALIER_SMTP_HOST and PALIER_EMAIL_TO")
	}
	m := Message{Kind: Test, Level: Info, Subject: "Test message", Title: "Palier can reach you",
		Summary: "This is a test message. Palier writes here when a problem opens and when it is resolved, and sends a weekly report when something happened."}
	if n.public != "" {
		m.Link = n.public + "/"
	}
	var errs []error
	for _, s := range n.senders {
		if err := s.Send(ctx, m); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	return errors.Join(errs...)
}

var retries = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Deliver sends the queued messages that are due, and waits longer after each failure, up to five more attempts.
func (n *Notifier) Deliver(ctx context.Context) {
	rows, err := n.db.QueryContext(ctx, `SELECT id, channel, message, attempts FROM outbox WHERE sent = '' AND attempts <= ? AND next_try <= ? ORDER BY id`, len(retries), now())
	if err != nil {
		slog.Error("notification queue", "err", err)
		return
	}
	type due struct {
		id       int64
		channel  string
		body     string
		attempts int
	}
	var list []due
	for rows.Next() {
		var d due
		if rows.Scan(&d.id, &d.channel, &d.body, &d.attempts) == nil {
			list = append(list, d)
		}
	}
	if err := rows.Err(); err != nil {
		slog.Error("notification queue", "err", err)
	}
	rows.Close()
	for _, d := range list {
		i := slices.IndexFunc(n.senders, func(s Sender) bool { return s.Name() == d.channel })
		var m Message
		if i < 0 || json.Unmarshal([]byte(d.body), &m) != nil {
			n.db.ExecContext(ctx, `UPDATE outbox SET attempts = ?, error = 'channel no longer configured' WHERE id = ?`, len(retries)+1, d.id)
			continue
		}
		if err := n.senders[i].Send(ctx, m); err != nil {
			next := time.Now().Add(retries[min(d.attempts, len(retries)-1)]).UTC().Format(time.RFC3339)
			n.db.ExecContext(ctx, `UPDATE outbox SET attempts = attempts + 1, next_try = ?, error = ? WHERE id = ?`, next, truncate(err.Error(), 500), d.id)
			slog.Error("notification", "channel", d.channel, "subject", m.Subject, "attempt", d.attempts+1, "err", err)
			continue
		}
		n.db.ExecContext(ctx, `UPDATE outbox SET attempts = attempts + 1, sent = ?, error = '' WHERE id = ?`, now(), d.id)
		slog.Info("notification sent", "channel", d.channel, "subject", m.Subject)
	}
}

// Run delivers the queue every 20 seconds and sends the weekly report on Monday from 8:00, local time, until ctx ends.
func (n *Notifier) Run(ctx context.Context, report func(since, until time.Time) Message) {
	for {
		n.Deliver(ctx)
		if t := time.Now(); t.Weekday() == time.Monday && t.Hour() >= 8 {
			if _, err := n.SendReport(ctx, report, false); err != nil {
				slog.Error("weekly report", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}
	}
}

// SendReport queues the report of what happened since the last one, once a day at most unless force, and only when it has something to say.
func (n *Notifier) SendReport(ctx context.Context, report func(since, until time.Time) Message, force bool) (bool, error) {
	var last sql.NullString
	if err := n.db.QueryRowContext(ctx, `SELECT max(sent) FROM reports`).Scan(&last); err != nil {
		return false, err
	}
	until := time.Now()
	since := until.AddDate(0, 0, -7)
	if t, err := time.Parse(time.RFC3339, last.String); err == nil {
		if !force && until.Sub(t) < 24*time.Hour {
			return false, nil
		}
		since = t
	}
	if _, err := n.db.ExecContext(ctx, `INSERT INTO reports (sent) VALUES (?)`, until.UTC().Format(time.RFC3339)); err != nil {
		return false, err
	}
	m := report(since, until)
	opened, resolved, err := n.between(ctx, since, until)
	if err != nil {
		return false, err
	}
	still, err := n.Incidents(ctx, true)
	if err != nil {
		return false, err
	}
	for _, l := range []List{{"Problems opened", opened}, {"Problems resolved", resolved}, {"Still open", lines(still)}} {
		if len(l.Items) > 0 {
			m.Lists = append(m.Lists, l)
		}
	}
	if len(m.Lists) == 0 {
		return false, nil
	}
	m.Kind, m.Level = Report, Info
	if n.public != "" {
		m.Link = n.public + "/"
	}
	n.queue(ctx, m)
	return true, nil
}

func (n *Notifier) between(ctx context.Context, since, until time.Time) (opened, resolved []string, err error) {
	all, err := n.Incidents(ctx, false)
	for _, i := range all {
		o, _ := time.Parse(time.RFC3339, i.Opened)
		r, _ := time.Parse(time.RFC3339, i.Resolved)
		if !o.Before(since) && !o.After(until) {
			opened = append(opened, i.Title)
		}
		if !r.Before(since) && !r.After(until) {
			resolved = append(resolved, i.Title)
		}
	}
	return opened, resolved, err
}

func lines(list []Incident) []string {
	var out []string
	for _, i := range list {
		out = append(out, fmt.Sprintf("%s, since %s", i.Title, when(i.Opened)))
	}
	return out
}
