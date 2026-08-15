package config

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadPathReadOnlyReportsUnsettledSnapshot is the #552 regression. A
// read-only load of a file that never holds still still returns the newest
// readable snapshot, because its callers have useful non-config output to
// print and a refusal would leave them with nothing to say. What it must not
// do is present that snapshot as the user's configuration: the bounded settle
// declined to certify these bytes, and the flag saying so used to be dropped
// on the floor at the load boundary.
func TestLoadPathReadOnlyReportsUnsettledSnapshot(t *testing.T) {
	now := time.Unix(0, 0)
	sleep := func(delay time.Duration) { now = now.Add(delay) }
	reads := 0
	// Every read returns different, individually valid TOML: a writer that
	// never settles, not a corrupt file.
	snapshot := func(string) fileSnapshot {
		reads++
		return fileSnapshot{
			exists: true,
			data:   []byte(fmt.Sprintf("enabled = true\npin = %q\n", fmt.Sprintf("revision-%d", reads))),
		}
	}

	cfg, settled, err := loadPathReadOnlyWith("config.toml", snapshot, func() time.Time { return now }, sleep)
	if err != nil {
		t.Fatalf("loadPathReadOnlyWith() error = %v, want the newest snapshot and no error", err)
	}
	if settled {
		t.Fatal("loadPathReadOnlyWith() certified a file that changed on every read; callers cannot tell a guess from the saved config")
	}
	// The load must still be useful: the caller gets the newest bytes, it
	// just may not present them as authoritative.
	if cfg.Pin == "" {
		t.Fatalf("loadPathReadOnlyWith() pin = %q, want the newest readable snapshot", cfg.Pin)
	}
	if reads < 2 {
		t.Fatalf("snapshot reads = %d, want the settle loop to have actually run", reads)
	}
}

// TestLoadPathReadOnlyCertifiesSavedConfig is the positive control for the
// test above: without it, a settle verdict hardwired to false would satisfy
// the regression and make every read-only caller cry wolf on a config that is
// perfectly stable.
func TestLoadPathReadOnlyCertifiesSavedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = false\npin = \"vim\"\n")

	cfg, settled, err := LoadPathReadOnly(path)
	if err != nil {
		t.Fatalf("LoadPathReadOnly() error = %v", err)
	}
	if !settled {
		t.Fatal("LoadPathReadOnly() reported a stable saved config as unsettled")
	}
	if cfg.Enabled || cfg.Pin != "vim" {
		t.Fatalf("LoadPathReadOnly() = %#v, want the saved config", cfg)
	}
}

// TestLoadPathReadOnlyUnsettledSnapshotCanContradictTheSavedConfig states the
// harm in one assertion: the bytes a read-only load returns without a settle
// verdict can say the opposite of what the user saved. The caller is only
// entitled to describe this as the user's configuration when settled is true,
// which is why the flag is now part of the load's result.
func TestLoadPathReadOnlyUnsettledSnapshotCanContradictTheSavedConfig(t *testing.T) {
	const saved = "enabled = false\npin = \"vim\"\n"

	now := time.Unix(0, 0)
	sleep := func(delay time.Duration) { now = now.Add(delay) }
	reads := 0
	// A non-atomic save in flight: the file holds a partial that parses and
	// omits the user's opt-out, and it keeps growing, so it never settles.
	snapshot := func(string) fileSnapshot {
		reads++
		return fileSnapshot{
			exists: true,
			data:   []byte(fmt.Sprintf("pin = \"vim\"\nscan_interval = \"%ds\"\n", reads)),
		}
	}

	cfg, settled, err := loadPathReadOnlyWith("config.toml", snapshot, func() time.Time { return now }, sleep)
	if err != nil {
		t.Fatalf("loadPathReadOnlyWith() error = %v", err)
	}
	if settled {
		t.Fatal("loadPathReadOnlyWith() certified a partial write that omits the saved opt-out")
	}
	// The precondition of the harm, asserted rather than assumed: the
	// uncertified snapshot really does contradict the saved file.
	savedCfg, savedSettled, err := func() (Config, bool, error) {
		path := filepath.Join(t.TempDir(), "config.toml")
		writeConfig(t, path, saved)
		return LoadPathReadOnly(path)
	}()
	if err != nil || !savedSettled {
		t.Fatalf("saved config load = (%v, settled %t), want a clean settled load", err, savedSettled)
	}
	if savedCfg.Enabled {
		t.Fatal("precondition not established: the saved fixture does not opt out")
	}
	if !cfg.Enabled {
		t.Fatal("precondition not established: the partial write does not diverge from the saved config")
	}
}
