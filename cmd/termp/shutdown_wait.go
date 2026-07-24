package main

type shutdownWaitResult int

const (
	shutdownWaitStop shutdownWaitResult = iota
	shutdownWaitCancel
)

func runShutdownSignalWait(wait func() (shutdownWaitResult, error), cancel func()) {
	result, err := wait()
	if err != nil {
		debugf("shutdown signal wait stopped: %v", err)
		return
	}
	if result == shutdownWaitCancel {
		cancel()
	}
}
