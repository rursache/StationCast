package broadcast

import (
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const ICYMetaInt = 16384

const (
	// The block length is carried in a single byte counting 16-byte units, so
	// a payload cannot exceed 255 of them
	maxICYPayload = 16 * 255
	icyPrefix     = "StreamTitle='"
	icySuffix     = "';"
	// What is left for the title itself once the framing is accounted for
	maxICYTitleBytes = maxICYPayload - len(icyPrefix) - len(icySuffix)
)

// WriteICYBlock writes a SHOUTcast/Icecast metadata block.
// Block format: 1 length byte (length / 16) + payload padded to a multiple of 16.
// Payload looks like: StreamTitle='Artist - Title';\0\0...
func WriteICYBlock(w io.Writer, title string) error {
	// Trim the title before assembling, not the finished payload. Cutting
	// afterwards could land mid-rune and also lop off the closing ';'
	// terminator, leaving clients with a block they cannot parse
	payload := icyPrefix + truncateUTF8(sanitizeMeta(title), maxICYTitleBytes) + icySuffix
	if rem := len(payload) % 16; rem != 0 {
		payload += strings.Repeat("\x00", 16-rem)
	}
	lenByte := byte(len(payload) / 16)
	if _, err := w.Write([]byte{lenByte}); err != nil {
		return err
	}
	if lenByte == 0 {
		return nil
	}
	_, err := w.Write([]byte(payload))
	return err
}

// WriteEmptyICYBlock writes a single zero byte indicating no metadata change.
func WriteEmptyICYBlock(w io.Writer) error {
	_, err := w.Write([]byte{0})
	return err
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune. Slicing
// a Go string is a byte operation, so a non-ASCII title cut at an arbitrary
// offset yields invalid UTF-8 on the wire
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// sanitizeMeta strips characters that could break ICY framing or confuse
// stream parsers: the single-quote and semicolon delimiters used by the
// StreamTitle key, plus all bytes below 0x20 (CR, LF, NUL, other control
// codes) which some clients treat as record separators
func sanitizeMeta(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 {
			continue
		}
		switch r {
		case '\'':
			b.WriteByte(' ')
		case ';':
			b.WriteByte(',')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ICYStream pumps audio from a Subscriber into w, inserting metadata blocks
// every metaInt bytes when wantMeta is true. On flush failure or write error
// it returns. titleFn is called whenever a metadata block needs to be written
// so the latest station metadata is always used.
func ICYStream(w io.Writer, flush func(), sub *Subscriber, wantMeta bool, metaInt int, titleFn func() string) error {
	var lastTitle string
	bytesUntilMeta := metaInt
	for chunk := range sub.Chan() {
		for len(chunk) > 0 {
			if wantMeta && bytesUntilMeta == 0 {
				title := titleFn()
				if title != lastTitle {
					if err := WriteICYBlock(w, title); err != nil {
						return err
					}
					lastTitle = title
				} else {
					if err := WriteEmptyICYBlock(w); err != nil {
						return err
					}
				}
				bytesUntilMeta = metaInt
			}
			n := len(chunk)
			if wantMeta && n > bytesUntilMeta {
				n = bytesUntilMeta
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return err
			}
			chunk = chunk[n:]
			if wantMeta {
				bytesUntilMeta -= n
			}
		}
		if flush != nil {
			flush()
		}
	}
	return errors.New("subscriber closed")
}
