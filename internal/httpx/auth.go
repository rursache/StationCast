package httpx

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const (
	sessionCookie = "stationcast_session"
	sessionTTL    = 90 * 24 * time.Hour
	sweepInterval = 1 * time.Hour
)

type AuthStore struct {
	password string
	mu       sync.Mutex
	tokens   map[string]time.Time
}

func NewAuthStore(password string) *AuthStore {
	return &AuthStore{
		password: password,
		tokens:   map[string]time.Time{},
	}
}

func (a *AuthStore) Verify(password string) bool {
	return subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1
}

func (a *AuthStore) Issue() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	a.mu.Lock()
	a.tokens[tok] = time.Now()
	a.mu.Unlock()
	return tok
}

func (a *AuthStore) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	issued, ok := a.tokens[tok]
	if !ok {
		return false
	}
	if time.Since(issued) > sessionTTL {
		delete(a.tokens, tok)
		return false
	}
	return true
}

func (a *AuthStore) Revoke(tok string) {
	a.mu.Lock()
	delete(a.tokens, tok)
	a.mu.Unlock()
}

// Sweep walks the token map and evicts expired entries. Called periodically
// from a goroutine so dead tokens don't accumulate forever in memory
func (a *AuthStore) Sweep() {
	cutoff := time.Now().Add(-sessionTTL)
	a.mu.Lock()
	for tok, issued := range a.tokens {
		if issued.Before(cutoff) {
			delete(a.tokens, tok)
		}
	}
	a.mu.Unlock()
}

// RunSweeper runs Sweep on an interval until ctx is done
func (a *AuthStore) RunSweeper(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Sweep()
		}
	}
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || !s.auth.Valid(c.Value) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestIsHTTPS reports whether the incoming request reached us over TLS,
// either directly or via a reverse proxy that set X-Forwarded-Proto. We
// only honour the forwarded header when it looks well-formed
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		return true
	}
	return false
}
