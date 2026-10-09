package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestStateOperationDiagnosticKeepsPublicCodeAndOnlyTrustedCause(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "private-path-sentinel")
	secretContent := "private-content-sentinel"
	store, err := state.Open(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(secretContent)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	run.Status = "running"
	runPath := filepath.Join(secretPath, run.ID, "run.json")
	if err := os.Remove(runPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runPath, 0700); err != nil {
		t.Fatal(err)
	}
	stateErr := store.Save(lock, run)
	if stateErr == nil || stateErr.Error() != "STATE_WRITE_FAILED" {
		t.Fatalf("expected fixed atomic write failure, got %v", stateErr)
	}

	wrapped := withStateDiagnostic("OPERATION_FAILED", stateErr)
	if wrapped == nil || wrapped.Error() != "OPERATION_FAILED" {
		t.Fatalf("public error changed: %v", wrapped)
	}
	diagnostic, ok := operationDiagnosticFor(wrapped)
	if !ok || diagnostic.PublicCode != "OPERATION_FAILED" || diagnostic.StateCode != "STATE_WRITE_FAILED" || diagnostic.Operation != "rename" || diagnostic.Errno == 0 {
		t.Fatalf("missing safe state diagnostic: %+v, %v", diagnostic, ok)
	}
	if rendered := fmt.Sprintf("%+v", diagnostic); strings.Contains(rendered, secretPath) || strings.Contains(rendered, secretContent) {
		t.Fatalf("diagnostic contains sensitive input: %q", rendered)
	}
	backupWrapped := withStateDiagnostic("BACKUP_FAILED", stateErr)
	if backupWrapped.Error() != "BACKUP_FAILED" {
		t.Fatalf("backup public error changed: %v", backupWrapped)
	}
	backupDiagnostic, ok := operationDiagnosticFor(backupWrapped)
	if !ok || backupDiagnostic.PublicCode != "BACKUP_FAILED" || backupDiagnostic.Operation != "rename" || backupDiagnostic.Errno == 0 {
		t.Fatalf("backup diagnostic missing: %+v, %v", backupDiagnostic, ok)
	}

	unknown := withStateDiagnostic("OPERATION_FAILED", errors.New(secretContent+" "+secretPath))
	if unknown.Error() != "OPERATION_FAILED" {
		t.Fatalf("unknown public error changed: %v", unknown)
	}
	if got, ok := operationDiagnosticFor(unknown); ok {
		t.Fatalf("raw error entered diagnostic chain: %+v", got)
	}
	if strings.Contains(unknown.Error(), secretPath) || strings.Contains(unknown.Error(), secretContent) {
		t.Fatalf("raw error leaked through public error: %q", unknown)
	}
}

func safeOperationDiagnostic(err error) string {
	diagnostic, ok := operationDiagnosticFor(err)
	if !ok {
		return "diagnostic unavailable"
	}
	return fmt.Sprintf("public=%s state=%s operation=%s errno=%d", diagnostic.PublicCode, diagnostic.StateCode, diagnostic.Operation, diagnostic.Errno)
}
