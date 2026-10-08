package state

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	DefaultHistoryLimit = 20
	MaxHistoryEntries   = 100
	maxHistoryScan      = 1000
)

// HistoryEntry is a deliberately narrow summary. It excludes daemon identity,
// backup paths, backup hashes, credentials, reports and application records.
type HistoryEntry struct {
	RunID                     string    `json:"runId"`
	CreatedAt                 time.Time `json:"createdAt,omitempty"`
	Status                    string    `json:"status"`
	ResourceCount             int       `json:"resourceCount,omitempty"`
	HasBackup                 bool      `json:"hasBackup,omitempty"`
	HasPendingBackup          bool      `json:"hasPendingBackup,omitempty"`
	Code                      string    `json:"code,omitempty"`
	ReportFinalizationPending bool      `json:"reportFinalizationPending,omitempty"`
}

// HistorySnapshot bounds both the returned rows and the directory scan. A
// truncated scan is explicit so callers cannot mistake a partial view for a
// complete inventory. InvalidRecordsCount covers malformed entries observed
// during the bounded scan; ScanLimitReached means more may remain unseen.
type HistorySnapshot struct {
	Entries             []HistoryEntry `json:"entries"`
	InvalidRecordsCount int            `json:"invalidRecords"`
	Truncated           bool           `json:"truncated"`
	ScanLimitReached    bool           `json:"scanLimitReached"`
}

// History reads a bounded summary of persisted runs. limit zero selects the
// default; other values outside [1, MaxHistoryEntries] are rejected.
func (s *Store) History(limit int) (HistorySnapshot, error) {
	if limit == 0 {
		limit = DefaultHistoryLimit
	}
	if limit < 1 || limit > MaxHistoryEntries {
		return HistorySnapshot{}, code("HISTORY_LIMIT_INVALID")
	}
	if s == nil || regularDir(s.root) != nil {
		return HistorySnapshot{}, code("STATE_DIRECTORY_INVALID")
	}
	dir, err := os.Open(s.root)
	if err != nil {
		return HistorySnapshot{}, code("HISTORY_READ_FAILED")
	}
	children, readErr := dir.ReadDir(maxHistoryScan + 1)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return HistorySnapshot{}, code("HISTORY_READ_FAILED")
	}

	snapshot := HistorySnapshot{Entries: make([]HistoryEntry, 0, limit)}
	if len(children) > maxHistoryScan {
		snapshot.ScanLimitReached = true
		children = children[:maxHistoryScan]
	}
	entries := make([]HistoryEntry, 0, len(children))
	for _, child := range children {
		name := child.Name()
		if name == "owner-id" {
			continue
		}
		if !validID(name) {
			snapshot.InvalidRecordsCount++
			continue
		}
		path := filepath.Join(s.root, name)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			snapshot.InvalidRecordsCount++
			entries = append(entries, HistoryEntry{RunID: name, Status: "invalid", Code: "RUN_DIRECTORY_INVALID"})
			continue
		}
		run, err := s.Load(name)
		if err != nil {
			snapshot.InvalidRecordsCount++
			entries = append(entries, HistoryEntry{RunID: name, Status: "invalid", Code: "RUN_METADATA_INVALID"})
			continue
		}
		_, publicationErr := os.Lstat(filepath.Join(path, "report.finalizing"))
		entries = append(entries, HistoryEntry{
			RunID:                     run.ID,
			CreatedAt:                 run.CreatedAt,
			Status:                    run.Status,
			ResourceCount:             len(run.Resources),
			HasBackup:                 run.Backup != nil || run.PendingBackup != nil,
			HasPendingBackup:          run.PendingBackup != nil,
			ReportFinalizationPending: !errors.Is(publicationErr, os.ErrNotExist),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].RunID < entries[j].RunID
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if len(entries) > limit {
		snapshot.Truncated = true
		entries = entries[:limit]
	}
	if snapshot.ScanLimitReached {
		snapshot.Truncated = true
	}
	snapshot.Entries = entries
	return snapshot, nil
}
