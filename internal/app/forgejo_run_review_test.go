package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
)

type forgejoReviewRunRuntime struct {
	runtimeClient
	create      func()
	cleanup     func(state.Run) error
	cleanupRuns int
}

type forgejoRestoreStreamRuntime struct {
	runtimeClient
	cancelAfterListPrefix context.CancelFunc
	listPrefixBytes       int
	restoreBytes          int
	dataCopyCalls         int
}

func (r *forgejoRestoreStreamRuntime) Inside(_ context.Context, _ state.Run, _, role string, args []string, input io.Reader, _ io.Writer) error {
	if role != "db" || len(args) == 0 || args[0] != "pg_restore" {
		return code("OPERATION_FAILED")
	}
	if len(args) > 1 && args[1] == "--list" {
		prefix := make([]byte, 5)
		n, err := io.ReadFull(input, prefix)
		r.listPrefixBytes = n
		if err != nil {
			return err
		}
		if r.cancelAfterListPrefix != nil {
			r.cancelAfterListPrefix()
		}
		// Model pg_restore --list returning after consuming only a prefix.
		return nil
	}
	n, err := io.Copy(io.Discard, input)
	r.restoreBytes = int(n)
	return err
}

func (r *forgejoRestoreStreamRuntime) CopyForgejoData(_ context.Context, _ state.Run, _ string, input io.Reader) error {
	r.dataCopyCalls++
	_, err := io.Copy(io.Discard, input)
	return err
}

func (*forgejoRestoreStreamRuntime) ReadForgejoData(context.Context, state.Run, string, io.Writer) error {
	return code("OPERATION_FAILED")
}

func (*forgejoRestoreStreamRuntime) StopForgejoApp(context.Context, state.Run, string) error {
	return code("OPERATION_FAILED")
}

func TestRestoreForgejoArchiveDrainsSuccessfulListBeforeVerifiedClose(t *testing.T) {
	ctx := context.Background()
	store, run, lock := stagedForgejoReviewBackup(t, ctx)
	defer lock.Release()
	runtime := &forgejoRestoreStreamRuntime{}
	if err := restoreForgejoArchive(ctx, runtime, store, run, "baseline", []byte("config tar")); err != nil {
		t.Fatalf("restore failed after partial successful --list consumption: %v", err)
	}
	if runtime.listPrefixBytes != 5 || runtime.restoreBytes == 0 || runtime.dataCopyCalls != 2 {
		t.Fatalf("restore stream behavior: list prefix=%d restore bytes=%d data copies=%d", runtime.listPrefixBytes, runtime.restoreBytes, runtime.dataCopyCalls)
	}
}

func TestRestoreForgejoArchiveSurfacesCancellationDuringListDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, run, lock := stagedForgejoReviewBackup(t, ctx)
	defer lock.Release()
	runtime := &forgejoRestoreStreamRuntime{cancelAfterListPrefix: cancel}
	err := restoreForgejoArchive(ctx, runtime, store, run, "baseline", []byte("config tar"))
	if !errors.Is(err, forgejo.ErrArchiveCanceled) {
		t.Fatalf("drain cancellation error=%v, want archive cancellation", err)
	}
	if runtime.listPrefixBytes != 5 || runtime.restoreBytes != 0 || runtime.dataCopyCalls != 0 {
		t.Fatalf("canceled stream continued: list prefix=%d restore bytes=%d data copies=%d", runtime.listPrefixBytes, runtime.restoreBytes, runtime.dataCopyCalls)
	}
}

func stagedForgejoReviewBackup(t *testing.T, ctx context.Context) (*state.Store, state.Run, *state.RunLock) {
	t.Helper()
	cfg, store, _, _ := forgejoReviewRunInputs(t)
	run, err := store.CreateForAdapter("synthetic-daemon", "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageBackup(ctx, lock, cfg.BackupPath); err != nil {
		_ = lock.Release()
		t.Fatal(err)
	}
	return store, run, lock
}

func (r *forgejoReviewRunRuntime) Info(context.Context) (engine.Daemon, error) {
	return engine.Daemon{ID: "synthetic-review-daemon"}, nil
}

func (r *forgejoReviewRunRuntime) CreatePhase(context.Context, state.Run, string, string) error {
	if r.create != nil {
		r.create()
	}
	return code("NETWORK_FAILED")
}

func (r *forgejoReviewRunRuntime) Cleanup(_ context.Context, run state.Run) error {
	r.cleanupRuns++
	if r.cleanup != nil {
		return r.cleanup(run)
	}
	return nil
}

func (r *forgejoReviewRunRuntime) CopyForgejoData(context.Context, state.Run, string, io.Reader) error {
	return code("OPERATION_FAILED")
}

