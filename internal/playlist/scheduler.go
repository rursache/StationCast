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

	mu               sync.Mutex
	mode             Mode
	current          *Track
	currentStartedAt int64 // unix seconds when MarkPlaying recorded the current track
	manual           []int64
	recent           []int64

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

// CurrentStartedAt returns the unix timestamp (seconds) when MarkPlaying
// recorded the current track, or 0 when nothing is playing
func (s *Scheduler) CurrentStartedAt() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentStartedAt
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
	defer s.mu.Unlock()
	s.manual = append(s.manual, id)
	if err := s.persistQueueLocked(); err != nil {
		s.manual = s.manual[:len(s.manual)-1]
		return err
	}
	return nil
}

func (s *Scheduler) Dequeue(idx int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < 0 || idx >= len(s.manual) {
		return errors.New("index out of range")
	}
	removed := s.manual[idx]
	s.manual = append(s.manual[:idx], s.manual[idx+1:]...)
	if err := s.persistQueueLocked(); err != nil {
		s.manual = append(s.manual[:idx], append([]int64{removed}, s.manual[idx:]...)...)
		return err
	}
	return nil
}

// Errors a reorder can fail with, distinguished so the caller can tell a bad
// request from one that simply arrived too late
var (
	ErrQueueIndexOutOfRange = errors.New("queue index out of range")
	ErrQueueChanged         = errors.New("queue changed")
)

// MoveQueueItem moves the entry at `from` to `to`.
//
// expectID is the track the caller believes sits at `from`. The queue advances
// on its own as tracks play, so by the time a drag finishes the entry may have
// become the current track and everything shifted up. Without the check the
// move would silently reorder the wrong entries. A track may legitimately be
// queued twice, so identity alone cannot locate the entry, which is why this
// takes a position and verifies it rather than taking an order of ids
func (s *Scheduler) MoveQueueItem(from, to int, expectID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if from < 0 || from >= len(s.manual) || to < 0 || to >= len(s.manual) {
		return ErrQueueIndexOutOfRange
	}
	if s.manual[from] != expectID {
		return ErrQueueChanged
	}
	if from == to {
		return nil
	}

	before := append([]int64(nil), s.manual...)
	moved := s.manual[from]
	rest := make([]int64, 0, len(s.manual)-1)
	rest = append(rest, s.manual[:from]...)
	rest = append(rest, s.manual[from+1:]...)

	next := make([]int64, 0, len(s.manual))
	next = append(next, rest[:to]...)
	next = append(next, moved)
	next = append(next, rest[to:]...)
	s.manual = next

	if err := s.persistQueueLocked(); err != nil {
		s.manual = before
		return err
	}
	return nil
}

// persistQueueLocked rewrites the queue table to match s.manual. It must be
// called with s.mu held: the read of s.manual and the write have to be one
// atomic step, otherwise a concurrent caller can take its snapshot in between
// and the DELETE + reinsert either resurrects a dequeued track or drops a
// freshly enqueued one. SetMaxOpenConns(1) serialises individual statements
// but not a read-modify-write spanning two calls
func (s *Scheduler) persistQueueLocked() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM queue`); err != nil {
		return err
	}
	for i, id := range s.manual {
		if _, err := tx.Exec(`INSERT INTO queue(track_id, position) VALUES(?, ?)`, id, i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Pick chooses the next track per mode, respecting manual queue priority.
// Returns nil if library is empty.
func (s *Scheduler) Pick() *Track {
	s.mu.Lock()
	for len(s.manual) > 0 {
		id := s.manual[0]
		s.manual = s.manual[1:]
		// Persist while still holding the lock so the drain and the rewrite
		// cannot interleave with a concurrent Enqueue or Dequeue
		_ = s.persistQueueLocked()
		if t, ok := s.lib.Get(id); ok {
			s.mu.Unlock()
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
		s.currentStartedAt = 0
		s.mu.Unlock()
		_ = s.db.SetSetting(settingCurrent, "")
		return
	}
	now := time.Now().Unix()
	s.mu.Lock()
	s.current = t
	s.currentStartedAt = now
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
		// Same fallback Pick uses. Without the lookup, deleting the looping
		// track left the admin showing it as up next indefinitely while
		// playback had already moved on to the first track
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
