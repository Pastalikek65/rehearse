//go:build linux

package state

import (
	"errors"
	"syscall"
)

func probeLocalProcess(pid int) processLiveness {
	if pid <= 0 {
		return processUnknown
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return processAlive
	}
	if errors.Is(err, syscall.ESRCH) {
		return processDead
	}
	// EPERM and every other failure are conservatively unknown. In particular,
	// lack of permission is never interpreted as proof that the process died.
	return processUnknown
}
