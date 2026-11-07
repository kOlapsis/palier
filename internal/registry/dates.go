package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxDatePages = 20

// listDates stores the push dates the API of r's registry lists, newest first, page by page until tag is among them; it reports whether the registry has such a list and gave it.
func (c *Client) listDates(ctx context.Context, r Ref, tag string) bool {
	var page func(n int) (map[string]time.Time, bool, error)
	switch {
	case r.Domain == "quay.io":
		page = func(n int) (map[string]time.Time, bool, error) { return c.quayPage(ctx, r, n) }
	case r.Domain == "ghcr.io" && c.github != "":
		page = func(n int) (map[string]time.Time, bool, error) { return c.ghcrPage(ctx, r, n) }
	default:
		return false
	}
	c.mu.Lock()
	failed := time.Since(c.unlisted[r.Name()]) < hubRefill
	whole := time.Since(c.dated[r.Name()]) < hubRefill
	c.mu.Unlock()
	if failed || whole {
		return !failed
	}
	found, count, pages := false, 0, 0
	for n := 1; n <= maxDatePages && !found; n++ {
		pages++
		dates, more, err := page(n)
		if err != nil {
			slog.Warn("tag dates list", "image", r.Name(), "err", err)
			c.mu.Lock()
			c.unlisted[r.Name()] = time.Now()
			c.mu.Unlock()
			return false
		}
		for name, pushed := range dates {
			if _, err := c.db.ExecContext(ctx, `INSERT OR IGNORE INTO tag_dates (image, tag, pushed) VALUES (?, ?, ?)`, r.Name(), name, pushed.UTC().Format(time.RFC3339)); err != nil {
				return false
			}
			count++
			found = found || name == tag
		}
		if !more {
			c.mu.Lock()
			c.dated[r.Name()] = time.Now()
			c.mu.Unlock()
			break
		}
	}
	slog.Info("tag dates listed", "image", r.Name(), "requests", pages, "tags", count, "found", found)
	return true
}

func (c *Client) quayPage(ctx context.Context, r Ref, n int) (map[string]time.Time, bool, error) {
	var page struct {
		Tags []struct {
			Name    string
			StartTS int64 `json:"start_ts"`
		}
		HasAdditional bool `json:"has_additional"`
	}
	u := fmt.Sprintf("https://quay.io/api/v1/repository/%s/tag/?onlyActiveTags=true&limit=100&page=%d", r.Repo, n)
	if _, err := c.apiGet(ctx, u, "", &page); err != nil {
		return nil, false, err
	}
	dates := map[string]time.Time{}
	for _, t := range page.Tags {
		if t.StartTS > 0 {
			dates[t.Name] = time.Unix(t.StartTS, 0)
		}
	}
	return dates, page.HasAdditional, nil
}

func (c *Client) ghcrPage(ctx context.Context, r Ref, n int) (map[string]time.Time, bool, error) {
	owner, name, _ := strings.Cut(r.Repo, "/")
	var versions []struct {
		CreatedAt time.Time `json:"created_at"`
		Metadata  struct {
			Container struct{ Tags []string }
		}
	}
	path := "/packages/container/" + url.PathEscape(name) + "/versions?per_page=100&page=" + strconv.Itoa(n)
	h, err := c.apiGet(ctx, "https://api.github.com/orgs/"+owner+path, "Bearer "+c.github, &versions)
	if se, ok := err.(*StatusError); ok && se.Code == http.StatusNotFound {
		h, err = c.apiGet(ctx, "https://api.github.com/users/"+owner+path, "Bearer "+c.github, &versions)
	}
	if err != nil {
		return nil, false, err
	}
	dates := map[string]time.Time{}
	for _, v := range versions {
		for _, t := range v.Metadata.Container.Tags {
			dates[t] = v.CreatedAt
		}
	}
	return dates, strings.Contains(h.Get("Link"), `rel="next"`), nil
}

func (c *Client) apiGet(ctx context.Context, u, authz string, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := c.send(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode, msg: fmt.Sprintf("GET %s: %s", req.URL.Redacted(), resp.Status)}
	}
	return resp.Header, json.NewDecoder(resp.Body).Decode(out)
}
