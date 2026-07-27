package broadcast

import (
	"bytes"
	"testing"
	"time"
)

func TestBurstBytes(t *testing.T) {
	cases := []struct {
		bitrate, seconds, want int
	}{
		{128, 4, 64000}, // the Icecast default, near enough 64 KB
		{128, 1, 16000},
		{320, 4, 160000},
		{128, 0, 0},
		{0, 4, 0},
		{-1, 4, 0},
		{128, -5, 0},
		{128, 1000, 128 * 1000 / 8 * MaxBurstSeconds}, // clamped
	}
	for _, tc := range cases {
		if got := burstBytes(tc.bitrate, tc.seconds); got != tc.want {
			t.Errorf("burstBytes(%d, %d) = %d, want %d", tc.bitrate, tc.seconds, got, tc.want)
		}
	}
}

func TestBurstBufferBelowCapacity(t *testing.T) {
	b := newBurstBuffer(100)
	b.write([]byte("hello "))
	b.write([]byte("world"))

	if got := string(b.snapshot()); got != "hello world" {
		t.Errorf("snapshot = %q, want %q", got, "hello world")
	}
}

func TestBurstBufferEmpty(t *testing.T) {
	if got := newBurstBuffer(100).snapshot(); got != nil {
		t.Errorf("snapshot of an unused buffer = %v, want nil", got)
	}
	if got := newBurstBuffer(0).snapshot(); got != nil {
		t.Errorf("snapshot of a zero-size buffer = %v, want nil", got)
	}
}

// Once the ring wraps it must hand back the newest bytes in order, since a
// joining listener needs the audio immediately before it connected
func TestBurstBufferKeepsNewestAfterWrapping(t *testing.T) {
	b := newBurstBuffer(10)
	for i := 0; i < 26; i++ {
		b.write([]byte{byte('a' + i)})
	}
	if got := string(b.snapshot()); got != "qrstuvwxyz" {
		t.Errorf("snapshot = %q, want the last 10 bytes %q", got, "qrstuvwxyz")
	}
}

func TestBurstBufferWriteLargerThanCapacity(t *testing.T) {
	b := newBurstBuffer(5)
	b.write([]byte("abcdefghij"))

	if got := string(b.snapshot()); got != "fghij" {
		t.Errorf("snapshot = %q, want the trailing %q", got, "fghij")
	}
}

func TestBurstBufferWriteExactlyCapacity(t *testing.T) {
	b := newBurstBuffer(5)
	b.write([]byte("abcde"))
	if got := string(b.snapshot()); got != "abcde" {
		t.Errorf("snapshot = %q, want %q", got, "abcde")
	}

	b.write([]byte("fghij"))
	if got := string(b.snapshot()); got != "fghij" {
		t.Errorf("snapshot after a second full write = %q, want %q", got, "fghij")
	}
}

func TestBurstBufferStraddlingWrites(t *testing.T) {
	b := newBurstBuffer(8)
	b.write([]byte("abcde"))  // pos 5
	b.write([]byte("fghijk")) // wraps, keeps the last 8
	if got := string(b.snapshot()); got != "defghijk" {
		t.Errorf("snapshot = %q, want %q", got, "defghijk")
	}
}

func TestBurstBufferZeroSizeIsInert(t *testing.T) {
	b := newBurstBuffer(0)
	b.write([]byte("anything"))
	if got := b.snapshot(); len(got) != 0 {
		t.Errorf("a disabled burst returned %d bytes", len(got))
	}
}

func TestBurstBufferIgnoresEmptyWrites(t *testing.T) {
	b := newBurstBuffer(10)
	b.write([]byte("ab"))
	b.write(nil)
	b.write([]byte{})
	if got := string(b.snapshot()); got != "ab" {
		t.Errorf("snapshot = %q, want %q", got, "ab")
	}
}

