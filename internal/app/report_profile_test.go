package app

import (
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestReadReportRejectsAdapterDifferentFromStoredRun(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("synthetic-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := report.NewForgejo(run.ID, "windows/amd64", time.Now().UTC())
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeReportFiles(dir, r); err != nil {
		t.Fatal(err)
	}
	run.Status = "failed"
	if err := store.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(store, run.ID); err == nil {
		t.Fatal("report adapter was not bound to stored run intent")
	}
}
