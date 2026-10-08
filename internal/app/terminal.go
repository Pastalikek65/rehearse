package app

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/state"
)

const terminalMarkerName = "report.finalizing"

// commitTerminalReport publishes a report under the held run lock. A state
// rename or lock removal can succeed before its following durability operation
// fails. A marker blocks ReadReport until both operations succeed, even when
// error-path file removal is refused. Save retries remain under the lock; no
// state mutation is attempted after release. Marker removal is the last commit
// point, with no further fallible operation; transactional power-loss recovery
// is not promised.
func commitTerminalReport(dir string, r *report.Report, run state.Run, save func(state.Run) error, release func() error) (returnErr error, released bool) {
	marker, markerErr := beginTerminalPublication(dir)
	if markerErr != nil {
		if run.PendingBackup == nil && run.Status != "cleanup-held" {
			run.Status = "failed"
		}
		_ = save(run)
		released = release() == nil
		return code("OPERATION_FAILED"), released
	}
	writeErr := writeReportFiles(dir, r)
	if writeErr != nil && run.PendingBackup == nil && run.Status != "cleanup-held" {
		run.Status = "failed"
	}
	saveErr := save(run)
	if writeErr != nil || saveErr != nil {
		invalidateTerminalReport(dir)
		if run.PendingBackup == nil && run.Status != "cleanup-held" {
			run.Status = "failed"
		}
		// A failed Save may already have renamed completed state into place.
		// Best-effort demotion complements report invalidation; it cannot turn
		// persistent storage failure into a successful operation.
		_ = save(run)
		returnErr = code("OPERATION_FAILED")
	}
	if err := release(); err != nil {
		invalidateTerminalReport(dir)
		returnErr = code("OPERATION_FAILED")
	} else {
		released = true
	}
	if returnErr == nil {
		path := filepath.Join(dir, terminalMarkerName)
		current, err := os.Lstat(path)
		if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || current.Size() != 0 || !os.SameFile(marker, current) {
			returnErr = code("OPERATION_FAILED")
		} else if err := os.Remove(path); err != nil {
			returnErr = code("OPERATION_FAILED")
		}
		if returnErr != nil {
			invalidateTerminalReport(dir)
		}
	}
	return returnErr, released
}

func beginTerminalPublication(dir string) (os.FileInfo, error) {
	f, err := os.OpenFile(filepath.Join(dir, terminalMarkerName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, code("REPORT_WRITE_FAILED")
	}
	info, statErr := f.Stat()
	syncErr := f.Sync()
	closeErr := f.Close()
	if statErr != nil || syncErr != nil || closeErr != nil {
		return nil, code("REPORT_WRITE_FAILED")
	}
	return info, nil
}

func terminalPublicationAbsent(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, terminalMarkerName))
	return errors.Is(err, os.ErrNotExist)
}

func invalidateTerminalReport(dir string) {
	// Fixed children only. Remove does not traverse a directory or follow a
	// symbolic link. ReadReport requires both files to be intact.
	_ = os.Remove(filepath.Join(dir, "report.json"))
	_ = os.Remove(filepath.Join(dir, "report.html"))
}
