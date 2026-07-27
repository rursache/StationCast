package audio

import (
	"errors"
	"io"
	"testing"
	"time"
)

// blockingReader hands out PCM until release is closed, then blocks forever.
// Stands in for a decoder that has gone quiet
type blockingReader struct {
	release chan struct{}
	block   chan struct{}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	select {
	case <-r.release:
		<-r.block // never returns, like a parked decoder read
		return 0, io.EOF
	default:
		for i := range p {
			p[i] = 0
		}
		return len(p), nil
	}
}

// When the encoder dies, runOnce closes encStdin to unblock the PCM pump.
// That only works if a failed write actually terminates the copy loop, so
// pin the behaviour the cleanup path depends on
func TestCopyChunksReturnsWhenDestinationCloses(t *testing.T) {
	pr, pw := io.Pipe()

	// Drain briefly, then tear the destination down under the copier
	go func() {
		buf := make([]byte, 4096)
		for i := 0; i < 4; i++ {
			if _, err := pr.Read(buf); err != nil {
				return
			}
		}
		_ = pw.Close()
		_ = pr.Close()
	}()

	src := &blockingReader{release: make(chan struct{}), block: make(chan struct{})}
	returned := make(chan error, 1)
	go func() {
		_, err := copyChunks(pw, src, 1024)
		returned <- err
	}()

	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("copyChunks returned a nil error after the destination closed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copyChunks did not return after the destination closed")
	}
}

func TestCopyChunksPropagatesSourceError(t *testing.T) {
	want := errors.New("decoder blew up")
	src := &errReader{err: want}

	_, err := copyChunks(io.Discard, src, 512)
	if !errors.Is(err, want) {
		t.Fatalf("copyChunks error = %v, want %v", err, want)
	}
}

type errReader struct {
	err  error
	sent bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return len(p), nil
	}
	return 0, r.err
}

// A short read is normal for a streaming source and must not end the loop,
// otherwise the engine would restart on every partial PCM chunk
func TestCopyChunksToleratesShortReads(t *testing.T) {
	want := errors.New("done")
	src := &shortReader{limit: 5, err: want}

	n, err := copyChunks(io.Discard, src, 1024)
	if !errors.Is(err, want) {
		t.Fatalf("copyChunks error = %v, want %v", err, want)
	}
	if n == 0 {
		t.Fatal("copyChunks copied nothing despite the source yielding data")
	}
}

type shortReader struct {
	calls int
	limit int
	err   error
}

func (r *shortReader) Read(p []byte) (int, error) {
	r.calls++
	if r.calls > r.limit {
		return 0, r.err
	}
	// Deliberately partial, so io.ReadFull reports ErrUnexpectedEOF
	return len(p) / 3, io.EOF
}

// The pacer is what keeps the encoder fed at exactly real time. Too fast and
// listeners drift ahead of the stream, too slow and the audio underruns
func TestRealtimeWriterPacesToByteRate(t *testing.T) {
	const rate = 16000 // bytes/sec, so 8000 bytes is half a second
	w := &realtimeWriter{w: io.Discard, bytesPerSec: rate}

	start := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := w.Write(make([]byte, rate/4)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	elapsed := time.Since(start)

	// Four quarter-second chunks, so roughly a second. Generous bounds, this
	// is checking that throttling happens at all and is not wildly off
	if elapsed < 700*time.Millisecond {
		t.Errorf("wrote one second of audio in %v, pacing is too fast", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("wrote one second of audio in %v, pacing is too slow", elapsed)
	}
}
