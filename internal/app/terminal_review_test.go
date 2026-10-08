package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestCommitTerminalReportInvalidatesPairAfterSaveError(t *testing.T) {
	for _, adapter := range []string{"miniflux", "forgejo"} {
		t.Run(adapter, func(t *testing.T) {
			store, run, lock := finalizationReviewRun(t, adapter, "completed")
			r := finalizationReviewPassedReport(run)
			dir, lock := finalizationReviewBaseline(t, store, run, lock, r)
			defer lock.Release()
			saveCalls := 0
			releaseCalls := 0
			save := func(next state.Run) error {
				saveCalls++
				if err := store.Save(lock, next); err != nil {
					return err
				}
				if saveCalls == 1 {
					// Model a directory-sync failure after atomicWrite has renamed
					// run.json: the on-disk status has already become completed.
					return errors.New("simulated post-rename state sync failure")
				}
				return nil
			}
			release := func() error {
				releaseCalls++
				return lock.Release()
			}
			finalErr, _ := commitTerminalReport(dir, r, run, save, release)
			if finalErr == nil || saveCalls != 2 || releaseCalls != 1 {
				t.Fatalf("finalization error=%v save calls=%d release calls=%d", finalErr, saveCalls, releaseCalls)
			}
			stored, loadErr := store.Load(run.ID)
			if loadErr != nil || stored.Status != "failed" {
				t.Fatalf("post-save-fault status=%q err=%v", stored.Status, loadErr)
			}
			assertFinalizationReviewReportsUnavailable(t, store, run.ID, dir)
			assertTerminalReviewMarkerPresent(t, dir)
		})
	}
}

func TestCommitTerminalReportInvalidatesPairAfterReleaseError(t *testing.T) {
	for _, adapter := range []string{"miniflux", "forgejo"} {
		t.Run(adapter, func(t *testing.T) {
			store, run, lock := finalizationReviewRun(t, adapter, "completed")
			r := finalizationReviewPassedReport(run)
			dir, lock := finalizationReviewBaseline(t, store, run, lock, r)
			released := false
			save := func(next state.Run) error { return store.Save(lock, next) }
			release := func() error {
				if err := lock.Release(); err != nil {
					return err
				}
				released = true
				// Model an error reported after the canonical lock was removed.
				return errors.New("simulated post-removal release failure")
			}
			finalErr, _ := commitTerminalReport(dir, r, run, save, release)
			if finalErr == nil || !released {
				t.Fatalf("finalization error=%v real lock released=%t", finalErr, released)
			}
			stored, loadErr := store.Load(run.ID)
			if loadErr != nil || stored.Status != "completed" {
				t.Fatalf("release-fault path rewrote state outside the released lock: status=%q err=%v", stored.Status, loadErr)
			}
			assertFinalizationReviewReportsUnavailable(t, store, run.ID, dir)
			assertTerminalReviewMarkerPresent(t, dir)

			// Simulate invalidation failing to remove either report after Release
			// already returned an error. The retained marker must keep even a
			// fully recreated, otherwise-valid completed report unreadable.
			if err := writeReportFiles(dir, r); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadReport(store, run.ID); err == nil {
				t.Fatalf("retained terminal marker allowed completed report outcome %q", got.Result)
			}
		})
	}
}

