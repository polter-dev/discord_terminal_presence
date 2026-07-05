package presence

import (
	"context"
	"errors"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
)

var defaultRetryDelays = []time.Duration{
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

// Writer consumes activities and owns all Discord IPC client calls.
type Writer struct {
	client Client
	appID  string

	options    DisplayOptions
	retryDelay retryDelay
}

// WriterOption configures a Writer.
type WriterOption func(*Writer)

// WithDisplayOptions replaces the default display/privacy options used by Run.
func WithDisplayOptions(options DisplayOptions) WriterOption {
	return func(writer *Writer) {
		writer.options = options
	}
}

// WithRetryDelays replaces the reconnect backoff sequence.
func WithRetryDelays(delays ...time.Duration) WriterOption {
	return func(writer *Writer) {
		writer.retryDelay = newRetryDelay(delays)
	}
}

// NewWriter creates a presence writer. An empty appID uses DefaultAppID.
func NewWriter(client Client, appID string, options ...WriterOption) (*Writer, error) {
	if client == nil {
		return nil, errors.New("presence: client is required")
	}
	if appID == "" {
		appID = DefaultAppID
	}

	writer := &Writer{
		client:     client,
		appID:      appID,
		options:    DefaultDisplayOptions(),
		retryDelay: newRetryDelay(defaultRetryDelays),
	}
	for _, option := range options {
		option(writer)
	}
	return writer, nil
}

// Run consumes detector events until ctx is cancelled or detections is closed,
// mapping each detection to an activity via the writer's DisplayOptions. For
// config-driven per-tool options (per-tool toggles, directory allowlist, button
// overrides), build activities yourself and use RunActivities instead.
func (w *Writer) Run(ctx context.Context, detections <-chan detector.Detection) {
	activities := make(chan *Activity)
	go func() {
		defer close(activities)
		for {
			select {
			case detection, ok := <-detections:
				if !ok {
					return
				}
				var next *Activity
				if activity, active := ActivityFromDetection(detection, w.options); active {
					next = &activity
				}
				select {
				case activities <- next:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	w.RunActivities(ctx, activities)
}

// RunActivities applies pre-built activities to Discord until ctx is cancelled or
// the channel is closed. A nil activity clears presence. The writer owns the IPC
// client on this single goroutine and reconnects with backoff when Discord is
// unavailable, re-applying the current activity once it reconnects.
func (w *Writer) RunActivities(ctx context.Context, activities <-chan *Activity) {
	var (
		current   *Activity
		connected bool
		retry     *time.Timer
		retryC    <-chan time.Time
	)

	stopRetry := func() {
		if retry == nil {
			return
		}
		if !retry.Stop() {
			select {
			case <-retry.C:
			default:
			}
		}
		retry = nil
		retryC = nil
	}

	scheduleRetry := func() {
		stopRetry()
		retry = time.NewTimer(w.retryDelay.Next())
		retryC = retry.C
	}

	clear := func() {
		current = nil
		stopRetry()
		w.retryDelay.Reset()
		if connected {
			_ = w.client.Logout()
			connected = false
		}
	}

	applyCurrent := func() {
		if current == nil {
			return
		}
		if !connected {
			if err := w.client.Login(w.appID); err != nil {
				scheduleRetry()
				return
			}
			connected = true
		}
		if err := w.client.SetActivity(*current); err != nil {
			connected = false
			scheduleRetry()
			return
		}
		stopRetry()
		w.retryDelay.Reset()
	}

	defer func() {
		stopRetry()
		if connected {
			_ = w.client.Logout()
		}
	}()

	for {
		select {
		case activity, ok := <-activities:
			if !ok {
				return
			}
			if activity == nil {
				clear()
				continue
			}
			current = activity
			applyCurrent()
		case <-retryC:
			retry = nil
			retryC = nil
			applyCurrent()
		case <-ctx.Done():
			return
		}
	}
}

type retryDelay struct {
	delays []time.Duration
	next   int
}

func newRetryDelay(delays []time.Duration) retryDelay {
	copied := append([]time.Duration(nil), delays...)
	if len(copied) == 0 {
		copied = append([]time.Duration(nil), defaultRetryDelays...)
	}
	return retryDelay{delays: copied}
}

func (r *retryDelay) Next() time.Duration {
	delay := r.delays[r.next]
	if r.next < len(r.delays)-1 {
		r.next++
	}
	if delay < 0 {
		return 0
	}
	return delay
}

func (r *retryDelay) Reset() {
	r.next = 0
}
