package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.ScanInterval != time.Hour || c.Maturation != 72*time.Hour || c.StableSeconds != 60 || c.Auto != "minor" || c.AIModel != "claude-sonnet-5-5" {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	_, err := Load(env(map[string]string{"PALIER_AUTO": "major", "PALIER_SCAN_INTERVAL": "soon", "PALIER_STABLE_SECONDS": "0"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"PALIER_AUTO", "PALIER_SCAN_INTERVAL", "PALIER_STABLE_SECONDS"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func TestEnsureTokenIsStable(t *testing.T) {
	c := Config{DataDir: t.TempDir()}
	generated, err := c.EnsureToken()
	if err != nil || !generated || len(c.Token) != 64 {
		t.Fatalf("first start: generated=%v token=%q err=%v", generated, c.Token, err)
	}
	again := Config{DataDir: c.DataDir}
	generated, err = again.EnsureToken()
	if err != nil || generated || again.Token != c.Token {
		t.Fatalf("second start: generated=%v token=%q err=%v", generated, again.Token, err)
	}
}
