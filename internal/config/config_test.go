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
	if c.Listen != "" || c.ScanInterval != time.Hour || c.Maturation != 72*time.Hour || c.StableSeconds != 60 || c.Auto != "all" || c.AIModel != "claude-sonnet-5-5" || c.AITimeout != 30*time.Minute {
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

func TestAIProvider(t *testing.T) {
	c, err := Load(env(map[string]string{"PALIER_AI_PROVIDER": "openai", "PALIER_AI_URL": "http://ollama:11434/v1/", "PALIER_AI_MODEL": "qwen3:8b"}))
	if err != nil || !c.AI || c.AIURL != "http://ollama:11434/v1" {
		t.Fatalf("local model: %+v %v", c, err)
	}
	if c, _ := Load(env(nil)); c.AI {
		t.Error("anthropic without key must leave the AI off")
	}
	for _, bad := range []map[string]string{
		{"PALIER_AI_PROVIDER": "openai", "PALIER_AI_MODEL": "gpt-5"},
		{"PALIER_AI_PROVIDER": "openai", "PALIER_AI_URL": "http://ollama:11434/v1"},
		{"PALIER_AI_PROVIDER": "openai", "PALIER_AI_URL": "ollama:11434", "PALIER_AI_MODEL": "m"},
		{"PALIER_AI_PROVIDER": "mistral"},
		{"PALIER_AI_URL": "http://x/v1"},
	} {
		if _, err := Load(env(bad)); err == nil || !strings.Contains(err.Error(), "PALIER_AI_") {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestChats(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PALIER_NTFY_URL":       "http://ntfy/palier",
		"PALIER_NTFY_LEVEL":     "alert",
		"PALIER_TELEGRAM_URL":   "https://api.telegram.org/bot1:abc/sendMessage?chat_id=-100",
		"PALIER_WEBHOOK_URL":    "https://hooks.example/x",
		"PALIER_WEBHOOK_SECRET": "s",
	}))
	if err != nil || len(c.Chats) != 2 || c.Chats[0].Type != "ntfy" || c.Chats[0].Level != "alert" || c.Chats[1].Level != "info" {
		t.Fatalf("%+v %v", c.Chats, err)
	}
	for _, bad := range []map[string]string{
		{"PALIER_SLACK_URL": "http://hooks.slack.com/services/x"},
		{"PALIER_DISCORD_URL": "discord.com/api/webhooks/1/x"},
		{"PALIER_TELEGRAM_URL": "https://api.telegram.org/bot1:abc/sendMessage"},
		{"PALIER_NTFY_URL": "https://ntfy.sh/x", "PALIER_NTFY_LEVEL": "all"},
		{"PALIER_WEBHOOK_SECRET": "s"},
	} {
		if _, err := Load(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
