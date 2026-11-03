// Package registry lists the tags of an image and their push dates.
package registry

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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

// ParseRef reads an image reference as Docker does, defaulting to docker.io and the library namespace, and names Docker Hub docker.io whatever alias the reference uses.
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
	switch r.Domain {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		r.Domain = "docker.io"
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
	http   *http.Client
	db     *sql.DB
	auths  *Auths
	mirror string
	mu     sync.Mutex
	authz  map[string]string
	helped map[string]helped
	hub    string
	dated  map[string]time.Time
}

// New returns a registry client whose date cache lives in db, with the credentials of auths, and mirror, when set, serving the Docker Hub reads by digest.
func New(db *sql.DB, auths *Auths, mirror string) *Client {
	mirror = strings.TrimSuffix(mirror, "/")
	if mirror != "" && !strings.Contains(mirror, "://") {
		mirror = "https://" + mirror
	}
	return &Client{http: &http.Client{Timeout: time.Minute}, db: db, auths: auths, mirror: mirror, authz: map[string]string{}, helped: map[string]helped{}, dated: map[string]time.Time{}}
}

// Auths are the credentials of config.json: its auths section, and the credential helpers of credHelpers and credsStore.
type Auths struct {
	Basic   map[string]string
	Helpers map[string]string
	Store   string
}

