package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryReportsPendingTerminalPublicationWithoutChangingStoredStatus(t *testing.T) {
	for _, kind := range []string{"regular-file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.Create("private-history-daemon-marker")
			if err != nil {
				t.Fatal(err)
			}
			run.Status = "completed"
			if err := store.save(run); err != nil {
				t.Fatal(err)
			}
			runDir, err := store.RunDir(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(runDir, "report.finalizing")
			switch kind {
			case "regular-file":
				if err := os.WriteFile(marker, []byte("private-marker-content"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(marker, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "marker-target")
				if err := os.WriteFile(target, []byte("private-marker-target"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, marker); err != nil {
					t.Skipf("symlink creation is unavailable on this host: %v", err)
				}
			}

			snapshot, err := store.History(20)
			if err != nil || len(snapshot.Entries) != 1 {
				t.Fatalf("history failed to include run: snapshot=%+v err=%v", snapshot, err)
			}
			entry := snapshot.Entries[0]
			if entry.Status != "completed" || !entry.ReportFinalizationPending {
				t.Fatalf("pending marker changed status or was hidden: %#v", entry)
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Entries []map[string]any `json:"entries"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded.Entries) != 1 || decoded.Entries[0]["reportFinalizationPending"] != true {
				t.Fatalf("history JSON omitted pending-publication flag: %s err=%v", raw, err)
			}
			for _, private := range []string{"private-history-daemon-marker", "private-marker-content", "private-marker-target"} {
				if strings.Contains(string(raw), private) {
					t.Fatalf("history leaked marker or daemon contents %q: %s", private, raw)
				}
			}
		})
	}
}

func TestHistoryOmitsTerminalPublicationFlagWhenMarkerIsAbsent(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("private-history-daemon-marker")
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "completed"
	if err := store.save(run); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.History(20)
	if err != nil || len(snapshot.Entries) != 1 {
		t.Fatalf("history failed to include run: snapshot=%+v err=%v", snapshot, err)
	}
	if snapshot.Entries[0].Status != "completed" || snapshot.Entries[0].ReportFinalizationPending {
		t.Fatalf("absent marker was reported pending: %#v", snapshot.Entries[0])
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "reportFinalizationPending") {
		t.Fatalf("history JSON emitted a false pending field for a normal record: %s", raw)
	}
}
