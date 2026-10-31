// Package registry lists the tags of an image and their push dates.
package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Ref is an image reference split into registry, repository and tag.
type Ref struct {
	Domain string
	Repo   string
	Tag    string
}

// ParseRef reads an image reference as Docker does, defaulting to docker.io and the library namespace.
func ParseRef(s string) Ref {
	s, _, _ = strings.Cut(s, "@")
	var r Ref
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		s, r.Tag = s[:i], s[i+1:]
	}
	if r.Tag == "" {
		r.Tag = "latest"
	}
	first, rest, found := strings.Cut(s, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Domain, r.Repo = first, rest
	} else {
		r.Domain, r.Repo = "docker.io", s
	}
	if r.Domain == "docker.io" && !strings.Contains(r.Repo, "/") {
		r.Repo = "library/" + r.Repo
	}
	return r
}

// Name is the reference without its tag.
func (r Ref) Name() string { return r.Domain + "/" + r.Repo }

// Client queries registries with the credentials of the Docker configuration and keeps push dates in the database.
type Client struct {
	http  *http.Client
	db    *sql.DB
	auths map[string]string
	mu    sync.Mutex
	authz map[string]string
}

// New returns a registry client whose date cache lives in db; auths maps a registry address to its base64 "user:password", as in config.json.
func New(db *sql.DB, auths map[string]string) *Client {
	return &Client{http: &http.Client{Timeout: time.Minute}, db: db, auths: auths, authz: map[string]string{}}
}

// LoadAuths reads the "auths" section of config.json in the Docker configuration directory dir.
func LoadAuths(dir string) (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Auths map[string]struct{ Auth string }
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s : %w", filepath.Join(dir, "config.json"), err)
	}
	auths := map[string]string{}
	for host, a := range cfg.Auths {
		if a.Auth != "" {
			auths[host] = a.Auth
		}
	}
	return auths, nil
}

func (c *Client) credentials(domain string) string {
	keys := []string{domain, "https://" + domain}
	if domain == "docker.io" {
		keys = []string{"https://index.docker.io/v1/", "index.docker.io", "docker.io", "registry-1.docker.io"}
	}
	for _, k := range keys {
		if a := c.auths[k]; a != "" {
			return a
		}
	}
	return ""
}

// UsesCredentials reports whether requests to domain carry credentials.
func (c *Client) UsesCredentials(domain string) bool { return c.credentials(domain) != "" }

func apiHost(domain string) string {
	if domain == "docker.io" {
		return "registry-1.docker.io"
	}
	return domain
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

func (c *Client) get(ctx context.Context, r Ref, url, accept string) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, r, url, accept)
}

func (c *Client) do(ctx context.Context, method string, r Ref, url, accept string) (*http.Response, error) {
	throttled := 0
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		c.mu.Lock()
		authz := c.authz[r.Name()]
		c.mu.Unlock()
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			challenge := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			authz, err := c.authorize(ctx, r, challenge)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			c.authz[r.Name()] = authz
			c.mu.Unlock()
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests && throttled < 4 {
			resp.Body.Close()
			throttled++
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(throttled) * time.Second):
			}
			attempt--
			continue
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(b)))
		}
		return resp, nil
	}
}

func (c *Client) authorize(ctx context.Context, r Ref, challenge string) (string, error) {
	creds := c.credentials(r.Domain)
	if strings.HasPrefix(challenge, "Basic ") {
		if creds == "" {
			return "", fmt.Errorf("le registre %s demande des identifiants", r.Domain)
		}
		return "Basic " + creds, nil
	}
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if !strings.HasPrefix(challenge, "Bearer ") || params["realm"] == "" {
		return "", fmt.Errorf("authentification du registre non prise en charge : %q", challenge)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"], nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	for _, k := range []string{"service", "scope"} {
		if params[k] != "" {
			q.Set(k, params[k])
		}
	}
	req.URL.RawQuery = q.Encode()
	if creds != "" {
		req.Header.Set("Authorization", "Basic "+creds)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("jeton du registre %s : %s", params["realm"], resp.Status)
	}
	var t struct {
		Token       string
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	return "Bearer " + t.Token, nil
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// Tags lists every tag of the repository, following pagination.
func (c *Client) Tags(ctx context.Context, r Ref) ([]string, error) {
	base := "https://" + apiHost(r.Domain)
	url := base + "/v2/" + r.Repo + "/tags/list?n=1000"
	var all []string
	for url != "" {
		resp, err := c.get(ctx, r, url, "")
		if err != nil {
			return nil, err
		}
		var page struct{ Tags []string }
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, page.Tags...)
		url = ""
		if m := nextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			url = m[1]
			if strings.HasPrefix(url, "/") {
				url = base + url
			}
		}
	}
	return all, nil
}

