//go:build !windows

package detector

const DefaultCorroborateIdleWithCPU = false

func inactiveCollectionEligible(Process) bool {
	return false
}
