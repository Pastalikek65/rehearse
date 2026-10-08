package state

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMutationGuardExcludesAnotherProcessAndReleasesOnProcessExit(t *testing.T) {
	runDir := t.TempDir()
	child, childOutput, childInput := startMutationGuardChild(t, runDir)
	guardPath := filepath.Join(runDir, lockMutationGuardFile)
	before, err := os.Stat(guardPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != 0 {
		t.Fatalf("guard file contains data: %d bytes", before.Size())
	}

	if _, err := acquireLockMutationGuard(runDir); err == nil || err.Error() != "RUN_LOCK_GUARD_HELD" {
		t.Fatalf("second process was not excluded by the kernel lock: %v", err)
	}

	if _, err := io.WriteString(childInput, "release\n"); err != nil {
		t.Fatalf("release child guard: %v", err)
	}
	if got, err := childOutput.ReadString('\n'); err != nil || strings.TrimSpace(got) != "released" {
		t.Fatalf("child did not confirm guard release: line=%q err=%v", got, err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("guard child exited with error: %v", err)
	}

	after, err := os.Stat(guardPath)
	if err != nil {
		t.Fatalf("persistent guard file disappeared after process exit: %v", err)
	}
	if !os.SameFile(before, after) || after.Size() != 0 {
		t.Fatalf("guard path identity/data changed: sameFile=%t size=%d", os.SameFile(before, after), after.Size())
	}
	guard, err := acquireLockMutationGuard(runDir)
	if err != nil {
		t.Fatalf("kernel released dead child guard: %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("release parent guard: %v", err)
	}
}

func TestMutationGuardExcludesASecondHandleInTheSameProcess(t *testing.T) {
	runDir := t.TempDir()
	first, err := acquireLockMutationGuard(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLockMutationGuard(runDir); err == nil || err.Error() != "RUN_LOCK_GUARD_HELD" {
		t.Fatalf("second same-process handle was not excluded: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first same-process guard: %v", err)
	}
	second, err := acquireLockMutationGuard(runDir)
	if err != nil {
		t.Fatalf("second handle could not acquire released guard: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release second same-process guard: %v", err)
	}
}

func TestMutationGuardRejectsMalformedPersistentGuardFile(t *testing.T) {
	t.Run("non-empty file", func(t *testing.T) {
		runDir := t.TempDir()
		path := filepath.Join(runDir, lockMutationGuardFile)
		if err := os.WriteFile(path, []byte("must-not-be-overwritten"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := acquireLockMutationGuard(runDir); err == nil || err.Error() != "RUN_LOCK_GUARD_INVALID" {
			t.Fatalf("accepted non-empty mutation guard: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != "must-not-be-overwritten" {
			t.Fatalf("malformed guard file was changed: %q err=%v", raw, err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		runDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(runDir, lockMutationGuardFile), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := acquireLockMutationGuard(runDir); err == nil || err.Error() != "RUN_LOCK_GUARD_INVALID" {
			t.Fatalf("accepted directory as a mutation guard file: %v", err)
		}
	})
}

// TestMutationGuardChild runs only as an explicitly marked helper process.
// Parent tests hold and release a real kernel file lock across process handles.
func TestMutationGuardChild(t *testing.T) {
	if os.Getenv("REHEARSE_LOCK_GUARD_CHILD") != "1" {
		return
	}
	runDir := os.Getenv("REHEARSE_LOCK_GUARD_DIR")
	guard, err := acquireLockMutationGuard(runDir)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stdout, "error")
		t.Fatalf("child could not acquire mutation guard: %v", err)
	}
	defer guard.Release()
	_, _ = fmt.Fprintln(os.Stdout, "locked")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatalf("wait for parent release: %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("child guard release failed: %v", err)
	}
	_, _ = fmt.Fprintln(os.Stdout, "released")
}

func startMutationGuardChild(t *testing.T, runDir string) (*exec.Cmd, *bufio.Reader, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMutationGuardChild$")
	cmd.Env = appendFilteredEnv(os.Environ(), "REHEARSE_LOCK_GUARD_CHILD", "REHEARSE_LOCK_GUARD_DIR")
	cmd.Env = append(cmd.Env, "REHEARSE_LOCK_GUARD_CHILD=1", "REHEARSE_LOCK_GUARD_DIR="+runDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start mutation guard child: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	reader := bufio.NewReader(stdout)
	if got, err := reader.ReadString('\n'); err != nil || strings.TrimSpace(got) != "locked" {
		t.Fatalf("child did not acquire guard: line=%q err=%v", got, err)
	}
	return cmd, reader, stdin
}

func appendFilteredEnv(env []string, names ...string) []string {
	var filtered []string
	for _, entry := range env {
		matched := false
		for _, name := range names {
			if strings.HasPrefix(entry, name+"=") {
				matched = true
				break
			}
		}
		if !matched {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
