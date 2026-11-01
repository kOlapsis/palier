// Package notes reads the release notes of a project from its forge and keeps them in the database.
package notes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kolapsis/palier/internal/version"
)

// Note is the release note of one version, as untrusted text.
type Note struct {
	Tag       string    `json:"tag"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
	Body      string    `json:"-"`
}

// Client reads GitHub and GitLab releases, with a token when one is given.
type Client struct {
	http        *http.Client
	db          *sql.DB
	token       string
	gitlabToken string
}

// New returns a client whose cache lives in db.
func New(db *sql.DB, githubToken, gitlabToken string) *Client {
	return &Client{http: &http.Client{Timeout: time.Minute}, db: db, token: githubToken, gitlabToken: gitlabToken}
}

var (
	githubRepo = regexp.MustCompile(`github\.com[/:]([\w.-]+)/([\w.-]+?)(?:\.git)?(?:[/#?]|$)`)
	gitlabRepo = regexp.MustCompile(`(?:^|[/@])gitlab\.com[/:]([\w.-]+(?:/[\w.-]+)+)`)
)

const gitlabPrefix = "gitlab.com/"

// Repo returns "owner/name" for a GitHub source URL, "gitlab.com/group/project" for a gitlab.com one, or "" for another forge.
func Repo(source string) string {
	if m := githubRepo.FindStringSubmatch(source); m != nil {
		return m[1] + "/" + m[2]
	}
	m := gitlabRepo.FindStringSubmatch(source)
	if m == nil {
		return ""
	}
	path, _, _ := strings.Cut(m[1], "/-/")
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if !strings.Contains(path, "/") {
		return ""
	}
	return gitlabPrefix + path
}

const maxPages = 10

// Releases returns the published releases of repo newer than after, newest first, falling back on the cache when the forge fails.
func (c *Client) Releases(ctx context.Context, repo string, after version.V) ([]Note, error) {
	var fetched []Note
	var err error
	for page := 1; page <= maxPages; page++ {
		var batch []Note
		var older bool
		batch, older, err = c.page(ctx, repo, page, after)
		fetched = append(fetched, batch...)
		if err != nil || older || len(batch) == 0 {
			break
		}
	}
	if err == nil {
		for _, n := range fetched {
			if _, e := c.db.ExecContext(ctx, `INSERT INTO releases (repo, tag, url, published, body) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (repo, tag) DO UPDATE SET url = excluded.url, published = excluded.published, body = excluded.body`,
				repo, n.Tag, n.URL, n.Published.UTC().Format(time.RFC3339), n.Body); e != nil {
				return fetched, e
			}
		}
		return fetched, nil
	}
	rows, e := c.db.QueryContext(ctx, `SELECT tag, url, published, body FROM releases WHERE repo = ?`, repo)
	if e != nil {
		return nil, err
	}
	defer rows.Close()
	var cached []Note
	for rows.Next() {
		var n Note
		var published string
		if e := rows.Scan(&n.Tag, &n.URL, &published, &n.Body); e != nil {
			return nil, err
		}
		n.Published, _ = time.Parse(time.RFC3339, published)
		if v, ok := version.Parse(trimV(n.Tag)); ok && Less(after, v) {
			cached = append(cached, n)
		}
	}
	slices.SortFunc(cached, func(a, b Note) int { return b.Published.Compare(a.Published) })
	return cached, err
}

func (c *Client) page(ctx context.Context, repo string, page int, after version.V) ([]Note, bool, error) {
	if path, ok := strings.CutPrefix(repo, gitlabPrefix); ok {
		return c.gitlabPage(ctx, path, page, after)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", repo, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("notes de version de %s : GET %s : %s", repo, url, resp.Status)
	}
	var releases []struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, false, err
	}
	var out []Note
	older := false
	for _, r := range releases {
		v, ok := version.Parse(trimV(r.TagName))
		if r.Draft || r.Prerelease || !ok {
			continue
		}
		if !Less(after, v) {
			older = true
			continue
		}
		out = append(out, Note{Tag: r.TagName, URL: r.HTMLURL, Published: r.PublishedAt, Body: r.Body})
	}
	return out, older, nil
}

var preRelease = regexp.MustCompile(`(?i)^[-.+]?(rc|alpha|beta|pre|dev)`)

func (c *Client) gitlabPage(ctx context.Context, path string, page int, after version.V) ([]Note, bool, error) {
	u := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/releases?per_page=100&page=%d", url.PathEscape(path), page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	if c.gitlabToken != "" {
		req.Header.Set("PRIVATE-TOKEN", c.gitlabToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("notes de version de %s : GET %s : %s", gitlabPrefix+path, u, resp.Status)
	}
	var releases []struct {
		TagName     string    `json:"tag_name"`
		Description string    `json:"description"`
		ReleasedAt  time.Time `json:"released_at"`
		Upcoming    bool      `json:"upcoming_release"`
		Links       struct {
			Self string `json:"self"`
		} `json:"_links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, false, err
	}
	var out []Note
	older := false
	for _, r := range releases {
		v, ok := version.Parse(trimV(r.TagName))
		if r.Upcoming || !ok || preRelease.MatchString(v.Suffix) {
			continue
		}
		if !Less(after, v) {
			older = true
			continue
		}
		out = append(out, Note{Tag: r.TagName, URL: r.Links.Self, Published: r.ReleasedAt, Body: r.Description})
	}
	return out, older, nil
}

func trimV(tag string) string {
	if len(tag) > 1 && tag[0] == 'v' {
		return tag[1:]
	}
	return tag
}

func trimZeros(n []int) []int {
	for len(n) > 1 && n[len(n)-1] == 0 {
		n = n[:len(n)-1]
	}
	return n
}

// Less compares the numbers of two versions, ignoring prefix, suffix and trailing zeros.
func Less(a, b version.V) bool {
	return version.V{Nums: trimZeros(a.Nums)}.Less(version.V{Nums: trimZeros(b.Nums)})
}

// Same reports whether a release tag and an image tag name the same version.
func Same(releaseTag string, v version.V) bool {
	r, ok := version.Parse(trimV(releaseTag))
	return ok && slices.Equal(trimZeros(r.Nums), trimZeros(v.Nums))
}
