package app

import (
	"testing"
)

func TestReadReportReturnsPersistedFailedRehearsal(t *testing.T) {
	store, run := newReadReportTestStore(t)
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "failed"
	if err := store.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}

	r := passedFinalizationReport(run.ID)
	for _, id := range []string{
		"target.schema", "target.data", "target.removed-transformation", "target.auth", "target.unauthenticated",
		"recovery.network", "recovery.restore", "recovery.schema", "recovery.data", "recovery.auth", "recovery.unauthenticated",
	} {
		if err := r.Set(id, "not-run", "NOT_RUN"); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Set("target.migration", "failed", "MIGRATION_FAILED"); err != nil {
		t.Fatal(err)
	}
	if r.Result != "failed" {
		t.Fatalf("fixture outcome = %q, want failed", r.Result)
	}
	writeFinalizationReportPair(t, store, run.ID, r)

	got, err := ReadReport(store, run.ID)
	if err != nil {
		t.Fatalf("ReadReport rejected a valid terminal failed report: %v", err)
	}
	if got.RunID != run.ID || got.Result != "failed" || got.Outcome() != "failed" {
		t.Fatalf("ReadReport returned unexpected report: run=%q result=%q outcome=%q", got.RunID, got.Result, got.Outcome())
	}
	for _, check := range got.Checks {
		if check.ID == "target.migration" && (check.Status != "failed" || check.Code != "MIGRATION_FAILED") {
			t.Fatalf("migration evidence = %#v, want failed MIGRATION_FAILED", check)
		}
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("returned report is invalid: %v", err)
	}
}
