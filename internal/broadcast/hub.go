package broadcast

import (
	"errors"
	"sync"
	"sync/atomic"
)

const subBufferChunks = 64

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
		return 0, errors.New("hub closed")
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

// Subscribe registers a client listener. Returns nil if the configured cap
// is reached. The HLS feeder uses SubscribeInternal to bypass the cap
func (h *Hub) Subscribe() *Subscriber {
	s := &Subscriber{
		hub: h,
		ch:  make(chan []byte, subBufferChunks),
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(s.ch)
		return s
	}
	if h.maxListeners > 0 && len(h.subs) >= h.maxListeners {
		h.mu.Unlock()
		close(s.ch)
		return nil
	}
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// SubscribeInternal is for in-process consumers (the HLS feeder) that must
// not be subject to the listener cap
func (h *Hub) SubscribeInternal() *Subscriber {
	s := &Subscriber{
		hub: h,
		ch:  make(chan []byte, subBufferChunks),
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(s.ch)
		return s
	}
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
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