func (r *forgejoReviewRunRuntime) ReadForgejoData(context.Context, state.Run, string, io.Writer) error {
	return code("OPERATION_FAILED")
}

func (*forgejoReviewRunRuntime) StopForgejoApp(context.Context, state.Run, string) error {
	return code("OPERATION_FAILED")
}

func TestForgejoRunCancellationAfterStagingPersistsCanceledOutcomeAndCleans(t *testing.T) {
	cfg, store, _, _ := forgejoReviewRunInputs(t)
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &forgejoReviewRunRuntime{create: cancel}
	r, err := runForgejoWithRuntime(ctx, cfg, store, runtime, forgejo.Auth{Token: strings.Repeat("x", 40)})
	if err == nil || r == nil || r.Result != "failed" || runtime.cleanupRuns != 1 {
		t.Fatalf("cancel outcome: report=%+v error=%v cleanup=%d", r, err, runtime.cleanupRuns)
	}
	if checkCode(r, "baseline.network") != "CANCELED" || checkCode(r, "source.unchanged") != "CANCELED" || checkCode(r, "cleanup.ownership") != "VALIDATED" {
		t.Fatalf("unexpected cancellation/finalization checks: %+v", r.Checks)
	}
	stored, loadErr := store.Load(r.RunID)
	if loadErr != nil || stored.Status != "failed" {
		t.Fatalf("canceled run status=%q err=%v", stored.Status, loadErr)
	}
	if readable, readErr := ReadReport(store, r.RunID); readErr != nil || readable.Result != "failed" {
		t.Fatalf("failed cancellation report unavailable: report=%+v err=%v", readable, readErr)
	}
}

func TestForgejoRunDetectsSourceMutationDuringFinalCleanup(t *testing.T) {
	cfg, store, backupPath, _ := forgejoReviewRunInputs(t)
	runtime := &forgejoReviewRunRuntime{cleanup: func(state.Run) error {
		return os.WriteFile(backupPath, []byte("changed after archive staging"), 0600)
	}}
	r, err := runForgejoWithRuntime(context.Background(), cfg, store, runtime, forgejo.Auth{Token: strings.Repeat("x", 40)})
	if err == nil || r == nil || runtime.cleanupRuns != 1 {
		t.Fatalf("source-change run: report=%+v error=%v diagnostic=%s cleanup=%d", r, err, safeOperationDiagnostic(err), runtime.cleanupRuns)
	}
	if checkCode(r, "source.unchanged") != "SOURCE_CHANGED" || r.Result != "failed" {
		t.Fatalf("mutated input was not recorded as a failed source check: %+v", r.Checks)
	}
	stored, loadErr := store.Load(r.RunID)
	if loadErr != nil || stored.Status != "failed" {
		t.Fatalf("source-change status=%q err=%v", stored.Status, loadErr)
	}
	readable, readErr := ReadReport(store, r.RunID)
	if readErr != nil || checkCode(readable, "source.unchanged") != "SOURCE_CHANGED" {
		t.Fatalf("source-change evidence not persisted: report=%+v err=%v", readable, readErr)
	}
}

func TestForgejoRunReportFinalizationFailureDoesNotExposeTerminalReport(t *testing.T) {
	cfg, store, _, _ := forgejoReviewRunInputs(t)
	runtime := &forgejoReviewRunRuntime{cleanup: func(run state.Run) error {
		dir, err := store.RunDir(run.ID)
		if err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(dir, "report.html"), 0700)
	}}
	r, err := runForgejoWithRuntime(context.Background(), cfg, store, runtime, forgejo.Auth{Token: strings.Repeat("x", 40)})
	if err == nil || r == nil || runtime.cleanupRuns != 1 {
		t.Fatalf("finalization failure: report=%+v error=%v cleanup=%d", r, err, runtime.cleanupRuns)
	}
	stored, loadErr := store.Load(r.RunID)
	if loadErr != nil || stored.Status != "failed" {
		t.Fatalf("failed report write exposed a terminal status=%q err=%v", stored.Status, loadErr)
	}
	if _, readErr := ReadReport(store, r.RunID); readErr == nil {
		t.Fatal("report with failed HTML finalization was exposed as usable")
	}
}

func forgejoReviewRunInputs(t *testing.T) (spec.Config, *state.Store, string, []byte) {
	t.Helper()
	archive := forgejoPlanArchive(t)
	backupPath := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(backupPath, archive, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return forgejoConfig(backupPath), store, backupPath, archive
}

func checkCode(r *report.Report, id string) string {
	if r == nil {
		return ""
	}
	for _, check := range r.Checks {
		if check.ID == id {
			return check.Code
		}
	}
	return ""
}