// The ring almost always starts mid-frame. Handing a decoder a partial frame
// produces the click the burst exists to avoid
func TestAlignToFrame(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{
			name: "already aligned",
			in:   []byte{0xFF, 0xFB, 0x90, 0x00},
			want: []byte{0xFF, 0xFB, 0x90, 0x00},
		},
		{
			name: "partial frame at the head is trimmed",
			in:   []byte{0x11, 0x22, 0x33, 0xFF, 0xFB, 0x90},
			want: []byte{0xFF, 0xFB, 0x90},
		},
		{
			name: "0xFF not followed by a sync byte is not a header",
			in:   []byte{0xFF, 0x01, 0xFF, 0xFA, 0x00},
			want: []byte{0xFF, 0xFA, 0x00},
		},
		{
			name: "no header found, returned untouched",
			in:   []byte{0x01, 0x02, 0x03},
			want: []byte{0x01, 0x02, 0x03},
		},
		{
			name: "empty",
			in:   []byte{},
			want: []byte{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := alignToFrame(tc.in); !bytes.Equal(got, tc.want) {
				t.Errorf("alignToFrame(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The whole point: a joining listener must start with a buffer rather than
// having to earn one by stalling first
func TestSubscribeDeliversBurstImmediately(t *testing.T) {
	h := NewHub(128)

	// Simulate audio already on air before anyone connects
	frame := make([]byte, 400)
	frame[0], frame[1] = 0xFF, 0xFB
	for i := 0; i < 50; i++ {
		if _, err := h.Write(frame); err != nil {
			t.Fatal(err)
		}
	}

	sub := mustSubscribe(t, h)
	select {
	case got := <-sub.Chan():
		if len(got) == 0 {
			t.Fatal("joining listener received an empty burst")
		}
		if got[0] != 0xFF {
			t.Errorf("burst does not start on a frame header, first byte = %#x", got[0])
		}
		want := 50 * len(frame)
		if len(got) != want {
			t.Errorf("burst = %d bytes, want the %d bytes already broadcast", len(got), want)
		}
	default:
		t.Fatal("joining listener got no burst, so it starts with no buffer at all")
	}
}

func TestBurstIsCappedAtConfiguredSize(t *testing.T) {
	h := NewHub(128) // 4s default => 64000 bytes
	frame := make([]byte, 1000)
	frame[0], frame[1] = 0xFF, 0xFB
	for i := 0; i < 500; i++ { // 500 KB, far more than the burst holds
		if _, err := h.Write(frame); err != nil {
			t.Fatal(err)
		}
	}

	sub := mustSubscribe(t, h)
	got := <-sub.Chan()
	if len(got) > burstBytes(128, DefaultBurstSeconds) {
		t.Errorf("burst = %d bytes, over the %d cap", len(got), burstBytes(128, DefaultBurstSeconds))
	}
	if len(got) < burstBytes(128, DefaultBurstSeconds)/2 {
		t.Errorf("burst = %d bytes, far below the configured size", len(got))
	}
}

// The HLS remuxer has already segmented this audio. Replaying it would
// duplicate a few seconds of the timeline on every restart of that process
func TestInternalSubscriberGetsNoBurst(t *testing.T) {
	h := NewHub(128)
	frame := make([]byte, 400)
	frame[0], frame[1] = 0xFF, 0xFB
	for i := 0; i < 20; i++ {
		if _, err := h.Write(frame); err != nil {
			t.Fatal(err)
		}
	}

	feeder, err := h.SubscribeInternal()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-feeder.Chan():
		t.Fatalf("the HLS feeder received a %d byte burst, duplicating already segmented audio", len(got))
	default:
	}
}

func TestSetBurstSecondsZeroDisablesIt(t *testing.T) {
	h := NewHub(128)
	h.SetBurstSeconds(0)

	frame := make([]byte, 400)
	frame[0], frame[1] = 0xFF, 0xFB
	for i := 0; i < 20; i++ {
		if _, err := h.Write(frame); err != nil {
			t.Fatal(err)
		}
	}

	sub := mustSubscribe(t, h)
	select {
	case got := <-sub.Chan():
		t.Fatalf("burst disabled but the listener still received %d bytes", len(got))
	default:
	}
}

func TestSetBurstSecondsResizes(t *testing.T) {
	h := NewHub(128)
	h.SetBurstSeconds(1) // 16000 bytes

	frame := make([]byte, 1000)
	frame[0], frame[1] = 0xFF, 0xFB
	for i := 0; i < 100; i++ {
		if _, err := h.Write(frame); err != nil {
			t.Fatal(err)
		}
	}

	sub := mustSubscribe(t, h)
	got := <-sub.Chan()
	if len(got) > burstBytes(128, 1) {
		t.Errorf("burst = %d bytes, over the 1s size of %d", len(got), burstBytes(128, 1))
	}
}

// A listener joining a station that has only just started must still work,
// with whatever little audio exists so far
func TestBurstOnAStationThatJustStarted(t *testing.T) {
	h := NewHub(128)
	sub := mustSubscribe(t, h)

	select {
	case got := <-sub.Chan():
		t.Fatalf("received %d bytes of burst before any audio was broadcast", len(got))
	default:
	}

	// Live audio still flows normally afterwards
	if _, err := h.Write([]byte{0xFF, 0xFB, 0x01}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-sub.Chan():
		if len(got) != 3 {
			t.Errorf("live chunk = %d bytes, want 3", len(got))
		}
	case <-time.After(time.Second):
		t.Error("live audio did not reach the listener")
	}
}

// The burst occupies one slot, so it must not eat into the tolerance the
// channel provides for a briefly slow client
func TestBurstDoesNotStarveTheSubscriberChannel(t *testing.T) {
	h := NewHub(128)
	frame := make([]byte, 1000)
	frame[0], frame[1] = 0xFF, 0xFB
	for i := 0; i < 100; i++ {
		if _, err := h.Write(frame); err != nil {
			t.Fatal(err)
		}
	}

	sub := mustSubscribe(t, h)
	if got := len(sub.Chan()); got != 1 {
		t.Errorf("burst occupies %d channel slots, want 1", got)
	}
}
