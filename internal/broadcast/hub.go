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
	// client distinguishes a real listener from an in-process consumer such
	// as the HLS feeder. Only clients count toward the listener figure and
	// the configured cap
	client bool
}

func (s *Subscriber) Chan() <-chan []byte { return s.ch }

func (s *Subscriber) Close() {
	s.hub.unsubscribe(s)
}

type Hub struct {
	bitrate      int
	maxListeners int // 0 = unlimited

	mu      sync.Mutex
	subs    map[*Subscriber]struct{}
	clients int // subs excluding in-process consumers
	closed  bool
	burst   *burstBuffer

	meta atomic.Pointer[string]
}

func NewHub(bitrate int) *Hub {
	h := &Hub{
		bitrate: bitrate,
		subs:    map[*Subscriber]struct{}{},
		burst:   newBurstBuffer(burstBytes(bitrate, DefaultBurstSeconds)),
	}
	empty := ""
	h.meta.Store(&empty)
	return h
}

// SetBurstSeconds resizes the burst handed to joining listeners. Zero
// disables it, which returns to giving new listeners no buffer at all
func (h *Hub) SetBurstSeconds(sec int) {
	h.mu.Lock()
	h.burst = newBurstBuffer(burstBytes(h.bitrate, sec))
	h.mu.Unlock()
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
	h.burst.write(p)
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
// not be subject to the listener cap and must not show up as a listener
func (h *Hub) SubscribeInternal() (*Subscriber, error) {
	return h.subscribe(false)
}

// subscribe reports why it failed rather than overloading the return value.
// Subscribe used to hand back a live-looking Subscriber whose channel was
// already closed when the hub was shut down, but nil when the cap was hit, so
// the only caller's nil check silently let the shutdown case through and
// served a 200 with a body that ended immediately
func (h *Hub) subscribe(client bool) (*Subscriber, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrHubClosed
	}
	// Counted against h.clients, not len(h.subs): the HLS feeder is always
	// subscribed, so measuring the map charged every deployment one phantom
	// listener and quietly cost a slot of the configured cap
	if client && h.maxListeners > 0 && h.clients >= h.maxListeners {
		return nil, ErrAtCapacity
	}
	s := &Subscriber{
		hub:    h,
		ch:     make(chan []byte, subBufferChunks),
		client: client,
	}
	// Only real clients get the burst. Handing it to the HLS feeder would
	// re-encode audio that has already been segmented, duplicating a few
	// seconds of the timeline every time that process restarts.
	// The channel is empty and buffered, so this send cannot block
	if client {
		if b := alignToFrame(h.burst.snapshot()); len(b) > 0 {
			s.ch <- b
		}
	}
	h.subs[s] = struct{}{}
	if client {
		h.clients++
	}
	return s, nil
}

func (h *Hub) unsubscribe(s *Subscriber) {
	h.mu.Lock()
	if _, ok := h.subs[s]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.subs, s)
	if s.client {
		h.clients--
	}
	h.mu.Unlock()
	// Whoever won the delete above is the only one that closes, so this is
	// safe against a concurrent Write dropping the same slow subscriber.
	// Sends only happen under the lock and only to subscribers still in the
	// map, so nothing can send after this point
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
	h.clients = 0
	h.mu.Unlock()
	for _, s := range subs {
		close(s.ch)
	}
}

// Listeners reports how many real clients are connected. In-process
// consumers such as the HLS feeder are excluded
func (h *Hub) Listeners() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients
}
