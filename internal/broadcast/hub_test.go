package broadcast

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func mustSubscribe(t *testing.T, h *Hub) *Subscriber {
	t.Helper()
	sub, err := h.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	return sub
}

// A closed hub used to hand back a live-looking Subscriber whose channel was
// already closed, while a full hub returned nil. The handler only checked for
// nil, so a request arriving during shutdown got a 200 and a body that ended
// immediately instead of a 503
func TestSubscribeReportsClosedHub(t *testing.T) {
	h := NewHub(128)
	h.Close()

	sub, err := h.Subscribe()
	if sub != nil {
		t.Error("Subscribe on a closed hub returned a subscriber")
	}
	if !errors.Is(err, ErrHubClosed) {
		t.Errorf("Subscribe on a closed hub error = %v, want ErrHubClosed", err)
	}
}

func TestSubscribeInternalReportsClosedHub(t *testing.T) {
	h := NewHub(128)
	h.Close()

	sub, err := h.SubscribeInternal()
	if sub != nil {
		t.Error("SubscribeInternal on a closed hub returned a subscriber")
	}
	if !errors.Is(err, ErrHubClosed) {
		t.Errorf("SubscribeInternal on a closed hub error = %v, want ErrHubClosed", err)
	}
}

func TestSubscribeReportsCapacity(t *testing.T) {
	h := NewHub(128)
	h.SetMaxListeners(2)

	a := mustSubscribe(t, h)
	b := mustSubscribe(t, h)

	sub, err := h.Subscribe()
	if sub != nil {
		t.Error("Subscribe past the cap returned a subscriber")
	}
	if !errors.Is(err, ErrAtCapacity) {
		t.Errorf("Subscribe past the cap error = %v, want ErrAtCapacity", err)
	}

	// Freeing a slot lets the next listener in
	a.Close()
	if _, err := h.Subscribe(); err != nil {
		t.Errorf("Subscribe after a slot freed up: %v", err)
	}
	b.Close()
}

// The cap exists to bound client listeners, and the HLS remuxer must never be
// counted against it or a busy station loses its iOS stream
func TestSubscribeInternalBypassesCapacity(t *testing.T) {
	h := NewHub(128)
	h.SetMaxListeners(1)

	mustSubscribe(t, h)
	if _, err := h.Subscribe(); !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("expected the cap to be reached, got %v", err)
	}

	sub, err := h.SubscribeInternal()
	if err != nil {
		t.Fatalf("SubscribeInternal was refused at capacity: %v", err)
	}
	if sub == nil {
		t.Fatal("SubscribeInternal returned no subscriber")
	}
}

func TestSubscribeUnlimitedWhenMaxListenersZero(t *testing.T) {
	h := NewHub(128)
	h.SetMaxListeners(0)

	for i := 0; i < 50; i++ {
		if _, err := h.Subscribe(); err != nil {
			t.Fatalf("subscriber %d refused with an unlimited cap: %v", i, err)
		}
	}
	if got := h.Listeners(); got != 50 {
		t.Errorf("Listeners = %d, want 50", got)
	}
}

