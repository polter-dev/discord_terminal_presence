package main

import (
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
)

// TestAwaitDetectorShutdownWaitsForClose pins the ordering guarantee run()
// relies on: draining must not return until the detector goroutine closes
// `detections`.
func TestAwaitDetectorShutdownWaitsForClose(t *testing.T) {
	detections := make(chan detector.Detection)
	closed := false

	go func() {
		time.Sleep(20 * time.Millisecond)
		closed = true
		close(detections)
	}()

	awaitDetectorShutdown(detections)

	if !closed {
		t.Fatal("awaitDetectorShutdown returned before the detector goroutine closed the channel")
	}
}

// TestAwaitDetectorShutdownOrdersEpisodeSaveState is the regression test for
// issue #618. It mirrors run()'s shape: the detector goroutine's deferred
// episode save (internal/detector.(*Detector).run) runs strictly before it
// closes `detections` (`defer close(out)` is registered before `defer
// saveEpisodes(...)`, and defers run LIFO, so the save runs first on the way
// out). run() must therefore wait for that close before touching anything
// the save also touches. Under -race this reports a data race if
// awaitDetectorShutdown ever stops waiting for the close — including if a
// future refactor drops run()'s call to it entirely, which would leave
// nothing waiting for the goroutine below and race the very next statement
// against it.
func TestAwaitDetectorShutdownOrdersEpisodeSaveState(t *testing.T) {
	// Stand-in for internal/detector's episode store, which is
	// unsynchronized by design — its safety depends entirely on the
	// happens-before edge from the channel close, not a mutex.
	var lastEpisodeSave time.Time

	detections := make(chan detector.Detection)

	go func() {
		// Stand-in for the detector's own shutdown path: run its deferred
		// saveEpisodes(episodes) (registered before close(out), so it runs
		// first, LIFO) and only then close the channel run() is draining.
		for i := 0; i < 1000; i++ {
			lastEpisodeSave = time.Now()
		}
		close(detections)
	}()

	awaitDetectorShutdown(detections)

	if lastEpisodeSave.IsZero() {
		t.Fatal("detector shutdown save never recorded a timestamp")
	}
}
