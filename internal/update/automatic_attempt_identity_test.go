package update

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAutomaticUpdateAttemptRoundTripsInstallerIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	attemptedAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if err := RecordAutomaticUpdateAttemptForProcess(path, "0.1.5", attemptedAt, nil, 4242, 987654); err != nil {
		t.Fatal(err)
	}

	attempt, ok := ReadAutomaticUpdateAttempt(path)
	if !ok {
		t.Fatal("automatic update attempt was not recorded")
	}
	if attempt.InstallerPID != 4242 || attempt.InstallerStartTime != 987654 {
		t.Fatalf("installer identity = (%d, %d), want (4242, 987654)", attempt.InstallerPID, attempt.InstallerStartTime)
	}
}
