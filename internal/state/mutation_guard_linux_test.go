//go:build linux

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMutationGuardRejectsHardLinkAlias(t *testing.T) {
	runDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "unrelated-empty-file")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	guardPath := filepath.Join(runDir, lockMutationGuardFile)
	if err := os.Link(target, guardPath); err != nil {
		t.Skipf("filesystem does not support hard links: %v", err)
	}
	if _, err := acquireLockMutationGuard(runDir); err == nil || err.Error() != "RUN_LOCK_GUARD_INVALID" {
		t.Fatalf("accepted a hard-link alias as a mutation guard: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Size() != 0 {
		t.Fatalf("malformed guard validation changed target file: info=%v err=%v", info, err)
	}
}
