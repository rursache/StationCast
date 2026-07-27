package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/playlist"
	"github.com/rursache/StationCast/internal/storage"
)

// newTestManager builds a Manager over a scanned temp music directory. Files
// are created before the scan so library lookups by id resolve
func newTestManager(t *testing.T, names ...string) (*Manager, *playlist.Library, string) {
	t.Helper()
	music := t.TempDir()
	data := t.TempDir()
	for _, n := range names {
		p := filepath.Join(music, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("not really audio"), 0o644); err != nil {
			t.Fatalf("write %q: %v", n, err)
		}
	}

	cfg := &config.Config{MusicDir: music, DataDir: data}
	if err := os.MkdirAll(filepath.Join(data, "art"), 0o755); err != nil {
		t.Fatal(err)
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
	return NewManager(cfg, lib), lib, music
}

func trackID(t *testing.T, lib *playlist.Library, music, name string) int64 {
	t.Helper()
	tr, ok := lib.GetByPath(filepath.Join(music, name))
	if !ok {
		t.Fatalf("track %q was not indexed", name)
	}
	return tr.ID
}

func TestValidateUserFilename(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"ordinary", "song.mp3", false},
		{"spaces", "my great song.mp3", false},
		{"unicode", "café 日本語 🎵.mp3", false},
		{"dash inside", "a-b.mp3", false},
		{"dot inside", "a.b.mp3", false},
		{"empty", "", true},
		{"forward slash", "sub/song.mp3", true},
		{"backslash", `sub\song.mp3`, true},
		{"traversal", "../song.mp3", true},
		{"absolute", "/etc/passwd", true},
		{"nul byte", "song\x00.mp3", true},
		{"leading dash", "-af.mp3", true},
		{"leading dot", ".hidden.mp3", true},
		{"parent dir", "..", true},
		{"current dir", ".", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUserFilename(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("validateUserFilename(%q) = nil, want an error", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateUserFilename(%q) = %v, want nil", tc.input, err)
			}
		})
	}
}

func TestRenameMovesTheFile(t *testing.T) {
	m, lib, music := newTestManager(t, "old.mp3")
	id := trackID(t, lib, music, "old.mp3")

	if err := m.Rename(id, "new.mp3"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(music, "new.mp3")); err != nil {
		t.Errorf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(music, "old.mp3")); !os.IsNotExist(err) {
		t.Errorf("original still present (stat err %v)", err)
	}
}

// A name with no extension inherits the original one, so a user renaming
// "track01" does not end up with a file the scanner ignores
func TestRenameKeepsExtensionWhenOmitted(t *testing.T) {
	m, lib, music := newTestManager(t, "old.flac")
	id := trackID(t, lib, music, "old.flac")

	if err := m.Rename(id, "renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(music, "renamed.flac")); err != nil {
		t.Errorf("expected renamed.flac: %v", err)
	}
}

func TestRenameRejectsUnsupportedExtension(t *testing.T) {
	m, lib, music := newTestManager(t, "song.mp3")
	id := trackID(t, lib, music, "song.mp3")

	if err := m.Rename(id, "song.txt"); err == nil {
		t.Fatal("Rename to an unsupported extension was accepted")
	}
	if _, err := os.Stat(filepath.Join(music, "song.mp3")); err != nil {
		t.Errorf("original was moved despite the rejection: %v", err)
	}
}

