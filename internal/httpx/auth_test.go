package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthStoreVerify(t *testing.T) {
	a := NewAuthStore("s3cret")

	if !a.Verify("s3cret") {
		t.Error("correct password was rejected")
	}
	for _, wrong := range []string{"", "s3cre", "s3crett", "S3cret", "wrong"} {
		if a.Verify(wrong) {
			t.Errorf("password %q was accepted", wrong)
		}
	}
}

func TestAuthStoreIssueAndValidate(t *testing.T) {
	a := NewAuthStore("pw")

	tok := a.Issue()
	if tok == "" {
		t.Fatal("Issue returned an empty token")
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars for 32 random bytes", len(tok))
	}
	if !a.Valid(tok) {
		t.Error("freshly issued token was rejected")
	}
	if a.Valid("") {
		t.Error("empty token was accepted")
	}
	if a.Valid("deadbeef") {
		t.Error("token that was never issued was accepted")
	}
}

func TestAuthStoreIssuesDistinctTokens(t *testing.T) {
	a := NewAuthStore("pw")

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok := a.Issue()
		if seen[tok] {
			t.Fatalf("Issue returned a duplicate token after %d calls", i)
		}
		seen[tok] = true
	}
}

func TestAuthStoreRevoke(t *testing.T) {
	a := NewAuthStore("pw")
	tok := a.Issue()

	a.Revoke(tok)
	if a.Valid(tok) {
		t.Error("revoked token is still valid")
	}
	a.Revoke(tok) // must not panic on a second revoke
	a.Revoke("never issued")
}

func TestAuthStoreRejectsExpiredToken(t *testing.T) {
	a := NewAuthStore("pw")
	tok := a.Issue()

	// Backdate past the TTL rather than waiting 90 days
	a.mu.Lock()
	a.tokens[tok] = time.Now().Add(-sessionTTL - time.Hour)
	a.mu.Unlock()

	if a.Valid(tok) {
		t.Error("expired token was accepted")
	}
	// Validating an expired token also evicts it
	a.mu.Lock()
	_, still := a.tokens[tok]
	a.mu.Unlock()
	if still {
		t.Error("expired token was left in the map after validation")
	}
}

func TestAuthStoreSweepEvictsOnlyExpired(t *testing.T) {
	a := NewAuthStore("pw")
	fresh := a.Issue()
	stale := a.Issue()

	a.mu.Lock()
	a.tokens[stale] = time.Now().Add(-sessionTTL - time.Hour)
	a.mu.Unlock()

	a.Sweep()

	if !a.Valid(fresh) {
		t.Error("Sweep evicted a live token")
	}
	a.mu.Lock()
	_, stillThere := a.tokens[stale]
	count := len(a.tokens)
	a.mu.Unlock()
	if stillThere {
		t.Error("Sweep left an expired token behind")
	}
	if count != 1 {
		t.Errorf("token map holds %d entries after Sweep, want 1", count)
	}
}

func TestRunSweeperStopsOnContextCancel(t *testing.T) {
	a := NewAuthStore("pw")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.RunSweeper(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunSweeper did not return after cancellation")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	req.Form = map[string][]string{"password": {"not the password"}}
	rec := env.do(t, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/login?error=1" {
		t.Errorf("Location = %q, want the login page with an error", loc)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Error("a session cookie was issued for a failed login")
		}
	}
}

func TestLoginRejectsEmptyPassword(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	req.Form = map[string][]string{"password": {""}}
	rec := env.do(t, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Error("an empty password produced a session")
		}
	}
}

func TestLoginSetsHardenedCookie(t *testing.T) {
	env := newTestEnv(t)
	c := env.login(t)

	if !c.HttpOnly {
		t.Error("session cookie is not HttpOnly, so scripts can read it")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if c.MaxAge != int(sessionTTL.Seconds()) {
		t.Errorf("MaxAge = %d, want %d", c.MaxAge, int(sessionTTL.Seconds()))
	}
	if c.Value == "" {
		t.Error("session cookie has no value")
	}
}

// Secure must track how the request actually arrived, so a cookie is not
// marked Secure over plain HTTP (browsers would drop it) but is behind a
// TLS-terminating proxy
func TestLoginCookieSecureFlagFollowsScheme(t *testing.T) {
	env := newTestEnv(t)

	plain := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	plain.Form = map[string][]string{"password": {testPassword}}
	for _, c := range env.do(t, plain).Result().Cookies() {
		if c.Name == sessionCookie && c.Secure {
			t.Error("cookie marked Secure over plain HTTP")
		}
	}

	proxied := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	proxied.Form = map[string][]string{"password": {testPassword}}
	proxied.Header.Set("X-Forwarded-Proto", "https")
	for _, c := range env.do(t, proxied).Result().Cookies() {
		if c.Name == sessionCookie && !c.Secure {
			t.Error("cookie not marked Secure behind an https proxy")
		}
	}
}

func TestRequestIsHTTPS(t *testing.T) {
	cases := []struct {
		name  string
		proto string
		want  bool
	}{
		{"no header", "", false},
		{"https", "https", true},
		{"http", "http", false},
		{"uppercase is not honoured", "HTTPS", false},
		{"proxy list is not honoured", "https, http", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			if got := requestIsHTTPS(r); got != tc.want {
				t.Errorf("requestIsHTTPS = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoginGrantsAccessToAdmin(t *testing.T) {
	env := newTestEnv(t)
	cookie := env.login(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(cookie)
	rec := env.do(t, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("admin home with a session = %d, want 200", rec.Code)
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	env := newTestEnv(t)
	cookie := env.login(t)

	out := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	out.AddCookie(cookie)
	rec := env.do(t, out)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	// The same cookie must no longer work
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(cookie)
	if got := env.do(t, req).Code; got != http.StatusSeeOther {
		t.Errorf("admin home after logout = %d, want a redirect to login", got)
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	env := newTestEnv(t)
	cookie := env.login(t)

	out := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	out.AddCookie(cookie)
	rec := env.do(t, out)

	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value == "" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not send an expiring empty session cookie")
	}
}

func TestLogoutWithoutASessionIsHarmless(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, httptest.NewRequest(http.MethodPost, "/admin/logout", nil))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
}

func TestForgedSessionCookieIsRejected(t *testing.T) {
	env := newTestEnv(t)

	for _, forged := range []string{
		"",
		"deadbeef",
		"0000000000000000000000000000000000000000000000000000000000000000",
	} {
		req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: forged})
		if got := env.do(t, req).Code; got != http.StatusSeeOther {
			t.Errorf("forged cookie %q got status %d, want a redirect to login", forged, got)
		}
	}
}

// A session issued by one server must not be honoured by another, since
// tokens live in memory and do not survive a restart
func TestSessionFromAnotherServerIsRejected(t *testing.T) {
	first := newTestEnv(t)
	second := newTestEnv(t)

	cookie := first.login(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(cookie)

	if got := second.do(t, req).Code; got != http.StatusSeeOther {
		t.Errorf("another server accepted a foreign session (status %d)", got)
	}
}

func TestVerifyRecaptchaWithoutASecretPassesThrough(t *testing.T) {
	if !verifyRecaptcha(context.Background(), "", "", "") {
		t.Error("an unconfigured reCAPTCHA should not block logins")
	}
}

func TestVerifyRecaptchaRejectsMissingToken(t *testing.T) {
	if verifyRecaptcha(context.Background(), "a-secret", "", "1.2.3.4") {
		t.Error("a missing token was accepted while reCAPTCHA is configured")
	}
}
