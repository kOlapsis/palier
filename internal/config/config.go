// Package config reads Palier's configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the whole configuration of a Palier process.
type Config struct {
	DockerHost     string
	DataDir        string
	Listen         string
	AIKey          string
	AIModel        string
	ScanInterval   time.Duration
	Maturation     time.Duration
	StableSeconds  int
	Auto           string
	WebhookURL     string
	GitHubToken    string
	GitLabToken    string
	RegistryMirror string
	DockerConfig   string
	OIDCIssuer     string
	OIDCClientID   string
	OIDCSecret     string
	OIDCAdmins     []string
}

// Load reads the environment through getenv and applies the defaults.
func Load(getenv func(string) string) (Config, error) {
	get := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}
	c := Config{
		DockerHost:     get("PALIER_DOCKER_HOST", "unix:///var/run/docker.sock"),
		DataDir:        get("PALIER_DATA_DIR", "/var/lib/palier"),
		Listen:         get("PALIER_LISTEN", ""),
		AIKey:          get("PALIER_AI_KEY", ""),
		AIModel:        get("PALIER_AI_MODEL", "claude-sonnet-5-5"),
		Auto:           get("PALIER_AUTO", "all"),
		WebhookURL:     get("PALIER_WEBHOOK_URL", ""),
		GitHubToken:    get("PALIER_GITHUB_TOKEN", ""),
		GitLabToken:    get("PALIER_GITLAB_TOKEN", ""),
		RegistryMirror: get("PALIER_REGISTRY_MIRROR", ""),
		DockerConfig:   get("DOCKER_CONFIG", filepath.Join(get("HOME", "/"), ".docker")),
		OIDCIssuer:     get("PALIER_OIDC_ISSUER", ""),
		OIDCClientID:   get("PALIER_OIDC_CLIENT_ID", ""),
		OIDCSecret:     get("PALIER_OIDC_CLIENT_SECRET", ""),
	}
	for _, a := range strings.Split(get("PALIER_OIDC_ADMINS", ""), ",") {
		if a = strings.TrimSpace(a); a != "" {
			c.OIDCAdmins = append(c.OIDCAdmins, a)
		}
	}
	var errs []error
	duration := func(name, def string) time.Duration {
		d, err := time.ParseDuration(get(name, def))
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: invalid duration %q", name, get(name, def)))
		}
		return d
	}
	c.ScanInterval = duration("PALIER_SCAN_INTERVAL", "1h")
	c.Maturation = duration("PALIER_MATURATION", "72h")
	stable, err := strconv.Atoi(get("PALIER_STABLE_SECONDS", "60"))
	if err != nil || stable <= 0 {
		errs = append(errs, fmt.Errorf("PALIER_STABLE_SECONDS: invalid number of seconds %q", get("PALIER_STABLE_SECONDS", "60")))
	}
	c.StableSeconds = stable
	switch c.Auto {
	case "off", "patch", "minor", "all":
	default:
		errs = append(errs, fmt.Errorf("PALIER_AUTO: %q, expected all, minor, patch or off", c.Auto))
	}
	if c.OIDCIssuer != "" {
		if u, err := url.Parse(c.OIDCIssuer); err != nil || u.Host == "" || u.Scheme != "https" && !(u.Scheme == "http" && localHost(u.Hostname())) {
			errs = append(errs, fmt.Errorf("PALIER_OIDC_ISSUER: %q, expected an https address (http only on a local network)", c.OIDCIssuer))
		}
		if c.OIDCClientID == "" {
			errs = append(errs, errors.New("PALIER_OIDC_CLIENT_ID: required with PALIER_OIDC_ISSUER"))
		}
		if len(c.OIDCAdmins) == 0 {
			errs = append(errs, errors.New("PALIER_OIDC_ADMINS: required with PALIER_OIDC_ISSUER, otherwise nobody could sign in"))
		}
	}
	return c, errors.Join(errs...)
}

func localHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
