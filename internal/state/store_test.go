package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIntentSurvivesReopenBeforeDockerCreation(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OwnerID != run.OwnerID || loaded.DaemonID != "test-daemon" || loaded.Status != "planned" {
		t.Fatalf("intent not preserved: %#v", loaded)
	}
	if len(loaded.Resources) != 18 {
		t.Fatalf("want three independent phases with six resources each, got %d", len(loaded.Resources))
	}
	names := map[string]bool{}
	for _, r := range loaded.Resources {
		if names[r.Name] {
			t.Fatal("duplicate resource")
		}
		names[r.Name] = true
		if r.Labels["io.rehearse.owner"] != loaded.OwnerID || r.Labels["io.rehearse.run"] != loaded.ID || r.Labels["io.rehearse.kind"] != r.Role {
			t.Fatal("missing ownership")
		}
	}
	next, err := reopened.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == run.ID || next.OwnerID != run.OwnerID {
		t.Fatal("installation identity unstable or run reused")
	}
}

func TestTraversalRunIDCannotLoadFilesOutsideStore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../outside", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("0", 32) + "/../other", ""} {
		if _, err := s.Load(id); err == nil || err.Error() != "RUN_ID_INVALID" {
			t.Fatalf("accepted unsafe ID %q: %v", id, err)
		}
	}
}

