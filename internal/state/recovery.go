package state

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type processLiveness uint8

const (
	processUnknown processLiveness = iota
	processAlive
	processDead
)

type processProbe interface {
	probe(pid int) processLiveness
}

type localProcessProbe struct{}

func (localProcessProbe) probe(pid int) processLiveness { return probeLocalProcess(pid) }

const recoveryClaimDirectoryName = lockDirectoryName + ".recovery"

// RecoveryResult contains safe status only; it never exposes the lock token,
// hostname, PID, backup path, Docker identity or resource names.
type RecoveryResult struct {
	RunID         string `json:"runId"`
	LockRecovered bool   `json:"lockRecovered"`
	StatusUpdated bool   `json:"statusUpdated"`
	Status        string `json:"status"`
}

// RecoverDeadLock explicitly removes one dead local process lock. It never
// adopts the old lock or deletes any application resources. Call Cleanup
// separately after reviewing the run's owned-resource state.
func (s *Store) RecoverDeadLock(ctx context.Context, runID string) (RecoveryResult, error) {
	return s.recoverDeadLock(ctx, runID, localProcessProbe{})
}

func (s *Store) recoverDeadLock(ctx context.Context, runID string, probe processProbe) (result RecoveryResult, retErr error) {
	if ctx == nil || ctx.Err() != nil {
		return RecoveryResult{}, code("CANCELED")
	}
	if s == nil || !validID(runID) {
		return RecoveryResult{}, code("RUN_ID_INVALID")
	}
	runDir, err := s.RunDir(runID)
	if err != nil {
		return RecoveryResult{}, err
	}
	guard, err := acquireLockMutationGuard(runDir)
	if err != nil {
		return RecoveryResult{}, err
	}
	defer func() {
		if err := guard.Release(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	run, err := s.Load(runID)
	if err != nil {
		return RecoveryResult{}, err
	}
	result = RecoveryResult{RunID: runID, Status: run.Status}
	lockDir := filepath.Join(runDir, lockDirectoryName)
	claimDir := filepath.Join(runDir, recoveryClaimDirectoryName)
	_, lockStatErr := os.Lstat(lockDir)
	_, claimStatErr := os.Lstat(claimDir)
	if lockStatErr != nil && !errors.Is(lockStatErr, os.ErrNotExist) {
		return result, code("RUN_LOCK_INFO_INVALID")
	}
	if claimStatErr != nil && !errors.Is(claimStatErr, os.ErrNotExist) {
		return result, code("RUN_RECOVERY_CLAIM_FAILED")
	}
	hasCanonicalLock := lockStatErr == nil
	hasStableClaim := claimStatErr == nil
	if hasCanonicalLock && hasStableClaim {
		// An uncompleted claim beside a canonical lock is ambiguous. In
		// particular, never rename or remove a possibly live canonical holder.
		return result, code("RUN_LOCK_OWNERSHIP_CHANGED")
	}
	if !hasCanonicalLock && !hasStableClaim {
		return result, code("RUN_LOCK_NOT_FOUND")
	}

	if hasStableClaim {
		entries, err := readClaimedLockDirectoryEntries(runDir, recoveryClaimDirectoryName)
		if err != nil {
			return result, err
		}
		if len(entries) == 0 {
			// The metadata was removed only after the prior recovery had twice
			// verified process death. Completing this exact empty claim is the
			// durable post-delete checkpoint, not resource cleanup.
			if ctx.Err() != nil {
				return result, code("CANCELED")
			}
			if err := os.Remove(claimDir); err != nil {
				return result, code("RUN_RECOVERY_CLAIM_FAILED")
			}
			result.LockRecovered = true
			if err := syncDir(runDir); err != nil {
				return result, code("STATE_SYNC_FAILED")
			}
		} else {
			claimed, err := readClaimedLockRecord(runDir, recoveryClaimDirectoryName, s.owner, runID)
			if err != nil {
				return result, err
			}
			localHost, err := os.Hostname()
			if err != nil || strings.TrimSpace(localHost) == "" {
				return result, code("RUN_RECOVERY_PROCESS_UNKNOWN")
			}
			if claimed.Hostname != localHost {
				return result, code("RUN_RECOVERY_FOREIGN_HOST")
			}
			if probe == nil {
				return result, code("RUN_RECOVERY_PROCESS_UNKNOWN")
			}
			if err := requireDeadProcess(probe, claimed.PID); err != nil {
				return result, err
			}
			if ctx.Err() != nil {
				return result, code("CANCELED")
			}
			currentHost, err := os.Hostname()
			if err != nil || currentHost != claimed.Hostname {
				return result, code("RUN_RECOVERY_FOREIGN_HOST")
			}
			if err := requireDeadProcess(probe, claimed.PID); err != nil {
				return result, err
			}
			latest, err := readClaimedLockRecord(runDir, recoveryClaimDirectoryName, s.owner, runID)
			if err != nil || !sameLockRecord(claimed, latest) {
				return result, code("RUN_LOCK_OWNERSHIP_CHANGED")
			}
			if err := os.Remove(filepath.Join(claimDir, lockRecordName)); err != nil {
				return result, code("RUN_RECOVERY_CLAIM_FAILED")
			}
			result.LockRecovered = true
			if err := os.Remove(claimDir); err != nil {
				return result, code("RUN_RECOVERY_CLAIM_FAILED")
			}
			if err := syncDir(runDir); err != nil {
				return result, code("STATE_SYNC_FAILED")
			}
		}
	} else {
		old, err := s.readLockRecord(runID)
		if err != nil {
			return result, err
		}
		localHost, err := os.Hostname()
		if err != nil || strings.TrimSpace(localHost) == "" {
			return result, code("RUN_RECOVERY_PROCESS_UNKNOWN")
		}
		if old.Hostname != localHost {
			return result, code("RUN_RECOVERY_FOREIGN_HOST")
		}
		if probe == nil {
			return result, code("RUN_RECOVERY_PROCESS_UNKNOWN")
		}
		if err := requireDeadProcess(probe, old.PID); err != nil {
			return result, err
		}
		if ctx.Err() != nil {
			return result, code("CANCELED")
		}
		if err := os.Rename(lockDir, claimDir); err != nil {
			return result, code("RUN_LOCK_OWNERSHIP_CHANGED")
		}
		if err := syncDir(runDir); err != nil {
			// The stable claim makes this checkpoint discoverable if recovery is
			// interrupted. Restore the canonical lock when possible.
			if restoreErr := restoreClaim(runDir, claimDir, lockDir); restoreErr != nil {
				return result, code("RUN_LOCK_OWNERSHIP_CHANGED")
			}
			return result, code("STATE_SYNC_FAILED")
		}
		claimed, err := readClaimedLockRecord(runDir, recoveryClaimDirectoryName, s.owner, runID)
		if err != nil || !sameLockRecord(old, claimed) {
			_ = restoreClaim(runDir, claimDir, lockDir)
			return result, code("RUN_LOCK_OWNERSHIP_CHANGED")
		}
		currentHost, err := os.Hostname()
		if err != nil || currentHost != old.Hostname {
			_ = restoreClaim(runDir, claimDir, lockDir)
			return result, code("RUN_RECOVERY_FOREIGN_HOST")
		}
		if err := requireDeadProcess(probe, old.PID); err != nil {
			_ = restoreClaim(runDir, claimDir, lockDir)
			return result, err
		}
		claimed, err = readClaimedLockRecord(runDir, recoveryClaimDirectoryName, s.owner, runID)
		if err != nil || !sameLockRecord(old, claimed) {
			_ = restoreClaim(runDir, claimDir, lockDir)
			return result, code("RUN_LOCK_OWNERSHIP_CHANGED")
		}
		if err := os.Remove(filepath.Join(claimDir, lockRecordName)); err != nil {
			return result, code("RUN_RECOVERY_CLAIM_FAILED")
		}
		result.LockRecovered = true
		if err := os.Remove(claimDir); err != nil {
			return result, code("RUN_RECOVERY_CLAIM_FAILED")
		}
		if err := syncDir(runDir); err != nil {
			return result, code("STATE_SYNC_FAILED")
		}
	}

	if run.Status != "running" || run.PendingBackup != nil {
		return result, nil
	}
	if err := guard.Release(); err != nil {
		return result, err
	}
	newLock, err := s.AcquireRunLock(runID)
	if err != nil {
		if err.Error() == "RUN_LOCK_HELD" || err.Error() == "RUN_LOCK_GUARD_HELD" {
			return result, code("RUN_RECOVERY_STATUS_HELD")
		}
		return result, code("RUN_RECOVERY_STATUS_FAILED")
	}
	latest, err := s.Load(runID)
	if err == nil && latest.Status == "running" && latest.PendingBackup == nil {
		latest.Status = "interrupted"
		err = s.Save(newLock, latest)
		// Save may report a directory-sync failure after the atomic rename has
		// already persisted the new status. Reflect only what can be read back;
		// the operation still returns a failure below when Save errored.
		persisted, readErr := s.Load(runID)
		if readErr == nil {
			result.Status = persisted.Status
			result.StatusUpdated = persisted.Status == "interrupted"
		} else {
			result.Status = "unknown"
		}
	} else if err == nil {
		result.Status = latest.Status
	}
	releaseErr := newLock.Release()
	if err != nil {
		return result, code("RUN_RECOVERY_STATUS_FAILED")
	}
	if releaseErr != nil {
		return result, code("RUN_LOCK_RELEASE_FAILED")
	}
	return result, nil
}

func requireDeadProcess(probe processProbe, pid int) error {
	if probe == nil {
		return code("RUN_RECOVERY_PROCESS_UNKNOWN")
	}
	liveness := probe.probe(pid)
	if liveness == processDead {
		return nil
	}
	if liveness == processAlive {
		return code("RUN_RECOVERY_PROCESS_LIVE")
	}
	return code("RUN_RECOVERY_PROCESS_UNKNOWN")
}

func sameLockRecord(left, right lockRecord) bool {
	return reflect.DeepEqual(left, right)
}

func readClaimedLockRecord(runDir, name, owner, runID string) (lockRecord, error) {
	children, err := readClaimedLockDirectoryEntries(runDir, name)
	if err != nil {
		return lockRecord{}, err
	}
	if len(children) != 1 || children[0].Name() != lockRecordName {
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	claimDir := filepath.Join(runDir, name)
	metadataPath := filepath.Join(claimDir, lockRecordName)
	info, err := os.Lstat(metadataPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 4096 {
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	f, err := os.Open(metadataPath)
	if err != nil {
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	dec := json.NewDecoder(io.LimitReader(f, 4097))
	dec.DisallowUnknownFields()
	var record lockRecord
	decodeErr := dec.Decode(&record)
	var extra any
	trailingErr := dec.Decode(&extra)
	closeErr := f.Close()
	if decodeErr != nil || trailingErr != io.EOF || closeErr != nil {
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	if err := validateLockRecord(record, owner, runID); err != nil {
		return lockRecord{}, err
	}
	return record, nil
}

func readClaimedLockDirectoryEntries(runDir, name string) ([]os.DirEntry, error) {
	claimDir := filepath.Join(runDir, name)
	if err := regularDir(claimDir); err != nil {
		return nil, code("RUN_LOCK_INFO_INVALID")
	}
	dir, err := os.Open(claimDir)
	if err != nil {
		return nil, code("RUN_LOCK_INFO_INVALID")
	}
	children, readErr := dir.ReadDir(2)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil || len(children) > 1 {
		return nil, code("RUN_LOCK_INFO_INVALID")
	}
	return children, nil
}

func restoreClaim(runDir, claimDir, lockDir string) error {
	if _, err := os.Lstat(lockDir); err == nil {
		return code("RUN_LOCK_OWNERSHIP_CHANGED")
	} else if !errors.Is(err, os.ErrNotExist) {
		return code("RUN_LOCK_OWNERSHIP_CHANGED")
	}
	if err := os.Rename(claimDir, lockDir); err != nil {
		return code("RUN_LOCK_OWNERSHIP_CHANGED")
	}
	if err := syncDir(runDir); err != nil {
		return err
	}
	return nil
}