func TestReadReportRejectsAnyTerminalPublicationMarker(t *testing.T) {
	for _, kind := range []string{"regular file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			store, run, lock := finalizationReviewRun(t, "forgejo", "completed")
			r := finalizationReviewPassedReport(run)
			dir, err := store.RunDir(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeReportFiles(dir, r); err != nil {
				t.Fatal(err)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, terminalMarkerName)
			switch kind {
			case "regular file":
				if err := os.WriteFile(marker, []byte("publication in progress\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(marker, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "marker-target")
				if err := os.WriteFile(target, []byte("outside marker target"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, marker); err != nil {
					t.Skipf("symlink creation is unavailable on this host: %v", err)
				}
			}
			if got, err := ReadReport(store, run.ID); err == nil {
				t.Fatalf("marker kind %q allowed report outcome %q", kind, got.Result)
			}
		})
	}
}

func TestCommitTerminalReportRemovesMarkerOnlyAfterSuccessfulPublication(t *testing.T) {
	store, run, lock := finalizationReviewRun(t, "forgejo", "completed")
	r := finalizationReviewPassedReport(run)
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalErr, released := commitTerminalReport(dir, r, run, func(next state.Run) error {
		return store.Save(lock, next)
	}, lock.Release)
	if finalErr != nil || !released {
		t.Fatalf("successful publication failed: err=%v released=%t", finalErr, released)
	}
	if _, err := os.Lstat(filepath.Join(dir, terminalMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("successful publication left marker: %v", err)
	}
	got, err := ReadReport(store, run.ID)
	if err != nil || got.Result != "passed" {
		t.Fatalf("published report was not readable: result=%v err=%v", got, err)
	}
}

func TestCommitTerminalReportPreservesHeldAndStagingStatesOnSaveError(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		pending bool
	}{
		{name: "cleanup held", status: "cleanup-held"},
		{name: "pending backup staging", status: "staging", pending: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, run, lock := finalizationReviewRun(t, "forgejo", tc.status)
			defer lock.Release()
			if tc.pending {
				run.PendingBackup = &state.Backup{SourcePath: filepath.Join(t.TempDir(), "source.zip"), SHA256: strings.Repeat("a", 64), Bytes: 64}
				if err := store.Save(lock, run); err != nil {
					t.Fatal(err)
				}
			}
			dir, err := store.RunDir(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			r := finalizationReviewFailedReport(run)
			if err := r.Validate(); err != nil {
				t.Fatalf("invalid failure-report fixture: %v", err)
			}
			if tc.status == "cleanup-held" {
				dir, lock = finalizationReviewBaseline(t, store, run, lock, r)
			}
			saveCalls := 0
			save := func(next state.Run) error {
				saveCalls++
				if err := store.Save(lock, next); err != nil {
					return err
				}
				if saveCalls == 1 {
					return errors.New("simulated post-rename state sync failure")
				}
				return nil
			}
			finalErr, _ := commitTerminalReport(dir, r, run, save, lock.Release)
			if finalErr == nil || saveCalls != 2 {
				t.Fatalf("finalization error=%v save calls=%d", finalErr, saveCalls)
			}
			stored, loadErr := store.Load(run.ID)
			if loadErr != nil || stored.Status != tc.status {
				t.Fatalf("special status changed: got=%q want=%q err=%v", stored.Status, tc.status, loadErr)
			}
			assertFinalizationReviewReportsUnavailable(t, store, run.ID, dir)
		})
	}
}

func finalizationReviewRun(t *testing.T, adapter, status string) (*state.Store, state.Run, *state.RunLock) {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateForAdapter("synthetic-daemon", adapter)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = status
	if err := store.Save(lock, run); err != nil {
		_ = lock.Release()
		t.Fatal(err)
	}
	return store, run, lock
}

func finalizationReviewBaseline(t *testing.T, store *state.Store, run state.Run, lock *state.RunLock, r *report.Report) (string, *state.RunLock) {
	t.Helper()
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("invalid terminal report fixture: %v", err)
	}
	if err := writeReportFiles(dir, r); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	baseline, err := ReadReport(store, run.ID)
	if err != nil || baseline.Result != r.Result {
		t.Fatalf("pre-fault report baseline unavailable: report=%+v err=%v", baseline, err)
	}
	lock, err = store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return dir, lock
}

func finalizationReviewPassedReport(run state.Run) *report.Report {
	started := time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)
	var r *report.Report
	if run.AdapterID() == "forgejo" {
		r = report.NewForgejo(run.ID, "windows/amd64", started)
		r.BackupSHA256 = strings.Repeat("b", 64)
		r.BackupBytes = 2048
		for _, phase := range []string{"baseline", "target", "recovery"} {
			r.Snapshots[phase] = report.Snapshot{SHA256: strings.Repeat("c", 64), Rows: 3, Bytes: 64}
			r.FileSnapshots[phase] = report.Snapshot{SHA256: strings.Repeat("d", 64), Rows: 2, Bytes: 32}
		}
	} else {
		r = report.New(run.ID, "windows/amd64", started)
		r.BackupSHA256 = strings.Repeat("b", 64)
		r.BackupBytes = 2048
		for _, phase := range []string{"baseline", "target", "recovery"} {
			r.Snapshots[phase] = report.Snapshot{SHA256: strings.Repeat("c", 64), Rows: 3, Bytes: 64}
		}
	}
	finished := started.Add(time.Minute)
	r.FinishedAt = &finished
	for _, check := range r.Checks {
		_ = r.Set(check.ID, "passed", "VALIDATED")
	}
	return r
}

func finalizationReviewFailedReport(run state.Run) *report.Report {
	started := time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)
	if run.AdapterID() == "forgejo" {
		r := report.NewForgejo(run.ID, "windows/amd64", started)
		_ = r.Set("backup.inspect", "failed", "BACKUP_FAILED")
		return r
	}
	r := report.New(run.ID, "windows/amd64", started)
	_ = r.Set("backup.inspect", "failed", "BACKUP_FAILED")
	return r
}

func assertFinalizationReviewReportsUnavailable(t *testing.T, store *state.Store, runID, dir string) {
	t.Helper()
	for _, name := range []string{"report.json", "report.html"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("report file %q remains after finalization failure: %v", name, err)
		}
	}
	if reportValue, err := ReadReport(store, runID); err == nil {
		t.Fatalf("finalization failure exposed report outcome %q", reportValue.Result)
	}
}

func assertTerminalReviewMarkerPresent(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Lstat(filepath.Join(dir, terminalMarkerName))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("terminal publication marker missing or unsafe: info=%v err=%v", info, err)
	}
}