func TestBackupStagingCopiesExactBytesWithoutChangingSource(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "private.backup")
	data := []byte("PGDMP\x01\x10\x00synthetic\x00backup")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	staged, err := s.StageBackup(context.Background(), lock, source)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if staged.SHA256 != hex.EncodeToString(want[:]) || staged.Bytes != int64(len(data)) {
		t.Fatal("incorrect backup fingerprint")
	}
	copied, err := os.ReadFile(staged.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != string(data) || staged.Path == source {
		t.Fatal("backup not staged")
	}
	original, err := os.ReadFile(source)
	if err != nil || string(original) != string(data) {
		t.Fatal("source changed")
	}
	if _, err := s.StageBackup(context.Background(), lock, source); err == nil {
		t.Fatal("silently replaced staged backup")
	}
	loaded, err := s.Load(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Backup == nil || loaded.Backup.SHA256 != staged.SHA256 {
		t.Fatal("staging evidence not durable")
	}
}

func TestCanceledStagingLeavesNoAcceptedBackup(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "backup")
	if err := os.WriteFile(source, []byte("PGDMPsynthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.StageBackup(ctx, lock, source); err == nil {
		t.Fatal("canceled staging accepted")
	}
	loaded, err := s.Load(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Backup != nil {
		t.Fatal("canceled backup committed")
	}
}

func TestTamperedResourceIntentIsRejected(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	run.Resources[0].Name = "production-postgres"
	if err := s.Save(lock, run); err == nil {
		t.Fatal("accepted arbitrary resource name")
	}
}

func TestInvalidBackupDoesNotCreateCommittedFile(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "backup")
	if err := os.WriteFile(source, []byte("not a custom format backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageBackup(context.Background(), lock, source); err == nil || err.Error() != "BACKUP_FORMAT_UNSUPPORTED" {
		t.Fatalf("bad format accepted: %v", err)
	}
	loaded, err := s.Load(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Backup != nil {
		t.Fatal("bad backup committed")
	}
}

func TestRunLockExcludesSecondOwnerAndReleaseAllowsReacquire(t *testing.T) {
	root := t.TempDir()
	first, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := first.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.AcquireRunLock(run.ID); err == nil || err.Error() != "RUN_LOCK_HELD" {
		t.Fatalf("second owner must fail closed while lock exists: %v", err)
	}
	info, err := second.InspectRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.RunID != run.ID || info.OwnerID != run.OwnerID || info.PID <= 0 || info.Hostname == "" {
		t.Fatalf("incomplete process ownership record: %#v", info)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	newLock, err := second.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatalf("lock not reusable after normal release: %v", err)
	}
	if err := newLock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleRunLockIsNeverAdoptedAutomatically(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(dir, lockDirectoryName)
	if err := os.Mkdir(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	record := lockRecord{LockInfo: LockInfo{SchemaVersion: 1, RunID: run.ID, OwnerID: run.OwnerID, Hostname: "former-host", PID: 2147483647, StartedAt: time.Unix(1, 0).UTC()}, Token: strings.Repeat("a", 32)}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, lockRecordName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireRunLock(run.ID); err == nil || err.Error() != "RUN_LOCK_HELD" {
		t.Fatalf("stale lock was silently adopted: %v", err)
	}
	if _, err := os.Stat(lockDir); err != nil {
		t.Fatalf("stale lock was removed: %v", err)
	}
}

func TestRunMutationRequiresLock(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(nil, run); err == nil || err.Error() != "RUN_LOCK_REQUIRED" {
		t.Fatalf("unlocked state update was accepted: %v", err)
	}
}

func TestInterruptedPartialIsDiscardedAndRestagedFromSuppliedBackup(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "source.dump")
	want := []byte("PGDMPcurrent-original-backup")
	if err := os.WriteFile(source, want, 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup.partial"), []byte("PGDMPinterrupted partial"), 0600); err != nil {
		t.Fatal(err)
	}

	staged, err := s.RecoverBackup(context.Background(), lock, source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(staged.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("recovery adopted partial bytes instead of supplied source: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "backup.partial")); !os.IsNotExist(err) {
		t.Fatalf("partial orphan remains: %v", err)
	}
}

func TestInterruptedRenameUsesPersistedDigestToCommitManifest(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "source.dump")
	data := []byte("PGDMPoriginal-bytes")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	manifest := &Backup{SourcePath: source, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}
	run.PendingBackup = manifest
	run.Status = "staging"
	if err := s.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup.dump"), data, 0600); err != nil {
		t.Fatal(err)
	}

	staged, err := s.RecoverBackup(context.Background(), lock, source)
	if err != nil {
		t.Fatal(err)
	}
	if staged.SHA256 != manifest.SHA256 || staged.Bytes != manifest.Bytes {
		t.Fatalf("recovery changed the persisted backup fingerprint: %#v", staged)
	}
	loaded, err := s.Load(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Backup == nil || loaded.PendingBackup != nil || loaded.Status != "staged" {
		t.Fatalf("staging manifest was not committed: %#v", loaded)
	}
}

func TestInterruptedPartialWithPendingIntentIsCommittedOnlyOnDigestMatch(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "source.dump")
	data := []byte("PGDMPpending-original")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	run.PendingBackup = &Backup{SourcePath: source, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}
	run.Status = "staging"
	if err := s.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, "backup.partial")
	if err := os.WriteFile(partial, data, 0600); err != nil {
		t.Fatal(err)
	}
	staged, err := s.RecoverBackup(context.Background(), lock, source)
	if err != nil {
		t.Fatal(err)
	}
	if staged.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("recovered fingerprint changed: %#v", staged)
	}
	if _, err := os.Lstat(partial); !os.IsNotExist(err) {
		t.Fatalf("verified partial was not renamed: %v", err)
	}
}

func TestPendingIntentDoesNotAdoptDifferentPartialBytes(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "source.dump")
	intended := []byte("PGDMPintended-content")
	if err := os.WriteFile(source, intended, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(intended)
	run.PendingBackup = &Backup{SourcePath: source, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(intended))}
	run.Status = "staging"
	if err := s.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	partial := []byte("PGDMPdifferent-content")
	partialPath := filepath.Join(dir, "backup.partial")
	if err := os.WriteFile(partialPath, partial, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverBackup(context.Background(), lock, source); err == nil || err.Error() != "BACKUP_INTEGRITY_FAILED" {
		t.Fatalf("different bytes passed pending verification: %v", err)
	}
	got, err := os.ReadFile(partialPath)
	if err != nil || string(got) != string(partial) {
		t.Fatalf("mismatched pending artifact was modified: %q, %v", got, err)
	}
}

func TestInterruptedRecoveryRejectsUnknownDestinationWithoutOverwriting(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "source.dump")
	if err := os.WriteFile(source, []byte("PGDMPknown"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := []byte("PGDMPunrecorded destination")
	dest := filepath.Join(dir, "backup.dump")
	if err := os.WriteFile(dest, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverBackup(context.Background(), lock, source); err == nil || err.Error() != "BACKUP_ORPHAN_UNSAFE" {
		t.Fatalf("unknown destination must fail closed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(foreign) {
		t.Fatalf("unknown destination was modified: %q, %v", got, err)
	}
}

func TestVerifiedBackupOpenerDetectsMutationAndRewindsSameHandle(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	source := filepath.Join(t.TempDir(), "source.dump")
	data := []byte("PGDMPexact-data")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	staged, err := s.StageBackup(context.Background(), lock, source)
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.OpenVerifiedBackup(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(read) != string(data) {
		t.Fatalf("verified descriptor was not rewound for restore: %q, %v", read, err)
	}
	if err := os.WriteFile(staged.Path, []byte("PGDMPtampered-data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenVerifiedBackup(run.ID); err == nil || err.Error() != "BACKUP_INTEGRITY_FAILED" {
		t.Fatalf("mutated staged bytes passed verification: %v", err)
	}
}
