//go:build windows

package detector

const DefaultCorroborateIdleWithCPU = true

// Windows foreground and CPU checks decide which attached tool may be
// featured. A running tool in another terminal window is still part of the
// collection even though its synthetic atime makes it ineligible to headline.
func inactiveCollectionEligible(process Process) bool {
	if process.TTY.State != TTYResolved || process.TTY.DetachedTmux {
		return false
	}
	_, err := parseWindowsTTYPath(process.TTY.Path)
	return err == nil
}
