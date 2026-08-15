package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
)

// TestCommandUpdateAlertReportsUnsettledConfig is the cmd-side half of #552.
// When the read-only load cannot certify the bytes it decoded, the CLI must
// not quietly act on them: the update alert is gated on update_check and
// auto_update, either of which may have been turned off in the bytes that
// failed to settle. The user gets a sentence saying what happened and what to
// do instead of silence.
func TestCommandUpdateAlertReportsUnsettledConfig(t *testing.T) {
	unsettled := config.Default()
	unsettled.AutoUpdate = false
	unsettled.UpdateCheck = true

	restore := readOnlyConfigLoader
	readOnlyConfigLoader = func() (config.Config, bool, error) {
		return unsettled, false, nil
	}
	t.Cleanup(func() { readOnlyConfigLoader = restore })

	var stderr bytes.Buffer
	maybePrintCommandUpdateAlert("start", nil, true, &stderr)

	got := stderr.String()
	if !strings.Contains(got, unsettledConfigNotice) {
		t.Fatalf("stderr = %q, want the unsettled-config notice", got)
	}
	if strings.Contains(got, "is available") {
		t.Fatalf("stderr = %q, want no update alert from a config termp could not confirm", got)
	}
}

// TestCommandUpdateAlertStaysSilentForSettledConfig is the positive control:
// the notice must fire on the uncertified case only, or every ordinary run
// would grow a line of noise.
func TestCommandUpdateAlertStaysSilentForSettledConfig(t *testing.T) {
	restore := readOnlyConfigLoader
	readOnlyConfigLoader = func() (config.Config, bool, error) {
		return config.Default(), true, nil
	}
	t.Cleanup(func() { readOnlyConfigLoader = restore })

	var stderr bytes.Buffer
	maybePrintCommandUpdateAlert("start", nil, true, &stderr)

	if got := stderr.String(); strings.Contains(got, unsettledConfigNotice) {
		t.Fatalf("stderr = %q, want no unsettled-config notice for a certified config", got)
	}
}

// TestUnsettledConfigMessagesAreActionable guards the wording rules the repo
// keeps re-learning: an unactionable notice is a standing complaint (#472),
// and the start-command variant has to name the file it could not confirm
// because the daemon it is about to spawn will not print anything itself.
func TestUnsettledConfigMessagesAreActionable(t *testing.T) {
	if !strings.Contains(unsettledConfigNotice, "Re-run this command") {
		t.Fatalf("unsettledConfigNotice = %q, want an action the user can take", unsettledConfigNotice)
	}
	startNotice := startupUnsettledConfigNotice("/home/u/.config/termp/config.toml")
	if !strings.Contains(startNotice, "/home/u/.config/termp/config.toml") {
		t.Fatalf("startupUnsettledConfigNotice() = %q, want the config path", startNotice)
	}
	if !strings.Contains(startNotice, "termp status") {
		t.Fatalf("startupUnsettledConfigNotice() = %q, want an action the user can take", startNotice)
	}
	if strings.Contains(unsettledConfigWarning, "presence is off") {
		t.Fatalf("unsettledConfigWarning = %q, must not claim a presence state it did not check", unsettledConfigWarning)
	}
}
