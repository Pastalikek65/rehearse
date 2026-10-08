package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestFinalizationErrorCannotPrintSuccessfulRunSummary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backup.dump"), []byte("PGDMPsynthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t, dir, `"backupPath":"backup.dump","authEnvRefs":{"apiToken":"REHEARSE_TOKEN"}`)
	opts := testOptions(t.TempDir())
	opts.goos = "linux"
	opts.getenv = func(string) string { return "synthetic-token" }
	opts.newEngine = func(string) (*engine.Engine, error) { return nil, nil }
	opts.run = func(_ context.Context, _ spec.Config, store *state.Store, _ *engine.Engine, _ miniflux.Auth) (*report.Report, error) {
		r, err := persistTestReport(store, "passed")
		if err != nil {
			return nil, err
		}
		for _, check := range r.Checks {
			if err := r.Set(check.ID, "passed", "VALIDATED"); err != nil {
				return nil, err
			}
		}
		r.BackupSHA256, r.BackupBytes = strings.Repeat("a", 64), 10
		for _, phase := range []string{"baseline", "target", "recovery"} {
			r.Snapshots[phase] = report.Snapshot{SHA256: strings.Repeat("b", 64), Rows: 1, Bytes: 1}
		}
		finished := time.Now().UTC()
		r.FinishedAt = &finished
		r.Result = r.Outcome() // Phase evidence succeeded, but finalization returned an error.
		return r, errors.New("OPERATION_FAILED")
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	status := execute(context.Background(), []string{"run", configPath}, stdout, stderr, opts)
	if status == 0 || strings.Contains(stdout.String(), "Outcome: passed") || strings.Contains(stdout.String(), "report.html") {
		t.Fatalf("finalization failure appeared successful: exit=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Run: ") || !strings.Contains(stderr.String(), "OPERATION_FAILED") {
		t.Fatalf("failure lost run ID or safe diagnostic: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
