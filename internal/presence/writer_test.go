package presence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func TestWriterNoneDetectionClearsPresence(t *testing.T) {
	client := newFakeClient(nil)
	writer, err := NewWriter(client, "app-id", WithRetryDelays(0))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	detections := make(chan detector.Detection, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer.Run(ctx, detections)
	}()

	detections <- activeDetection(time.Now())
	client.waitForSet(t, 1)
	detections <- detector.Detection{None: true}
	client.waitForLogout(t, 1)

	cancel()
	<-done
}

func TestWriterReconnectsAndReappliesActivity(t *testing.T) {
	client := newFakeClient([]error{errors.New("discord unavailable"), nil})
	writer, err := NewWriter(client, "", WithRetryDelays(0))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	detections := make(chan detector.Detection, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer.Run(ctx, detections)
	}()

	startedAt := time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC)
	detections <- activeDetection(startedAt)
	client.waitForSet(t, 1)

	if got := client.loginCount(); got < 2 {
		t.Fatalf("login count = %d, want at least 2", got)
	}
	if got := client.loginAppIDs(); len(got) != 2 || got[0] != DefaultAppID || got[1] != DefaultAppID {
		t.Fatalf("login app IDs = %#v, want two placeholder logins", got)
	}
	activities := client.activities()
	if len(activities) != 1 {
		t.Fatalf("set activities len = %d, want 1", len(activities))
	}
	if activities[0].Details != "Using Claude Code" {
		t.Fatalf("details = %q, want Claude activity", activities[0].Details)
	}
	if activities[0].StartTimestamp == nil || !activities[0].StartTimestamp.Equal(startedAt) {
		t.Fatalf("start timestamp = %v, want %v", activities[0].StartTimestamp, startedAt)
	}

	cancel()
	<-done
}

func TestWriterThrottlesAndCoalescesActivityUpdates(t *testing.T) {
	client := newFakeClient(nil)
	clock := newFakeWriteClock(time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC))
	writer, err := NewWriter(client, "app-id",
		WithRetryDelays(0),
		WithMinWriteInterval(15*time.Second),
		withWriteClock(clock),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activities := make(chan *Activity)
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer.RunActivities(ctx, activities)
	}()

	sendActivity(t, ctx, activities, &Activity{Details: "first"})
	client.waitForSet(t, 1)
	sendActivity(t, ctx, activities, &Activity{Details: "second"})
	clock.waitForTimerCount(t, 1)
	sendActivity(t, ctx, activities, &Activity{Details: "third"})
	clock.waitForTimerCount(t, 2)

	if got := len(client.activities()); got != 1 {
		t.Fatalf("set count before interval = %d, want 1", got)
	}

	clock.Advance(15 * time.Second)
	client.waitForSet(t, 2)
	sets := client.activities()
	if sets[1].Details != "third" {
		t.Fatalf("coalesced details = %q, want third", sets[1].Details)
	}

	cancel()
	<-done
}

func TestWriterClearsPromptlyWhileUpdateIsThrottled(t *testing.T) {
	client := newFakeClient(nil)
	clock := newFakeWriteClock(time.Date(2026, 7, 4, 13, 0, 0, 0, time.UTC))
	writer, err := NewWriter(client, "app-id",
		WithRetryDelays(0),
		WithMinWriteInterval(15*time.Second),
		withWriteClock(clock),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activities := make(chan *Activity)
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer.RunActivities(ctx, activities)
	}()

	sendActivity(t, ctx, activities, &Activity{Details: "first"})
	client.waitForSet(t, 1)
	sendActivity(t, ctx, activities, &Activity{Details: "pending"})
	clock.waitForTimerCount(t, 1)
	sendActivity(t, ctx, activities, nil)
	client.waitForLogout(t, 1)

	clock.Advance(15 * time.Second)
	if got := len(client.activities()); got != 1 {
		t.Fatalf("set count after clear = %d, want 1", got)
	}

	cancel()
	<-done
}

func sendActivity(t *testing.T, ctx context.Context, ch chan<- *Activity, activity *Activity) {
	t.Helper()
	select {
	case ch <- activity:
	case <-ctx.Done():
		t.Fatal("context ended while sending activity")
	case <-time.After(time.Second):
		t.Fatal("timed out sending activity")
	}
}

