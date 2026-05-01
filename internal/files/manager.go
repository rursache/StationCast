package files

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/playlist"
)

type Manager struct {
	cfg *config.Config
	lib *playlist.Library
}

func NewManager(cfg *config.Config, lib *playlist.Library) *Manager {
	return &Manager{cfg: cfg, lib: lib}
}

func (m *Manager) Rename(id int64, newName string) error {
	t, ok := m.lib.Get(id)
	if !ok {
		return errors.New("track not found")
	}
	if err := validateUserFilename(newName); err != nil {
		return err
	}
	dir := filepath.Dir(t.Path)
	newPath := filepath.Join(dir, newName)
	if filepath.Ext(newPath) == "" {
		newPath += filepath.Ext(t.Path)
	}
	if !playlist.IsSupportedExt(newPath) {
		return errors.New("unsupported file extension")
	}
	rel, err := filepath.Rel(m.cfg.MusicDir, newPath)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return errors.New("invalid path")
	}
	if _, err := os.Stat(newPath); err == nil {
		return errors.New("target already exists")
	}
	return os.Rename(t.Path, newPath)
}

func (m *Manager) Delete(id int64) error {
	t, ok := m.lib.Get(id)
	if !ok {
		return errors.New("track not found")
	}
	return os.Remove(t.Path)
}

// MaxUploadBytes caps the size of a single uploaded audio file
const MaxUploadBytes int64 = 250 << 20

func (m *Manager) Save(name string, r io.Reader) error {
	if err := validateUserFilename(name); err != nil {
		return err
	}
	if !playlist.IsSupportedExt(name) {
		return errors.New("unsupported file extension")
	}
	dst := filepath.Join(m.cfg.MusicDir, name)
	rel, err := filepath.Rel(m.cfg.MusicDir, dst)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return errors.New("invalid path")
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("file already exists: %s", name)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	// Cap copy size as defence-in-depth - the HTTP layer also wraps the
	// request body with MaxBytesReader
	if _, err := io.Copy(f, io.LimitReader(r, MaxUploadBytes+1)); err != nil {
		_ = os.Remove(dst)
		return err
	}
	if st, err := f.Stat(); err == nil && st.Size() > MaxUploadBytes {
		_ = os.Remove(dst)
		return fmt.Errorf("file exceeds %d byte limit", MaxUploadBytes)
	}
	return nil
}

// validateUserFilename enforces the rules common to upload + rename:
// non-empty, no path separators, no leading dash (which ffmpeg or other
// argv-parsing tools may misinterpret), no NUL bytes, no leading dot
func validateUserFilename(name string) error {
	if name == "" {
		return errors.New("empty filename")
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return errors.New("invalid filename")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("filename must not start with -")
	}
	if strings.HasPrefix(name, ".") {
		return errors.New("filename must not start with .")
	}
	return nil
}
