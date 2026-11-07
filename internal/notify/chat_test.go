package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatBody(t *testing.T) {
	text := "Alert: x\n<!channel> @everyone [click](https://evil.example) *bold*\n# title\n- item palier-lab\n> quote"
	var slack struct{ Text string }
	b, _ := (&Chat{Type: "slack"}).body(Message{}, text)
	json.Unmarshal(b, &slack)
	if strings.ContainsAny(slack.Text, "<>") || !strings.Contains(slack.Text, "&lt;!channel&gt;") {
		t.Errorf("slack: %q", slack.Text)
	}
	var discord struct {
		Content         string
		AllowedMentions struct{ Parse []string } `json:"allowed_mentions"`
	}
	b, _ = (&Chat{Type: "discord"}).body(Message{}, text)
	json.Unmarshal(b, &discord)
	if !strings.Contains(string(b), `"parse":[]`) || !strings.Contains(discord.Content, `\[click\]`) || !strings.Contains(discord.Content, `\*bold\*`) || !strings.Contains(discord.Content, "\n\\# title\n\\- item palier-lab\n\\> quote") {
		t.Errorf("discord: %s", b)
	}
	b, ct := (&Chat{Type: "ntfy"}).body(Message{}, strings.Repeat("é", 5000))
	if ct != "text/plain; charset=utf-8" || len([]rune(string(b))) != 4001 {
		t.Errorf("ntfy: %s, %d runes", ct, len([]rune(string(b))))
	}
	var telegram map[string]any
	b, _ = (&Chat{Type: "telegram"}).body(Message{}, text)
	json.Unmarshal(b, &telegram)
	if telegram["text"] != text || telegram["parse_mode"] != nil {
		t.Errorf("telegram: %s", b)
	}
}

func TestChatDescribeHidesSecret(t *testing.T) {
	d := (&Chat{Type: "slack", URL: "https://hooks.slack.com/services/T0/B0/secret", Level: "alert"}).Describe()
	if d != "hooks.slack.com, level alert" {
		t.Error(d)
	}
}
