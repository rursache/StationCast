package broadcast

// DefaultBurstSeconds is how much already-encoded audio a joining listener
// receives up front. Without it a client only ever gets audio produced from
// the moment it connects, and because the encoder is paced at exactly real
// time it receives one second of audio per second and never builds a buffer.
// It then plays with no margin at all, so the first jitter of any kind drains
// it and the player rebuffers once before settling. Icecast defaults to a
// 64 KB burst for the same reason, which is about four seconds at 128 kbps
const DefaultBurstSeconds = 4

// MaxBurstSeconds bounds the configured value. A burst is audio the listener
// hears late, so a large one is a growing delay behind live, not free safety
const MaxBurstSeconds = 30

// burstBuffer keeps the most recent encoded bytes in a fixed ring. Writes are
// O(len(p)) with no per-write allocation, since the hub calls it for every
// chunk the encoder produces
type burstBuffer struct {
	buf     []byte
	pos     int  // where the next byte goes
	wrapped bool // whether the ring has filled at least once
}

func newBurstBuffer(size int) *burstBuffer {
	if size < 0 {
		size = 0
	}
	return &burstBuffer{buf: make([]byte, size)}
}

func (b *burstBuffer) write(p []byte) {
	if len(b.buf) == 0 || len(p) == 0 {
		return
	}
	// Anything older than the tail of p is unreachable, so keep only the tail
	if len(p) >= len(b.buf) {
		copy(b.buf, p[len(p)-len(b.buf):])
		b.pos = 0
		b.wrapped = true
		return
	}
	n := copy(b.buf[b.pos:], p)
	if n < len(p) {
		copy(b.buf, p[n:])
		b.pos = len(p) - n
		b.wrapped = true
		return
	}
	b.pos += n
	if b.pos == len(b.buf) {
		b.pos = 0
		b.wrapped = true
	}
}

// snapshot returns the buffered bytes oldest first
func (b *burstBuffer) snapshot() []byte {
	if len(b.buf) == 0 {
		return nil
	}
	if !b.wrapped {
		if b.pos == 0 {
			return nil
		}
		out := make([]byte, b.pos)
		copy(out, b.buf[:b.pos])
		return out
	}
	out := make([]byte, len(b.buf))
	n := copy(out, b.buf[b.pos:])
	copy(out[n:], b.buf[:b.pos])
	return out
}

// alignToFrame trims b forward to the first MP3 frame header so the burst
// does not open with a partial frame. Decoders resync on their own, but they
// tend to emit a click or drop the leading frames while doing it, which is
// exactly the artefact the burst exists to avoid. An MP3 frame starts with
// eleven set sync bits: 0xFF followed by a byte whose top three bits are set
func alignToFrame(b []byte) []byte {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == 0xFF && b[i+1]&0xE0 == 0xE0 {
			return b[i:]
		}
	}
	// No header found, hand it over untouched rather than dropping audio
	return b
}

// burstBytes converts a bitrate in kbps and a duration in seconds into a
// buffer size
func burstBytes(bitrateKbps, seconds int) int {
	if bitrateKbps <= 0 || seconds <= 0 {
		return 0
	}
	if seconds > MaxBurstSeconds {
		seconds = MaxBurstSeconds
	}
	return bitrateKbps * 1000 / 8 * seconds
}
