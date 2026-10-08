package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestHistoryJSONShowsOnlySafeSummaryFromPrivateState(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("private-daemon-marker")
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"history", "--json", "--limit", "1"}, stdout, stderr, testOptions(root))
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("history failed: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var snapshot state.HistorySnapshot
	if err := json.Unmarshal([]byte(stdout.String()), &snapshot); err != nil {
		t.Fatalf("history did not emit its safe JSON snapshot: %v: %q", err, stdout.String())
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].RunID != run.ID || snapshot.Entries[0].Status != "planned" {
		t.Fatalf("history omitted persisted run metadata: %#v", snapshot)
	}
	if strings.Contains(stdout.String(), "private-daemon-marker") || strings.Contains(stdout.String(), root) {
		t.Fatalf("history leaked private state: %q", stdout.String())
	}
}

func TestHistoryInvalidRecordsArePrintedAndReturnNonzeroWithoutEchoingNames(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	validID := "0123456789abcdef0123456789abcdef"
	validDir := filepath.Join(root, validID)
	if err := os.Mkdir(validDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(validDir, "run.json"), []byte("not-json-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	badName := "private-child-name-marker"
	if err := os.Mkdir(filepath.Join(root, badName), 0700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"history", "--json"}, stdout, stderr, testOptions(root))
	if code == 0 || !strings.Contains(stderr.String(), "HISTORY_INVALID_RECORDS") {
		t.Fatalf("invalid state was silently reported as success: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var snapshot state.HistorySnapshot
	if err := json.Unmarshal([]byte(stdout.String()), &snapshot); err != nil {
		t.Fatalf("history omitted invalid record summary: %v: %q", err, stdout.String())
	}
	if snapshot.InvalidRecordsCount != 2 || len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != "invalid" || snapshot.Entries[0].RunID != validID {
		t.Fatalf("invalid records were hidden or exposed unsafely: %#v", snapshot)
	}
	if strings.Contains(stdout.String()+stderr.String(), badName) || strings.Contains(stdout.String()+stderr.String(), "not-json-marker") || strings.Contains(stdout.String()+stderr.String(), root) {
		t.Fatalf("history diagnostic echoed untrusted metadata: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	_ = store
}

func TestRecoverCommandFailsClosedForCurrentLiveProcessWithoutDocker(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"recover", run.ID}, stdout, stderr, testOptions(root))
	if code == 0 || !strings.Contains(stderr.String(), "RUN_RECOVERY_PROCESS_LIVE") {
		t.Fatalf("recover accepted a live process: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), lock.Info().Hostname) {
		t.Fatalf("recover exposed local process identity: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := store.InspectRunLock(run.ID); err != nil {
		t.Fatalf("failed live-process recovery removed the lock: %v", err)
	}
}

func TestRecoverRejectsUnsafeRunIDAndCancellationWithoutEcho(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		args []string
		want string
	}{
		{name: "unsafe ID", ctx: context.Background(), args: []string{"recover", "../private-recovery-marker"}, want: "RUN_ID_INVALID"},
		{name: "missing ID", ctx: context.Background(), args: []string{"recover"}, want: "ARGUMENTS_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := &strings.Builder{}, &strings.Builder{}
			code := execute(tc.ctx, tc.args, stdout, stderr, testOptions(root))
			if code == 0 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("invalid recovery command accepted: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), "private-recovery-marker") {
				t.Fatalf("recovery error echoed unsafe ID: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}
