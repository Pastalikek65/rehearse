package app

import (
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestReadReportBindsOutcomeToPersistedRunStatus(t *testing.T) {
	tests := []struct {
		name      string
		runStatus string
		report    *report.Report
	}{
		{name: "passed report paired with failed run", runStatus: "failed", report: passedForgejoReviewReport()},
		{name: "failed report paired with completed run", runStatus: "completed", report: failedForgejoReviewReport()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := state.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.CreateForAdapter("synthetic-daemon", "forgejo")
			if err != nil {
				t.Fatal(err)
			}
			lock, err := store.AcquireRunLock(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			tt.report.RunID = run.ID
			dir, err := store.RunDir(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeReportFiles(dir, tt.report); err != nil {
				t.Fatal(err)
			}
			run.Status = tt.runStatus
			if err := store.Save(lock, run); err != nil {
				t.Fatal(err)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadReport(store, run.ID); err == nil {
				t.Fatalf("mismatched run/report outcomes were readable: stored=%q report=%q", tt.runStatus, got.Result)
			}
		})
	}
}

func passedForgejoReviewReport() *report.Report {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	r := report.NewForgejo("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "windows/amd64", start)
	r.BackupSHA256 = strings.Repeat("1", 64)
	r.BackupBytes = 2048
	for _, phase := range []string{"baseline", "target", "recovery"} {
		r.Snapshots[phase] = report.Snapshot{SHA256: strings.Repeat("2", 64), Rows: 3, Bytes: 128}
		r.FileSnapshots[phase] = report.Snapshot{SHA256: strings.Repeat("3", 64), Rows: 2, Bytes: 64}
	}
	finished := start.Add(time.Minute)
	r.FinishedAt = &finished
	for _, check := range r.Checks {
		_ = r.Set(check.ID, "passed", "VALIDATED")
	}
	return r
}

func failedForgejoReviewReport() *report.Report {
	r := report.NewForgejo("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "windows/amd64", time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	_ = r.Set("baseline.network", "failed", "NETWORK_FAILED")
	return r
}
