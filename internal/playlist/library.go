package playlist

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dhowden/tag"
	"github.com/fsnotify/fsnotify"
	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/storage"
)

var supportedExt = map[string]bool{
	".mp3":  true,
	".wav":  true,
	".flac": true,
	".ogg":  true,
	".oga":  true,
	".m4a":  true,
	".aac":  true,
}

// IsSupportedExt reports whether the given filename has a supported audio
// extension (matched case-insensitively against the library scanner's set)
func IsSupportedExt(name string) bool {
	return supportedExt[strings.ToLower(filepath.Ext(name))]
}

type Library struct {
	cfg *config.Config
	db  *storage.DB

	mu     sync.RWMutex
	byPath map[string]*Track
	byID   map[int64]*Track
}

func NewLibrary(cfg *config.Config, db *storage.DB) *Library {
	return &Library{
		cfg:    cfg,
		db:     db,
		byPath: map[string]*Track{},
		byID:   map[int64]*Track{},
	}
}

func (l *Library) Snapshot() []*Track {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*Track, 0, len(l.byPath))
	for _, t := range l.byPath {
		out = append(out, t)
	}
	return out
}

func (l *Library) Get(id int64) (*Track, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.byID[id]
	return t, ok
}

func (l *Library) GetByPath(p string) (*Track, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.byPath[p]
	return t, ok
}

func (l *Library) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.byPath)
}

func (l *Library) InitialScan(ctx context.Context) error {
	if err := l.loadFromDB(); err != nil {
		return fmt.Errorf("load from db: %w", err)
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(l.cfg.MusicDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("walk", "path", path, "err", err)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		if !supportedExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		seen[path] = true
		if err := l.upsertFile(path); err != nil {
			slog.Warn("upsert", "path", path, "err", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	l.reconcileRemoved(seen)
	slog.Info("library scan complete", "tracks", l.Count())
	return nil
}

func (l *Library) loadFromDB() error {
	rows, err := l.db.Query(`SELECT id, path, size, mtime, COALESCE(title,''), COALESCE(artist,''), COALESCE(album,''), COALESCE(duration_ms,0), has_art, added_at FROM tracks`)
	if err != nil {
		return err
	}
	defer rows.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	for rows.Next() {
		t := &Track{}
		var hasArt int64
		if err := rows.Scan(&t.ID, &t.Path, &t.Size, &t.MTime, &t.Title, &t.Artist, &t.Album, &t.DurationMS, &hasArt, &t.AddedAt); err != nil {
			return err
		}
		t.HasArt = hasArt == 1
		l.byPath[t.Path] = t
		l.byID[t.ID] = t
	}
	return rows.Err()
}

func (l *Library) reconcileRemoved(seen map[string]bool) {
	l.mu.Lock()
	gone := []*Track{}
	for path, t := range l.byPath {
		if !seen[path] {
			gone = append(gone, t)
		}
	}
	l.mu.Unlock()
	for _, t := range gone {
		l.removeTrack(t)
	}
}

func (l *Library) upsertFile(path string) error {
	// Reject symlinks outright. Docker bind mounts are not symlinks so this
	// only affects user-created links inside MUSIC_DIR. Also verify the path
	// is still inside the (already symlink-resolved) music root so a symlinked
	// directory deeper in the tree cannot escape
	li, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return errors.New("symlinks are not allowed in the music directory")
	}
	rel, err := filepath.Rel(l.cfg.MusicDir, path)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return errors.New("path outside music root")
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	size := st.Size()
	mt := st.ModTime().Unix()

	l.mu.RLock()
	existing, has := l.byPath[path]
	l.mu.RUnlock()
	if has && existing.Size == size && existing.MTime == mt {
		return nil
	}

	title, artist, album, dur, hasArt := readTags(path)

	now := time.Now().Unix()
	if has {
		_, err := l.db.Exec(`UPDATE tracks SET size=?, mtime=?, title=?, artist=?, album=?, duration_ms=?, has_art=? WHERE id=?`,
			size, mt, nullStr(title), nullStr(artist), nullStr(album), dur, boolInt(hasArt), existing.ID)
		if err != nil {
			return err
		}
		existing.Size = size
		existing.MTime = mt
		existing.Title = title
		existing.Artist = artist
		existing.Album = album
		existing.DurationMS = dur
		existing.HasArt = hasArt
		if hasArt {
			_ = saveArt(l.cfg.DataDir, existing.ID, path)
		}
		return nil
	}

	res, err := l.db.Exec(`INSERT INTO tracks(path, size, mtime, title, artist, album, duration_ms, has_art, added_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		path, size, mt, nullStr(title), nullStr(artist), nullStr(album), dur, boolInt(hasArt), now)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	t := &Track{ID: id, Path: path, Size: size, MTime: mt, Title: title, Artist: artist, Album: album, DurationMS: dur, HasArt: hasArt, AddedAt: now}
	l.mu.Lock()
	l.byPath[path] = t
	l.byID[id] = t
	l.mu.Unlock()
	if hasArt {
		_ = saveArt(l.cfg.DataDir, id, path)
	}
	return nil
}

func (l *Library) removeTrack(t *Track) {
	_, _ = l.db.Exec(`DELETE FROM tracks WHERE id = ?`, t.ID)
	_, _ = l.db.Exec(`DELETE FROM queue WHERE track_id = ?`, t.ID)
	l.mu.Lock()
	delete(l.byPath, t.Path)
	delete(l.byID, t.ID)
	l.mu.Unlock()
	_ = os.Remove(filepath.Join(l.cfg.DataDir, "art", fmt.Sprintf("%d.jpg", t.ID)))
}

func (l *Library) Watch(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("fsnotify", "err", err)
		return
	}
	defer w.Close()

	addRecursive := func(root string) {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				_ = w.Add(path)
			}
			return nil
		})
	}
	addRecursive(l.cfg.MusicDir)

	debounce := map[string]*time.Timer{}
	var dmu sync.Mutex
	schedule := func(path string, fn func()) {
		dmu.Lock()
		defer dmu.Unlock()
		if t, ok := debounce[path]; ok {
			t.Stop()
		}
		debounce[path] = time.AfterFunc(500*time.Millisecond, fn)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			path := ev.Name
			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(path); err == nil && st.IsDir() {
					addRecursive(path)
					continue
				}
			}
			if !supportedExt[strings.ToLower(filepath.Ext(path))] {
				continue
			}
			schedule(path, func() {
				if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
					if t, ok := l.GetByPath(path); ok {
						l.removeTrack(t)
						slog.Info("library remove", "path", path)
					}
					return
				}
				if err := l.upsertFile(path); err != nil {
					slog.Warn("library upsert", "path", path, "err", err)
				} else {
					slog.Info("library upsert", "path", path)
				}
			})
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			slog.Warn("fsnotify err", "err", err)
		}
	}
}

func readTags(path string) (title, artist, album string, durationMS int64, hasArt bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", 0, false
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		base := filepath.Base(path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
		return title, "", "", 0, false
	}
	title = m.Title()
	if title == "" {
		base := filepath.Base(path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	artist = m.Artist()
	album = m.Album()
	if pic := m.Picture(); pic != nil && len(pic.Data) > 0 {
		hasArt = true
	}
	return title, artist, album, 0, hasArt
}

func saveArt(dataDir string, id int64, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return err
	}
	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return errors.New("no embedded art")
	}
	out := filepath.Join(dataDir, "art", fmt.Sprintf("%d.jpg", id))
	return os.WriteFile(out, pic.Data, 0o644)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
