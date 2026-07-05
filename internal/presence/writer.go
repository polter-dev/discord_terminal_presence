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

const defaultMinWriteInterval = 15 * time.Second

// Writer consumes activities and owns all Discord IPC client calls.
type Writer struct {
	client Client
	appID  string

	options          DisplayOptions
	retryDelay       retryDelay
	minWriteInterval time.Duration
	clock            writeClock
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

// WithMinWriteInterval replaces the minimum interval between SetActivity calls.
func WithMinWriteInterval(interval time.Duration) WriterOption {
	return func(writer *Writer) {
		writer.minWriteInterval = interval
	}
}

func withWriteClock(clock writeClock) WriterOption {
	return func(writer *Writer) {
		if clock != nil {
			writer.clock = clock
		}
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
		client:           client,
		appID:            appID,
		options:          DefaultDisplayOptions(),
		retryDelay:       newRetryDelay(defaultRetryDelays),
		minWriteInterval: defaultMinWriteInterval,
		clock:            realWriteClock{},
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
		desired   *Activity
		connected bool
		retry     *time.Timer
		retryC    <-chan time.Time
		write     writeTimer
		writeC    <-chan time.Time
		lastWrite time.Time
		wrote     bool
		pending   bool
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

	stopWrite := func() {
		if write == nil {
			return
		}
		write.Stop()
		write = nil
		writeC = nil
	}

	scheduleWrite := func(delay time.Duration) {
		stopWrite()
		write = w.clock.NewTimer(delay)
		writeC = write.C()
	}

	clear := func() {
		desired = nil
		pending = false
		stopWrite()
		stopRetry()
		w.retryDelay.Reset()
		if connected {
			_ = w.client.Logout()
			connected = false
		}
	}

	applyDesired := func() {
		if desired == nil {
			return
		}
		if !connected {
			if err := w.client.Login(w.appID); err != nil {
				scheduleRetry()
				return
			}
			connected = true
		}
		if err := w.client.SetActivity(*desired); err != nil {
			connected = false
			scheduleRetry()
			return
		}
		lastWrite = w.clock.Now()
		wrote = true
		pending = false
		stopWrite()
		stopRetry()
		w.retryDelay.Reset()
	}

	requestApply := func() {
		if desired == nil {
			return
		}
		if w.minWriteInterval <= 0 || !wrote {
			applyDesired()
			return
		}
		elapsed := w.clock.Now().Sub(lastWrite)
		if elapsed >= w.minWriteInterval {
			applyDesired()
			return
		}
		pending = true
		scheduleWrite(w.minWriteInterval - elapsed)
	}

	defer func() {
		stopRetry()
		stopWrite()
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
			desired = activity
			requestApply()
		case <-retryC:
			retry = nil
			retryC = nil
			applyDesired()
		case <-writeC:
			write = nil
			writeC = nil
			if pending {
				applyDesired()
			}
		case <-ctx.Done():
			return
		}
	}
}

type writeClock interface {
	Now() time.Time
	NewTimer(time.Duration) writeTimer
}

type writeTimer interface {
	C() <-chan time.Time
	Stop()
}

type realWriteClock struct{}

func (realWriteClock) Now() time.Time {
	return time.Now()
}

func (realWriteClock) NewTimer(delay time.Duration) writeTimer {
	if delay < 0 {
		delay = 0
	}
	return &realWriteTimer{timer: time.NewTimer(delay)}
}

type realWriteTimer struct {
	timer *time.Timer
}

func (t *realWriteTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t *realWriteTimer) Stop() {
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
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
