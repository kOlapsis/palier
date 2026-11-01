package notes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kolapsis/palier/internal/version"
)

// Advisory is a published security advisory of a project, as untrusted text.
type Advisory struct {
	ID        string    `json:"id"`
	CVE       string    `json:"cve,omitempty"`
	Severity  string    `json:"severity"`
	Summary   string    `json:"summary"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
	Affected  []string  `json:"affected"`
	Patched   []string  `json:"patched"`
	Body      string    `json:"-"`
}

const advisoryTTL = 30 * time.Minute

type cachedAdvisories struct {
	at   time.Time
	list []Advisory
}

var (
	advisoryMu    sync.Mutex
	advisoryCache = map[string]cachedAdvisories{}
)

// Advisories returns the published security advisories of a GitHub repository, kept half an hour between two reads.
func (c *Client) Advisories(ctx context.Context, repo string) ([]Advisory, error) {
	if strings.HasPrefix(repo, gitlabPrefix) {
		return nil, nil
	}
	advisoryMu.Lock()
	cached, ok := advisoryCache[repo]
	advisoryMu.Unlock()
	if ok && time.Since(cached.at) < advisoryTTL {
		return cached.list, nil
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/security-advisories?state=published&per_page=100", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("avis de sécurité de %s : GET %s : %s", repo, url, resp.Status)
	}
	var raw []struct {
		GHSA            string     `json:"ghsa_id"`
		CVE             string     `json:"cve_id"`
		Severity        string     `json:"severity"`
		Summary         string     `json:"summary"`
		Description     string     `json:"description"`
		HTMLURL         string     `json:"html_url"`
		PublishedAt     time.Time  `json:"published_at"`
		WithdrawnAt     *time.Time `json:"withdrawn_at"`
		Vulnerabilities []struct {
			Range   string `json:"vulnerable_version_range"`
			Patched string `json:"patched_versions"`
		} `json:"vulnerabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var list []Advisory
	for _, r := range raw {
		if r.WithdrawnAt != nil {
			continue
		}
		a := Advisory{ID: r.GHSA, CVE: r.CVE, Severity: r.Severity, Summary: r.Summary, URL: r.HTMLURL, Published: r.PublishedAt}
		for _, v := range r.Vulnerabilities {
			a.Affected = append(a.Affected, v.Range)
			a.Patched = append(a.Patched, v.Patched)
		}
		a.Body = fmt.Sprintf("Security advisory %s %s, severity %s: %s\nAffected versions: %s\nPatched versions: %s\n\n%s",
			r.GHSA, r.CVE, r.Severity, r.Summary, strings.Join(a.Affected, " ; "), strings.Join(a.Patched, " ; "), r.Description)
		list = append(list, a)
	}
	advisoryMu.Lock()
	advisoryCache[repo] = cachedAdvisories{at: time.Now(), list: list}
	advisoryMu.Unlock()
	return list, nil
}

var (
	constraint = regexp.MustCompile(`(>=|<=|>|<|=)?\s*v?(\d+(?:\.\d+)*)`)
	separator  = regexp.MustCompile(`,|&&|\|\|`)
)

// Affects reports whether v falls in one of the affected ranges of a and is not at or above a patched version of its major.
func (a Advisory) Affects(v version.V) bool {
	for i, rng := range a.Affected {
		if !inRange(rng, v) {
			continue
		}
		patched := ""
		if i < len(a.Patched) {
			patched = a.Patched[i]
		}
		if fixed, ok := fixedFor(patched, v); ok && !Less(v, fixed) {
			continue
		}
		return true
	}
	return false
}

// FixedFrom returns the lowest patched version above v among the advisories that affect v.
func FixedFrom(list []Advisory, v version.V) (version.V, bool) {
	var best version.V
	found := false
	for _, a := range list {
		if !a.Affects(v) {
			continue
		}
		for _, p := range a.Patched {
			for _, f := range splitVersions(p) {
				if Less(v, f) && (!found || Less(f, best)) {
					best, found = f, true
				}
			}
		}
	}
	return best, found
}

func inRange(rng string, v version.V) bool {
	ok := false
	for _, part := range separator.Split(rng, -1) {
		m := constraint.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			continue
		}
		bound, parsed := version.Parse(m[2])
		if !parsed {
			continue
		}
		ok = true
		switch m[1] {
		case ">=":
			if Less(v, bound) {
				return false
			}
		case ">":
			if !Less(bound, v) {
				return false
			}
		case "<=":
			if Less(bound, v) {
				return false
			}
		case "<":
			if !Less(v, bound) {
				return false
			}
		default:
			if Less(v, bound) || Less(bound, v) {
				return false
			}
		}
	}
	return ok
}

func splitVersions(s string) []version.V {
	var out []version.V
	for _, m := range constraint.FindAllStringSubmatch(s, -1) {
		if v, ok := version.Parse(m[2]); ok {
			out = append(out, v)
		}
	}
	return out
}

func fixedFor(patched string, v version.V) (version.V, bool) {
	for _, f := range splitVersions(patched) {
		if len(f.Nums) > 0 && len(v.Nums) > 0 && f.Nums[0] == v.Nums[0] {
			return f, true
		}
	}
	return version.V{}, false
}