func TestRenameRejectsPathSeparators(t *testing.T) {
	m, lib, music := newTestManager(t, "song.mp3")
	id := trackID(t, lib, music, "song.mp3")

	for _, bad := range []string{"../escaped.mp3", "sub/song.mp3", `sub\song.mp3`, "/tmp/song.mp3"} {
		t.Run(bad, func(t *testing.T) {
			if err := m.Rename(id, bad); err == nil {
				t.Errorf("Rename to %q was accepted", bad)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(music, "song.mp3")); err != nil {
		t.Errorf("original was moved: %v", err)
	}
}

// A leading dash could be read as an option flag by ffmpeg or any other tool
// handed the path
func TestRenameRejectsLeadingDash(t *testing.T) {
	m, lib, music := newTestManager(t, "song.mp3")
	id := trackID(t, lib, music, "song.mp3")

	if err := m.Rename(id, "-af volume=10.mp3"); err == nil {
		t.Fatal("Rename to a name starting with - was accepted")
	}
}

func TestRenameRejectsExistingTarget(t *testing.T) {
	m, lib, music := newTestManager(t, "a.mp3", "b.mp3")
	id := trackID(t, lib, music, "a.mp3")

	if err := m.Rename(id, "b.mp3"); err == nil {
		t.Fatal("Rename over an existing file was accepted")
	}
	// Both must survive
	for _, n := range []string{"a.mp3", "b.mp3"} {
		if _, err := os.Stat(filepath.Join(music, n)); err != nil {
			t.Errorf("%s missing after the rejected rename: %v", n, err)
		}
	}
}

func TestRenameUnknownTrack(t *testing.T) {
	m, _, _ := newTestManager(t, "song.mp3")
	if err := m.Rename(99999, "new.mp3"); err == nil {
		t.Fatal("Rename of an unknown id was accepted")
	}
}

func TestRenamePreservesFileContents(t *testing.T) {
	m, lib, music := newTestManager(t, "song.mp3")
	id := trackID(t, lib, music, "song.mp3")
	want := []byte("distinctive bytes")
	if err := os.WriteFile(filepath.Join(music, "song.mp3"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := m.Rename(id, "moved.mp3"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(music, "moved.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

func TestDeleteRemovesTheFile(t *testing.T) {
	m, lib, music := newTestManager(t, "song.mp3")
	id := trackID(t, lib, music, "song.mp3")

	if err := m.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(music, "song.mp3")); !os.IsNotExist(err) {
		t.Errorf("file survived Delete (stat err %v)", err)
	}
}

func TestDeleteUnknownTrack(t *testing.T) {
	m, _, _ := newTestManager(t, "song.mp3")
	if err := m.Delete(99999); err == nil {
		t.Fatal("Delete of an unknown id was accepted")
	}
}

func TestDeleteLeavesOtherFilesAlone(t *testing.T) {
	m, lib, music := newTestManager(t, "a.mp3", "b.mp3", "c.mp3")
	id := trackID(t, lib, music, "b.mp3")

	if err := m.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, n := range []string{"a.mp3", "c.mp3"} {
		if _, err := os.Stat(filepath.Join(music, n)); err != nil {
			t.Errorf("%s was removed too: %v", n, err)
		}
	}
}

func TestSaveWritesUpload(t *testing.T) {
	m, _, music := newTestManager(t)
	body := strings.NewReader("uploaded audio bytes")

	if err := m.Save("upload.mp3", body); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(music, "upload.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "uploaded audio bytes" {
		t.Errorf("saved contents = %q", got)
	}
}

func TestSaveRejectsBadNames(t *testing.T) {
	m, _, music := newTestManager(t)

	for _, bad := range []string{
		"",
		"../escape.mp3",
		"sub/song.mp3",
		`sub\song.mp3`,
		"-af.mp3",
		".hidden.mp3",
		"song\x00.mp3",
		"notaudio.txt",
		"noextension",
	} {
		t.Run(bad, func(t *testing.T) {
			if err := m.Save(bad, strings.NewReader("x")); err == nil {
				t.Errorf("Save(%q) was accepted", bad)
			}
		})
	}

	entries, err := os.ReadDir(music)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("music dir gained %d entries from rejected uploads", len(entries))
	}
}

func TestSaveRejectsExistingFile(t *testing.T) {
	m, _, music := newTestManager(t, "taken.mp3")

	if err := m.Save("taken.mp3", strings.NewReader("replacement")); err == nil {
		t.Fatal("Save over an existing file was accepted")
	}
	got, err := os.ReadFile(filepath.Join(music, "taken.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == "replacement" {
		t.Error("existing file was overwritten")
	}
}

func TestSaveAcceptsEverySupportedExtension(t *testing.T) {
	m, _, music := newTestManager(t)

	for _, ext := range []string{".mp3", ".wav", ".flac", ".ogg", ".oga", ".m4a", ".aac"} {
		name := "track" + ext
		if err := m.Save(name, strings.NewReader("audio")); err != nil {
			t.Errorf("Save(%q): %v", name, err)
			continue
		}
		if _, err := os.Stat(filepath.Join(music, name)); err != nil {
			t.Errorf("%s missing after Save: %v", name, err)
		}
	}
}

func TestSaveIsCaseInsensitiveAboutExtension(t *testing.T) {
	m, _, _ := newTestManager(t)
	if err := m.Save("SHOUTING.MP3", strings.NewReader("audio")); err != nil {
		t.Errorf("Save with an uppercase extension: %v", err)
	}
}

// The HTTP layer caps the body too, but Save must not depend on that
func TestSaveRejectsOversizeUpload(t *testing.T) {
	m, _, music := newTestManager(t)

	// A reader that yields more than the cap without allocating it
	oversize := io.LimitReader(zeroes{}, MaxUploadBytes+1024)
	err := m.Save("huge.mp3", oversize)
	if err == nil {
		t.Fatal("Save accepted an upload over the size limit")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error %q does not mention the limit", err)
	}
	if _, statErr := os.Stat(filepath.Join(music, "huge.mp3")); !os.IsNotExist(statErr) {
		t.Errorf("oversize upload left a partial file behind (stat err %v)", statErr)
	}
}

func TestSaveAtExactlyTheSizeLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates the full upload limit")
	}
	m, _, music := newTestManager(t)

	atLimit := io.LimitReader(zeroes{}, MaxUploadBytes)
	if err := m.Save("exact.mp3", atLimit); err != nil {
		t.Fatalf("Save at exactly the limit was rejected: %v", err)
	}
	st, err := os.Stat(filepath.Join(music, "exact.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != MaxUploadBytes {
		t.Errorf("saved %d bytes, want %d", st.Size(), MaxUploadBytes)
	}
}

// A read failure partway through must not leave a truncated file that the
// scanner would index as a real track
func TestSaveCleansUpAfterAReadError(t *testing.T) {
	m, _, music := newTestManager(t)

	failing := io.MultiReader(strings.NewReader("some bytes"), errReader{errors.New("network died")})
	if err := m.Save("broken.mp3", failing); err == nil {
		t.Fatal("Save returned nil despite the source failing")
	}
	if _, err := os.Stat(filepath.Join(music, "broken.mp3")); !os.IsNotExist(err) {
		t.Errorf("partial file left behind (stat err %v)", err)
	}
}

func TestSaveEmptyUpload(t *testing.T) {
	m, _, music := newTestManager(t)

	if err := m.Save("empty.mp3", strings.NewReader("")); err != nil {
		t.Fatalf("Save of an empty body: %v", err)
	}
	st, err := os.Stat(filepath.Join(music, "empty.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 0 {
		t.Errorf("size = %d, want 0", st.Size())
	}
}

func TestSaveUnicodeFilename(t *testing.T) {
	m, _, music := newTestManager(t)
	name := "Zoë's Über-mix 🎵.mp3"

	if err := m.Save(name, strings.NewReader("audio")); err != nil {
		t.Fatalf("Save(%q): %v", name, err)
	}
	if _, err := os.Stat(filepath.Join(music, name)); err != nil {
		t.Errorf("unicode filename did not round-trip: %v", err)
	}
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
