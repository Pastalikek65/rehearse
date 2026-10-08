//go:build windows

package state

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusiveLock   = 0x00000002
	fileAttributeReparse    = 0x00000400
)

var (
	lockFileExProc   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	unlockFileExProc = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

func openLockMutationGuardFile(runDir string) (*os.File, error) {
	if regularDir(runDir) != nil {
		return nil, code("RUN_LOCK_GUARD_INVALID")
	}
	path := filepath.Join(runDir, lockMutationGuardFile)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != 0 {
			return nil, code("RUN_LOCK_GUARD_INVALID")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, code("RUN_LOCK_GUARD_CREATE_FAILED")
	}
	const shareReadWrite = syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE
	handle, err := syscall.CreateFile(
		syscall.StringToUTF16Ptr(path), syscall.GENERIC_READ|syscall.GENERIC_WRITE, shareReadWrite,
		nil, syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, code("RUN_LOCK_GUARD_CREATE_FAILED")
	}
	file := os.NewFile(uintptr(handle), path)
	if err := validateLockMutationGuardFile(path, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateLockMutationGuardFile(path string, file *os.File) error {
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != 0 {
		return code("RUN_LOCK_GUARD_INVALID")
	}
	linked, err := os.Lstat(path)
	if err != nil || !linked.Mode().IsRegular() || linked.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, linked) {
		return code("RUN_LOCK_GUARD_INVALID")
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil ||
		info.FileAttributes&fileAttributeReparse != 0 || info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0 ||
		info.FileSizeHigh != 0 || info.FileSizeLow != 0 || info.NumberOfLinks != 1 {
		return code("RUN_LOCK_GUARD_INVALID")
	}
	return nil
}

func lockLockMutationGuardFile(file *os.File) (func() error, error) {
	// Keep the OVERLAPPED memory alive until UnlockFileEx is called. The file
	// handle is synchronous; FAIL_IMMEDIATELY prevents waiting on another owner.
	overlapped := new(syscall.Overlapped)
	r1, _, callErr := lockFileExProc.Call(
		file.Fd(), uintptr(lockFileExclusiveLock|lockFileFailImmediately), 0, 1, 0,
		uintptr(unsafe.Pointer(overlapped)))
	if r1 == 0 {
		if errors.Is(callErr, syscall.Errno(33)) {
			return nil, code("RUN_LOCK_GUARD_HELD")
		}
		return nil, code("RUN_LOCK_GUARD_LOCK_FAILED")
	}
	unlock := func() error {
		r1, _, err := unlockFileExProc.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(overlapped)))
		if r1 == 0 {
			return err
		}
		return nil
	}
	return unlock, nil
}
