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
