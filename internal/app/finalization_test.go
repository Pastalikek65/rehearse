package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestReadReportRejectsPassedReportForNonterminalRun(t *testing.T) {
	store, run := newReadReportTestStore(t)
	writeFinalizationReportPair(t, store, run.ID, passedFinalizationReport(run.ID))

	if _, err := ReadReport(store, run.ID); err == nil {
		t.Fatal("passed report was exposed while its run was still planned")
	}
}

func TestReadReportRejectsReportWhileRunLockIsHeld(t *testing.T) {
	store, run := newReadReportTestStore(t)
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "completed"
	if err := store.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	writeFinalizationReportPair(t, store, run.ID, passedFinalizationReport(run.ID))
	if _, err := ReadReport(store, run.ID); err == nil {
		t.Fatal("passed report was exposed before the run lock was released")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(store, run.ID); err != nil {
		t.Fatalf("completed report was not readable after lock release: %v", err)
	}
}

func TestReadReportRequiresUsableHTMLPeer(t *testing.T) {
	store, run := newReadReportTestStore(t)
	markRunCompleted(t, store, run)
	r := passedFinalizationReport(run.ID)
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadReport(store, run.ID); err == nil {
		t.Fatal("JSON report was exposed without its HTML peer")
	}
}

func TestRunReportWriteFailureDoesNotExposePassedReport(t *testing.T) {
	cfg, store, _ := flowConfig(t)
	runtime := &finalizationRuntime{flowRuntime: &flowRuntime{}, beforeCleanup: func(run state.Run) error {
		dir, err := store.RunDir(run.ID)
		if err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(dir, "report.html"), 0700)
	}}

	r, err := runWithRuntime(context.Background(), cfg, store, runtime, fixtureAuth())
	if err == nil || err.Error() != "OPERATION_FAILED" {
		t.Fatalf("report write failure = %v, want OPERATION_FAILED", err)
	}
	if r == nil || r.Outcome() != "passed" {
		t.Fatalf("phase evidence changed unexpectedly: %#v", r)
	}
	stored, loadErr := store.Load(r.RunID)
	if loadErr != nil || stored.Status == "completed" {
		t.Fatalf("report write failure persisted terminal success: status=%q err=%v", stored.Status, loadErr)
	}
	if _, err := ReadReport(store, r.RunID); err == nil {
		t.Fatal("passed report was readable after report finalization failed")
	}
}

func TestRunLockReleaseFailureDoesNotExposePassedReport(t *testing.T) {
	cfg, store, _ := flowConfig(t)
	runtime := &finalizationRuntime{flowRuntime: &flowRuntime{}, beforeCleanup: func(run state.Run) error {
		dir, err := store.RunDir(run.ID)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "run.lock", "unexpected-owner-file"), []byte("synthetic"), 0600)
	}}

	r, err := runWithRuntime(context.Background(), cfg, store, runtime, fixtureAuth())
	if err == nil || err.Error() != "OPERATION_FAILED" {
		t.Fatalf("lock release failure = %v, want OPERATION_FAILED", err)
	}
	if r == nil || r.Outcome() != "passed" {
		t.Fatalf("phase evidence changed unexpectedly: %#v", r)
	}
	if _, err := ReadReport(store, r.RunID); err == nil {
		t.Fatal("passed report was readable while lock release remained incomplete")
	}
}

type finalizationRuntime struct {
	*flowRuntime
	beforeCleanup func(state.Run) error
}

func (r *finalizationRuntime) Cleanup(ctx context.Context, run state.Run) error {
	if r.beforeCleanup != nil {
		if err := r.beforeCleanup(run); err != nil {
			return err
		}
	}
	return r.flowRuntime.Cleanup(ctx, run)
}

func markRunCompleted(t *testing.T, store *state.Store, run state.Run) {
	t.Helper()
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "completed"
	if err := store.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func writeFinalizationReportPair(t *testing.T, store *state.Store, runID string, r *report.Report) {
	t.Helper()
	dir, err := store.RunDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.HTML(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func passedFinalizationReport(runID string) *report.Report {
	started := time.Date(2026, time.October, 9, 10, 0, 0, 0, time.UTC)
	r := report.New(runID, "windows/amd64", started)
	for _, id := range []string{
		"backup.inspect", "baseline.network", "baseline.restore", "baseline.schema", "baseline.data", "baseline.auth", "baseline.unauthenticated",
		"target.network", "target.restore", "target.migration", "target.schema", "target.data", "target.removed-transformation", "target.auth", "target.unauthenticated",
		"recovery.network", "recovery.restore", "recovery.schema", "recovery.data", "recovery.auth", "recovery.unauthenticated", "source.unchanged", "cleanup.ownership",
	} {
		_ = r.Set(id, "passed", "VALIDATED")
	}
	r.BackupSHA256 = strings.Repeat("a", 64)
	r.BackupBytes = 5
	fingerprint := report.Snapshot{SHA256: strings.Repeat("b", 64), Rows: 1, Bytes: 1}
	r.Snapshots["baseline"] = fingerprint
	r.Snapshots["target"] = fingerprint
	r.Snapshots["recovery"] = fingerprint
	finished := started.Add(time.Second)
	r.FinishedAt = &finished
	r.Result = r.Outcome()
	return r
}
