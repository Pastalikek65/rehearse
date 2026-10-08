package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/state"
)

type sourceCancelAfterChecks struct {
	context.Context
	checks, remaining int
}

func (c *sourceCancelAfterChecks) Err() error {
	c.checks++
	if c.checks >= c.remaining {
		return context.Canceled
	}
	return nil
}

func TestSourceRehashObservesCancellationBetweenReads(t *testing.T) {
	data := []byte("PGDMP" + strings.Repeat("synthetic-", 65536))
	path := filepath.Join(t.TempDir(), "source.dump")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	backup := state.Backup{SourcePath: path, Bytes: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	for _, after := range []int{1, 4} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			ctx := &sourceCancelAfterChecks{Context: context.Background(), remaining: after}
			unchanged, err := sourceUnchangedContext(ctx, backup)
			if unchanged || err == nil || err.Error() != "CANCELED" {
				t.Fatalf("canceled source rehash claimed %v, err=%v", unchanged, err)
			}
		})
	}
	if unchanged, err := sourceUnchangedContext(context.Background(), backup); err != nil || !unchanged {
		t.Fatalf("unchanged source rejected: %v %v", unchanged, err)
	}
}

func TestSourceMutationDuringCleanupCannotReportPass(t *testing.T) {
	cfg, store, _ := flowConfig(t)
	runtime := &finalizationRuntime{flowRuntime: &flowRuntime{}, beforeCleanup: func(run state.Run) error {
		return os.WriteFile(run.Backup.SourcePath, []byte("PGDMPchanged during cleanup"), 0600)
	}}
	r, err := runWithRuntime(context.Background(), cfg, store, runtime, fixtureAuth())
	if err == nil || err.Error() != "SOURCE_CHANGED" || r == nil || r.Outcome() != "failed" {
		t.Fatalf("source changed during cleanup passed: report=%#v err=%v", r, err)
	}
	stored, err := ReadReport(store, r.RunID)
	if err != nil || stored.Outcome() != "failed" {
		t.Fatalf("failed source-stability evidence unavailable: %v", err)
	}
}

func TestCancellationDuringCleanupCannotClaimSourceVerified(t *testing.T) {
	cfg, store, _ := flowConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanupReached := false
	runtime := &finalizationRuntime{flowRuntime: &flowRuntime{}, beforeCleanup: func(run state.Run) error {
		cleanupReached = true
		cancel()
		return nil
	}}
	r, err := runWithRuntime(ctx, cfg, store, runtime, fixtureAuth())
	if !cleanupReached || err == nil || err.Error() != "CANCELED" || r == nil || r.Outcome() != "failed" {
		t.Fatalf("cancellation during cleanup claimed pass: report=%#v err=%v", r, err)
	}
	checks := map[string]string{}
	for _, check := range r.Checks {
		checks[check.ID] = check.Status
	}
	if checks["cleanup.ownership"] != "passed" || checks["source.unchanged"] != "failed" {
		t.Fatalf("canceled hash/independent cleanup evidence incorrect: %v", checks)
	}
}
