package audio

import (
	"io"
	"time"
)

// realtimeWriter throttles writes to a fixed bytes-per-second rate by sleeping
// after each Write, based on cumulative output and elapsed wall time.
// Wraps a downstream writer.
type realtimeWriter struct {
	w           io.Writer
	bytesPerSec int
	start       time.Time
	written     int64
}

func (r *realtimeWriter) Write(p []byte) (int, error) {
	if r.start.IsZero() {
		r.start = time.Now()
	}
	n, err := r.w.Write(p)
	r.written += int64(n)
	expected := time.Duration(float64(r.written) * float64(time.Second) / float64(r.bytesPerSec))
	actual := time.Since(r.start)
	if delta := expected - actual; delta > 0 {
		time.Sleep(delta)
	}
	return n, err
}
