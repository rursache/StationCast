package playlist

import (
	"errors"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rursache/StationCast/internal/config"
	"github.com/rursache/StationCast/internal/storage"
)

type Mode string

const (
	ModeShuffle    Mode = "shuffle"
	ModeSequential Mode = "sequential"
	ModeLoop       Mode = "loop"
)

const (
	settingMode    = "mode"
	settingCurrent = "current_track_id"
	historyWindow  = 50
)

type Scheduler struct {
	cfg *config.Config
	db  *storage.DB
	lib *Library

	mu       sync.Mutex
	mode     Mode
	current  *Track
	manual   []int64
	recent   []int64
	skipNext bool

	// Deck shuffle state. The deck is a freshly-shuffled list of track ids
	// drawn one at a time. When deckPos catches the tail, the deck is
	// rebuilt from the current library snapshot. State is in-memory only,
	// so a restart yields a fresh deck. Library mutations during a deck
	// cycle are absorbed transparently: deleted ids are skipped, newly
	// added tracks join only on the next reshuffle. Manual queue plays do
	// not advance deckPos
	deck    []int64
	deckPos int
}

func NewScheduler(cfg *config.Config, db *storage.DB, lib *Library) *Scheduler {
	return &Scheduler{
		cfg:  cfg,
		db:   db,
		lib:  lib,
		mode: ModeShuffle,
	}
}

func (s *Scheduler) Restore() error {
	if v, _ := s.db.GetSetting(settingMode); v != "" {
		s.mode = Mode(v)
	}
	rows, err := s.db.Query(`SELECT track_id FROM queue ORDER BY position ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		s.manual = append(s.manual, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	hrows, err := s.db.Query(`SELECT track_id FROM history ORDER BY played_at DESC LIMIT ?`, historyWindow)
	if err != nil {
		return err
	}
	defer hrows.Close()
	for hrows.Next() {
		var id int64
		if err := hrows.Scan(&id); err != nil {
			return err
		}
		s.recent = append(s.recent, id)
	}
	return hrows.Err()
}

func (s *Scheduler) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func (s *Scheduler) SetMode(m Mode) error {
	switch m {
	case ModeShuffle, ModeSequential, ModeLoop:
	default:
		return errors.New("invalid mode")
	}
	s.mu.Lock()
	s.mode = m
	s.mu.Unlock()
	return s.db.SetSetting(settingMode, string(m))
}

func (s *Scheduler) Current() *Track {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *Scheduler) Queue() []*Track {
	s.mu.Lock()
	ids := append([]int64(nil), s.manual...)
	s.mu.Unlock()
	out := make([]*Track, 0, len(ids))
	for _, id := range ids {
		if t, ok := s.lib.Get(id); ok {
			out = append(out, t)
		}
	}
	return out
}

func (s *Scheduler) History() []*Track {
	s.mu.Lock()
	ids := append([]int64(nil), s.recent...)
	s.mu.Unlock()
	out := make([]*Track, 0, len(ids))
	for _, id := range ids {
		if t, ok := s.lib.Get(id); ok {
			out = append(out, t)
		}
	}
	return out
}

func (s *Scheduler) Enqueue(id int64) error {
	if _, ok := s.lib.Get(id); !ok {
		return errors.New("track not found")
	}
	s.mu.Lock()
	s.manual = append(s.manual, id)
	pos := len(s.manual)
	s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO queue(track_id, position) VALUES(?, ?)`, id, pos)
	return err
}

func (s *Scheduler) Dequeue(idx int) error {
	s.mu.Lock()
	if idx < 0 || idx >= len(s.manual) {
		s.mu.Unlock()
		return errors.New("index out of range")
	}
	s.manual = append(s.manual[:idx], s.manual[idx+1:]...)
	snapshot := append([]int64(nil), s.manual...)
	s.mu.Unlock()
	return s.replaceQueue(snapshot)
}

func (s *Scheduler) Reorder(ids []int64) error {
	s.mu.Lock()
	s.manual = append([]int64(nil), ids...)
	snapshot := append([]int64(nil), s.manual...)
	s.mu.Unlock()
	return s.replaceQueue(snapshot)
}

