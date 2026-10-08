//go:build windows

package state

import (
	"errors"
	"syscall"
)

func probeLocalProcess(pid int) processLiveness {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return processUnknown
	}
	const processQueryLimitedInformation = 0x1000
	handle, err := syscall.OpenProcess(processQueryLimitedInformation|syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.Errno(87)) {
			return processDead
		}
		// Access denied and other API failures do not establish process death.
		return processUnknown
	}
	defer syscall.CloseHandle(handle)
	state, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return processUnknown
	}
	switch state {
	case syscall.WAIT_OBJECT_0:
		return processDead
	case syscall.WAIT_TIMEOUT:
		return processAlive
	default:
		return processUnknown
	}
}
