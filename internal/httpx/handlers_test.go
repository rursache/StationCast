package httpx

import (
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rursache/StationCast/internal/broadcast"
)

// Every admin route must sit behind requireAuth. A route added to the wrong
// chi group would otherwise be publicly writable
func TestAdminRoutesRequireAuth(t *testing.T) {
	env := newTestEnv(t, "song.mp3")

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/"},
		{http.MethodGet, "/admin/library.json"},
		{http.MethodGet, "/admin/state.json"},
		{http.MethodPost, "/admin/skip"},
		{http.MethodPost, "/admin/mode"},
		{http.MethodPost, "/admin/queue"},
		{http.MethodPost, "/admin/queue/remove"},
		{http.MethodPost, "/admin/files/rename"},
		{http.MethodPost, "/admin/files/delete"},
		{http.MethodPost, "/admin/files/upload"},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			rec := env.do(t, httptest.NewRequest(r.method, r.path, nil))
			if rec.Code != http.StatusSeeOther {
				t.Errorf("status = %d, want %d (redirect to login)", rec.Code, http.StatusSeeOther)
			}
			if loc := rec.Header().Get("Location"); loc != "/admin/login" {
				t.Errorf("Location = %q, want /admin/login", loc)
			}
		})
	}
}

func TestPublicRoutesNeedNoAuth(t *testing.T) {
	env := newTestEnv(t, "song.mp3")

	for _, path := range []string{
		"/",
		"/now-playing",
		"/history",
		"/stream.pls",
		"/stream.m3u",
		"/admin/login",
	} {
		t.Run(path, func(t *testing.T) {
			if got := env.get(t, path).Code; got != http.StatusOK {
				t.Errorf("status = %d, want 200", got)
			}
		})
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	env := newTestEnv(t)

	for _, path := range []string{"/", "/now-playing", "/admin/login"} {
		rec := env.get(t, path)
		h := rec.Header()
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
		if got := h.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "same-origin" {
			t.Errorf("%s: Referrer-Policy = %q, want same-origin", path, got)
		}
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	env := newTestEnv(t)
	if got := env.get(t, "/nope").Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestNowPlayingJSONWithNothingPlaying(t *testing.T) {
	env := newTestEnv(t)
	rec := env.get(t, "/now-playing")

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	var np nowPlaying
	if err := json.Unmarshal(rec.Body.Bytes(), &np); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if np.StationName != "Test Station" {
		t.Errorf("StationName = %q", np.StationName)
	}
	if np.Title != "" {
		t.Errorf("Title = %q, want empty when nothing is playing", np.Title)
	}
	if np.Listeners != 0 {
		t.Errorf("Listeners = %d, want 0", np.Listeners)
	}
}

func TestNowPlayingJSONReflectsCurrentTrack(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	tr, _ := env.lib.Get(env.trackID(t, "song.mp3"))
	env.sched.MarkPlaying(tr)

	var np nowPlaying
	if err := json.Unmarshal(env.get(t, "/now-playing").Body.Bytes(), &np); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if np.Title != tr.Title {
		t.Errorf("Title = %q, want %q", np.Title, tr.Title)
	}
	if np.StartedAt == 0 {
		t.Error("StartedAt = 0 while a track is playing")
	}
	// No artist tag on the fake file, so the station name stands in
	if np.Artist != "Test Station" {
		t.Errorf("Artist = %q, want the station name as fallback", np.Artist)
	}
}

func TestNowPlayingCountsListeners(t *testing.T) {
	env := newTestEnv(t)
	sub, err := env.hub.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	var np nowPlaying
	if err := json.Unmarshal(env.get(t, "/now-playing").Body.Bytes(), &np); err != nil {
		t.Fatal(err)
	}
	if np.Listeners != 1 {
		t.Errorf("Listeners = %d, want 1", np.Listeners)
	}
}

func TestHistoryJSON(t *testing.T) {
	env := newTestEnv(t, "a.mp3", "b.mp3")

	rec := env.get(t, "/history")
	var empty []historyEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode empty history: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("fresh history has %d entries, want 0", len(empty))
	}

	first, _ := env.lib.Get(env.trackID(t, "a.mp3"))
	second, _ := env.lib.Get(env.trackID(t, "b.mp3"))
	env.sched.MarkPlaying(first)
	env.sched.MarkPlaying(second)

	var got []historyEntry
	if err := json.Unmarshal(env.get(t, "/history").Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("history has %d entries, want 2", len(got))
	}
	// Most recent first
	if got[0].ID != second.ID {
		t.Errorf("history[0].ID = %d, want the most recent track %d", got[0].ID, second.ID)
	}
}

func TestArtNotFoundForUnknownOrArtlessTrack(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	id := env.trackID(t, "song.mp3")

	for _, path := range []string{
		"/art/999999",
		"/art/" + strconv.FormatInt(id, 10), // indexed but has_art is false
		"/art/notanumber",
		"/art/-1",
	} {
		t.Run(path, func(t *testing.T) {
			if got := env.get(t, path).Code; got != http.StatusNotFound {
				t.Errorf("status = %d, want 404", got)
			}
		})
	}
}

func TestArtServesCachedCover(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	id := env.trackID(t, "song.mp3")
	tr, _ := env.lib.Get(id)
	tr.HasArt = true

	want := []byte("jpeg bytes")
	if err := os.WriteFile(filepath.Join(env.data, "art", strconv.FormatInt(id, 10)+".jpg"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := env.get(t, "/art/"+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != string(want) {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
	// Revalidation matters because RefreshArt overwrites the file in place
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "must-revalidate") {
		t.Errorf("Cache-Control = %q, want must-revalidate so overwrites propagate", cc)
	}
}

func TestStaticAssetsAreServedWithCorrectTypes(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		path string
		ct   string
	}{
		{"/static/app.css", "text/css; charset=utf-8"},
		{"/static/app.js", "application/javascript; charset=utf-8"},
		{"/static/utils.js", "application/javascript; charset=utf-8"},
		{"/static/icon.svg", "image/svg+xml; charset=utf-8"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := env.get(t, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.ct {
				t.Errorf("Content-Type = %q, want %q", got, tc.ct)
			}
			if rec.Body.Len() == 0 {
				t.Error("empty body")
			}
		})
	}
}

func TestStaticRejectsTraversalAndUnknownFiles(t *testing.T) {
	env := newTestEnv(t)

	for _, path := range []string{
		"/static/../templates/admin.html",
		"/static/nope.css",
		"/static/",
	} {
		t.Run(path, func(t *testing.T) {
			rec := env.do(t, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestPLSAndM3UPointAtTheStream(t *testing.T) {
	env := newTestEnv(t)

	pls := env.get(t, "/stream.pls")
	if ct := pls.Header().Get("Content-Type"); ct != "audio/x-scpls" {
		t.Errorf("pls Content-Type = %q", ct)
	}
	if !strings.Contains(pls.Body.String(), "/stream") {
		t.Errorf("pls body does not reference the stream: %q", pls.Body.String())
	}
	if !strings.Contains(pls.Body.String(), "Test Station") {
		t.Error("pls body is missing the station name")
	}

	m3u := env.get(t, "/stream.m3u")
	if ct := m3u.Header().Get("Content-Type"); ct != "audio/x-mpegurl" {
		t.Errorf("m3u Content-Type = %q", ct)
	}
	if !strings.HasPrefix(m3u.Body.String(), "#EXTM3U") {
		t.Errorf("m3u body does not start with #EXTM3U: %q", m3u.Body.String())
	}
}

// PublicURL wins over the request host so links behind a proxy stay correct
func TestStreamURLPrefersConfiguredPublicURL(t *testing.T) {
	env := newTestEnv(t)
	env.cfg.PublicURL = "https://radio.example.com/"

	body := env.get(t, "/stream.pls").Body.String()
	if !strings.Contains(body, "https://radio.example.com/stream") {
		t.Errorf("pls body did not use PublicURL: %q", body)
	}
}

func TestStreamURLFallsBackToRequestHost(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/stream.m3u", nil)
	req.Host = "listen.example.org"
	req.Header.Set("X-Forwarded-Proto", "https")

	body := env.do(t, req).Body.String()
	if !strings.Contains(body, "https://listen.example.org/stream") {
		t.Errorf("m3u body = %q, want the forwarded scheme and host", body)
	}
}

func TestStreamAtCapacityReturns503(t *testing.T) {
	env := newTestEnv(t)
	env.hub.SetMaxListeners(1)

	held, err := env.hub.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	rec := env.get(t, "/stream")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("no Retry-After on a capacity refusal")
	}
	if !strings.Contains(rec.Body.String(), "capacity") {
		t.Errorf("body = %q, want it to mention capacity", rec.Body.String())
	}
}

func TestStreamOnAClosedHubReturns503(t *testing.T) {
	env := newTestEnv(t)
	env.hub.Close()

	rec := env.get(t, "/stream")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 once the hub is closed", rec.Code)
	}
}

func TestStreamSendsICYHeadersAndAudio(t *testing.T) {
	env := newTestEnv(t)

	srv := httptest.NewServer(env.handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Icy-MetaData", "1")

	type result struct {
		resp *http.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		ch <- result{resp, err}
	}()

	// Give the handler a moment to subscribe, then push a frame through
	deadline := time.Now().Add(2 * time.Second)
	for env.hub.Listeners() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := env.hub.Write([]byte{0xff, 0xfb, 0x90, 0x00}); err != nil {
		t.Fatalf("hub write: %v", err)
	}

	res := <-ch
	if res.err != nil {
		t.Fatalf("request: %v", res.err)
	}
	defer res.resp.Body.Close()

	if res.resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.resp.StatusCode)
	}
	h := res.resp.Header
	if got := h.Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", got)
	}
	if got := h.Get("icy-name"); got != "Test Station" {
		t.Errorf("icy-name = %q", got)
	}
	if got := h.Get("icy-genre"); got != "Testing" {
		t.Errorf("icy-genre = %q", got)
	}
	if got := h.Get("icy-br"); got != "128" {
		t.Errorf("icy-br = %q, want 128", got)
	}
	if got := h.Get("icy-metaint"); got != strconv.Itoa(broadcast.ICYMetaInt) {
		t.Errorf("icy-metaint = %q, want %d", got, broadcast.ICYMetaInt)
	}

	// Closing the hub ends the stream so the read can finish
	env.hub.Close()
	buf := make([]byte, 4)
	if _, err := res.resp.Body.Read(buf); err != nil && err.Error() != "EOF" {
		t.Logf("read after close: %v", err)
	}
}

// A client that does not ask for metadata must not be sent the metaint
// header, or it will treat interleaved metadata bytes as audio
func TestStreamOmitsMetaintWhenNotRequested(t *testing.T) {
	env := newTestEnv(t)
	srv := httptest.NewServer(env.handler)
	defer srv.Close()

	ch := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/stream")
		if err == nil {
			ch <- resp
		} else {
			close(ch)
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for env.hub.Listeners() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	_, _ = env.hub.Write([]byte{0xff, 0xfb})

	resp, ok := <-ch
	if !ok {
		t.Fatal("request failed")
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("icy-metaint"); got != "" {
		t.Errorf("icy-metaint = %q, want it absent when Icy-MetaData was not sent", got)
	}
	env.hub.Close()
}

func TestHLSPlaylistUnavailableBeforeFFmpegRuns(t *testing.T) {
	env := newTestEnv(t)
	if got := env.get(t, "/hls.m3u8").Code; got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when no playlist exists yet", got)
	}
}

// Segment lines are relative in the file ffmpeg writes, and the browser
// resolves them against /hls.m3u8, so they need the hls/ prefix
func TestHLSPlaylistRewritesSegmentPaths(t *testing.T) {
	env := newTestEnv(t)

	raw := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:4",
		"#EXTINF:4.0,",
		"seg-00001.ts",
		"#EXTINF:4.0,",
		"seg-00002.ts",
		"",
	}, "\n")
	if err := os.WriteFile(env.hls.PlaylistPath(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := env.get(t, "/hls.m3u8")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"hls/seg-00001.ts", "hls/seg-00002.ts"} {
		if !strings.Contains(body, want) {
			t.Errorf("playlist missing %q:\n%s", want, body)
		}
	}
	// Directives must pass through untouched
	if !strings.Contains(body, "#EXT-X-TARGETDURATION:4") {
		t.Error("playlist directives were altered")
	}
	if strings.Contains(body, "hls/#") {
		t.Error("a directive line was given the hls/ prefix")
	}
}

func TestHLSSegmentServesRealSegment(t *testing.T) {
	env := newTestEnv(t)
	want := []byte("mpegts bytes")
	if err := os.WriteFile(filepath.Join(env.hls.Dir(), "seg-00001.ts"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := env.get(t, "/hls/seg-00001.ts")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "video/mp2t" {
		t.Errorf("Content-Type = %q, want video/mp2t", ct)
	}
	if rec.Body.String() != string(want) {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestHLSSegmentRejectsTraversalAndNonSegments(t *testing.T) {
	env := newTestEnv(t)
	// A file worth stealing, one directory up from the segment directory
	if err := os.WriteFile(filepath.Join(env.data, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/hls/..%2fsecret.txt",
		"/hls/..%2F..%2Fetc%2Fpasswd",
		"/hls/playlist.m3u8",
		"/hls/secret.txt",
		"/hls/%2e%2e%2fsecret.txt",
	} {
		t.Run(path, func(t *testing.T) {
			rec := env.do(t, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code == http.StatusOK {
				t.Errorf("status 200 for %q, body %q", path, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "top secret") {
				t.Errorf("traversal leaked file contents for %q", path)
			}
		})
	}
}

// The route above is also protected by chi cleaning the path before the
// handler ever runs, so those cases cannot tell whether the handler's own
// guard works. Drive the handler directly with a crafted URL param, which is
// the check that has to hold if the routing layer ever stops normalising
func TestHLSSegmentGuardRejectsCraftedParams(t *testing.T) {
	env := newTestEnv(t)
	if err := os.WriteFile(filepath.Join(env.data, "secret.txt"), []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.hls.Dir(), "real.ts"), []byte("segment"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := func(seg string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/hls/placeholder.ts", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("seg", seg)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		env.srv.handleHLSSegment(rec, req)
		return rec
	}

	for _, seg := range []string{
		"",
		"../secret.txt",
		"../../etc/passwd",
		`..\secret.txt`,
		"secret.txt",
		"playlist.m3u8",
		"sub/nested.ts",
		"../secret.txt.ts",
		"/etc/passwd",
	} {
		t.Run(seg, func(t *testing.T) {
			rec := call(seg)
			if rec.Code == http.StatusOK {
				t.Errorf("seg %q was served (status 200, body %q)", seg, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "top secret") {
				t.Errorf("seg %q leaked file contents", seg)
			}
		})
	}

	// A genuine segment name must still work through the same path
	if rec := call("real.ts"); rec.Code != http.StatusOK {
		t.Errorf("a real segment was refused: status %d", rec.Code)
	}
}

func TestSSESendsAnInitialSnapshot(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	tr, _ := env.lib.Get(env.trackID(t, "song.mp3"))
	env.sched.MarkPlaying(tr)

	srv := httptest.NewServer(env.handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/now-playing/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	buf := make([]byte, 512)
	n, err := resp.Body.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])
	if !strings.HasPrefix(got, "data: ") {
		t.Fatalf("first frame = %q, want a data: event", got)
	}
	payload := strings.TrimSpace(strings.TrimPrefix(got, "data: "))
	var np nowPlaying
	if err := json.Unmarshal([]byte(payload), &np); err != nil {
		t.Fatalf("decode SSE payload %q: %v", payload, err)
	}
	if np.Title != tr.Title {
		t.Errorf("SSE Title = %q, want %q", np.Title, tr.Title)
	}
}

func TestAdminLibraryJSON(t *testing.T) {
	env := newTestEnv(t, "b song.mp3", "a song.mp3")

	rec := env.authed(t, http.MethodGet, "/admin/library.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got []adminViewTrack
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("library has %d entries, want 2", len(got))
	}
	for _, e := range got {
		if e.Filename == "" {
			t.Error("entry is missing its filename")
		}
	}
}

func TestAdminStateJSON(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	id := env.trackID(t, "song.mp3")
	if err := env.sched.Enqueue(id); err != nil {
		t.Fatal(err)
	}

	rec := env.authed(t, http.MethodGet, "/admin/state.json", nil)
	var state adminStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(state.Queue) != 1 {
		t.Fatalf("queue has %d entries, want 1", len(state.Queue))
	}
	if state.Queue[0].ID != id {
		t.Errorf("queued id = %d, want %d", state.Queue[0].ID, id)
	}
	if state.Queue[0].Display == "" {
		t.Error("queue entry has no display line")
	}
}

func TestSetMode(t *testing.T) {
	env := newTestEnv(t, "song.mp3")

	for _, mode := range []string{"sequential", "loop", "shuffle"} {
		rec := env.authed(t, http.MethodPost, "/admin/mode", map[string][]string{"mode": {mode}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("mode=%s status = %d, want %d", mode, rec.Code, http.StatusSeeOther)
		}
		if got := string(env.sched.Mode()); got != mode {
			t.Errorf("mode = %q, want %q", got, mode)
		}
	}
}

func TestSetModeRejectsUnknownValue(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	before := env.sched.Mode()

	rec := env.authed(t, http.MethodPost, "/admin/mode", map[string][]string{"mode": {"chaos"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if env.sched.Mode() != before {
		t.Error("an invalid mode changed the scheduler state")
	}
}

func TestEnqueueAndDequeue(t *testing.T) {
	env := newTestEnv(t, "a.mp3", "b.mp3")
	idA := env.trackID(t, "a.mp3")
	idB := env.trackID(t, "b.mp3")

	for _, id := range []int64{idA, idB} {
		rec := env.authed(t, http.MethodPost, "/admin/queue", map[string][]string{
			"id": {strconv.FormatInt(id, 10)},
		})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("enqueue status = %d", rec.Code)
		}
	}
	if got := len(env.sched.Queue()); got != 2 {
		t.Fatalf("queue length = %d, want 2", got)
	}

	rec := env.authed(t, http.MethodPost, "/admin/queue/remove", map[string][]string{"idx": {"0"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("dequeue status = %d", rec.Code)
	}
	q := env.sched.Queue()
	if len(q) != 1 || q[0].ID != idB {
		t.Errorf("queue after removal = %v, want just %d", q, idB)
	}
}

func TestEnqueueRejectsBadInput(t *testing.T) {
	env := newTestEnv(t, "song.mp3")

	for _, tc := range []struct{ name, id string }{
		{"not a number", "abc"},
		{"unknown track", "999999"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.authed(t, http.MethodPost, "/admin/queue", map[string][]string{"id": {tc.id}})
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
	if got := len(env.sched.Queue()); got != 0 {
		t.Errorf("queue gained %d entries from rejected requests", got)
	}
}

func TestDequeueRejectsOutOfRangeIndex(t *testing.T) {
	env := newTestEnv(t, "song.mp3")

	for _, idx := range []string{"0", "-1", "5", "abc"} {
		rec := env.authed(t, http.MethodPost, "/admin/queue/remove", map[string][]string{"idx": {idx}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("idx=%s status = %d, want 400", idx, rec.Code)
		}
	}
}

// fetch() callers get 204 so the page does not reload, plain form posts get
// the classic redirect
func TestAdminPostsAnswer204ForJSONClients(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	cookie := env.login(t)

	for _, header := range []struct{ key, value string }{
		{"Accept", "application/json"},
		{"HX-Request", "true"},
	} {
		t.Run(header.key, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/admin/skip", nil)
			req.AddCookie(cookie)
			req.Header.Set(header.key, header.value)
			if got := env.do(t, req).Code; got != http.StatusNoContent {
				t.Errorf("status = %d, want 204", got)
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/skip", nil)
	req.AddCookie(cookie)
	if got := env.do(t, req).Code; got != http.StatusSeeOther {
		t.Errorf("plain form post status = %d, want 303", got)
	}
}

func TestRenameThroughTheAdmin(t *testing.T) {
	env := newTestEnv(t, "old.mp3")
	id := env.trackID(t, "old.mp3")

	rec := env.authed(t, http.MethodPost, "/admin/files/rename", map[string][]string{
		"id":   {strconv.FormatInt(id, 10)},
		"name": {"new.mp3"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.music, "new.mp3")); err != nil {
		t.Errorf("file was not renamed: %v", err)
	}
}

func TestRenameRejectsTraversalThroughTheAdmin(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	id := env.trackID(t, "song.mp3")

	for _, name := range []string{"../escaped.mp3", "sub/song.mp3", "-af.mp3", ".hidden.mp3"} {
		t.Run(name, func(t *testing.T) {
			rec := env.authed(t, http.MethodPost, "/admin/files/rename", map[string][]string{
				"id":   {strconv.FormatInt(id, 10)},
				"name": {name},
			})
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(env.music, "song.mp3")); err != nil {
		t.Errorf("original file was moved: %v", err)
	}
}

func TestDeleteThroughTheAdmin(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	id := env.trackID(t, "song.mp3")

	rec := env.authed(t, http.MethodPost, "/admin/files/delete", map[string][]string{
		"id": {strconv.FormatInt(id, 10)},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.music, "song.mp3")); !os.IsNotExist(err) {
		t.Errorf("file survived the delete (stat err %v)", err)
	}
}

func TestDeleteRejectsUnknownID(t *testing.T) {
	env := newTestEnv(t, "song.mp3")

	rec := env.authed(t, http.MethodPost, "/admin/files/delete", map[string][]string{"id": {"999999"}})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(env.music, "song.mp3")); err != nil {
		t.Errorf("the real file was removed: %v", err)
	}
}

func TestUpload(t *testing.T) {
	env := newTestEnv(t)
	body, contentType := multipartUpload(t, "file", "uploaded.mp3", "audio payload")

	req := httptest.NewRequest(http.MethodPost, "/admin/files/upload", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(env.login(t))

	rec := env.do(t, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(env.music, "uploaded.mp3"))
	if err != nil {
		t.Fatalf("uploaded file missing: %v", err)
	}
	if string(got) != "audio payload" {
		t.Errorf("contents = %q", got)
	}
}

func TestUploadRejectsUnsupportedAndUnsafeNames(t *testing.T) {
	env := newTestEnv(t)
	cookie := env.login(t)

	for _, name := range []string{"notaudio.txt", "-af.mp3", ".hidden.mp3", "noextension"} {
		t.Run(name, func(t *testing.T) {
			body, contentType := multipartUpload(t, "file", name, "payload")
			req := httptest.NewRequest(http.MethodPost, "/admin/files/upload", strings.NewReader(body))
			req.Header.Set("Content-Type", contentType)
			req.AddCookie(cookie)

			if got := env.do(t, req).Code; got != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", got)
			}
		})
	}

	entries, err := os.ReadDir(env.music)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("music dir gained %d entries from rejected uploads", len(entries))
	}
}

// mime/multipart reduces a part's filename to its base, so a traversal
// attempt arrives at the handler already stripped and is stored as an
// ordinary name. Pinning that down because the guard lives in the standard
// library rather than in this package, and the upload path would be wide
// open if it ever stopped applying
func TestUploadNeutralisesTraversalInTheFilename(t *testing.T) {
	env := newTestEnv(t)
	cookie := env.login(t)
	parent := filepath.Dir(env.music)

	cases := []struct{ sent, stored string }{
		{"../escape.mp3", "escape.mp3"},
		{"../../deep.mp3", "deep.mp3"},
		{"/etc/evil.mp3", "evil.mp3"},
		{`..\windows.mp3`, `..\windows.mp3`},
	}
	for _, tc := range cases {
		t.Run(tc.sent, func(t *testing.T) {
			body, contentType := multipartUpload(t, "file", tc.sent, "payload")
			req := httptest.NewRequest(http.MethodPost, "/admin/files/upload", strings.NewReader(body))
			req.Header.Set("Content-Type", contentType)
			req.AddCookie(cookie)
			rec := env.do(t, req)

			// Either it is refused outright or it lands inside the music root
			// under a safe name. What must never happen is a write outside
			if rec.Code == http.StatusSeeOther {
				if _, err := os.Stat(filepath.Join(env.music, tc.stored)); err != nil {
					t.Errorf("accepted upload but %q is not in the music dir: %v", tc.stored, err)
				}
			}
		})
	}

	// Nothing may have been written next to the music directory
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".mp3") {
			t.Errorf("an upload escaped the music root: %q", filepath.Join(parent, e.Name()))
		}
	}
}

func TestUploadRejectsMalformedRequest(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/files/upload", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=nope")
	req.AddCookie(env.login(t))

	if got := env.do(t, req).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got)
	}
}

func TestUploadRejectsMissingFileField(t *testing.T) {
	env := newTestEnv(t)
	body, contentType := multipartUpload(t, "wrongfield", "song.mp3", "payload")

	req := httptest.NewRequest(http.MethodPost, "/admin/files/upload", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(env.login(t))

	if got := env.do(t, req).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got)
	}
}

func TestLoginPageRenders(t *testing.T) {
	env := newTestEnv(t)
	rec := env.get(t, "/admin/login")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Test Station") {
		t.Error("login page does not show the station name")
	}
	if !strings.Contains(body, "password") {
		t.Error("login page has no password field")
	}
}

func TestPublicHomeRenders(t *testing.T) {
	env := newTestEnv(t)
	rec := env.get(t, "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Test Station") {
		t.Error("public page does not show the station name")
	}
	for _, want := range []string{"/stream.pls", "/stream.m3u", "/hls.m3u8"} {
		if !strings.Contains(body, want) {
			t.Errorf("public page is missing the %s link", want)
		}
	}
}

func TestAdminHomeRenders(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	rec := env.authed(t, http.MethodGet, "/admin/", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Test Station") {
		t.Error("admin page does not show the station name")
	}
	if !strings.Contains(body, "v0.0.0-test") {
		t.Error("admin page does not show the build version")
	}
}

// Track titles come from user-controlled filenames and are rendered into the
// admin and public pages
func TestTemplatesEscapeTrackTitles(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	tr, _ := env.lib.Get(env.trackID(t, "song.mp3"))
	tr.Title = `<script>alert('xss')</script>`
	tr.Artist = `" onerror="alert(1)`
	env.sched.MarkPlaying(tr)

	for _, path := range []string{"/", "/admin/"} {
		t.Run(path, func(t *testing.T) {
			var body string
			if strings.HasPrefix(path, "/admin") {
				body = env.authed(t, http.MethodGet, path, nil).Body.String()
			} else {
				body = env.get(t, path).Body.String()
			}
			if strings.Contains(body, "<script>alert('xss')</script>") {
				t.Error("an unescaped script tag reached the rendered page")
			}
		})
	}
}

func TestJSONEndpointsEscapeTrackTitles(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	tr, _ := env.lib.Get(env.trackID(t, "song.mp3"))
	tr.Title = `</script><script>alert(1)</script>`
	env.sched.MarkPlaying(tr)

	body := env.get(t, "/now-playing").Body.String()
	var np nowPlaying
	if err := json.Unmarshal([]byte(body), &np); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if np.Title != tr.Title {
		t.Errorf("Title round-trip = %q, want %q", np.Title, tr.Title)
	}
}

func TestIsHTMXAndAcceptsJSON(t *testing.T) {
	cases := []struct {
		name       string
		key, value string
		htmx, json bool
	}{
		{"plain request", "", "", false, false},
		{"htmx", "HX-Request", "true", true, false},
		{"htmx false", "HX-Request", "false", false, false},
		{"json accept", "Accept", "application/json", false, true},
		{"browser accept", "Accept", "text/html,application/xhtml+xml", false, false},
		{"mixed accept", "Accept", "application/json, text/plain", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.key != "" {
				r.Header.Set(tc.key, tc.value)
			}
			if got := isHTMX(r); got != tc.htmx {
				t.Errorf("isHTMX = %v, want %v", got, tc.htmx)
			}
			if got := acceptsJSON(r); got != tc.json {
				t.Errorf("acceptsJSON = %v, want %v", got, tc.json)
			}
		})
	}
}

func TestDisplayTitleTemplateFunc(t *testing.T) {
	tmpl := MustLoadTemplates()
	if tmpl == nil {
		t.Fatal("MustLoadTemplates returned nil")
	}
	// All three templates must be present, or a handler 500s at runtime
	for _, name := range []string{"admin.html", "login.html", "public.html"} {
		if tmpl.tmpl.Lookup(name) == nil {
			t.Errorf("template %q was not parsed", name)
		}
	}
}

func TestStaticFilesAreEmbedded(t *testing.T) {
	for _, name := range []string{"app.css", "app.js", "utils.js", "icon.svg"} {
		if len(staticFiles[name]) == 0 {
			t.Errorf("static asset %q is missing or empty in the embedded set", name)
		}
	}
}

// multipartUpload builds a multipart body with a single file part
func multipartUpload(t *testing.T, field, filename, content string) (body, contentType string) {
	t.Helper()
	var sb strings.Builder
	w := multipart.NewWriter(&sb)
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return sb.String(), w.FormDataContentType()
}

// The templates used to pull Tailwind from its Play CDN, which compiles CSS in
// the browser and is documented as development-only. It also meant the admin
// page could not render without internet access. The stylesheet is generated
// and committed instead, so these guard the ways that can silently regress
func TestTemplatesDoNotUseTheTailwindCDN(t *testing.T) {
	for _, name := range []string{"admin.html", "login.html", "public.html"} {
		body, err := templatesFS.ReadFile("templates/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(body), "cdn.tailwindcss.com") {
			t.Errorf("%s loads Tailwind from the CDN, which compiles in the browser and needs internet", name)
		}
		if !strings.Contains(string(body), "/static/tailwind.css") {
			t.Errorf("%s does not link the generated stylesheet, so it will render unstyled", name)
		}
	}
}

func TestGeneratedStylesheetIsEmbedded(t *testing.T) {
	css, ok := staticFiles["tailwind.css"]
	if !ok {
		t.Fatal("tailwind.css is missing from the embedded static set")
	}
	if len(css) < 5000 {
		t.Errorf("tailwind.css is only %d bytes, which suggests a failed or empty build", len(css))
	}

	// Utilities the layout depends on, including an arbitrary value and a data
	// variant, since those are the ones a scanner misconfiguration drops first
	for _, want := range []string{
		".bg-neutral-950",
		".backdrop-blur",
		".animate-ping",
		"text-\\[11px\\]",
		"data-\\[active\\=true\\]",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("generated stylesheet is missing %q, regenerate it per CLAUDE.md", want)
		}
	}
}

func TestStylesheetIsServedOverHTTP(t *testing.T) {
	env := newTestEnv(t)
	rec := env.get(t, "/static/tailwind.css")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestLyricsNotFoundWhenNoneCached(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	id := env.trackID(t, "song.mp3")

	for _, path := range []string{
		"/lyrics/999999",
		"/lyrics/" + strconv.FormatInt(id, 10), // indexed but has_lyrics is false
		"/lyrics/notanumber",
	} {
		t.Run(path, func(t *testing.T) {
			if got := env.get(t, path).Code; got != http.StatusNotFound {
				t.Errorf("status = %d, want 404", got)
			}
		})
	}
}

func TestLyricsServedFromCache(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	id := env.trackID(t, "song.mp3")
	tr, _ := env.lib.Get(id)
	tr.HasLyrics = true

	body := "[00:12.30] first line\n[00:15.00] second line\n"
	dir := filepath.Join(env.data, "lyrics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(id, 10)+".lrc"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := env.get(t, "/lyrics/"+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != body {
		t.Errorf("body = %q, want %q", rec.Body.String(), body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	// A file replaced at the same path keeps its id and gets fresh lyrics at
	// the same URL, so this must revalidate rather than serve the previous
	// song's words for a day
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "must-revalidate") {
		t.Errorf("Cache-Control = %q, want must-revalidate so replaced tracks propagate", cc)
	}
}

// The frontend decides whether to offer the button from these fields, so a
// track with no lyrics must not advertise a URL
func TestNowPlayingAdvertisesLyricsOnlyWhenPresent(t *testing.T) {
	env := newTestEnv(t, "song.mp3")
	tr, _ := env.lib.Get(env.trackID(t, "song.mp3"))
	env.sched.MarkPlaying(tr)

	var np nowPlaying
	if err := json.Unmarshal(env.get(t, "/now-playing").Body.Bytes(), &np); err != nil {
		t.Fatal(err)
	}
	if np.HasLyrics {
		t.Error("HasLyrics is set for a track with no cached lyrics")
	}
	if np.LyricsURL != "" {
		t.Errorf("LyricsURL = %q, want empty", np.LyricsURL)
	}

	tr.HasLyrics = true
	if err := json.Unmarshal(env.get(t, "/now-playing").Body.Bytes(), &np); err != nil {
		t.Fatal(err)
	}
	if !np.HasLyrics {
		t.Error("HasLyrics not set for a track that has lyrics")
	}
	if np.LyricsURL != "/lyrics/"+strconv.FormatInt(tr.ID, 10) {
		t.Errorf("LyricsURL = %q", np.LyricsURL)
	}
}

// The stylesheet is generated and shipped with the binary now, so a stale
// cached copy renders new markup with no rules at all. Static URLs carry the
// build version so an upgrade busts the cache immediately
func TestStaticAssetsAreVersionStamped(t *testing.T) {
	env := newTestEnv(t)

	pages := map[string]func() string{
		"/":            func() string { return env.get(t, "/").Body.String() },
		"/admin/login": func() string { return env.get(t, "/admin/login").Body.String() },
		"/admin/":      func() string { return env.authed(t, http.MethodGet, "/admin/", nil).Body.String() },
	}
	for path, body := range pages {
		t.Run(path, func(t *testing.T) {
			html := body()
			for _, asset := range []string{"/static/tailwind.css", "/static/app.css"} {
				if !strings.Contains(html, asset) {
					continue // not every page links every asset
				}
				want := asset + "?v=" + env.cfg.Version
				if !strings.Contains(html, want) {
					t.Errorf("%s links %s without the version stamp, so an upgrade serves stale CSS", path, asset)
				}
			}
			if strings.Contains(html, `.css"`) && !strings.Contains(html, "?v=") {
				t.Errorf("%s has an unstamped stylesheet link", path)
			}
		})
	}
}
