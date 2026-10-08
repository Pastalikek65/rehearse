package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryReturnsOnlyPrivateRunSummaryFields(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("private-daemon-marker")
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "staged"
	run.Backup = &Backup{SourcePath: "C:\\private\\source-marker.dump", SHA256: strings.Repeat("a", 64), Bytes: 10}
	if err := s.save(run); err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Truncated || snapshot.ScanLimitReached || snapshot.InvalidRecordsCount != 0 || len(snapshot.Entries) != 1 {
		t.Fatalf("unexpected history bounds: %#v", snapshot)
	}
	entry := snapshot.Entries[0]
	if entry.RunID != run.ID || entry.CreatedAt.IsZero() || entry.Status != "staged" || entry.ResourceCount != 18 || !entry.HasBackup || entry.HasPendingBackup || entry.Code != "" {
		t.Fatalf("unexpected safe run summary: %#v", entry)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-daemon-marker", "C:\\private\\source-marker.dump", strings.Repeat("a", 64)} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("history exposed private run metadata %q: %s", private, raw)
		}
	}
}

func TestHistoryMakesMalformedRecordsVisibleWithoutEchoingUntrustedNames(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	validID := "0123456789abcdef0123456789abcdef"
	validDir := filepath.Join(s.root, validID)
	if err := os.Mkdir(validDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(validDir, "run.json"), []byte("malformed-private-record-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	privateDirName := "private-directory-name-marker"
	if err := os.Mkdir(filepath.Join(s.root, privateDirName), 0700); err != nil {
		t.Fatal(err)
	}

	snapshot, err := s.History(10)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.InvalidRecordsCount != 2 || len(snapshot.Entries) != 1 {
		t.Fatalf("malformed state was hidden or miscounted: %#v", snapshot)
	}
	entry := snapshot.Entries[0]
	if entry.RunID != validID || entry.Status != "invalid" || entry.Code != "RUN_METADATA_INVALID" {
		t.Fatalf("malformed run was not represented by a safe fixed code: %#v", entry)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{privateDirName, "malformed-private-record-marker", s.root} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("history echoed untrusted/private data %q: %s", private, raw)
		}
	}
}

func TestHistoryLimitIsVisibleAndInvalidLimitsFailClosed(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Create("test-daemon"); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.History(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 2 || !snapshot.Truncated || snapshot.ScanLimitReached {
		t.Fatalf("history limit was not made visible: %#v", snapshot)
	}
	if _, err := s.History(101); err == nil || err.Error() != "HISTORY_LIMIT_INVALID" {
		t.Fatalf("accepted limit beyond 100: %v", err)
	}
	if _, err := s.History(-1); err == nil || err.Error() != "HISTORY_LIMIT_INVALID" {
		t.Fatalf("accepted negative limit: %v", err)
	}
}

func TestHistoryBoundsDirectoryScanAndMarksPartialInvalidCount(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1001; i++ {
		name := "invalid-child-" + strings.Repeat("x", 8) + "-" + string(rune('a'+(i%26))) + "-" + strings.Repeat("0", 4)
		name += strings.Repeat("z", i/26)
		if err := os.Mkdir(filepath.Join(s.root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	scanned, readErr := dir.ReadDir(maxHistoryScan)
	closeErr := dir.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read expected bounded directory prefix: read=%v close=%v", readErr, closeErr)
	}
	expectedInvalid := 0
	for _, child := range scanned {
		if child.Name() != "owner-id" {
			expectedInvalid++
		}
	}
	snapshot, err := s.History(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 0 || snapshot.InvalidRecordsCount != expectedInvalid || !snapshot.Truncated || !snapshot.ScanLimitReached {
		t.Fatalf("bounded scan did not report its incomplete view: %#v", snapshot)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "invalid-child-") {
		t.Fatalf("history exposed unsafe child names: %s", raw)
	}
}
