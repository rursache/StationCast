package audio

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// waitForGoroutines polls until the goroutine count drops to at most want, or
// the deadline passes. Polling rather than sampling once because a goroutine
// that has returned still takes a moment to be reaped
func waitForGoroutines(t *testing.T, want int, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		runtime.Gosched()
		got := runtime.NumGoroutine()
		if got <= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Close must not return until the reader goroutine has exited, otherwise
// callers have no way to know the goroutine is gone
func TestStderrPipeCloseWaitsForReader(t *testing.T) {
	p := stderrLogger("test")

	if _, err := p.Write([]byte("some ffmpeg warning\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-p.done:
	default:
		t.Fatal("Close returned while the reader goroutine was still running")
	}
}

// The leak this guards against: os/exec copies into a non-*os.File Stderr
// from its own goroutine and never closes the writer, so the reader blocked
// on Read forever once the child exited. A decoder is spawned per track, so
// this leaked one goroutine and one pipe for every track the station played
func TestStderrLoggerDoesNotLeakAcrossManySubprocesses(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh unavailable: %v", err)
	}

	// Warm up once so any lazily started runtime goroutines are already up
	// before the baseline is taken
	runSubprocessWithStderrLogger(t)

	baseline := waitForGoroutines(t, 0, time.Second)

	const spawns = 40
	for i := 0; i < spawns; i++ {
		runSubprocessWithStderrLogger(t)
	}

	// Allow a small margin for unrelated runtime goroutines. A real leak
	// would be one per spawn, far above this
	got := waitForGoroutines(t, baseline+2, 2*time.Second)
	if got > baseline+2 {
		t.Fatalf("goroutines grew from %d to %d across %d subprocess spawns, want no growth", baseline, got, spawns)
	}
}

func runSubprocessWithStderrLogger(t *testing.T) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "echo simulated ffmpeg warning >&2")
	errPipe := stderrLogger("test")
	cmd.Stderr = errPipe
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := errPipe.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
