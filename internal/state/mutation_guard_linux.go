//go:build linux

package state

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func openLockMutationGuardFile(runDir string) (*os.File, error) {
	if regularDir(runDir) != nil {
		return nil, code("RUN_LOCK_GUARD_INVALID")
	}
	path := filepath.Join(runDir, lockMutationGuardFile)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != 0 ||
			info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0600 != 0600 {
			return nil, code("RUN_LOCK_GUARD_INVALID")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, code("RUN_LOCK_GUARD_CREATE_FAILED")
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EISDIR) {
			return nil, code("RUN_LOCK_GUARD_INVALID")
		}
		return nil, code("RUN_LOCK_GUARD_CREATE_FAILED")
	}
	file := os.NewFile(uintptr(fd), path)
	if err := validateLockMutationGuardFile(path, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateLockMutationGuardFile(path string, file *os.File) error {
	opened, err := file.Stat()
	if err != nil {
		return code("RUN_LOCK_GUARD_INVALID")
	}
	metadata, ok := opened.Sys().(*syscall.Stat_t)
	linked, err := os.Lstat(path)
	if err != nil || !linked.Mode().IsRegular() || linked.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, linked) ||
		opened.Size() != 0 || opened.Mode().Perm()&0077 != 0 || opened.Mode().Perm()&0600 != 0600 || !ok || metadata.Nlink != 1 {
		return code("RUN_LOCK_GUARD_INVALID")
	}
	return nil
}

func lockLockMutationGuardFile(file *os.File) (func() error, error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, code("RUN_LOCK_GUARD_HELD")
		}
		return nil, code("RUN_LOCK_GUARD_LOCK_FAILED")
	}
	return func() error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }, nil
}
