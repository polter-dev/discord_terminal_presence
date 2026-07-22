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

const (
	defaultMinWriteInterval = 15 * time.Second
	defaultReapplyInterval  = 15 * time.Second
)

// Writer consumes activities and owns all Discord IPC client calls.
type Writer struct {
	client Client
	appID  string

	options          DisplayOptions
	retryDelay       retryDelay
	minWriteInterval time.Duration
	reapplyInterval  time.Duration
	clock            writeClock
	debugf           func(string, ...any)
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

// WithDebugf enables optional diagnostic logging for connection and write state.
func WithDebugf(debugf func(string, ...any)) WriterOption {
	return func(writer *Writer) {
		if debugf != nil {
			writer.debugf = debugf
		}
	}
}

func withWriteClock(clock writeClock) WriterOption {
	return func(writer *Writer) {
		if clock != nil {
			writer.clock = clock
		}
	}
}

func withReapplyInterval(interval time.Duration) WriterOption {
	return func(writer *Writer) {
		writer.reapplyInterval = interval
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
		reapplyInterval:  defaultReapplyInterval,
		clock:            realWriteClock{},
		debugf:           func(string, ...any) {},
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
		retry     writeTimer
		retryC    <-chan time.Time
		write     writeTimer
		writeC    <-chan time.Time
		reapply   writeTimer
		reapplyC  <-chan time.Time
		lastWrite time.Time
		wrote     bool
		pending   bool
	)

	stopRetry := func() {
		if retry == nil {
			return
		}
		retry.Stop()
		retry = nil
		retryC = nil
	}

	stopReapply := func() {
		if reapply == nil {
			return
		}
		reapply.Stop()
		reapply = nil
		reapplyC = nil
	}

	scheduleReapply := func() {
		stopReapply()
		if w.reapplyInterval <= 0 || desired == nil {
			return
		}
		reapply = w.clock.NewTimer(w.reapplyInterval)
		reapplyC = reapply.C()
	}

	scheduleRetry := func() {
		stopRetry()
		stopReapply()
		delay := w.retryDelay.Next()
		w.debugf("presence reconnect scheduled in %s", delay)
		retry = w.clock.NewTimer(delay)
		retryC = retry.C()
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
		w.debugf("presence clear")
		desired = nil
		pending = false
		stopWrite()
		stopRetry()
		stopReapply()
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
			w.debugf("presence connect attempt")
			if err := w.client.Login(w.appID); err != nil {
				w.debugf("presence connect failed: %v", err)
				scheduleRetry()
				return
			}
			connected = true
		}
		if err := w.client.SetActivity(*desired); err != nil {
			w.debugf("presence push failed: %v", err)
			if connected {
				_ = w.client.Logout()
				connected = false
			}
			scheduleRetry()
			return
		}
		w.debugf("presence push: details=%q state=%q", desired.Details, desired.State)
		lastWrite = w.clock.Now()
		wrote = true
		pending = false
		stopWrite()
		stopRetry()
		w.retryDelay.Reset()
		scheduleReapply()
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
		stopReapply()
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
		case <-reapplyC:
			reapply = nil
			reapplyC = nil
			applyDesired()
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
