package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func newReadReportTestStore(t *testing.T) (*state.Store, state.Run) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("synthetic-daemon")
	if err != nil {
		t.Fatal(err)
	}
	return store, run
}

func TestReadReportRequiresReportRunIDToMatchPrivateStatePath(t *testing.T) {
	store, run := newReadReportTestStore(t)
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherID := "0123456789abcdef0123456789abcdef"
	other := report.New(otherID, "windows/amd64", time.Now().UTC())
	raw, err := other.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(store, run.ID); err == nil {
		t.Fatal("report for another run was accepted from this run directory")
	}
}

func TestReadReportRejectsTraversalUnknownStateAndUnknownReportVersion(t *testing.T) {
	store, run := newReadReportTestStore(t)
	if _, err := ReadReport(store, "../owner-id"); err == nil {
		t.Fatal("traversal run ID was accepted")
	}
	if _, err := ReadReport(store, strings.Repeat("f", 32)); err == nil {
		t.Fatal("unknown run state was accepted")
	}
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(`{"schemaVersion":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(store, run.ID); err == nil {
		t.Fatal("unknown report schema was accepted")
	}
}

func TestRunRejectsInvalidAuthBeforeCreatingRunIntent(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup.dump")
	if err := os.WriteFile(backup, []byte("PGDMPsynthetic-input"), 0600); err != nil {
		t.Fatal(err)
	}
	_, runErr := Run(context.Background(), config(backup), store, nil, miniflux.Auth{})
	if runErr == nil || runErr.Error() != "AUTH_INVALID" {
		t.Fatalf("invalid auth error = %v, want AUTH_INVALID", runErr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "owner-id" {
		t.Fatalf("invalid auth created run state: %v", entries)
	}
}

func TestRunHandlesNilContextWithoutPanicOrRunIntent(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup.dump")
	if err := os.WriteFile(backup, []byte("PGDMPsynthetic-input"), 0600); err != nil {
		t.Fatal(err)
	}
	_, runErr := Run(nil, config(backup), store, nil, miniflux.Auth{Username: "user", Password: "password"})
	if runErr == nil || runErr.Error() != "CANCELED" {
		t.Fatalf("nil context error = %v, want CANCELED", runErr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "state"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "owner-id" {
		t.Fatalf("nil context created run state: %v (%v)", entries, err)
	}
}
