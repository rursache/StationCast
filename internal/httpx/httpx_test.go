package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rursache/StationCast/internal/audio"
	"github.com/rursache/StationCast/internal/broadcast"
	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/playlist"
	"github.com/rursache/StationCast/internal/storage"
)

const testPassword = "correct horse battery staple"

// testEnv holds the wired-up server plus the pieces a test needs to poke at
// directly. The router is the real one, so route registration and middleware
// are exercised rather than reimplemented
type testEnv struct {
	handler http.Handler
	srv     *Server // for calling handlers directly, past chi's path cleaning
	cfg     *config.Config
	lib     *playlist.Library
	sched   *playlist.Scheduler
	hub     *broadcast.Hub
	hls     *broadcast.HLSManager
	music   string
	data    string
}

func newTestEnv(t *testing.T, trackNames ...string) *testEnv {
	t.Helper()
	music := t.TempDir()
	data := t.TempDir()
	for _, n := range trackNames {
		p := filepath.Join(music, n)
		if err := os.WriteFile(p, []byte("not really audio"), 0o644); err != nil {
			t.Fatalf("write %q: %v", n, err)
		}
	}

	cfg := &config.Config{
		MusicDir:      music,
		DataDir:       data,
		AdminPassword: testPassword,
		StationName:   "Test Station",
		StationGenre:  "Testing",
		Bitrate:       128,
		MaxListeners:  256,
		Version:       "v0.0.0-test",
	}
	for _, sub := range []string{"art", "hls"} {
		if err := os.MkdirAll(filepath.Join(data, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	db, err := storage.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	lib := playlist.NewLibrary(cfg, db)
	if err := lib.InitialScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	sched := playlist.NewScheduler(cfg, db, lib)
	hub := broadcast.NewHub(cfg.Bitrate)
	hub.SetMaxListeners(cfg.MaxListeners)
	hls := broadcast.NewHLSManager(hub, data)
	eng := audio.NewEngine(cfg, sched, hub, lib)

	handler, _ := NewRouter(cfg, db, lib, sched, hub, hls, eng)
	t.Cleanup(hub.Close)

	return &testEnv{
		handler: handler,
		srv:     newServer(cfg, db, lib, sched, hub, hls, eng),
		cfg:     cfg,
		lib:     lib,
		sched:   sched,
		hub:     hub,
		hls:     hls,
		music:   music,
		data:    data,
	}
}

func (e *testEnv) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, httptest.NewRequest(http.MethodGet, path, nil))
}

// login performs a real login and returns the session cookie
func (e *testEnv) login(t *testing.T) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/login", nil)
	req.Form = map[string][]string{"password": {testPassword}}
	rec := e.do(t, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("login did not set a session cookie")
	return nil
}

func (e *testEnv) authed(t *testing.T, method, path string, form map[string][]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(e.login(t))
	if form != nil {
		req.Form = form
	}
	return e.do(t, req)
}

func (e *testEnv) trackID(t *testing.T, name string) int64 {
	t.Helper()
	tr, ok := e.lib.GetByPath(filepath.Join(e.music, name))
	if !ok {
		t.Fatalf("track %q was not indexed", name)
	}
	return tr.ID
}
