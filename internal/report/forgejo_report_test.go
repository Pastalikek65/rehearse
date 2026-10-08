package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMinifluxV1JSONAndHTMLBytesRemainCompatible(t *testing.T) {
	r := completeMinifluxReport()
	jsonBytes, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var htmlBytes bytes.Buffer
	if err := r.HTML(&htmlBytes); err != nil {
		t.Fatal(err)
	}
	jsonHash := sha256.Sum256(jsonBytes)
	if got := hex.EncodeToString(jsonHash[:]); got != "61d75f4756d8c670873a5bea7e52566a6806407289a0b8f552d0998e3e240bbf" {
		t.Fatalf("schema-1 JSON changed: sha256=%s bytes=%d", got, len(jsonBytes))
	}
	htmlHash := sha256.Sum256(htmlBytes.Bytes())
	if got := hex.EncodeToString(htmlHash[:]); got != "79ddc499a3fe7fa0f901199625e9eba78d5d01f471a32dcb27ab1433266e7ac7" {
		t.Fatalf("schema-1 HTML changed: sha256=%s bytes=%d", got, htmlBytes.Len())
	}
}

func TestForgejoReportUsesClosedV2ContractAndRendersFileProjection(t *testing.T) {
	r := completeForgejoReport()
	if r.SchemaVersion != 2 || r.Adapter != "forgejo" || r.AdapterContractVersion != 1 || r.SourceVersion != "15.0.9" || r.TargetVersion != "16.0.5" {
		t.Fatalf("unexpected Forgejo report contract: %+v", r)
	}
	if len(r.Checks) != 25 {
		t.Fatalf("Forgejo required checks=%d, want 25", len(r.Checks))
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("valid Forgejo report rejected: %v (schema=%d adapter=%q contract=%d source=%q target=%q id=%q started=%t platform=%q result=%q)", err, r.SchemaVersion, r.Adapter, r.AdapterContractVersion, r.SourceVersion, r.TargetVersion, r.RunID, !r.StartedAt.IsZero(), r.Platform, r.Result)
	}
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(bytes.NewReader(raw))
	if err != nil || parsed.Outcome() != "passed" {
		t.Fatalf("Forgejo report parse failed: outcome=%v err=%v", parsed, err)
	}
	var htmlBytes bytes.Buffer
	if err := r.HTML(&htmlBytes); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Forgejo 15.0.9 → 16.0.5", "Repository file fingerprints", "baseline", "target", "recovery"} {
		if !strings.Contains(htmlBytes.String(), text) {
			t.Fatalf("Forgejo HTML omitted %q", text)
		}
	}
	if strings.Contains(htmlBytes.String(), "synthetic repository content") || strings.Contains(string(raw), "synthetic repository content") {
		t.Fatal("Forgejo report exposed repository content")
	}
}

func TestForgejoPassedOutcomeRequiresThreeMatchingNonemptyDatabaseAndFileSnapshots(t *testing.T) {
	t.Run("missing recovery database projection", func(t *testing.T) {
		r := completeForgejoReport()
		delete(r.Snapshots, "recovery")
		if got := r.Outcome(); got != "not-run" {
			t.Fatalf("outcome=%q, want not-run", got)
		}
	})
	t.Run("mismatched target database projection", func(t *testing.T) {
		r := completeForgejoReport()
		snapshot := r.Snapshots["target"]
		snapshot.SHA256 = strings.Repeat("3", 64)
		r.Snapshots["target"] = snapshot
		if got := r.Outcome(); got != "not-run" {
			t.Fatalf("outcome=%q, want not-run", got)
		}
	})
	t.Run("empty database projection", func(t *testing.T) {
		r := completeForgejoReport()
		snapshot := r.Snapshots["baseline"]
		snapshot.Bytes = 0
		r.Snapshots["baseline"] = snapshot
		if got := r.Outcome(); got != "not-run" {
			t.Fatalf("outcome=%q, want not-run", got)
		}
	})
	t.Run("missing recovery file projection", func(t *testing.T) {
		r := completeForgejoReport()
		delete(r.FileSnapshots, "recovery")
		if got := r.Outcome(); got != "not-run" {
			t.Fatalf("outcome=%q, want not-run", got)
		}
	})
	t.Run("mismatched target file projection", func(t *testing.T) {
		r := completeForgejoReport()
		snapshot := r.FileSnapshots["target"]
		snapshot.SHA256 = strings.Repeat("3", 64)
		r.FileSnapshots["target"] = snapshot
		if got := r.Outcome(); got != "not-run" {
			t.Fatalf("outcome=%q, want not-run", got)
		}
	})
	t.Run("empty projection", func(t *testing.T) {
		r := completeForgejoReport()
		snapshot := r.FileSnapshots["baseline"]
		snapshot.Rows = 0
		r.FileSnapshots["baseline"] = snapshot
		if got := r.Outcome(); got != "not-run" {
			t.Fatalf("outcome=%q, want not-run", got)
		}
	})
}

