package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ChatTypes are the services a Chat posts to.
var ChatTypes = []string{"ntfy", "slack", "discord", "telegram"}

// Chat posts messages as plain text to ntfy, a Slack or Discord webhook, or the Telegram Bot API; URL carries the secret of the service.
type Chat struct {
	Type    string
	URL     string
	Level   string
	Machine string
}

// Name names the channel.
func (c *Chat) Name() string { return c.Type }

// Describe sums up the channel without its secret.
func (c Chat) Describe() string {
	u, _ := url.Parse(c.URL)
	return fmt.Sprintf("%s, level %s", u.Hostname(), c.Level)
}

// Wants tells whether the level of the channel lets m through: alert only, or alert and information.
func (c *Chat) Wants(m Message) bool { return wants(c.Level, m) }

var (
	slackEscape   = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	discordEscape = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`, "<", `\<`, "[", `\[`, "]", `\]`)
	discordLine   = regexp.MustCompile(`(?m)^(\s*)([#>-]|\d+\.)`)
)

// Send posts m and returns the refusal of the service, without the address that holds the secret.
func (c *Chat) Send(ctx context.Context, m Message) error {
	var text bytes.Buffer
	if err := textMail.Execute(&text, mailData(m, c.Machine, c.Level)); err != nil {
		return err
	}
	body, contentType := c.body(m, text.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid address")
	}
	req.Header.Set("Content-Type", contentType)
	if c.Type == "ntfy" {
		req.Header.Set("Title", mime.QEncoding.Encode("utf-8", "[Palier] "+m.Subject))
		switch {
		case m.Kind == Opened && m.Level == Alert:
			req.Header.Set("Priority", "high")
			req.Header.Set("Tags", "warning")
		case m.Kind == Resolved:
			req.Header.Set("Tags", "white_check_mark")
		}
		if m.Link != "" {
			req.Header.Set("Click", m.Link)
		}
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("http %d: %s", resp.StatusCode, bytes.TrimSpace(answer))
	}
	return nil
}

func (c *Chat) body(m Message, text string) ([]byte, string) {
	var v any
	switch c.Type {
	case "ntfy":
		return []byte(truncate(text, 4000)), "text/plain; charset=utf-8"
	case "slack":
		v = map[string]any{"text": slackEscape.Replace(truncate(text, 3500))}
	case "discord":
		v = map[string]any{"content": truncate(discordLine.ReplaceAllString(discordEscape.Replace(text), `$1\$2`), 1900), "allowed_mentions": map[string]any{"parse": []string{}}}
	case "telegram":
		v = map[string]any{"text": truncate(text, 4000), "link_preview_options": map[string]any{"is_disabled": true}}
	}
	b, _ := json.Marshal(v)
	return b, "application/json"
}
