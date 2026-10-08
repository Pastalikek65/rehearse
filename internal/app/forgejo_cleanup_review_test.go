package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

type forgejoCleanupHeldReviewRuntime struct {
	forgejoFailureRuntime
}

func (f *forgejoCleanupHeldReviewRuntime) Cleanup(context.Context, state.Run) error {
	f.cleanupCalls++
	return code("OPERATION_FAILED")
}

func TestForgejoCleanupFailurePersistsReadableHeldFailureReport(t *testing.T) {
	input := forgejoPlanArchive(t)
	backupPath := filepath.Join(t.TempDir(), "synthetic-backup.zip")
	if err := os.WriteFile(backupPath, input, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := &forgejoCleanupHeldReviewRuntime{}
	result, runErr := runForgejoWithRuntime(context.Background(), forgejoConfig(backupPath), store, client, forgejo.Auth{Token: strings.Repeat("a", 40)})
	if runErr == nil || result == nil || result.Result != "failed" || client.cleanupCalls != 1 {
		t.Fatalf("cleanup-held run outcome=%v report=%+v cleanup calls=%d", runErr, result, client.cleanupCalls)
	}
	stored, err := store.Load(result.RunID)
	if err != nil || stored.Status != "cleanup-held" {
		t.Fatalf("cleanup failure did not persist held status: status=%q err=%v", stored.Status, err)
	}
	loaded, err := ReadReport(store, result.RunID)
	if err != nil || loaded.Result != "failed" || checkCode(loaded, "cleanup.ownership") != "CLEANUP_HELD" {
		t.Fatalf("cleanup-held failure report is not readable and precise: report=%+v err=%v", loaded, err)
	}
	if checkCode(loaded, "cleanup.ownership") == "VALIDATED" {
		t.Fatal("cleanup ownership was reported as passed after cleanup failure")
	}
}
