package detector

import "github.com/polter-dev/discord_terminal_presence/internal/registry"

// testActiveDetection stands in for the exported ActiveDetection and
// (*Detector).ActiveDetection helpers deleted for #568. Both always called
// Select with a nil enricher, so Process.Owned was never set from a real
// scan and both always returned Detection{None: true} against real process
// data (0 of 1074 real processes came back Owned=true). Nothing outside
// tests referenced either helper; production code uses
// ActiveDetectionWithPresence, which does pass a real enricher. Tests below
// that exercise pure matching/selection logic (not ownership) keep doing so
// through this helper, with Owned set explicitly on each Process literal.
func testActiveDetection(reg *registry.Registry, processes []Process) Detection {
	return NewSelector(reg, Config{ActivitySwitching: true}, systemClock{}).Select(processes)
}
