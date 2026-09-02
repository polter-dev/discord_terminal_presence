package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

// The pending-restart notice (#584) is a claim about the process that is
// running right now, but it used to be derived only from on-disk facts: a
// retained success record plus a binary at or above the recorded target. Those
// two facts are identical whether the daemon is still on the old code or the
// user already restarted, and the record does not always get retired on
// restart -- retirement is skipped outright while either update-check opt-out
// is in force (#463). The tests below pin the evidence that separates the two
// states: the daemon's own recorded start generation (#606).

// writeAttemptAt records a succeeded automatic update at a chosen instant, so
// these tests can order the daemon's start against it in both directions.
func writeAttemptAt(t *testing.T, target string, at time.Time) string {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "update.json")
	if err := updatepkg.RecordAutomaticUpdateAttempt(statePath, target, at, nil); err != nil {
		t.Fatalf("RecordAutomaticUpdateAttempt() error = %v", err)
	}
	return statePath
}

func pendingRestartNotice(t *testing.T, statePath string, daemon runningDaemonEvidence, now time.Time) string {
	t.Helper()
	return automaticUpdateStatus(statePath, true, "0.1.4", "linux", updatepkg.InstallGeneric, daemon, now)
}

// The regression this issue is about. The record survived the restart because
// nothing retired it, but the daemon reading it started after the install, so
// it cannot be running pre-update code and there is nothing to restart. Before
// the fix this printed the notice on every status run, forever.
func TestStatusStaysQuietWhenTheDaemonStartedAfterTheUpdate(t *testing.T) {
	now := time.Now()
	attemptedAt := now.Add(-2 * time.Hour)
	statePath := writeAttemptAt(t, "0.1.4", attemptedAt)

	daemon := runningDaemonEvidence{running: true, startedAt: attemptedAt.Add(time.Minute)}
	if got := pendingRestartNotice(t, statePath, daemon, now); got != "" {
		t.Fatalf("automaticUpdateStatus() for a daemon started after the update = %q, want empty", got)
	}
}

// The genuine #584 case must keep working: the daemon predates the install, so
// it really is still on the previous version.
func TestStatusReportsPendingRestartWhenTheDaemonStartedBeforeTheUpdate(t *testing.T) {
	now := time.Now()
	attemptedAt := now.Add(-2 * time.Hour)
	statePath := writeAttemptAt(t, "0.1.4", attemptedAt)

	daemon := runningDaemonEvidence{running: true, startedAt: attemptedAt.Add(-time.Minute)}
	got := pendingRestartNotice(t, statePath, daemon, now)
	for _, want := range []string{"installed 0.1.4", "still on the previous version", `"termp stop"`, `"termp start"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("automaticUpdateStatus() for a daemon started before the update = %q, want it to contain %q", got, want)
		}
	}
}

// No recorded start generation is no evidence at all -- an older PID record, or
// a daemon found only through the Discord-state fallback. Silence is the right
// answer: a missed reminder costs one delayed restart, a wrong one repeats
// advice that provably does nothing.
func TestStatusStaysQuietWhenTheDaemonStartIsUnknown(t *testing.T) {
	now := time.Now()
	statePath := writeAttemptAt(t, "0.1.4", now.Add(-2*time.Hour))

	daemon := runningDaemonEvidence{running: true}
	if got := pendingRestartNotice(t, statePath, daemon, now); got != "" {
		t.Fatalf("automaticUpdateStatus() with no recorded daemon start = %q, want empty", got)
	}
}

// Equal timestamps are not evidence the daemon predates the install. A start
// recorded at the same instant is at least as likely to be the post-update
// daemon, and timestamp serialization can collapse a real ordering into
// equality.
func TestStatusStaysQuietWhenTheDaemonStartEqualsTheAttempt(t *testing.T) {
	now := time.Now()
	attemptedAt := now.Add(-2 * time.Hour)
	statePath := writeAttemptAt(t, "0.1.4", attemptedAt)

	attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath)
	if !ok {
		t.Fatal("ReadAutomaticUpdateAttempt() reported no record")
	}
	daemon := runningDaemonEvidence{running: true, startedAt: attempt.AttemptedAt}
	if got := pendingRestartNotice(t, statePath, daemon, now); got != "" {
		t.Fatalf("automaticUpdateStatus() with a daemon start equal to the attempt = %q, want empty", got)
	}
}

// A record stamped in the future means a clock moved between writing it and
// reading it, so neither timestamp can order anything -- even though the daemon
// start compares as earlier. Silence rather than a reminder derived from a
// clock we know is wrong.
func TestStatusStaysQuietWhenTheAttemptIsStampedInTheFuture(t *testing.T) {
	now := time.Now()
	attemptedAt := now.Add(time.Hour)
	statePath := writeAttemptAt(t, "0.1.4", attemptedAt)

	daemon := runningDaemonEvidence{running: true, startedAt: now.Add(-time.Hour)}
	if got := pendingRestartNotice(t, statePath, daemon, now); got != "" {
		t.Fatalf("automaticUpdateStatus() for an attempt stamped in the future = %q, want empty", got)
	}
}

// The evidence has to actually exist in production, not just in these tests:
// the daemon writes its start wall clock into the PID record it publishes, and
// status reads it back.
func TestPIDRecordCarriesTheDaemonStartWallClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termp.pid")
	before := time.Now().Add(-time.Second)
	if _, err := writePIDOwned(path, 1234); err != nil {
		t.Fatalf("writePIDOwned() error = %v", err)
	}
	after := time.Now().Add(time.Second)

	record, _, err := readPIDIdentity(path)
	if err != nil {
		t.Fatalf("readPIDIdentity() error = %v", err)
	}
	if record.StartedAt.IsZero() {
		t.Fatal("PID record StartedAt is zero; status has no evidence about the running daemon's generation")
	}
	if record.StartedAt.Before(before) || record.StartedAt.After(after) {
		t.Fatalf("PID record StartedAt = %v, want it within [%v, %v]", record.StartedAt, before, after)
	}
}

// A PID record written by an older build has no started_at at all. It must
// parse, and it must read back as the zero time so the notice stays quiet
// rather than treating the epoch as an early start.
func TestOlderPIDRecordWithoutAStartWallClockParsesAsUnknown(t *testing.T) {
	record, err := parsePIDRecord([]byte(`{"pid":42,"start_time":7,"executable_path":"/usr/local/bin/termp"}`))
	if err != nil {
		t.Fatalf("parsePIDRecord() error = %v", err)
	}
	if !record.StartedAt.IsZero() {
		t.Fatalf("parsePIDRecord() StartedAt = %v, want the zero time for a record without started_at", record.StartedAt)
	}
	if daemonPredatesAutomaticUpdate(record.StartedAt, time.Now(), time.Now()) {
		t.Fatal("an unknown daemon start was treated as predating the update")
	}
}