func TestWriteFansOutToEverySubscriber(t *testing.T) {
	h := NewHub(128)
	subs := []*Subscriber{mustSubscribe(t, h), mustSubscribe(t, h), mustSubscribe(t, h)}

	payload := []byte{0xff, 0xfb, 0x90, 0x00}
	if _, err := h.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}

	for i, sub := range subs {
		select {
		case got := <-sub.Chan():
			if string(got) != string(payload) {
				t.Errorf("subscriber %d got %v, want %v", i, got, payload)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

// The caller's buffer is reused across writes, so the hub has to hand out a
// copy or listeners see whatever the next chunk overwrote it with
func TestWriteCopiesTheCallerBuffer(t *testing.T) {
	h := NewHub(128)
	sub := mustSubscribe(t, h)

	buf := []byte{1, 2, 3, 4}
	if _, err := h.Write(buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for i := range buf {
		buf[i] = 0xaa
	}

	got := <-sub.Chan()
	want := []byte{1, 2, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("subscriber saw %v, want %v: the hub aliased the caller's buffer", got, want)
		}
	}
}

// Falling behind must cost the slow listener its connection, never the
// encoder's throughput
func TestWriteDropsSlowSubscriberInsteadOfBlocking(t *testing.T) {
	h := NewHub(128)
	slow := mustSubscribe(t, h)
	fast := mustSubscribe(t, h)

	// Overrun the slow listener's buffer without ever draining it
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < subBufferChunks+10; i++ {
			_, _ = h.Write([]byte{byte(i)})
			// Keep the fast listener from filling up too
			select {
			case <-fast.Chan():
			default:
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Write blocked on a subscriber that stopped reading")
	}

	// The slow listener is dropped, which closes its channel
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-slow.Chan():
			if !ok {
				return // dropped as intended
			}
		case <-deadline:
			t.Fatal("slow subscriber was never dropped")
		}
	}
}

func TestWriteAfterCloseReportsClosedHub(t *testing.T) {
	h := NewHub(128)
	h.Close()

	if _, err := h.Write([]byte{1}); !errors.Is(err, ErrHubClosed) {
		t.Errorf("Write on a closed hub error = %v, want ErrHubClosed", err)
	}
}

func TestCloseClosesEverySubscriberChannel(t *testing.T) {
	h := NewHub(128)
	subs := []*Subscriber{mustSubscribe(t, h), mustSubscribe(t, h)}

	h.Close()

	for i, sub := range subs {
		select {
		case _, ok := <-sub.Chan():
			if ok {
				t.Errorf("subscriber %d channel still open after Close", i)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d channel was not closed by Close", i)
		}
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	h := NewHub(128)
	mustSubscribe(t, h)
	h.Close()
	h.Close() // must not panic on a double channel close
}

// Close and Subscriber.Close both want to close the channel, and only one of
// them may actually do it
func TestUnsubscribeAfterCloseDoesNotPanic(t *testing.T) {
	h := NewHub(128)
	sub := mustSubscribe(t, h)
	h.Close()
	sub.Close()
	sub.Close()
}

func TestListenersTracksSubscriberCount(t *testing.T) {
	h := NewHub(128)
	if got := h.Listeners(); got != 0 {
		t.Fatalf("Listeners on a fresh hub = %d, want 0", got)
	}

	a := mustSubscribe(t, h)
	b := mustSubscribe(t, h)
	if got := h.Listeners(); got != 2 {
		t.Errorf("Listeners = %d, want 2", got)
	}

	a.Close()
	if got := h.Listeners(); got != 1 {
		t.Errorf("Listeners after one left = %d, want 1", got)
	}
	b.Close()
	if got := h.Listeners(); got != 0 {
		t.Errorf("Listeners after all left = %d, want 0", got)
	}
}

func TestMetadataRoundTrip(t *testing.T) {
	h := NewHub(128)
	if got := h.Metadata(); got != "" {
		t.Errorf("fresh hub metadata = %q, want empty", got)
	}
	h.SetMetadata("Artist - Title")
	if got := h.Metadata(); got != "Artist - Title" {
		t.Errorf("Metadata = %q, want %q", got, "Artist - Title")
	}
}

// Listeners connect and disconnect constantly while the encoder writes, so
// the whole surface has to hold up under -race
func TestHubConcurrentSubscribeWriteClose(t *testing.T) {
	h := NewHub(128)
	h.SetMaxListeners(0)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = h.Write([]byte{1, 2, 3})
			}
		}
	}()

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				sub, err := h.Subscribe()
				if err != nil {
					return
				}
				select {
				case <-sub.Chan():
				default:
				}
				sub.Close()
			}
		}()
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	h.Close()
}
