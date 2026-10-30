package docker

import "testing"

func TestDemux(t *testing.T) {
	raw := append([]byte{1, 0, 0, 0, 0, 0, 0, 6}, "hello\n"...)
	raw = append(raw, append([]byte{2, 0, 0, 0, 0, 0, 0, 4}, "err\n"...)...)
	if got := demux(raw); got != "hello\nerr\n" {
		t.Fatalf("demux = %q", got)
	}
	if got := demux([]byte("plain text")); got != "plain text" {
		t.Fatalf("tty = %q", got)
	}
}
