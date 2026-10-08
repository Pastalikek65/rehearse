package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRawUppercaseErrorIsNotTreatedAsPublicCode(t *testing.T) {
	if got := SafeCode(errors.New("PRIVATEACCESSTOKEN123")); got != "OPERATION_FAILED" {
		t.Fatalf("raw error leaked: %s", got)
	}
}

func TestCheckCannotSerializeUnregisteredUppercaseSecret(t *testing.T) {
	r := New(strings.Repeat("a", 32), "linux/amd64", time.Now())
	if err := r.Set("target.auth", "failed", "PRIVATEACCESSTOKEN123"); err == nil {
		t.Fatal("raw secret accepted as check code")
	}
}

func TestPassedChecksWithoutBackupAndSnapshotsRemainUnqualified(t *testing.T) {
	r := New(strings.Repeat("a", 32), "linux/amd64", time.Now())
	for _, c := range r.Checks {
		r.Set(c.ID, "passed", "VALIDATED")
	}
	if r.Outcome() != "not-run" {
		t.Fatal("passed without backup/snapshot/completion evidence")
	}
}

func TestUnexecutedChecksCannotProduceSuccessfulReport(t *testing.T) {
	r := New(strings.Repeat("a", 32), "linux/amd64", time.Now())
	if r.Outcome() != "not-run" {
		t.Fatal("new report implies success")
	}
	if err := r.Set("target.migration", "passed", "VALIDATED"); err != nil {
		t.Fatal(err)
	}
	if r.Outcome() != "not-run" {
		t.Fatal("migration alone implies success")
	}
	if err := r.Set("baseline.data", "failed", "DATA_CHANGED"); err != nil {
		t.Fatal(err)
	}
	if r.Outcome() != "failed" {
		t.Fatal("failure hidden")
	}
}

func TestRecoveryUnauthenticatedProbeFailureIsRequiredEvidence(t *testing.T) {
	r := New(strings.Repeat("f", 32), "linux/amd64", time.Now())
	if err := r.Set("recovery.unauthenticated", "failed", "AUTH_BYPASS"); err != nil {
		t.Fatalf("recovery anonymous-access failure was not representable: %v", err)
	}
	if r.Outcome() != "failed" {
		t.Fatalf("recovery anonymous-access failure outcome = %q, want failed", r.Outcome())
	}
}

func TestAllRequiredChecksAreNeededForPassedReport(t *testing.T) {
	r := New(strings.Repeat("b", 32), "windows/amd64", time.Now())
	r.BackupSHA256 = strings.Repeat("1", 64)
	r.BackupBytes = 1024
	for _, phase := range []string{"baseline", "target", "recovery"} {
		r.Snapshots[phase] = Snapshot{SHA256: strings.Repeat("2", 64), Rows: 10, Bytes: 900}
	}
	finish := time.Now()
	r.FinishedAt = &finish
	for _, check := range r.Checks {
		if err := r.Set(check.ID, "passed", "VALIDATED"); err != nil {
			t.Fatal(err)
		}
	}
	if r.Outcome() != "passed" {
		t.Fatal("completed report not passed")
	}
	if err := r.Set("target.auth", "not-run", "NOT_RUN"); err != nil {
		t.Fatal(err)
	}
	if r.Outcome() != "not-run" {
		t.Fatal("omitted API check hidden")
	}
}

func TestSerializedReportHasVersionAndConsistentOutcome(t *testing.T) {
	r := New(strings.Repeat("c", 32), "linux/amd64", time.Now())
	r.Set("target.migration", "failed", "MIGRATION_FAILED")
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["schemaVersion"] != float64(1) || doc["outcome"] != "failed" {
		t.Fatal("misleading report")
	}
	var html bytes.Buffer
	if err := r.HTML(&html); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html.String(), "MIGRATION_FAILED") || !strings.Contains(html.String(), "not-run") {
		t.Fatal("HTML omits failure or unexecuted checks")
	}
}

func TestReportRejectsUnregisteredChecksAndUnsafeEvidence(t *testing.T) {
	r := New(strings.Repeat("d", 32), "linux/amd64", time.Now())
	for _, c := range [][3]string{{"unknown.private", "passed", "VALIDATED"}, {"target.auth", "skipped", "IGNORED"}, {"target.auth", "failed", "token=SYNTHETIC-SECRET"}} {
		if err := r.Set(c[0], c[1], c[2]); err == nil {
			t.Fatal("unsafe check accepted")
		}
	}
	r.RunID = "<script>alert('private')</script>"
	if _, err := r.JSON(); err == nil {
		t.Fatal("unsafe run identity serialized")
	}
}

func TestReportReaderRejectsUnknownVersionAndInventedOutcome(t *testing.T) {
	r := New(strings.Repeat("e", 32), "linux/amd64", time.Now())
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Parse(bytes.NewReader(raw))
	if err != nil || loaded.Outcome() != "not-run" {
		t.Fatalf("report reopen failed: %v", err)
	}
	bad := strings.Replace(string(raw), `"schemaVersion": 1`, `"schemaVersion": 99`, 1)
	if _, err := Parse(strings.NewReader(bad)); err == nil {
		t.Fatal("unknown version accepted")
	}
	bad = strings.Replace(string(raw), `"outcome": "not-run"`, `"outcome": "passed"`, 1)
	if _, err := Parse(strings.NewReader(bad)); err == nil {
		t.Fatal("invented outcome accepted")
	}
}