func TestForgejoOutcomeCannotPassInvalidMetadataOrRequiredChecks(t *testing.T) {
	mutations := map[string]func(*Report){
		"wrong-check":           func(r *Report) { r.Checks[3].ID = "baseline.private" },
		"duplicate-check":       func(r *Report) { r.Checks[3].ID = r.Checks[2].ID },
		"wrong-contract":        func(r *Report) { r.AdapterContractVersion = 2 },
		"wrong-adapter":         func(r *Report) { r.Adapter = "miniflux" },
		"wrong-schema-version":  func(r *Report) { r.SchemaVersion = 1 },
		"wrong-source":          func(r *Report) { r.SourceVersion = "15.0.8" },
		"wrong-target":          func(r *Report) { r.TargetVersion = "16.0.4" },
		"unsafe-platform":       func(r *Report) { r.Platform = "windows/arm64" },
		"invalid-run-id":        func(r *Report) { r.RunID = "../private" },
		"unknown-snapshot":      func(r *Report) { r.Snapshots["private"] = r.Snapshots["baseline"] },
		"unknown-file-snapshot": func(r *Report) { r.FileSnapshots["private"] = r.FileSnapshots["baseline"] },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r := completeForgejoReport()
			mutate(r)
			if got := r.Outcome(); got == "passed" {
				t.Fatal("invalid Forgejo report outcome passed")
			}
		})
	}
}

func TestSchema1RejectsForgejoOnlyFields(t *testing.T) {
	raw, err := completeMinifluxReport().JSON()
	if err != nil {
		t.Fatal(err)
	}
	withContract := bytes.Replace(raw, []byte(`"schemaVersion": 1,`), []byte("\"schemaVersion\": 1,\n  \"adapterContractVersion\": 1,"), 1)
	if bytes.Equal(withContract, raw) {
		t.Fatal("test fixture did not add the Forgejo contract field")
	}
	if _, err := Parse(bytes.NewReader(withContract)); err == nil {
		t.Fatal("schema-1 report accepted Forgejo contract metadata")
	}
	withZeroContract := bytes.Replace(raw, []byte(`"schemaVersion": 1,`), []byte("\"schemaVersion\": 1,\n  \"adapterContractVersion\": 0,"), 1)
	if _, err := Parse(bytes.NewReader(withZeroContract)); err == nil {
		t.Fatal("schema-1 report accepted a present zero-valued Forgejo contract field")
	}
	withFiles := bytes.Replace(raw, []byte(`"schemaVersion": 1,`), []byte("\"schemaVersion\": 1,\n  \"fileSnapshots\": {},"), 1)
	if _, err := Parse(bytes.NewReader(withFiles)); err == nil {
		t.Fatal("schema-1 report accepted Forgejo file snapshots")
	}
}

func TestForgejoReportRejectsUnknownSchemaAndInventedPassedOutcome(t *testing.T) {
	raw, err := completeForgejoReport().JSON()
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(raw, []byte(`"schemaVersion": 2`), []byte(`"schemaVersion": 99`), 1)
	if _, err := Parse(bytes.NewReader(unknown)); err == nil || err.Error() != "REPORT_VERSION_UNSUPPORTED" {
		t.Fatalf("unknown schema error=%v", err)
	}
	invented := bytes.Replace(raw, []byte(`"outcome": "passed"`), []byte(`"outcome": "not-run"`), 1)
	if _, err := Parse(bytes.NewReader(invented)); err == nil {
		t.Fatal("invented outcome inconsistent with complete Forgejo evidence was accepted")
	}
}

func completeMinifluxReport() *Report {
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := New("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "windows/amd64", start)
	r.BackupSHA256 = strings.Repeat("1", 64)
	r.BackupBytes = 1024
	for _, phase := range []string{"baseline", "target", "recovery"} {
		r.Snapshots[phase] = Snapshot{SHA256: strings.Repeat("2", 64), Rows: 10, Bytes: 900}
	}
	finish := time.Date(2026, 1, 2, 4, 5, 6, 0, time.UTC)
	r.FinishedAt = &finish
	for _, check := range r.Checks {
		_ = r.Set(check.ID, "passed", "VALIDATED")
	}
	return r
}

func completeForgejoReport() *Report {
	start := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	r := NewForgejo("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "linux/amd64", start)
	r.BackupSHA256 = strings.Repeat("4", 64)
	r.BackupBytes = 2048
	for _, phase := range []string{"baseline", "target", "recovery"} {
		r.Snapshots[phase] = Snapshot{SHA256: strings.Repeat("5", 64), Rows: 12, Bytes: 700}
		r.FileSnapshots[phase] = Snapshot{SHA256: strings.Repeat("6", 64), Rows: 6, Bytes: 410}
	}
	finish := time.Date(2026, 3, 4, 6, 7, 8, 0, time.UTC)
	r.FinishedAt = &finish
	for _, check := range r.Checks {
		_ = r.Set(check.ID, "passed", "VALIDATED")
	}
	return r
}

func TestV2JSONContainsOnlyBoundedMetadataAndSnapshots(t *testing.T) {
	r := completeForgejoReport()
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["fileSnapshots"]; !ok {
		t.Fatal("Forgejo report omitted file snapshots")
	}
	for _, forbidden := range []string{"Token", "token", "privateEmail", "sourcePath", "repositoryContent", "records"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("Forgejo report contained forbidden field %q", forbidden)
		}
	}
}
