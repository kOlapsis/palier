package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTokenGuardsEverything(t *testing.T) {
	h := New("secret", func() Snapshot { return Snapshot{} })
	do := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do("GET", "/", "", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous /: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do("GET", "/anything", "", &http.Cookie{Name: cookieName, Value: "wrong"}); w.Code != http.StatusSeeOther {
		t.Fatalf("wrong cookie: %d", w.Code)
	}
	if w := do("POST", "/login", url.Values{"token": {"wrong"}}.Encode(), nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", w.Code)
	}
	w := do("POST", "/login", url.Values{"token": {"secret"}}.Encode(), nil)
	cookies := w.Result().Cookies()
	if w.Code != http.StatusSeeOther || len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("good token: %d %v", w.Code, cookies)
	}
	if w := do("GET", "/", "", cookies[0]); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Stacks") {
		t.Fatalf("authorized /: %d", w.Code)
	}
}
