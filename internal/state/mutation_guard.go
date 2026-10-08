package state

import (
	"os"
	"sync"
)

const lockMutationGuardFile = ".lock-mutation.guard"

// lockMutationGuard owns a kernel lock on a persistent file in one run
// directory. The file must never be removed: a later caller must lock the
// same filesystem object rather than a replacement created at the same path.
type lockMutationGuard struct {
	mu     sync.Mutex
	file   *os.File
	unlock func() error
}

func acquireLockMutationGuard(runDir string) (*lockMutationGuard, error) {
	file, err := openLockMutationGuardFile(runDir)
	if err != nil {
		return nil, err
	}
	unlock, err := lockLockMutationGuardFile(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &lockMutationGuard{file: file, unlock: unlock}, nil
}

// Release unlocks the same open file handle used for acquisition. Closing the
// handle also releases the kernel lock if explicit unlock reports an error.
func (g *lockMutationGuard) Release() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.file == nil {
		return nil
	}
	file := g.file
	unlock := g.unlock
	g.file = nil
	g.unlock = nil
	unlockErr := unlock()
	closeErr := file.Close()
	if unlockErr != nil || closeErr != nil {
		return code("RUN_LOCK_GUARD_RELEASE_FAILED")
	}
	return nil
}
