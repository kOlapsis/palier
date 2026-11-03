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
	if c.Listen != "" || c.ScanInterval != time.Hour || c.Maturation != 72*time.Hour || c.StableSeconds != 60 || c.Auto != "all" || c.AIModel != "claude-sonnet-5-5" {
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

func TestOIDC(t *testing.T) {
	c, err := Load(env(map[string]string{"PALIER_OIDC_ISSUER": "https://auth.example/application/o/palier/", "PALIER_OIDC_CLIENT_ID": "palier", "PALIER_OIDC_ADMINS": " palier-admins , @example.com,"}))
	if err != nil || len(c.OIDCAdmins) != 2 || c.OIDCAdmins[1] != "@example.com" {
		t.Fatalf("%v %q", err, c.OIDCAdmins)
	}
	for issuer, ok := range map[string]bool{"http://10.10.50.10:8090/palier": true, "http://localhost:9000": true, "http://auth.example": false, "auth.example": false} {
		_, err := Load(env(map[string]string{"PALIER_OIDC_ISSUER": issuer, "PALIER_OIDC_CLIENT_ID": "palier", "PALIER_OIDC_ADMINS": "a"}))
		if (err == nil) != ok {
			t.Errorf("issuer %s: %v", issuer, err)
		}
	}
	_, err = Load(env(map[string]string{"PALIER_OIDC_ISSUER": "https://auth.example"}))
	for _, name := range []string{"PALIER_OIDC_CLIENT_ID", "PALIER_OIDC_ADMINS"} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("missing %s not reported: %v", name, err)
		}
	}
}

func TestHookSecret(t *testing.T) {
	for secret, ok := range map[string]bool{strings.Repeat("ab", 16): true, "short": false, strings.Repeat("a", 31) + "/": false} {
		if _, err := Load(env(map[string]string{"PALIER_REGISTRY_WEBHOOK_SECRET": secret})); (err == nil) != ok {
			t.Errorf("%q: %v", secret, err)
		}
	}
}

func TestListen(t *testing.T) {
	for in, want := range map[string]string{"8080": ":8080", ":8080": ":8080", "127.0.0.1:9000": "127.0.0.1:9000", "": ""} {
		c, err := Load(env(map[string]string{"PALIER_LISTEN": in}))
		if err != nil || c.Listen != want {
			t.Errorf("%q: %q %v", in, c.Listen, err)
		}
	}
	if _, err := Load(env(map[string]string{"PALIER_LISTEN": "localhost"})); err == nil || !strings.Contains(err.Error(), "PALIER_LISTEN") {
		t.Errorf("an address without port: %v", err)
	}
}
