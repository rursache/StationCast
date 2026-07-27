package broadcast

import (
	"errors"
	"sync"
	"sync/atomic"
)

// Per-subscriber channel depth. ffmpeg with -flush_packets 1 emits one MP3
// frame per write (~26ms at 128kbps), so 512 slots is roughly 13s of audio.
// A slow client that briefly falls behind (tab throttle, GC pause, network
// blip) gets a generous tolerance window before the hub drops it. Hub fan-out
// still drops slow listeners rather than back-pressuring the encoder
const subBufferChunks = 512

// Reasons a subscription can be refused. Callers distinguish them because a
// capacity refusal is worth retrying and a closed hub is not
var (
	ErrHubClosed  = errors.New("hub closed")
	ErrAtCapacity = errors.New("listener limit reached")
)

type Subscriber struct {
	hub *Hub
	ch  chan []byte
}

func (s *Subscriber) Chan() <-chan []byte { return s.ch }

func (s *Subscriber) Close() {
	s.hub.unsubscribe(s)
}

type Hub struct {
	bitrate      int
	maxListeners int // 0 = unlimited

	mu     sync.Mutex
	subs   map[*Subscriber]struct{}
	closed bool

	meta atomic.Pointer[string]
}

func NewHub(bitrate int) *Hub {
	h := &Hub{
		bitrate: bitrate,
		subs:    map[*Subscriber]struct{}{},
	}
	empty := ""
	h.meta.Store(&empty)
	return h
}

func (h *Hub) Bitrate() int { return h.bitrate }

// SetMaxListeners caps the number of concurrent client subscribers.
// Zero means unlimited. Internal subscribers (HLS feeder) bypass this cap
// via SubscribeInternal
func (h *Hub) SetMaxListeners(n int) {
	h.mu.Lock()
	h.maxListeners = n
	h.mu.Unlock()
}

func (h *Hub) Metadata() string { return *h.meta.Load() }

func (h *Hub) SetMetadata(s string) {
	h.meta.Store(&s)
}

func (h *Hub) Write(p []byte) (int, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return 0, ErrHubClosed
	}
	dropped := []*Subscriber{}
	cp := make([]byte, len(p))
	copy(cp, p)
	for s := range h.subs {
		select {
		case s.ch <- cp:
		default:
			dropped = append(dropped, s)
		}
	}
	h.mu.Unlock()
	for _, s := range dropped {
		h.unsubscribe(s)
	}
	return len(p), nil
}

// Subscribe registers a client listener. The HLS feeder uses SubscribeInternal
// to bypass the cap
func (h *Hub) Subscribe() (*Subscriber, error) {
	return h.subscribe(true)
}

// SubscribeInternal is for in-process consumers (the HLS feeder) that must
// not be subject to the listener cap
func (h *Hub) SubscribeInternal() (*Subscriber, error) {
	return h.subscribe(false)
}

// subscribe reports why it failed rather than overloading the return value.
// Subscribe used to hand back a live-looking Subscriber whose channel was
// already closed when the hub was shut down, but nil when the cap was hit, so
// the only caller's nil check silently let the shutdown case through and
// served a 200 with a body that ended immediately
func (h *Hub) subscribe(capped bool) (*Subscriber, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrHubClosed
	}
	if capped && h.maxListeners > 0 && len(h.subs) >= h.maxListeners {
		return nil, ErrAtCapacity
	}
	s := &Subscriber{
		hub: h,
		ch:  make(chan []byte, subBufferChunks),
	}
	h.subs[s] = struct{}{}
	return s, nil
}

func (h *Hub) unsubscribe(s *Subscriber) {
	h.mu.Lock()
	if _, ok := h.subs[s]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.subs, s)
	h.mu.Unlock()
	go func() {
		for range s.ch {
		}
	}()
	close(s.ch)
}

func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := make([]*Subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.subs = map[*Subscriber]struct{}{}
	h.mu.Unlock()
	for _, s := range subs {
		close(s.ch)
	}
}

func (h *Hub) Listeners() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