var helperName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// LoadAuths reads config.json in the Docker configuration directory dir, or returns nil when there is none.
func LoadAuths(dir string) (*Auths, error) {
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Auths       map[string]struct{ Auth string }
		CredHelpers map[string]string `json:"credHelpers"`
		CredsStore  string            `json:"credsStore"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "config.json"), err)
	}
	a := &Auths{Basic: map[string]string{}, Helpers: map[string]string{}, Store: cfg.CredsStore}
	for host, v := range cfg.Auths {
		if v.Auth != "" {
			a.Basic[host] = v.Auth
		}
	}
	for host, h := range cfg.CredHelpers {
		if !helperName.MatchString(h) {
			return nil, fmt.Errorf("%s: credential helper %q of %s is not a helper name", filepath.Join(dir, "config.json"), h, host)
		}
		a.Helpers[host] = h
	}
	if a.Store != "" && !helperName.MatchString(a.Store) {
		return nil, fmt.Errorf("%s: credsStore %q is not a helper name", filepath.Join(dir, "config.json"), a.Store)
	}
	return a, nil
}

// credentials returns the base64 "user:password" for domain: from auths, else from its credential helper, else from credsStore.
func (c *Client) credentials(domain string) string {
	if c.auths == nil {
		return ""
	}
	keys := []string{domain, "https://" + domain}
	if domain == "docker.io" {
		keys = []string{"https://index.docker.io/v1/", "index.docker.io", "docker.io", "registry-1.docker.io"}
	}
	for _, k := range keys {
		if a := c.auths.Basic[k]; a != "" {
			return a
		}
	}
	for _, k := range keys {
		if h := c.auths.Helpers[k]; h != "" {
			return c.helper(h, k)
		}
	}
	if c.auths.Store != "" {
		return c.helper(c.auths.Store, keys[0])
	}
	return ""
}

type helped struct {
	auth  string
	until time.Time
}

// helper asks the credential helper docker-credential-name for server, and keeps the answer ten minutes, a failure one.
func (c *Client) helper(name, server string) string {
	key := name + " " + server
	c.mu.Lock()
	h, ok := c.helped[key]
	c.mu.Unlock()
	if ok && time.Now().Before(h.until) {
		return h.auth
	}
	auth, err := runHelper(name, server)
	h = helped{auth: auth, until: time.Now().Add(10 * time.Minute)}
	if err != nil {
		h.until = time.Now().Add(time.Minute)
		slog.Warn("credential helper", "helper", "docker-credential-"+name, "registry", server, "err", err)
	}
	c.mu.Lock()
	c.helped[key] = h
	c.mu.Unlock()
	return auth
}

func runHelper(name, server string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker-credential-"+name, "get")
	cmd.Stdin = strings.NewReader(server)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("docker-credential-%s is not in the PATH of Palier (/usr/local/bin): mount it into the container", name)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %.300s", err, strings.TrimSpace(string(out)+" "+stderr.String()))
	}
	var r struct{ Username, Secret string }
	if err := json.Unmarshal(out, &r); err != nil {
		return "", fmt.Errorf("unreadable answer: %w", err)
	}
	switch {
	case r.Username == "<token>":
		return "", errors.New("it gives an identity token, which Palier does not support: use a helper that gives a user and a password")
	case r.Secret == "":
		return "", errors.New("it gives no secret")
	}
	return base64.StdEncoding.EncodeToString([]byte(r.Username + ":" + r.Secret)), nil
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
		key := req.URL.Host + "/" + r.Repo
		c.mu.Lock()
		authz := c.authz[key]
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
			domain := r.Domain
			if req.URL.Host != apiHost(r.Domain) {
				domain = req.URL.Host
			}
			authz, err := c.authorize(ctx, domain, challenge)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			c.authz[key] = authz
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

func (c *Client) authorize(ctx context.Context, domain, challenge string) (string, error) {
	creds := c.credentials(domain)
	if strings.HasPrefix(challenge, "Basic ") {
		if creds == "" {
			return "", fmt.Errorf("registry %s requires credentials", domain)
		}
		return "Basic " + creds, nil
	}
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if !strings.HasPrefix(challenge, "Bearer ") || params["realm"] == "" {
		return "", fmt.Errorf("unsupported registry authentication: %q", challenge)
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
		return "", fmt.Errorf("registry %s token: %s", params["realm"], resp.Status)
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
	if t, ok, err := c.storedDate(ctx, r, tag); ok || err != nil {
		return t, err
	}
	if r.Domain == "docker.io" {
		if err := c.hubDates(ctx, r); err != nil {
			return time.Time{}, err
		}
		if t, ok, err := c.storedDate(ctx, r, tag); ok || err != nil {
			return t, err
		}
		return time.Time{}, fmt.Errorf("%s:%s is not in the Docker Hub tag list", r.Name(), tag)
	}
	pushed, err := c.configCreated(ctx, r, tag)
	if err != nil {
		return time.Time{}, err
	}
	_, err = c.db.ExecContext(ctx, `INSERT OR IGNORE INTO tag_dates (image, tag, pushed) VALUES (?, ?, ?)`, r.Name(), tag, pushed.UTC().Format(time.RFC3339))
	return pushed, err
}

func (c *Client) storedDate(ctx context.Context, r Ref, tag string) (time.Time, bool, error) {
	var stored string
	err := c.db.QueryRowContext(ctx, `SELECT pushed FROM tag_dates WHERE image = ? AND tag = ?`, r.Name(), tag).Scan(&stored)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, stored)
	return t, true, err
}

const hubRefill = 10 * time.Minute

// hubDates stores the push date of every tag of a Docker Hub repository, read a hundred at a time.
func (c *Client) hubDates(ctx context.Context, r Ref) error {
	c.mu.Lock()
	recent := time.Since(c.dated[r.Name()]) < hubRefill
	c.mu.Unlock()
	if recent {
		return nil
	}
	ns, name, _ := strings.Cut(r.Repo, "/")
	url := "https://hub.docker.com/v2/namespaces/" + ns + "/repositories/" + name + "/tags?page_size=100"
	for url != "" {
		var page struct {
			Next    string
			Results []struct {
				Name          string
				TagLastPushed time.Time `json:"tag_last_pushed"`
				LastUpdated   time.Time `json:"last_updated"`
			}
		}
		if err := c.hubGet(ctx, url, &page); err != nil {
			return err
		}
		for _, t := range page.Results {
			pushed := t.TagLastPushed
			if pushed.IsZero() {
				pushed = t.LastUpdated
			}
			if pushed.IsZero() {
				continue
			}
			if _, err := c.db.ExecContext(ctx, `INSERT OR IGNORE INTO tag_dates (image, tag, pushed) VALUES (?, ?, ?)`, r.Name(), t.Name, pushed.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
		}
		url = page.Next
	}
	c.mu.Lock()
	c.dated[r.Name()] = time.Now()
	c.mu.Unlock()
	return nil
}

// hubGet reads the Docker Hub API with the Docker Hub credentials of the Docker configuration, waiting when it is throttled.
func (c *Client) hubGet(ctx context.Context, url string, out any) error {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		c.mu.Lock()
		token := c.hub
		c.mu.Unlock()
		if token == "" && c.credentials("docker.io") != "" {
			if token, err = c.hubLogin(ctx); err != nil {
				return err
			}
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized && token != "" && attempt == 0:
			resp.Body.Close()
			c.mu.Lock()
			c.hub = ""
			c.mu.Unlock()
			continue
		case resp.StatusCode == http.StatusTooManyRequests && attempt < 4:
			resp.Body.Close()
			wait := time.Duration(attempt+1) * 5 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(min(s, 120)) * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		case resp.StatusCode != http.StatusOK:
			resp.Body.Close()
			return fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		defer resp.Body.Close()
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func (c *Client) hubLogin(ctx context.Context) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(c.credentials("docker.io"))
	if err != nil {
		return "", fmt.Errorf("unreadable Docker Hub credentials: %w", err)
	}
	user, secret, _ := strings.Cut(string(raw), ":")
	body, _ := json.Marshal(map[string]string{"identifier": user, "secret": secret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://hub.docker.com/v2/auth/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Docker Hub API connection: %s", resp.Status)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.hub = t.AccessToken
	c.mu.Unlock()
	return t.AccessToken, nil
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
		return "", fmt.Errorf("%s:%s: the registry gives no digest", r.Name(), tag)
	}
	return d, nil
}

type index struct {
	Manifests []struct {
		Digest      string
		Platform    struct{ OS, Architecture string }
		Annotations map[string]string
	}
	Annotations map[string]string
}

func (c *Client) index(ctx context.Context, r Ref, digest string) (*index, error) {
	var m index
	path := "/v2/" + r.Repo + "/manifests/" + digest
	// A mirror's tag lists are partial and its tags can be stale, so only content addressed by digest is read from it.
	if r.Domain == "docker.io" && c.mirror != "" && c.getJSON(ctx, r, c.mirror+path, manifestTypes, &m) == nil {
		return &m, nil
	}
	if err := c.getJSON(ctx, r, "https://"+apiHost(r.Domain)+path, manifestTypes, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// PlatformDigests returns the digests of the per-platform images listed by the index digest.
func (c *Client) PlatformDigests(ctx context.Context, r Ref, digest string) ([]string, error) {
	m, err := c.index(ctx, r, digest)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range m.Manifests {
		out = append(out, d.Digest)
	}
	return out, nil
}

const versionAnnotation = "org.opencontainers.image.version"

// AnnotatedVersion returns the org.opencontainers.image.version annotation the registry gives the image digest names, for this machine's platform.
func (c *Client) AnnotatedVersion(ctx context.Context, r Ref, digest string) (string, error) {
	m, err := c.index(ctx, r, digest)
	if err != nil {
		return "", err
	}
	if v := m.Annotations[versionAnnotation]; v != "" {
		return v, nil
	}
	for _, d := range m.Manifests {
		if d.Platform.OS == "linux" && d.Platform.Architecture == runtime.GOARCH {
			return d.Annotations[versionAnnotation], nil
		}
	}
	return "", nil
}

func (c *Client) configCreated(ctx context.Context, r Ref, tag string) (time.Time, error) {
	base := "https://" + apiHost(r.Domain) + "/v2/" + r.Repo
	ref := tag
	var config string
	for depth := 0; config == ""; depth++ {
		var m struct {
			Manifests []struct {
				Digest   string
				Platform struct{ OS, Architecture string }
			}
			Config struct{ Digest string }
		}
		if err := c.getJSON(ctx, r, base+"/manifests/"+ref, manifestTypes, &m); err != nil {
			return time.Time{}, err
		}
		config, ref = m.Config.Digest, ""
		for _, d := range m.Manifests {
			if d.Platform.OS == "linux" && d.Platform.Architecture == runtime.GOARCH {
				ref = d.Digest
				break
			}
		}
		if config == "" && (ref == "" || depth == 2) {
			return time.Time{}, fmt.Errorf("%s:%s has no linux/%s image", r.Name(), tag, runtime.GOARCH)
		}
	}
	var cfg struct{ Created time.Time }
	if err := c.getJSON(ctx, r, base+"/blobs/"+config, "", &cfg); err != nil {
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
