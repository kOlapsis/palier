// Package config reads Palier's configuration from the environment.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
	Token          string
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
		Token:          get("PALIER_TOKEN", ""),
		AIKey:          get("PALIER_AI_KEY", ""),
		AIModel:        get("PALIER_AI_MODEL", "claude-sonnet-5-5"),
		Auto:           get("PALIER_AUTO", "all"),
		WebhookURL:     get("PALIER_WEBHOOK_URL", ""),
		GitHubToken:    get("PALIER_GITHUB_TOKEN", ""),
		GitLabToken:    get("PALIER_GITLAB_TOKEN", ""),
		RegistryMirror: get("PALIER_REGISTRY_MIRROR", ""),
		DockerConfig:   get("DOCKER_CONFIG", filepath.Join(get("HOME", "/"), ".docker")),
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
	return c, errors.Join(errs...)
}

// EnsureToken keeps PALIER_TOKEN when set, otherwise reads the token file of the data directory, creating it on first start.
func (c *Config) EnsureToken() (generated bool, err error) {
	if c.Token != "" {
		return false, nil
	}
	path := filepath.Join(c.DataDir, "token")
	b, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(b)) != "" {
		c.Token = strings.TrimSpace(string(b))
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return false, err
	}
	c.Token = hex.EncodeToString(raw)
	return true, os.WriteFile(path, []byte(c.Token+"\n"), 0o600)
}
