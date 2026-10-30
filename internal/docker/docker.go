// Package docker talks to the Docker engine API.
package docker

import (
	"context"
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("docker GET %s: %s: %s", path, resp.Status, b)
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
	RestartCount int
	Config       struct {
		Image       string
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
