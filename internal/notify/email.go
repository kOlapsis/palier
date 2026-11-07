package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"fmt"
	htmltemplate "html/template"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"
)

//go:embed templates
var templates embed.FS

var (
	htmlMail = htmltemplate.Must(htmltemplate.ParseFS(templates, "templates/email.html"))
	textMail = texttemplate.Must(texttemplate.ParseFS(templates, "templates/email.txt"))
)

// Email sends messages through an SMTP server, with implicit TLS on port 465 and STARTTLS elsewhere.
type Email struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	To       []string
	Level    string
	Machine  string
}

// Name names the channel.
func (e *Email) Name() string { return "email" }

// Describe sums up the channel without its secret.
func (e *Email) Describe() string {
	return fmt.Sprintf("%s through %s:%d, level %s", strings.Join(e.To, ", "), e.Host, e.Port, e.Level)
}

// Wants tells whether the level of the channel lets m through: alert only, or alert and information.
func (e *Email) Wants(m Message) bool { return wants(e.Level, m) }

func wants(level string, m Message) bool {
	return m.Kind == Report || m.Kind == Test || m.Level == Alert || level != Alert
}

// Send writes m to every recipient.
func (e *Email) Send(ctx context.Context, m Message) error {
	body, err := e.render(m)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	if e.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: e.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	return e.deliver(conn, body)
}

// deliver speaks SMTP on conn: STARTTLS when offered, required off the local network, then the message to every recipient.
func (e *Email) deliver(conn net.Conn, body []byte) error {
	conn.SetDeadline(time.Now().Add(time.Minute))
	c, err := smtp.NewClient(conn, e.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if e.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			// A relay on the local network often offers a self-signed certificate, whose check proves nothing.
			if err := c.StartTLS(&tls.Config{ServerName: e.Host, InsecureSkipVerify: Local(e.Host)}); err != nil {
				return err
			}
		} else if !Local(e.Host) {
			return fmt.Errorf("%s offers no STARTTLS: use port 465, or a server on the local network", e.Host)
		}
	}
	if e.User != "" {
		if err := c.Auth(smtp.PlainAuth("", e.User, e.Password, e.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(e.From); err != nil {
		return err
	}
	for _, to := range e.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("recipient %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// Local tells whether host is on the local network: localhost, a private address, or a name without a dot such as a Compose service.
func Local(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || !strings.Contains(host, ".") || ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

var labels = map[string]string{Resolved: "Resolved", Report: "Weekly report", Test: "Test"}

var colors = map[string]string{Alert: "#b4342d", Info: "#9a620f", Resolved: "#2e7d4f", Report: "#1d5c8c", Test: "#1d5c8c"}

// render builds the whole message: headers, then a text part and an HTML part.
func (e *Email) render(m Message) ([]byte, error) {
	color := colors[m.Level]
	if c, ok := colors[m.Kind]; ok {
		color = c
	}
	data := mailData(m, e.Machine, e.Level)
	data["Color"] = color
	var html, text bytes.Buffer
	if err := htmlMail.Execute(&html, data); err != nil {
		return nil, err
	}
	if err := textMail.Execute(&text, data); err != nil {
		return nil, err
	}
	boundary := random()
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", e.From)
	header("To", strings.Join(e.To, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", "[Palier] "+m.Subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", "<"+random()+"@palier>")
	header("Auto-Submitted", "auto-generated")
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct {
		kind string
		body []byte
	}{{"text/plain", text.Bytes()}, {"text/html", html.Bytes()}} {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", boundary, part.kind)
		qp := quotedprintable.NewWriter(&b)
		qp.Write(part.body)
		qp.Close()
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

func mailData(m Message, host, level string) map[string]any {
	if host == "" {
		host, _ = os.Hostname()
	}
	data := map[string]any{"M": m, "Host": host, "Level": level, "Label": labels[m.Kind]}
	if m.Kind == Opened {
		data["Label"] = map[string]string{Alert: "Alert", Info: "Information"}[m.Level]
	}
	return data
}

func random() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