func (s *Scheduler) replaceQueue(ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM queue`); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`INSERT INTO queue(track_id, position) VALUES(?, ?)`, id, i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Scheduler) Skip() {
	s.mu.Lock()
	s.skipNext = true
	s.mu.Unlock()
}

func (s *Scheduler) ShouldSkip() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skipNext {
		s.skipNext = false
		return true
	}
	return false
}

// Pick chooses the next track per mode, respecting manual queue priority.
// Returns nil if library is empty.
func (s *Scheduler) Pick() *Track {
	for {
		s.mu.Lock()
		if len(s.manual) == 0 {
			break
		}
		id := s.manual[0]
		s.manual = s.manual[1:]
		snapshot := append([]int64(nil), s.manual...)
		s.mu.Unlock()
		_ = s.replaceQueue(snapshot)
		if t, ok := s.lib.Get(id); ok {
			return t
		}
		// Stale queue entry, keep draining until we hit a valid track or
		// fall through to the autopick branch
	}
	mode := s.mode
	cur := s.current
	s.mu.Unlock()

	tracks := s.lib.Snapshot()
	if len(tracks) == 0 {
		return nil
	}

	switch mode {
	case ModeLoop:
		if cur != nil {
			if t, ok := s.lib.Get(cur.ID); ok {
				return t
			}
		}
		return tracks[0]
	case ModeSequential:
		if cur == nil {
			return tracks[0]
		}
		idx := -1
		for i, t := range tracks {
			if t.ID == cur.ID {
				idx = i
				break
			}
		}
		if idx < 0 || idx+1 >= len(tracks) {
			return tracks[0]
		}
		return tracks[idx+1]
	default:
		return s.pickShuffle(tracks)
	}
}

// pickShuffle draws the next track from the deck. When the deck is exhausted
// or empty it is rebuilt from the live library snapshot, shuffled, and the
// position reset. Each track plays exactly once per deck cycle. Tracks
// removed mid-cycle are silently skipped; tracks added mid-cycle join only
// on the next rebuild
func (s *Scheduler) pickShuffle(tracks []*Track) *Track {
	if len(tracks) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deckPos >= len(s.deck) {
		s.rebuildDeckLocked(tracks)
	}
	// At most two passes: first drains the current deck, second drains a
	// fresh rebuild. If neither yields a valid track the library is empty
	for attempt := 0; attempt < 2; attempt++ {
		for s.deckPos < len(s.deck) {
			id := s.deck[s.deckPos]
			s.deckPos++
			if t, ok := s.lib.Get(id); ok {
				return t
			}
		}
		s.rebuildDeckLocked(tracks)
	}
	return nil
}

// rebuildDeckLocked must be called while s.mu is held
func (s *Scheduler) rebuildDeckLocked(tracks []*Track) {
	if len(tracks) == 0 {
		s.deck = nil
		s.deckPos = 0
		return
	}
	ids := make([]int64, len(tracks))
	for i, t := range tracks {
		ids[i] = t.ID
	}
	rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	s.deck = ids
	s.deckPos = 0
}

// MarkPlaying records the track as the current one and updates history.
func (s *Scheduler) MarkPlaying(t *Track) {
	if t == nil {
		s.mu.Lock()
		s.current = nil
		s.mu.Unlock()
		_ = s.db.SetSetting(settingCurrent, "")
		return
	}
	s.mu.Lock()
	s.current = t
	s.recent = append([]int64{t.ID}, s.recent...)
	if len(s.recent) > historyWindow*2 {
		s.recent = s.recent[:historyWindow*2]
	}
	s.mu.Unlock()

	_, _ = s.db.Exec(`INSERT INTO history(track_id, played_at) VALUES(?, ?)`, t.ID, time.Now().Unix())
	_, _ = s.db.Exec(`DELETE FROM history WHERE id IN (SELECT id FROM history ORDER BY played_at DESC LIMIT -1 OFFSET ?)`, historyWindow*4)
	_ = s.db.SetSetting(settingCurrent, strconv.FormatInt(t.ID, 10))
}

// Peek returns what the next track will likely be, for UI display.
// It does not consume the manual queue and does not mutate state.
func (s *Scheduler) Peek() *Track {
	s.mu.Lock()
	if len(s.manual) > 0 {
		id := s.manual[0]
		s.mu.Unlock()
		if t, ok := s.lib.Get(id); ok {
			return t
		}
		return nil
	}
	mode := s.mode
	cur := s.current
	s.mu.Unlock()

	tracks := s.lib.Snapshot()
	if len(tracks) == 0 {
		return nil
	}
	switch mode {
	case ModeLoop:
		return cur
	case ModeSequential:
		if cur == nil {
			return tracks[0]
		}
		for i, t := range tracks {
			if t.ID == cur.ID && i+1 < len(tracks) {
				return tracks[i+1]
			}
		}
		return tracks[0]
	case ModeShuffle:
		s.mu.Lock()
		var nextID int64
		var have bool
		if s.deckPos < len(s.deck) {
			nextID = s.deck[s.deckPos]
			have = true
		}
		s.mu.Unlock()
		if !have {
			return nil
		}
		if t, ok := s.lib.Get(nextID); ok {
			return t
		}
		return nil
	default:
		return nil
	}
}

func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(s)) {
	case ModeShuffle, ModeSequential, ModeLoop:
		return Mode(strings.ToLower(s)), nil
	}
	return "", errors.New("invalid mode")
}
