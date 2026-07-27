package broadcast

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// decodeICYBlock unpacks a written block back into its length byte and the
// StreamTitle value, mirroring what a client parser does
func decodeICYBlock(t *testing.T, b []byte) (lenByte byte, title string) {
	t.Helper()
	if len(b) == 0 {
		t.Fatal("empty block")
	}
	lenByte = b[0]
	payload := b[1:]
	if int(lenByte)*16 != len(payload) {
		t.Fatalf("length byte says %d bytes, payload is %d", int(lenByte)*16, len(payload))
	}
	payload = bytes.TrimRight(payload, "\x00")
	s := string(payload)
	if !strings.HasPrefix(s, icyPrefix) {
		t.Fatalf("payload %q missing the StreamTitle prefix", s)
	}
	if !strings.HasSuffix(s, icySuffix) {
		t.Fatalf("payload %q missing the closing terminator", s)
	}
	return lenByte, strings.TrimSuffix(strings.TrimPrefix(s, icyPrefix), icySuffix)
}

func TestWriteICYBlockRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteICYBlock(&buf, "Portishead - Roads"); err != nil {
		t.Fatalf("WriteICYBlock: %v", err)
	}
	_, title := decodeICYBlock(t, buf.Bytes())
	if title != "Portishead - Roads" {
		t.Errorf("title = %q, want %q", title, "Portishead - Roads")
	}
}

func TestWriteICYBlockPadsToSixteenBytes(t *testing.T) {
	for _, title := range []string{"", "a", "short", strings.Repeat("x", 100), "日本語タイトル"} {
		var buf bytes.Buffer
		if err := WriteICYBlock(&buf, title); err != nil {
			t.Fatalf("WriteICYBlock(%q): %v", title, err)
		}
		payload := buf.Bytes()[1:]
		if len(payload)%16 != 0 {
			t.Errorf("payload for %q is %d bytes, not a multiple of 16", title, len(payload))
		}
	}
}

// Truncation used to slice the finished payload at a byte offset, which for a
// multibyte title could cut a rune in half and hand clients invalid UTF-8
func TestWriteICYBlockTruncatesOnRuneBoundary(t *testing.T) {
	// Emoji are 4 bytes each, so no multiple of them lands on the byte limit
	// cleanly and a naive cut is guaranteed to split one
	long := strings.Repeat("🎵", 2000)

	var buf bytes.Buffer
	if err := WriteICYBlock(&buf, long); err != nil {
		t.Fatalf("WriteICYBlock: %v", err)
	}

	lenByte, title := decodeICYBlock(t, buf.Bytes())
	if lenByte == 0 {
		t.Fatal("oversized title produced an empty block")
	}
	if !utf8.ValidString(title) {
		t.Error("truncated title is not valid UTF-8")
	}
	for i, r := range title {
		if r == utf8.RuneError {
			t.Fatalf("truncated title has a broken rune at byte %d", i)
		}
	}
}

// The length byte only counts to 255, so the payload must never exceed 4080
func TestWriteICYBlockRespectsMaximumLength(t *testing.T) {
	for _, title := range []string{
		strings.Repeat("a", 10000),
		strings.Repeat("🎵", 2000),
		strings.Repeat("é", 4000),
	} {
		var buf bytes.Buffer
		if err := WriteICYBlock(&buf, title); err != nil {
			t.Fatalf("WriteICYBlock: %v", err)
		}
		payload := buf.Bytes()[1:]
		if len(payload) > maxICYPayload {
			t.Errorf("payload is %d bytes, over the %d limit", len(payload), maxICYPayload)
		}
		if int(buf.Bytes()[0])*16 != len(payload) {
			t.Errorf("length byte %d does not match payload length %d", buf.Bytes()[0], len(payload))
		}
	}
}

// An oversized title must still end in ';' or clients cannot parse the block
func TestWriteICYBlockKeepsTerminatorWhenTruncating(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteICYBlock(&buf, strings.Repeat("x", 10000)); err != nil {
		t.Fatalf("WriteICYBlock: %v", err)
	}
	// decodeICYBlock fails the test if the prefix or terminator is missing
	decodeICYBlock(t, buf.Bytes())
}

func TestTruncateUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under the limit", "hello", 10, "hello"},
		{"exactly at the limit", "hello", 5, "hello"},
		{"ascii cut", "hello", 3, "hel"},
		{"cut before a multibyte rune", "ab🎵", 3, "ab"},
		{"cut inside a multibyte rune", "ab🎵", 4, "ab"},
		{"cut inside a multibyte rune, later byte", "ab🎵", 5, "ab"},
		{"multibyte rune fits exactly", "ab🎵", 6, "ab🎵"},
		{"two byte rune split", "café", 4, "caf"},
		{"zero limit", "hello", 0, ""},
		{"empty input", "", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateUTF8(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if len(got) > tc.max {
				t.Errorf("result %q is %d bytes, over the %d limit", got, len(got), tc.max)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result %q is not valid UTF-8", got)
			}
		})
	}
}

func TestSanitizeMetaStripsFramingCharacters(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"it's here", "it s here"},
		{"a;b", "a,b"},
		{"line\nbreak", "linebreak"},
		{"tab\there", "tabhere"},
		{"null\x00byte", "nullbyte"},
		{"kept 日本語 🎵", "kept 日本語 🎵"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitizeMeta(tc.in); got != tc.want {
				t.Errorf("sanitizeMeta(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A title carrying the delimiters must not be able to inject a second key
func TestWriteICYBlockCannotBreakOutOfStreamTitle(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteICYBlock(&buf, "evil';StreamURL='http://attacker.example"); err != nil {
		t.Fatalf("WriteICYBlock: %v", err)
	}
	payload := string(bytes.TrimRight(buf.Bytes()[1:], "\x00"))
	if strings.Count(payload, "';") != 1 {
		t.Errorf("payload %q contains more than one key terminator", payload)
	}
	if strings.Contains(payload, "StreamURL='") {
		t.Errorf("payload %q let a second key through", payload)
	}
}

func TestWriteEmptyICYBlock(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteEmptyICYBlock(&buf); err != nil {
		t.Fatalf("WriteEmptyICYBlock: %v", err)
	}
	if got := buf.Bytes(); len(got) != 1 || got[0] != 0 {
		t.Errorf("empty block = %v, want a single zero byte", got)
	}
}