func activeDetection(startedAt time.Time) detector.Detection {
	return detector.Detection{
		Tool: registry.Tool{
			ID:          "claude-code",
			DisplayName: "Claude Code",
			ImageKey:    "claude-code",
		},
		Cwd:       "/tmp/project",
		StartedAt: startedAt,
	}
}

type fakeWriteClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeWriteTimer
	changed chan struct{}
}

func newFakeWriteClock(now time.Time) *fakeWriteClock {
	return &fakeWriteClock{
		now:     now,
		changed: make(chan struct{}),
	}
}

func (f *fakeWriteClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeWriteClock) NewTimer(delay time.Duration) writeTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	timer := &fakeWriteTimer{
		deadline: f.now.Add(delay),
		ch:       make(chan time.Time, 1),
	}
	f.timers = append(f.timers, timer)
	f.notifyLocked()
	return timer
}

func (f *fakeWriteClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	timers := append([]*fakeWriteTimer(nil), f.timers...)
	f.mu.Unlock()

	for _, timer := range timers {
		timer.fire(now)
	}
}

func (f *fakeWriteClock) waitForTimerCount(t *testing.T, count int) {
	t.Helper()
	f.waitFor(t, func() bool {
		return len(f.timers) >= count
	})
}

func (f *fakeWriteClock) waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		if ready() {
			f.mu.Unlock()
			return
		}
		ch := f.changed
		f.mu.Unlock()

		select {
		case <-ch:
		case <-deadline:
			t.Fatal("timed out waiting for fake clock")
		}
	}
}

func (f *fakeWriteClock) notifyLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}

type fakeWriteTimer struct {
	mu       sync.Mutex
	deadline time.Time
	stopped  bool
	fired    bool
	ch       chan time.Time
}

func (f *fakeWriteTimer) C() <-chan time.Time {
	return f.ch
}

func (f *fakeWriteTimer) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

func (f *fakeWriteTimer) fire(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped || f.fired || now.Before(f.deadline) {
		return
	}
	f.fired = true
	f.ch <- now
}

type fakeClient struct {
	mu sync.Mutex

	loginErrs []error
	appIDs    []string

	setActivities []Activity
	logoutCalls   int

	setChanged    chan struct{}
	logoutChanged chan struct{}
}

func newFakeClient(loginErrs []error) *fakeClient {
	return &fakeClient{
		loginErrs:     append([]error(nil), loginErrs...),
		setChanged:    make(chan struct{}),
		logoutChanged: make(chan struct{}),
	}
}

func (f *fakeClient) Login(appID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.appIDs = append(f.appIDs, appID)
	if len(f.loginErrs) == 0 {
		return nil
	}
	err := f.loginErrs[0]
	f.loginErrs = f.loginErrs[1:]
	return err
}

func (f *fakeClient) SetActivity(activity Activity) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.setActivities = append(f.setActivities, activity)
	close(f.setChanged)
	f.setChanged = make(chan struct{})
	return nil
}

func (f *fakeClient) Logout() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.logoutCalls++
	close(f.logoutChanged)
	f.logoutChanged = make(chan struct{})
	return nil
}

func (f *fakeClient) waitForSet(t *testing.T, want int) {
	t.Helper()
	f.waitFor(t, func() bool {
		return len(f.setActivities) >= want
	}, func() <-chan struct{} {
		return f.setChanged
	})
}

func (f *fakeClient) waitForLogout(t *testing.T, want int) {
	t.Helper()
	f.waitFor(t, func() bool {
		return f.logoutCalls >= want
	}, func() <-chan struct{} {
		return f.logoutChanged
	})
}

func (f *fakeClient) waitFor(t *testing.T, ready func() bool, changed func() <-chan struct{}) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		if ready() {
			f.mu.Unlock()
			return
		}
		ch := changed()
		f.mu.Unlock()

		select {
		case <-ch:
		case <-deadline:
			t.Fatal("timed out waiting for fake client call")
		}
	}
}

func (f *fakeClient) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.appIDs)
}

func (f *fakeClient) loginAppIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.appIDs...)
}

func (f *fakeClient) activities() []Activity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Activity(nil), f.setActivities...)
}
