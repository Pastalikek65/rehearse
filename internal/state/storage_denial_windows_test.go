package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// This test establishes a real Windows sharing violation at the atomic
// replacement boundary, then verifies StageBackup fails closed and remains
// explicitly recoverable after the denial is removed.
func TestStageBackupRunMetadataFileShareDenialIsSafeAndRecoverable(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("synthetic-windows-sharing-test")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			t.Errorf("release run lock: %s", safeStateDiagnostic(err))
		}
	}()

	source := filepath.Join(t.TempDir(), "synthetic-source.dump")
	sourceBytes := []byte("PGDMPsynthetic-windows-file-sharing-fault")
	if err := os.WriteFile(source, sourceBytes, 0600); err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.Sum256(sourceBytes)

	// First prove this process can make a destination replacement fail by
	// opening the target without FILE_SHARE_DELETE.
	controlTarget := filepath.Join(t.TempDir(), "replace-target.json")
	controlTemp := filepath.Join(filepath.Dir(controlTarget), "replace-temp.json")
	if err := os.WriteFile(controlTarget, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlTemp, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	controlHandle := openWithoutDeleteShare(t, controlTarget)
	controlErr := os.Rename(controlTemp, controlTarget)
	if controlErr == nil {
		_ = syscall.CloseHandle(controlHandle)
		t.Fatal("control rename unexpectedly succeeded while delete sharing was denied")
	}
	var controlErrno syscall.Errno
	if !errors.As(controlErr, &controlErrno) || controlErrno == 0 {
		_ = syscall.CloseHandle(controlHandle)
		t.Fatalf("control rename did not expose an OS errno: type=%T", controlErr)
	}
	if err := syscall.CloseHandle(controlHandle); err != nil {
		t.Fatalf("close control handle: errno=%d", windowsTestErrno(err))
	}
	if err := os.Remove(controlTemp); err != nil {
		t.Fatal(err)
	}

	runDir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	metadataHandle := openWithoutDeleteShare(t, filepath.Join(runDir, "run.json"))
	_, stageErr := store.StageBackup(context.Background(), lock, source)
	if stageErr == nil || stageErr.Error() != "STATE_WRITE_FAILED" {
		_ = syscall.CloseHandle(metadataHandle)
		t.Fatalf("StageBackup public error = %v, want STATE_WRITE_FAILED", stageErr)
	}
	diagnostic, ok := DiagnosticFor(stageErr)
	if !ok || diagnostic.Code != "STATE_WRITE_FAILED" || diagnostic.Operation != "rename" || diagnostic.Errno != int(controlErrno) {
		_ = syscall.CloseHandle(metadataHandle)
		t.Fatalf("StageBackup diagnostic = %+v, present=%t; control errno=%d", diagnostic, ok, uint32(controlErrno))
	}
	t.Logf("controlled_windows_file_share_denial code=%s operation=%s errno=%d", diagnostic.Code, diagnostic.Operation, diagnostic.Errno)
	if err := syscall.CloseHandle(metadataHandle); err != nil {
		t.Fatalf("close metadata handle: errno=%d", windowsTestErrno(err))
	}

	unchanged, err := os.ReadFile(source)
	if err != nil || string(unchanged) != string(sourceBytes) || sha256.Sum256(unchanged) != sourceHash {
		t.Fatalf("source backup changed after controlled rejection: readErr=%v", err)
	}
	loaded, err := store.Load(run.ID)
	if err != nil || loaded.Status != "planned" || loaded.Backup != nil || loaded.PendingBackup != nil {
		t.Fatalf("run state after rejected stage: status=%q backup=%t pending=%t err=%v", loaded.Status, loaded.Backup != nil, loaded.PendingBackup != nil, err)
	}
	if _, err := os.Lstat(filepath.Join(runDir, "backup.partial")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial backup remained after rejected first metadata commit: existsErr=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(runDir, "backup.dump")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final backup appeared after rejected first metadata commit: existsErr=%v", err)
	}
	if _, err := store.OpenVerifiedBackup(run.ID); err == nil || err.Error() != "BACKUP_NOT_STAGED" {
		t.Fatalf("rejected stage was accepted as a verified backup: %v", err)
	}
	for _, name := range []string{"report.json", "report.html", "report.finalizing"} {
		if _, err := os.Lstat(filepath.Join(runDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("report artifact %s exists after failed staging", name)
		}
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("release after controlled failure: %s", safeStateDiagnostic(err))
	}
	lock, err = store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatalf("reacquire for explicit recovery: %s", safeStateDiagnostic(err))
	}
	if _, err := store.RecoverBackup(context.Background(), lock, source); err == nil || err.Error() != "BACKUP_RECOVERY_NOT_NEEDED" {
		t.Fatalf("explicit recovery of clean rejected staging state = %v", err)
	}
	restaged, err := store.StageBackup(context.Background(), lock, source)
	if err != nil {
		t.Fatalf("restage after removing sharing denial: %s (diagnostic=%s)", err, safeStateDiagnostic(err))
	}
	if restaged.Bytes != int64(len(sourceBytes)) || restaged.SHA256 != hex.EncodeToString(sourceHash[:]) {
		t.Fatalf("restaged metadata differs from source: bytes=%d hashMatches=%t", restaged.Bytes, restaged.SHA256 == hex.EncodeToString(sourceHash[:]))
	}
	verified, err := store.OpenVerifiedBackup(run.ID)
	if err != nil {
		t.Fatalf("open verified restaged backup: %v", err)
	}
	verifiedBytes, readErr := io.ReadAll(verified)
	closeErr := verified.Close()
	if readErr != nil || closeErr != nil || string(verifiedBytes) != string(sourceBytes) {
		t.Fatalf("verified restaged bytes mismatch: readErr=%v closeErr=%v", readErr, closeErr)
	}
	loaded, err = store.Load(run.ID)
	if err != nil || loaded.Status != "staged" || loaded.Backup == nil || loaded.PendingBackup != nil {
		t.Fatalf("run state after explicit restaging: status=%q backup=%t pending=%t err=%v", loaded.Status, loaded.Backup != nil, loaded.PendingBackup != nil, err)
	}
}

func openWithoutDeleteShare(t *testing.T, path string) syscall.Handle {
	t.Helper()
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal("convert controlled test path to UTF-16")
	}
	handle, err := syscall.CreateFile(path16, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("open controlled test handle: errno=%d", windowsTestErrno(err))
	}
	return handle
}

func windowsTestErrno(err error) int {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0
	}
	return int(errno)
}
