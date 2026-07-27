package broadcast

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// Mirrors the audio package's guard. The HLS remuxer restarts on backoff, so
// a leak here accumulates one goroutine per restart rather than per track
func TestStderrLoggerDoesNotLeakAcrossSubprocesses(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh unavailable: %v", err)
	}

	run := func() {
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

	run()
	baseline := runtime.NumGoroutine()

	const spawns = 30
	for i := 0; i < spawns; i++ {
		run()
	}

	deadline := time.Now().Add(2 * time.Second)
	got := runtime.NumGoroutine()
	for got > baseline+2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		got = runtime.NumGoroutine()
	}
	if got > baseline+2 {
		t.Fatalf("goroutines grew from %d to %d across %d subprocess spawns, want no growth", baseline, got, spawns)
	}
}
