package main

import (
	"fmt"
	"io"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
)

// readOnlyConfigLoader is the settled-but-not-horizon-extended config loader
// used by call sites that only read config (never save it back): the
// pre-dispatch update-alert check and status's own load. It is a package
// var, rather than a direct config.LoadReadOnly reference, so tests can
// substitute a counting stub and assert on call count instead of timing
// (issue #442).
var readOnlyConfigLoader = config.LoadReadOnly

// maybePrintCommandUpdateAlert loads config and prints the cached-update
// alert for commands that are eligible for it and do not already load config
// themselves for other work. setup and settings load config for their own
// real work and print this same alert from that result (see setup/settings),
// so skipping them here avoids paying config's settle/horizon wait twice for
// no benefit (issue #442).
func maybePrintCommandUpdateAlert(command string, args []string, interactive bool, stderr io.Writer) {
	if commandsLoadConfigForOwnAlert[command] || !eligibleForUpdateAlert(command, args, interactive) {
		return
	}
	cfg, loadErr := loadConfigWithNotice(readOnlyConfigLoader, stderr)
	printCommandUpdateAlert(command, args, interactive, cfg, loadErr, stderr)
}

// checkingConfigNoticeDelay bounds how long a config load may run silently
// before the CLI explains the pause on stderr. A stable existing config or a
// missing first-run file both resolve in a few milliseconds and never reach
// this delay. A genuinely blank or continuously rewritten config settles for
// config's standalone bound (~500ms) or, for setup/settings, the full 3s
// enabled-loosening horizon (#434/#438) — without a notice that looks like a
// hang rather than a deliberate, safe wait (issue #442).
const checkingConfigNoticeDelay = 150 * time.Millisecond

// loadConfigWithNotice runs load and, if it has not returned within
// checkingConfigNoticeDelay, prints a single explanatory line to stderr. The
// horizon/settle wait itself is unchanged; this only explains a pause that
// would otherwise look identical to a frozen prompt.
func loadConfigWithNotice(load func() (config.Config, error), stderr io.Writer) (config.Config, error) {
	return loadConfigWithNoticeAfter(load, checkingConfigNoticeDelay, stderr)
}

// loadConfigWithNoticeAfter is loadConfigWithNotice with an injectable delay
// so tests can exercise both the silent fast path and the announced slow
// path deterministically, without depending on real settle timing.
func loadConfigWithNoticeAfter(load func() (config.Config, error), delay time.Duration, stderr io.Writer) (config.Config, error) {
	type result struct {
		cfg config.Config
		err error
	}
	done := make(chan result, 1)
	go func() {
		cfg, err := load()
		done <- result{cfg, err}
	}()

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.cfg, r.err
	case <-timer.C:
		fmt.Fprintln(stderr, "checking config…")
		r := <-done
		return r.cfg, r.err
	}
}
