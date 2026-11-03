package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
)

func TestIsAdmin(t *testing.T) {
	admins := []string{"palier-admins", "boss@example.com", "@kolapsis.com", "3f2a-entra-group-id"}
	for name, c := range map[string]struct {
		claims map[string]any
		want   bool
	}{
		"authentik group":     {map[string]any{"groups": []any{"users", "palier-admins"}}, true},
		"keycloak group path": {map[string]any{"groups": []any{"/palier-admins"}}, true},
		"entra group id":      {map[string]any{"groups": []any{"3f2a-entra-group-id"}}, true},
		"app role":            {map[string]any{"roles": "palier-admins"}, true},
		"listed email":        {map[string]any{"email": "Boss@Example.com"}, true},
		"google domain":       {map[string]any{"email": "ben@kolapsis.com", "email_verified": true}, true},
		"unverified domain":   {map[string]any{"email": "ben@kolapsis.com", "email_verified": false}, false},
		"lookalike domain":    {map[string]any{"email": "ben@notkolapsis.com"}, false},
		"other group":         {map[string]any{"groups": []any{"palier-admins-old"}}, false},
		"nothing":             {map[string]any{"sub": "1"}, false},
	} {
		if got := isAdmin(c.claims, admins); got != c.want {
			t.Errorf("%s: isAdmin = %v", name, got)
		}
	}
}

func TestCheckClaims(t *testing.T) {
	now := time.Unix(1000, 0)
	good := func() map[string]any {
		return map[string]any{"iss": "https://idp", "aud": "palier", "exp": 2000.0, "nonce": "n", "sub": "u1"}
	}
	if err := checkClaims(good(), "https://idp", "palier", "n", now); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(map[string]any){
		"issuer":   func(c map[string]any) { c["iss"] = "https://evil" },
		"audience": func(c map[string]any) { c["aud"] = "other" },
		"azp":      func(c map[string]any) { c["aud"] = []any{"palier", "other"} },
		"expired":  func(c map[string]any) { c["exp"] = 999.0 },
		"nonce":    func(c map[string]any) { c["nonce"] = "x" },
		"subject":  func(c map[string]any) { delete(c, "sub") },
	} {
		c := good()
		change(c)
		if checkClaims(c, "https://idp", "palier", "n", now) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSealedCookies(t *testing.T) {
	g := newGate(config.Config{OIDCIssuer: "https://idp"}, nil)
	s := g.seal(identity{Name: "ben", Exp: 5})
	var id identity
	if !g.open(s, &id) || id.Name != "ben" {
		t.Fatalf("open(%q) = %+v", s, id)
	}
	data, sig, _ := strings.Cut(s, ".")
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"name":"eve","exp":9999999999}`))
	if g.open(forged+"."+sig, &id) || g.open(data+".AAAA", &id) || newGate(config.Config{}, nil).open(s, &id) {
		t.Error("a tampered or foreign cookie was accepted")
	}
}

func TestSignInFlow(t *testing.T) {
	var idp *httptest.Server
	var nonce string
	groups := []any{"palier-admins"}
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": idp.URL, "authorization_endpoint": idp.URL + "/authorize", "token_endpoint": idp.URL + "/token", "userinfo_endpoint": idp.URL + "/userinfo", "scopes_supported": []string{"openid", "groups"}})
		case "/token":
			if id, secret, _ := r.BasicAuth(); id != "palier" || secret != "s3cret" || r.FormValue("code") != "the-code" || len(r.FormValue("code_verifier")) < 43 {
				http.Error(w, "bad client", http.StatusUnauthorized)
				return
			}
			payload, _ := json.Marshal(map[string]any{"iss": idp.URL, "aud": "palier", "exp": time.Now().Add(time.Minute).Unix(), "nonce": nonce, "sub": "u1", "email": "ben@kolapsis.com"})
			json.NewEncoder(w).Encode(map[string]string{"id_token": "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig", "access_token": "at"})
		case "/userinfo":
			json.NewEncoder(w).Encode(map[string]any{"sub": "u1", "groups": groups})
		}
	}))
	defer idp.Close()
	decided := ""
	h := New(Deps{
		Current: func() Snapshot { return Snapshot{} },
		Runs:    func(string) ([]run.View, error) { return nil, nil },
		Plan:    func(string) (*plan.Record, error) { return nil, nil },
		Decide:  func(stack, author, kind, service, version, until string) error { decided = author; return nil },
		Config:  config.Config{OIDCIssuer: idp.URL, OIDCClientID: "palier", OIDCSecret: "s3cret", OIDCAdmins: []string{"palier-admins"}},
	})
	signIn := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://palier.test/stacks/x", nil))
		loc, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusFound || !strings.HasPrefix(loc.String(), idp.URL+"/authorize") || loc.Query().Get("redirect_uri") != "http://palier.test/auth/callback" || !strings.Contains(loc.Query().Get("scope"), "groups") {
			t.Fatalf("not sent to the provider: %d %s", w.Code, loc)
		}
		nonce = loc.Query().Get("nonce")
		r := httptest.NewRequest("GET", "http://palier.test/auth/callback?code=the-code&state="+loc.Query().Get("state"), nil)
		for _, c := range w.Result().Cookies() {
			r.AddCookie(c)
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := signIn()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/stacks/x" {
		t.Fatalf("admin not let in: %d %s", w.Code, w.Body.String())
	}
	session := w.Result().Cookies()[len(w.Result().Cookies())-1]
	r := httptest.NewRequest("POST", "http://palier.test/stacks/x/postpone", strings.NewReader("until=2026-12-01"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(session)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if decided != "ben@kolapsis.com" {
		t.Errorf("decision signed %q", decided)
	}
	groups = []any{"users"}
	if w := signIn(); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "You need to be an admin to access Palier") || len(w.Result().Cookies()) > 1 {
		t.Errorf("non-admin: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://palier.test/auth/callback?code=the-code&state=forged", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("callback without a sign-in started here: %d", w.Code)
	}
}