// Pushed returns when tag was pushed, as first seen by Palier; a tag rebuilt later keeps its first date.
func (c *Client) Pushed(ctx context.Context, r Ref, tag string) (time.Time, error) {
	var stored string
	err := c.db.QueryRowContext(ctx, `SELECT pushed FROM tag_dates WHERE image = ? AND tag = ?`, r.Name(), tag).Scan(&stored)
	if err == nil {
		return time.Parse(time.RFC3339, stored)
	}
	if err != sql.ErrNoRows {
		return time.Time{}, err
	}
	var pushed time.Time
	if r.Domain == "docker.io" {
		pushed, err = c.hubPushed(ctx, r, tag)
	} else {
		pushed, err = c.configCreated(ctx, r, tag)
	}
	if err != nil {
		return time.Time{}, err
	}
	_, err = c.db.ExecContext(ctx, `INSERT OR IGNORE INTO tag_dates (image, tag, pushed) VALUES (?, ?, ?)`, r.Name(), tag, pushed.UTC().Format(time.RFC3339))
	return pushed, err
}

func (c *Client) hubPushed(ctx context.Context, r Ref, tag string) (time.Time, error) {
	ns, name, _ := strings.Cut(r.Repo, "/")
	url := "https://hub.docker.com/v2/namespaces/" + ns + "/repositories/" + name + "/tags/" + tag
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var t struct {
		TagLastPushed time.Time `json:"tag_last_pushed"`
		LastUpdated   time.Time `json:"last_updated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return time.Time{}, err
	}
	if t.TagLastPushed.IsZero() {
		return t.LastUpdated, nil
	}
	return t.TagLastPushed, nil
}

const manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// Digest returns the digest the registry serves for tag, without counting as a pull.
func (c *Client) Digest(ctx context.Context, r Ref, tag string) (string, error) {
	resp, err := c.do(ctx, http.MethodHead, r, "https://"+apiHost(r.Domain)+"/v2/"+r.Repo+"/manifests/"+tag, manifestTypes)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	d := resp.Header.Get("Docker-Content-Digest")
	if d == "" {
		return "", fmt.Errorf("%s:%s : le registre ne donne pas de digest", r.Name(), tag)
	}
	return d, nil
}

// PlatformDigests returns the digests of the per-platform images listed by the index digest.
func (c *Client) PlatformDigests(ctx context.Context, r Ref, digest string) ([]string, error) {
	var m struct{ Manifests []struct{ Digest string } }
	if err := c.getJSON(ctx, r, "https://"+apiHost(r.Domain)+"/v2/"+r.Repo+"/manifests/"+digest, manifestTypes, &m); err != nil {
		return nil, err
	}
	var out []string
	for _, d := range m.Manifests {
		out = append(out, d.Digest)
	}
	return out, nil
}

func (c *Client) configCreated(ctx context.Context, r Ref, tag string) (time.Time, error) {
	base := "https://" + apiHost(r.Domain) + "/v2/" + r.Repo
	var m struct {
		Manifests []struct {
			Digest   string
			Platform struct{ OS, Architecture string }
		}
		Config struct{ Digest string }
	}
	if err := c.getJSON(ctx, r, base+"/manifests/"+tag, manifestTypes, &m); err != nil {
		return time.Time{}, err
	}
	if len(m.Manifests) > 0 {
		digest := ""
		for _, d := range m.Manifests {
			if d.Platform.OS == "linux" && d.Platform.Architecture == runtime.GOARCH {
				digest = d.Digest
				break
			}
		}
		if digest == "" {
			return time.Time{}, fmt.Errorf("%s:%s n'a pas d'image linux/%s", r.Name(), tag, runtime.GOARCH)
		}
		if err := c.getJSON(ctx, r, base+"/manifests/"+digest, manifestTypes, &m); err != nil {
			return time.Time{}, err
		}
	}
	var cfg struct{ Created time.Time }
	if err := c.getJSON(ctx, r, base+"/blobs/"+m.Config.Digest, "", &cfg); err != nil {
		return time.Time{}, err
	}
	return cfg.Created, nil
}

func (c *Client) getJSON(ctx context.Context, r Ref, url, accept string, out any) error {
	resp, err := c.get(ctx, r, url, accept)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}
