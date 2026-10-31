// Package docker talks to the Docker engine API.
package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Client is a client of the engine API.
type Client struct {
	http *http.Client
	base string
}

// New returns a client for a unix:// or tcp:// engine address.
func New(host string) (*Client, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "unix":
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", u.Path)
		}}
		return &Client{http: &http.Client{Transport: tr, Timeout: time.Minute}, base: "http://docker"}, nil
	case "tcp", "http":
		return &Client{http: &http.Client{Timeout: time.Minute}, base: "http://" + u.Host}, nil
	}
	return nil, fmt.Errorf("adresse du moteur Docker non prise en charge : %s", host)
}

// Get decodes the JSON answer of the engine to GET path into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, out)
}

// Post sends POST path with body as JSON when it is not nil, and decodes the answer into out when out is not nil.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.send(ctx, http.MethodPost, path, body, out)
}

// Delete sends DELETE path.
func (c *Client) Delete(ctx context.Context, path string) error {
	return c.send(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	return c.send(ctx, method, path, nil, out)
}

func (c *Client) send(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("docker %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(b))
	}
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Logs returns the last lines of the output of a container started without a TTY.
func (c *Client) Logs(ctx context.Context, id string, tail int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/containers/%s/logs?stdout=1&stderr=1&tail=%d", c.base, id, tail), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return demux(raw), nil
}

// demux strips the 8-byte frame headers of the stdout/stderr stream.
func demux(raw []byte) string {
	var out bytes.Buffer
	for len(raw) >= 8 && raw[0] <= 2 && raw[1] == 0 && raw[2] == 0 && raw[3] == 0 {
		n := int(binary.BigEndian.Uint32(raw[4:8]))
		raw = raw[8:]
		n = min(n, len(raw))
		out.Write(raw[:n])
		raw = raw[n:]
	}
	out.Write(raw)
	return out.String()
}

// Container is the part of a container list entry Palier reads.
type Container struct {
	ID      string
	Names   []string
	ImageID string
	State   string
	Labels  map[string]string
}

// ContainerDetail is the part of a container inspection Palier reads.
type ContainerDetail struct {
	ID           string
	Image        string
	RestartCount int
	Config       struct {
		Image       string
		Env         []string
		Labels      map[string]string
		Healthcheck *struct{ Test []string }
	}
	State struct {
		Status string
		Health *struct{ Status string }
	}
	Mounts []struct {
		Type        string
		Name        string
		Source      string
		Destination string
	}
}

// Image is the part of an image inspection Palier reads.
type Image struct {
	RepoDigests []string
	Config      struct{ Labels map[string]string }
}
