package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kolapsis/palier/internal/config"
)

const (
	sessionCookie = "palier_session"
	loginCookie   = "palier_login"
	sessionLength = 12 * time.Hour
)

// gate signs people in with an OpenID Connect provider and lets only admins through.
type gate struct {
	issuer   string
	clientID string
	secret   string
	admins   []string
	key      []byte
	http     *http.Client
	page     func(w http.ResponseWriter, status int, title, message string)

	mu   sync.Mutex
	meta *discovery
}

type discovery struct {
	Issuer        string   `json:"issuer"`
	Authorization string   `json:"authorization_endpoint"`
	Token         string   `json:"token_endpoint"`
	Userinfo      string   `json:"userinfo_endpoint"`
	EndSession    string   `json:"end_session_endpoint"`
	Scopes        []string `json:"scopes_supported"`
	AuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
}

type identity struct {
	Name string `json:"name"`
	Exp  int64  `json:"exp"`
}

type pending struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Exp      int64  `json:"exp"`
}

type ctxKey struct{}

func newGate(cfg config.Config, page func(http.ResponseWriter, int, string, string)) *gate {
	key := make([]byte, 32)
	rand.Read(key)
	return &gate{issuer: strings.TrimSuffix(cfg.OIDCIssuer, "/"), clientID: cfg.OIDCClientID, secret: cfg.OIDCSecret, admins: cfg.OIDCAdmins,
		key: key, http: &http.Client{Timeout: 15 * time.Second}, page: page}
}

// who returns the name of the person signed in on r, or "".
func who(r *http.Request) string {
	id, _ := r.Context().Value(ctxKey{}).(identity)
	return id.Name
}

func (g *gate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/callback":
			g.callback(w, r)
			return
		case "/auth/logout":
			g.logout(w, r)
			return
		}
		var id identity
		if c, err := r.Cookie(sessionCookie); err == nil && g.open(c.Value, &id) && time.Now().Unix() < id.Exp {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
			return
		}
		if r.Method != http.MethodGet {
			g.page(w, http.StatusUnauthorized, "Signed out", "Your session has ended. Reload the page to sign in again.")
			return
		}
		g.login(w, r)
	})
}

func (g *gate) discover(ctx context.Context) (*discovery, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.meta != nil {
		return g.meta, nil
	}
	var d discovery
	if err := g.getJSON(ctx, g.issuer+"/.well-known/openid-configuration", "", &d); err != nil {
		return nil, fmt.Errorf("discovery of %s: %w", g.issuer, err)
	}
	if strings.TrimSuffix(d.Issuer, "/") != g.issuer || d.Authorization == "" || d.Token == "" {
		return nil, fmt.Errorf("discovery of %s: issuer %q or endpoints missing", g.issuer, d.Issuer)
	}
	g.meta = &d
	return g.meta, nil
}

func (g *gate) login(w http.ResponseWriter, r *http.Request) {
	d, err := g.discover(r.Context())
	if err != nil {
		g.page(w, http.StatusBadGateway, "Sign-in unavailable", err.Error())
		return
	}
	p := pending{State: random(), Nonce: random(), Verifier: random(), Next: r.URL.RequestURI(), Exp: time.Now().Add(10 * time.Minute).Unix()}
	http.SetCookie(w, g.cookie(r, loginCookie, g.seal(p), "/auth/", 600))
	scope := []string{"openid", "email", "profile"}
	if slices.Contains(d.Scopes, "groups") {
		scope = append(scope, "groups")
	}
	challenge := sha256.Sum256([]byte(p.Verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {g.clientID}, "redirect_uri": {base(r) + "/auth/callback"},
		"scope": {strings.Join(scope, " ")}, "state": {p.State}, "nonce": {p.Nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, d.Authorization+sep(d.Authorization)+q.Encode(), http.StatusFound)
}

func (g *gate) callback(w http.ResponseWriter, r *http.Request) {
	var p pending
	c, err := r.Cookie(loginCookie)
	if err != nil || !g.open(c.Value, &p) || time.Now().Unix() > p.Exp || p.State == "" || r.FormValue("state") != p.State {
		g.page(w, http.StatusBadRequest, "Sign-in failed", "The sign-in took too long or did not start here. Open Palier again.")
		return
	}
	http.SetCookie(w, g.cookie(r, loginCookie, "", "/auth/", -1))
	if e := r.FormValue("error"); e != "" {
		g.page(w, http.StatusForbidden, "Sign-in refused", "The identity provider answered: "+e+" "+r.FormValue("error_description"))
		return
	}
	claims, err := g.exchange(r, r.FormValue("code"), p)
	if err != nil {
		g.page(w, http.StatusBadGateway, "Sign-in failed", err.Error())
		return
	}
	name := firstString(claims, "email", "preferred_username", "name", "sub")
	if !isAdmin(claims, g.admins) {
		g.page(w, http.StatusForbidden, "Admins only", "You need to be an admin to access Palier. You are signed in as "+name+", who is not an admin.")
		return
	}
	http.SetCookie(w, g.cookie(r, sessionCookie, g.seal(identity{Name: name, Exp: time.Now().Add(sessionLength).Unix()}), "/", int(sessionLength.Seconds())))
	next := p.Next
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/auth/") {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// exchange returns the claims of the ID token, checked but not its signature since it comes straight from the token endpoint over TLS (OpenID Connect Core 3.1.3.7).
func (g *gate) exchange(r *http.Request, code string, p pending) (map[string]any, error) {
	d, err := g.discover(r.Context())
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {base(r) + "/auth/callback"}, "code_verifier": {p.Verifier}, "client_id": {g.clientID}}
	basic := g.secret != "" && (len(d.AuthMethods) == 0 || slices.Contains(d.AuthMethods, "client_secret_basic"))
	if g.secret != "" && !basic {
		form.Set("client_secret", g.secret)
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, d.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(g.clientID), url.QueryEscape(g.secret))
	}
	var tok struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if err := g.do(req, &tok); err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	claims, err := idClaims(tok.IDToken)
	if err != nil {
		return nil, err
	}
	if err := checkClaims(claims, d.Issuer, g.clientID, p.Nonce, time.Now()); err != nil {
		return nil, err
	}
	if d.Userinfo != "" && tok.AccessToken != "" {
		var info map[string]any
		if g.getJSON(r.Context(), d.Userinfo, tok.AccessToken, &info) == nil && info["sub"] == claims["sub"] {
			for k, v := range info {
				if _, ok := claims[k]; !ok {
					claims[k] = v
				}
			}
		}
	}
	return claims, nil
}

func idClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("the identity provider returned no ID token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, fmt.Errorf("ID token: %w", err)
	}
	var claims map[string]any
	return claims, json.Unmarshal(payload, &claims)
}

