package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWaitForReturnsTrueWhenWorkersFinish(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond)
		}()
	}

	if !waitFor(&wg, 2*time.Second) {
		t.Fatal("waitFor timed out on workers that finished promptly")
	}
}

// A worker that ignores cancellation must not wedge shutdown forever
func TestWaitForTimesOutOnStuckWorker(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	release := make(chan struct{})
	go func() {
		defer wg.Done()
		<-release
	}()
	defer close(release)

	start := time.Now()
	if waitFor(&wg, 50*time.Millisecond) {
		t.Fatal("waitFor reported success while a worker was still running")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("waitFor returned after %v, want at least the 50ms timeout", elapsed)
	}
}

func TestWaitForReturnsImmediatelyWithNoWorkers(t *testing.T) {
	var wg sync.WaitGroup
	if !waitFor(&wg, time.Second) {
		t.Fatal("waitFor timed out on an empty WaitGroup")
	}
}

// Shutdown cancels the context and then waits. This covers the shape main
// relies on: workers that honour cancellation are all joined, so the ffmpeg
// kill and reap in each Run loop completes before main returns
func TestCancelThenWaitJoinsAllWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var workers sync.WaitGroup
	spawn := func(fn func(context.Context)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			fn(ctx)
		}()
	}

	var mu sync.Mutex
	var cleanedUp int
	for i := 0; i < 4; i++ {
		spawn(func(ctx context.Context) {
			<-ctx.Done()
			// Stands in for killing and reaping an ffmpeg child
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			cleanedUp++
			mu.Unlock()
		})
	}

	cancel()
	if !waitFor(&workers, 5*time.Second) {
		t.Fatal("workers did not stop after cancellation")
	}

	mu.Lock()
	defer mu.Unlock()
	if cleanedUp != 4 {
		t.Fatalf("%d of 4 workers finished cleanup before the wait returned", cleanedUp)
	}
}
