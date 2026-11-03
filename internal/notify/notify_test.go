package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kolapsis/palier/internal/store"
)

type fake struct {
	level string
	sent  []Message
	fail  error
}

func (f *fake) Name() string     { return "fake" }
func (f *fake) Describe() string { return "fake" }
func (f *fake) Wants(m Message) bool {
	return (&Email{Level: f.level}).Wants(m)
}
func (f *fake) Send(_ context.Context, m Message) error {
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, m)
	return nil
}

func setup(t *testing.T, level string) (*Notifier, *fake) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fake{level: level}
	return New(db, []Sender{f}, "https://palier.example"), f
}

func subjects(f *fake) string {
	var s []string
	for _, m := range f.sent {
		s = append(s, m.Subject)
	}
	return strings.Join(s, " | ")
}

func TestOpenedOnceThenResolved(t *testing.T) {
	ctx := context.Background()
	n, f := setup(t, Info)
	broken := Problem{Key: "gitlab:rolled_back", Stack: "gitlab", Level: Info, Title: "gitlab: update rolled back", Detail: "step 1 failed", Path: "/runs/3"}
	for range 3 {
		if err := n.Sync(ctx, []Problem{broken}); err != nil {
			t.Fatal(err)
		}
		n.Deliver(ctx)
	}
	n.Sync(ctx, nil)
	n.Deliver(ctx)
	n.Sync(ctx, nil)
	n.Deliver(ctx)
	if got := subjects(f); got != "gitlab: update rolled back | Resolved: gitlab: update rolled back" {
		t.Fatalf("sent: %s", got)
	}
	if f.sent[0].Link != "https://palier.example/runs/3" {
		t.Errorf("link %q", f.sent[0].Link)
	}
}

func TestFlakyProblemNeedsTwoScans(t *testing.T) {
	ctx := context.Background()
	n, f := setup(t, Info)
	limit := Problem{Key: "immich/ml:detection", Stack: "immich", Level: Info, Title: "immich: versions of ml unknown", Flaky: true}
	n.Sync(ctx, []Problem{limit})
	n.Sync(ctx, nil)
	n.Sync(ctx, []Problem{limit})
	n.Deliver(ctx)
	if len(f.sent) != 0 {
		t.Fatalf("a problem seen once at a time was sent: %s", subjects(f))
	}
	n.Sync(ctx, []Problem{limit})
	n.Deliver(ctx)
	if len(f.sent) != 1 {
		t.Fatalf("seen twice in a row, not sent: %s", subjects(f))
	}
}

func TestAlertLevelKeepsOnlyAlerts(t *testing.T) {
	ctx := context.Background()
	n, f := setup(t, Alert)
	n.Sync(ctx, []Problem{{Key: "a", Level: Info, Title: "info"}, {Key: "b", Level: Alert, Title: "boom"}})
	n.Sync(ctx, nil)
	n.Deliver(ctx)
	if got := subjects(f); got != "Alert: boom | Resolved: boom" {
		t.Fatalf("sent: %s", got)
	}
}

func TestFailedDeliveryIsRetriedLater(t *testing.T) {
	ctx := context.Background()
	n, f := setup(t, Info)
	f.fail = errors.New("smtp 535")
	n.Sync(ctx, []Problem{{Key: "a", Level: Alert, Title: "boom"}})
	n.Deliver(ctx)
	c := n.Channels(ctx)[0]
	if c.Last == nil || c.Last.Error != "smtp 535" || c.Last.Attempts != 1 || c.Last.Sent != "" {
		t.Fatalf("last delivery: %+v", c.Last)
	}
	f.fail = nil
	n.Deliver(ctx)
	if len(f.sent) != 0 {
		t.Fatal("retried before its delay")
	}
	n.db.Exec(`UPDATE outbox SET next_try = ?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339))
	n.Deliver(ctx)
	if len(f.sent) != 1 || n.Channels(ctx)[0].Last.Sent == "" {
		t.Fatalf("not sent after its delay: %d", len(f.sent))
	}
}

func TestReportOnlyWhenSomethingHappened(t *testing.T) {
	ctx := context.Background()
	n, f := setup(t, Info)
	empty := func(since, until time.Time) Message { return Message{Subject: "Weekly report"} }
	if queued, err := n.SendReport(ctx, empty, true); err != nil || queued {
		t.Fatalf("empty report queued: %v %v", queued, err)
	}
	n.Sync(ctx, []Problem{{Key: "a", Level: Info, Title: "gitlab: a decision is needed"}})
	n.Deliver(ctx)
	if queued, _ := n.SendReport(ctx, empty, true); !queued {
		t.Fatal("report with an open problem not queued")
	}
	n.Deliver(ctx)
	last := f.sent[len(f.sent)-1]
	if last.Kind != Report || len(last.Lists) != 2 || last.Lists[1].Title != "Still open" {
		t.Fatalf("report: %+v", last)
	}
	if queued, _ := n.SendReport(ctx, empty, false); queued {
		t.Error("a second report within a day")
	}
}

func TestEmailRendersTextAndEscapedHTML(t *testing.T) {
	e := &Email{From: "palier@example.com", To: []string{"ops@example.com"}, Level: Info}
	body, err := e.render(Message{Kind: Opened, Level: Alert, Subject: "Alert: gitlab: rollback failed", Title: "gitlab: rollback failed",
		Summary: `<script>alert(1)</script> step 1`, Fields: []Field{{"Stack", "gitlab"}}, Action: "Read palier log 3.", Link: "https://palier.example/runs/3"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{"Subject: [Palier] Alert: gitlab: rollback failed", "multipart/alternative", "Content-Type: text/plain", "Content-Type: text/html", "Auto-Submitted: auto-generated", "&lt;script&gt;"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestLocal(t *testing.T) {
	for host, want := range map[string]bool{"mailpit": true, "localhost": true, "10.10.50.10": true, "192.168.1.5": true, "smtp.gmail.com": false, "8.8.8.8": false} {
		if Local(host) != want {
			t.Errorf("Local(%s) != %v", host, want)
		}
	}
}

func TestNoticeIsNeverSent(t *testing.T) {
	ctx := context.Background()
	n, f := setup(t, Info)
	n.Sync(ctx, []Problem{{Key: "a", Level: Info, Title: "versions unknown"}})
	n.Deliver(ctx)
	n.Sync(ctx, []Problem{{Key: "a", Level: Notice, Title: "versions unknown"}})
	n.Sync(ctx, nil)
	n.Sync(ctx, []Problem{{Key: "b", Level: Notice, Title: "other"}})
	n.Deliver(ctx)
	if got := subjects(f); got != "versions unknown" {
		t.Fatalf("sent: %s", got)
	}
	if open, _ := n.Incidents(ctx, true); len(open) != 1 || open[0].Level != Notice {
		t.Errorf("open: %+v", open)
	}
}