func checkClaims(c map[string]any, issuer, clientID, nonce string, now time.Time) error {
	aud := strs(c["aud"])
	exp, _ := c["exp"].(float64)
	switch {
	case c["iss"] != issuer:
		return fmt.Errorf("ID token issued by %v, expected %s", c["iss"], issuer)
	case !slices.Contains(aud, clientID) || len(aud) > 1 && c["azp"] != clientID:
		return fmt.Errorf("ID token for %v, not for client %s", c["aud"], clientID)
	case float64(now.Unix()) > exp:
		return errors.New("ID token expired")
	case c["nonce"] != nonce:
		return errors.New("ID token nonce does not match this sign-in")
	case firstString(c, "sub") == "":
		return errors.New("ID token without subject")
	}
	return nil
}

// isAdmin tells whether the claims match one entry of admins: a group or role, an email, or an @domain of a verified email.
func isAdmin(claims map[string]any, admins []string) bool {
	email := strings.ToLower(firstString(claims, "email"))
	if v, ok := claims["email_verified"]; ok && v != true && v != "true" {
		email = ""
	}
	var values []string
	for _, k := range []string{"groups", "roles"} {
		for _, v := range strs(claims[k]) {
			values = append(values, strings.TrimPrefix(v, "/"))
		}
	}
	for _, a := range admins {
		a = strings.ToLower(a)
		switch {
		case strings.HasPrefix(a, "@"):
			if email != "" && strings.HasSuffix(email, a) {
				return true
			}
		case strings.Contains(a, "@"):
			if email == a {
				return true
			}
		default:
			if slices.ContainsFunc(values, func(v string) bool { return strings.EqualFold(v, strings.TrimPrefix(a, "/")) }) {
				return true
			}
		}
	}
	return false
}

func (g *gate) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, g.cookie(r, sessionCookie, "", "/", -1))
	if d, err := g.discover(r.Context()); err == nil && d.EndSession != "" {
		q := url.Values{"client_id": {g.clientID}, "post_logout_redirect_uri": {base(r) + "/"}}
		http.Redirect(w, r, d.EndSession+sep(d.EndSession)+q.Encode(), http.StatusFound)
		return
	}
	g.page(w, http.StatusOK, "Signed out", "You are signed out of Palier.")
}

func (g *gate) getJSON(ctx context.Context, u, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return g.do(req, out)
}

func (g *gate) do(req *http.Request, out any) error {
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %.200s", resp.Status, body)
	}
	return json.Unmarshal(body, out)
}

func (g *gate) seal(v any) string {
	b, _ := json.Marshal(v)
	m := hmac.New(sha256.New, g.key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (g *gate) open(s string, v any) bool {
	data, sig, ok := strings.Cut(s, ".")
	b, err1 := base64.RawURLEncoding.DecodeString(data)
	got, err2 := base64.RawURLEncoding.DecodeString(sig)
	if !ok || err1 != nil || err2 != nil {
		return false
	}
	m := hmac.New(sha256.New, g.key)
	m.Write(b)
	return hmac.Equal(got, m.Sum(nil)) && json.Unmarshal(b, v) == nil
}

func (g *gate) cookie(r *http.Request, name, value, path string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure(r)}
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// base is the address the browser uses to reach Palier, behind a reverse proxy too.
func base(r *http.Request) string {
	scheme := "http"
	if secure(r) {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

func sep(u string) string {
	if strings.Contains(u, "?") {
		return "&"
	}
	return "?"
}

func random() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func strs(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func firstString(c map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := c[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
