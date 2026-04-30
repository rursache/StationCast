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

// safe resolves a user-supplied path under the music root, rejecting traversal.
func (m *Manager) safe(p string) (string, error) {
	clean := filepath.Clean("/" + p)
	full := filepath.Join(m.cfg.MusicDir, clean)
	rel, err := filepath.Rel(m.cfg.MusicDir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("invalid path")
	}
	return full, nil
}

func (m *Manager) Rename(id int64, newName string) error {
	t, ok := m.lib.Get(id)
	if !ok {
		return errors.New("track not found")
	}
	if newName == "" || strings.ContainsAny(newName, "/\\") {
		return errors.New("invalid filename")
	}
	dir := filepath.Dir(t.Path)
	newPath := filepath.Join(dir, newName)
	if filepath.Ext(newPath) == "" {
		newPath += filepath.Ext(t.Path)
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

func (m *Manager) Save(name string, r io.Reader) error {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return errors.New("invalid filename")
	}
	dst := filepath.Join(m.cfg.MusicDir, name)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("file already exists: %s", name)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}
