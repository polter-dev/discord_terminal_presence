package main

import (
	"testing"
	"time"
)

// TestFinalizeAfterTranslatorWaitsForTheGoroutine pins the ordering guarantee
// run() relies on: the shutdown work must not start until the translation
// goroutine has returned.
func TestFinalizeAfterTranslatorWaitsForTheGoroutine(t *testing.T) {
	translatorDone := make(chan struct{})
	translatorReturned := false
	finalized := make(chan bool, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		translatorReturned = true
		close(translatorDone)
	}()

	finalizeAfterTranslator(translatorDone, func() {
		finalized <- translatorReturned
	})

	select {
	case sawReturn := <-finalized:
		if !sawReturn {
			t.Fatal("finalize ran before the translation goroutine returned")
		}
	default:
		t.Fatal("finalize did not run")
	}
}

// TestFinalizeAfterTranslatorOrdersUsageSaveState is the regression test for
// issue #593. It mirrors run()'s shape: the translation goroutine keeps
// touching the throttle timestamp after writer.RunActivities has returned on
// ctx cancellation, and the forced shutdown save touches the same timestamp.
// Under -race this reports a data race if finalizeAfterTranslator ever stops
// waiting for the goroutine.
func TestFinalizeAfterTranslatorOrdersUsageSaveState(t *testing.T) {
	// Stand-in for run()'s lastUsageSave, which is unsynchronized by design.
	var lastUsageSave time.Time

	translatorDone := make(chan struct{})
	writerReturned := make(chan struct{})

	go func() {
		defer close(translatorDone)
		// The goroutine is still mid-flight when the writer returns.
		<-writerReturned
		for i := 0; i < 1000; i++ {
			lastUsageSave = time.Now()
		}
	}()

	// writer.RunActivities(ctx, activities) returning on ctx cancellation.
	close(writerReturned)

	finalizeAfterTranslator(translatorDone, func() {
		lastUsageSave = time.Now()
	})

	<-translatorDone // attribute any race to this test rather than to exit

	if lastUsageSave.IsZero() {
		t.Fatal("shutdown save never recorded a timestamp")
	}
}
