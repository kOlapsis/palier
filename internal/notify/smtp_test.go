package notify

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func selfSigned(t *testing.T) tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "snakeoil"}, DNSNames: []string{"snakeoil"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// relay answers one SMTP session on conn, offering STARTTLS with cert, and reports whether a message arrived over TLS.
func relay(conn net.Conn, cert tls.Certificate, got chan<- string) {
	defer conn.Close()
	r, w := bufio.NewReader(conn), conn
	write := func(s string) { w.Write([]byte(s + "\r\n")) }
	write("220 relay ESMTP")
	secure := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
		case strings.HasPrefix(cmd, "EHLO"):
			if secure {
				write("250 relay")
			} else {
				write("250-relay")
				write("250 STARTTLS")
			}
		case cmd == "STARTTLS":
			write("220 go ahead")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
			if tc.Handshake() != nil {
				got <- ""
				return
			}
			conn, secure = tc, true
			r, w = bufio.NewReader(tc), tc
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			write("250 ok")
		case cmd == "DATA":
			write("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			write("250 queued")
			if secure {
				got <- b.String()
			} else {
				got <- "plain"
			}
		case cmd == "QUIT":
			write("221 bye")
		default:
			write("502 no")
		}
	}
}

func TestStartTLSWithSelfSignedCertificate(t *testing.T) {
	cert := selfSigned(t)
	for host, local := range map[string]bool{"smtp-relay": true, "127.0.0.1": true, "smtp.example.com": false} {
		server, client := net.Pipe()
		got := make(chan string, 1)
		go relay(server, cert, got)
		e := &Email{Host: host, Port: 25, From: "palier@example.com", To: []string{"ops@example.com"}, Level: Info}
		body, _ := e.render(Message{Kind: Test, Level: Info, Subject: "Test message", Title: "Palier can reach you"})
		err := e.deliver(client, body)
		message := ""
		select {
		case message = <-got:
		case <-time.After(time.Second):
		}
		switch {
		case local && (err != nil || !strings.Contains(message, "Palier can reach you")):
			t.Errorf("%s: %v, the message did not arrive over TLS", host, err)
		case !local && (err == nil || !strings.Contains(err.Error(), "certificate")):
			t.Errorf("%s: %v, want a refused certificate", host, err)
		}
		client.Close()
	}
}
