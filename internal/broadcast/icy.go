package broadcast

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

const ICYMetaInt = 16384

// WriteICYBlock writes a SHOUTcast/Icecast metadata block.
// Block format: 1 length byte (length / 16) + payload padded to a multiple of 16.
// Payload looks like: StreamTitle='Artist - Title';\0\0...
func WriteICYBlock(w io.Writer, title string) error {
	payload := fmt.Sprintf("StreamTitle='%s';", sanitizeMeta(title))
	rem := len(payload) % 16
	if rem != 0 {
		payload += strings.Repeat("\x00", 16-rem)
	}
	if len(payload) > 16*255 {
		payload = payload[:16*255]
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

func sanitizeMeta(s string) string {
	s = strings.ReplaceAll(s, "'", " ")
	s = strings.ReplaceAll(s, ";", ",")
	s = strings.ReplaceAll(s, "\x00", "")
	return s
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
