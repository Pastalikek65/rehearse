package state

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recoveryProbeFunc func(int) processLiveness

func (probe recoveryProbeFunc) probe(pid int) processLiveness { return probe(pid) }

func newStaleRun(t *testing.T, status string, pending bool) (*Store, Run, lockRecord) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("fixture-daemon-private")
	if err != nil {
		t.Fatal(err)
	}
	run.Status = status
	if pending {
		run.PendingBackup = &Backup{SourcePath: "C:\\private\\source.dump", SHA256: strings.Repeat("b", 64), Bytes: 10}
	}
	if err := s.save(run); err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	record := lockRecord{LockInfo: LockInfo{
		SchemaVersion: 1,
		RunID:         run.ID,
		OwnerID:       s.owner,
		Hostname:      host,
		PID:           2147483647,
		StartedAt:     time.Now().UTC(),
	}, Token: strings.Repeat("c", 32)}
	writeRecoveryLock(t, s, run.ID, record)
	return s, run, record
}

func writeRecoveryLock(t *testing.T, s *Store, id string, record lockRecord) {
	t.Helper()
	runDir, err := s.RunDir(id)
	if err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(runDir, lockDirectoryName)
	if err := os.Mkdir(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, lockRecordName), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverDeadRunningProcessMarksRunInterruptedAndReleasesLock(t *testing.T) {
	s, run, record := newStaleRun(t, "running", false)
	probes := 0
	probe := recoveryProbeFunc(func(pid int) processLiveness {
		probes++
		if pid != record.PID {
			t.Fatalf("probed PID %d, want recorded PID %d", pid, record.PID)
		}
		return processDead
	})
	result, err := s.recoverDeadLock(context.Background(), run.ID, probe)
	if err != nil {
		t.Fatal(err)
	}
	if probes != 2 {
		t.Fatalf("dead process checked %d times, want pre-claim and post-claim checks", probes)
	}
	if !result.LockRecovered || !result.StatusUpdated || result.Status != "interrupted" {
		t.Fatalf("unexpected recovery result: %#v", result)
	}
	loaded, err := s.Load(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "interrupted" {
		t.Fatalf("running run status not marked interrupted: %#v", loaded)
	}
	if _, err := os.Lstat(filepath.Join(s.root, run.ID, lockDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("canonical lock remains after successful recovery: %v", err)
	}
}

func TestRecoverDeadLockAfterTerminatingOurSyntheticChild(t *testing.T) {
	s, run, record := newStaleRun(t, "running", false)
	child := startOwnedProbeChild(t)
	record.PID = child.Process.Pid
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(s.root, run.ID, lockDirectoryName, lockRecordName)
	if err := os.WriteFile(lockPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if got := probeLocalProcess(record.PID); got != processAlive {
		stopOwnedProbeChild(t, child)
		t.Fatalf("owned synthetic process is not alive before kill: %v", got)
	}
	stopOwnedProbeChild(t, child)
	if got := waitForProcessState(t, record.PID, processDead); got != processDead {
		t.Fatalf("owned synthetic process is not dead after kill: %v", got)
	}

	result, err := s.RecoverDeadLock(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("recover lock for verified-dead owned child: %v", err)
	}
	if !result.LockRecovered || !result.StatusUpdated || result.Status != "interrupted" {
		t.Fatalf("unexpected real-process recovery result: %#v", result)
	}
	loaded, err := s.Load(run.ID)
	if err != nil || loaded.Status != "interrupted" {
		t.Fatalf("persisted run was not marked interrupted: %#v err=%v", loaded, err)
	}
	if _, err := s.InspectRunLock(run.ID); err == nil || err.Error() != "RUN_LOCK_NOT_FOUND" {
		t.Fatalf("dead child lock remains after recovery: %v", err)
	}
}

func TestRecoverDeadLockResumesStableClaimAfterRenameInterruption(t *testing.T) {
	s, run, record := newStaleRun(t, "running", false)
	runDir := filepath.Join(s.root, run.ID)
	canonical := filepath.Join(runDir, lockDirectoryName)
	claim := filepath.Join(runDir, recoveryClaimDirectoryName)
	if err := os.Rename(canonical, claim); err != nil {
		t.Fatal(err)
	}
	probes := 0
	result, err := s.recoverDeadLock(context.Background(), run.ID, recoveryProbeFunc(func(pid int) processLiveness {
		probes++
		if pid != record.PID {
			t.Fatalf("probed PID %d, want claimed PID %d", pid, record.PID)
		}
		return processDead
	}))
	if err != nil {
		t.Fatalf("recover stable claim left by interrupted rename: %v", err)
	}
	if probes != 2 || !result.LockRecovered || !result.StatusUpdated || result.Status != "interrupted" {
		t.Fatalf("stable claim recovery was incomplete: probes=%d result=%#v", probes, result)
	}
	if _, err := os.Lstat(claim); !os.IsNotExist(err) {
		t.Fatalf("stable claim remains after recovery: %v", err)
	}
	loaded, err := s.Load(run.ID)
	if err != nil || loaded.Status != "interrupted" {
		t.Fatalf("interrupted run status was not persisted: %#v err=%v", loaded, err)
	}
}

func TestRecoverDeadLockRemovesOnlyEmptyStableClaimAndPreservesPendingStage(t *testing.T) {
	t.Run("running claim already committed", func(t *testing.T) {
		s, run, _ := newStaleRun(t, "running", false)
		runDir := filepath.Join(s.root, run.ID)
		claim := filepath.Join(runDir, recoveryClaimDirectoryName)
		if err := os.Rename(filepath.Join(runDir, lockDirectoryName), claim); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(claim, lockRecordName)); err != nil {
			t.Fatal(err)
		}
		result, err := s.recoverDeadLock(context.Background(), run.ID, recoveryProbeFunc(func(int) processLiveness {
			t.Fatal("empty committed claim should not require an unavailable process record")
			return processUnknown
		}))
		if err != nil || !result.LockRecovered || !result.StatusUpdated || result.Status != "interrupted" {
			t.Fatalf("empty stable claim was not completed safely: result=%#v err=%v", result, err)
		}
		if _, err := os.Lstat(claim); !os.IsNotExist(err) {
			t.Fatalf("empty stable claim remains: %v", err)
		}
	})

	t.Run("pending backup remains staging", func(t *testing.T) {
		s, run, _ := newStaleRun(t, "staging", true)
		runDir := filepath.Join(s.root, run.ID)
		claim := filepath.Join(runDir, recoveryClaimDirectoryName)
		if err := os.Rename(filepath.Join(runDir, lockDirectoryName), claim); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(claim, lockRecordName)); err != nil {
			t.Fatal(err)
		}
		result, err := s.recoverDeadLock(context.Background(), run.ID, recoveryProbeFunc(func(int) processLiveness {
			t.Fatal("empty committed claim should not require an unavailable process record")
			return processUnknown
		}))
		loaded, loadErr := s.Load(run.ID)
		if err != nil || loadErr != nil || !result.LockRecovered || result.StatusUpdated || result.Status != "staging" || loaded.Status != "staging" || loaded.PendingBackup == nil {
			t.Fatalf("empty claim changed pending backup state: result=%#v run=%#v err=%v loadErr=%v", result, loaded, err, loadErr)
		}
	})
}

func TestRecoverDeadLockFailsClosedWhenCanonicalAndStableClaimCoexist(t *testing.T) {
	s, run, record := newStaleRun(t, "running", false)
	runDir := filepath.Join(s.root, run.ID)
	claim := filepath.Join(runDir, recoveryClaimDirectoryName)
	if err := os.Mkdir(claim, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claim, lockRecordName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	probes := 0
	_, err = s.recoverDeadLock(context.Background(), run.ID, recoveryProbeFunc(func(int) processLiveness {
		probes++
		return processDead
	}))
	if err == nil || err.Error() != "RUN_LOCK_OWNERSHIP_CHANGED" || probes != 0 {
		t.Fatalf("ambiguous canonical+claim state was not rejected before process action: err=%v probes=%d", err, probes)
	}
	for _, path := range []string{filepath.Join(runDir, lockDirectoryName), claim} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("ambiguous lock state was mutated at %s: %v", path, err)
		}
	}
}

func TestAcquireRunLockDoesNotIgnoreUnfinishedRecoveryClaim(t *testing.T) {
	s, run, _ := newStaleRun(t, "running", false)
	runDir := filepath.Join(s.root, run.ID)
	claim := filepath.Join(runDir, recoveryClaimDirectoryName)
	if err := os.Rename(filepath.Join(runDir, lockDirectoryName), claim); err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if lock != nil {
		_ = lock.Release()
	}
	if err == nil || err.Error() != "RUN_RECOVERY_CLAIM_PENDING" {
		t.Fatalf("normal acquisition ignored unfinished recovery claim: err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(runDir, lockDirectoryName)); !os.IsNotExist(err) {
		t.Fatalf("canonical lock created beside unfinished claim: %v", err)
	}
	if _, err := os.Lstat(claim); err != nil {
		t.Fatalf("unfinished claim was altered: %v", err)
	}
}

func TestRecoverDeadLockPreservesUnsafeStableClaims(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(t *testing.T, s *Store, claimPath string, record *lockRecord)
		liveness processLiveness
		wantCode string
		probes   int
	}{
		{
			name: "malformed metadata",
			mutate: func(t *testing.T, _ *Store, claimPath string, _ *lockRecord) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(claimPath, lockRecordName), []byte("private malformed marker"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "RUN_LOCK_INFO_INVALID",
		},
		{
			name: "unexpected claim child",
			mutate: func(t *testing.T, _ *Store, claimPath string, _ *lockRecord) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(claimPath, "private-unexpected-child"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "RUN_LOCK_INFO_INVALID",
		},
		{
			name:     "live process",
			liveness: processAlive,
			wantCode: "RUN_RECOVERY_PROCESS_LIVE",
			probes:   1,
		},
		{
			name: "foreign host",
			mutate: func(t *testing.T, _ *Store, _ string, record *lockRecord) {
				t.Helper()
				record.Hostname = "foreign-host"
			},
			wantCode: "RUN_RECOVERY_FOREIGN_HOST",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, run, record := newStaleRun(t, "running", false)
			runDir := filepath.Join(s.root, run.ID)
			claim := filepath.Join(runDir, recoveryClaimDirectoryName)
			if err := os.Rename(filepath.Join(runDir, lockDirectoryName), claim); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(t, s, claim, &record)
				recordPath := filepath.Join(claim, lockRecordName)
				if tc.name != "malformed metadata" && tc.name != "unexpected claim child" {
					raw, err := json.Marshal(record)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(recordPath, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			probes := 0
			_, err := s.recoverDeadLock(context.Background(), run.ID, recoveryProbeFunc(func(int) processLiveness {
				probes++
				return tc.liveness
			}))
			if err == nil || err.Error() != tc.wantCode || probes != tc.probes {
				t.Fatalf("unsafe claim not rejected as expected: err=%v probes=%d want=%s/%d", err, probes, tc.wantCode, tc.probes)
			}
			if _, err := os.Lstat(claim); err != nil {
				t.Fatalf("unsafe claim was removed: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(runDir, lockDirectoryName)); !os.IsNotExist(err) {
				t.Fatalf("unsafe claim was restored or canonical lock appeared: %v", err)
			}
			loaded, err := s.Load(run.ID)
			if err != nil || loaded.Status != "running" {
				t.Fatalf("unsafe claim changed run status: %#v err=%v", loaded, err)
			}
		})
	}
}

func TestRecoverDeadLockAfterRecoveryChildDiesAtClaimCheckpoint(t *testing.T) {
	s, run, _ := newStaleRun(t, "running", false)
	cmd, output := startRecoveryClaimCrashChild(t, s.root, run.ID)
	line, err := output.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "CLAIMED" {
		t.Fatalf("recovery child did not reach post-rename checkpoint: line=%q err=%v", line, err)
	}
	stopOwnedProbeChild(t, cmd)
	runDir := filepath.Join(s.root, run.ID)
	canonical := filepath.Join(runDir, lockDirectoryName)
	claim := filepath.Join(runDir, recoveryClaimDirectoryName)
	if _, err := os.Lstat(canonical); !os.IsNotExist(err) {
		t.Fatalf("canonical lock exists after child died at claim checkpoint: %v", err)
	}
	if _, err := os.Lstat(claim); err != nil {
		t.Fatalf("stable claim was not durable after child death: %v", err)
	}
	result, err := s.RecoverDeadLock(context.Background(), run.ID)
	if err != nil || !result.LockRecovered || !result.StatusUpdated || result.Status != "interrupted" {
		t.Fatalf("parent could not finish dead-child recovery claim: result=%#v err=%v", result, err)
	}
}

func TestRecoveryClaimCrashChild(t *testing.T) {
	if os.Getenv("REHEARSE_RECOVERY_CLAIM_CHILD") != "1" {
		return
	}
	s, err := Open(os.Getenv("REHEARSE_RECOVERY_CLAIM_STATE"))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stdout, "STATE_OPEN_FAILED")
		return
	}
	probeCalls := 0
	_, err = s.recoverDeadLock(context.Background(), os.Getenv("REHEARSE_RECOVERY_CLAIM_ID"), recoveryProbeFunc(func(int) processLiveness {
		probeCalls++
		if probeCalls == 2 {
			_, _ = fmt.Fprintln(os.Stdout, "CLAIMED")
			for {
				time.Sleep(time.Hour)
			}
		}
		return processDead
	}))
	_, _ = fmt.Fprintf(os.Stdout, "RECOVERY_RETURNED:%v\n", err)
}
func startRecoveryClaimCrashChild(t *testing.T, stateRoot, runID string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryClaimCrashChild$")
	cmd.Env = appendFilteredEnv(os.Environ(), "REHEARSE_RECOVERY_CLAIM_CHILD", "REHEARSE_RECOVERY_CLAIM_STATE", "REHEARSE_RECOVERY_CLAIM_ID")
	cmd.Env = append(cmd.Env, "REHEARSE_RECOVERY_CLAIM_CHILD=1", "REHEARSE_RECOVERY_CLAIM_STATE="+stateRoot, "REHEARSE_RECOVERY_CLAIM_ID="+runID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start recovery crash-checkpoint child: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, bufio.NewReader(stdout)
}

func TestRecoverDeadLockPreservesPendingBackupStaging(t *testing.T) {
	s, run, _ := newStaleRun(t, "staging", true)
	result, err := s.recoverDeadLock(context.Background(), run.ID, recoveryProbeFunc(func(int) processLiveness { return processDead }))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusUpdated || result.Status != "staging" || loaded.Status != "staging" || loaded.PendingBackup == nil {
		t.Fatalf("recovery changed pending backup state: result=%#v run=%#v", result, loaded)
	}
}

func TestRecoverDeadLockFailsClosedForLiveUnknownAndForeignProcess(t *testing.T) {
	cases := []struct {
		name       string
		status     processLiveness
		host       string
		wantCode   string
		probeCalls int
	}{
		{name: "live", status: processAlive, wantCode: "RUN_RECOVERY_PROCESS_LIVE", probeCalls: 1},
		{name: "permission unknown", status: processUnknown, wantCode: "RUN_RECOVERY_PROCESS_UNKNOWN", probeCalls: 1},
		{name: "foreign host", status: processDead, host: "another-host", wantCode: "RUN_RECOVERY_FOREIGN_HOST", probeCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, run, record := newStaleRun(t, "running", false)
			if tc.host != "" {
				record.Hostname = tc.host
				runDir := filepath.Join(s.root, run.ID, lockDirectoryName)
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(runDir, lockRecordName), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			_, err := s.recoverDeadLock(context.Background(), run.ID, recoveryProbeFunc(func(int) processLiveness {
				calls++
				return tc.status
			}))
			if err == nil || err.Error() != tc.wantCode {
				t.Fatalf("got error %v, want %s", err, tc.wantCode)
			}
			if calls != tc.probeCalls {
				t.Fatalf("probe called %d times, want %d", calls, tc.probeCalls)
			}
			if _, err := os.Lstat(filepath.Join(s.root, run.ID, lockDirectoryName)); err != nil {
				t.Fatalf("refused lock was mutated: %v", err)
			}
			loaded, err := s.Load(run.ID)
			if err != nil || loaded.Status != "running" {
				t.Fatalf("refusal changed run metadata: run=%#v err=%v", loaded, err)
			}
		})
	}
}

func TestRecoverDeadLockCancellationAndMalformedMetadataDoNotMutate(t *testing.T) {
	t.Run("canceled before claim", func(t *testing.T) {
		s, run, _ := newStaleRun(t, "running", false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		_, err := s.recoverDeadLock(ctx, run.ID, recoveryProbeFunc(func(int) processLiveness { calls++; return processDead }))
		if err == nil || err.Error() != "CANCELED" || calls != 0 {
			t.Fatalf("cancellation did not stop before probing/mutation: err=%v calls=%d", err, calls)
		}
		if _, err := os.Lstat(filepath.Join(s.root, run.ID, lockDirectoryName)); err != nil {
			t.Fatalf("canceled recovery moved the lock: %v", err)
		}
	})

	t.Run("malformed lock", func(t *testing.T) {
		s, run, _ := newStaleRun(t, "running", false)
		lockPath := filepath.Join(s.root, run.ID, lockDirectoryName, lockRecordName)
		if err := os.WriteFile(lockPath, []byte("not-json"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := s.recoverDeadLock(context.Background(), run.ID, recoveryProbeFunc(func(int) processLiveness { return processDead }))
		if err == nil || err.Error() != "RUN_LOCK_INFO_INVALID" {
			t.Fatalf("malformed lock accepted: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(s.root, run.ID, lockDirectoryName)); err != nil {
			t.Fatalf("malformed lock was mutated: %v", err)
		}
	})
}

func TestRecoverDeadLockPreservesClaimWhenRecordChangesAfterRename(t *testing.T) {
	s, run, record := newStaleRun(t, "running", false)
	calls := 0
	probe := recoveryProbeFunc(func(pid int) processLiveness {
		calls++
		if calls == 2 {
			entries, err := os.ReadDir(filepath.Join(s.root, run.ID))
			if err != nil {
				t.Fatal(err)
			}
			var claim string
			for _, entry := range entries {
				if entry.Name() == recoveryClaimDirectoryName {
					claim = filepath.Join(s.root, run.ID, entry.Name())
				}
			}
			if claim == "" {
				t.Fatal("recovery did not atomically claim the original lock directory")
			}
			record.Token = strings.Repeat("d", 32)
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(claim, lockRecordName), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		return processDead
	})
	_, err := s.recoverDeadLock(context.Background(), run.ID, probe)
	if err == nil || err.Error() != "RUN_LOCK_OWNERSHIP_CHANGED" {
		t.Fatalf("changed claimed lock was not rejected: %v", err)
	}
	canonical := filepath.Join(s.root, run.ID, lockDirectoryName)
	if _, err := os.Lstat(canonical); err != nil {
		t.Fatalf("unverified lock directory was not restored for operator review: %v", err)
	}
	claimedRecord, err := s.readLockRecord(run.ID)
	if err != nil || claimedRecord.Token != strings.Repeat("d", 32) {
		t.Fatalf("unverified lock record was not preserved unchanged: record=%#v err=%v", claimedRecord, err)
	}
	loaded, err := s.Load(run.ID)
	if err != nil || loaded.Status != "running" {
		t.Fatalf("unverified recovery changed run state: %#v err=%v", loaded, err)
	}
}

func TestRecoverDeadLockBlocksNewCrossProcessCanonicalHolderDuringClaim(t *testing.T) {
	s, run, _ := newStaleRun(t, "running", false)
	calls := 0
	var acquireOutcome string
	probe := recoveryProbeFunc(func(int) processLiveness {
		calls++
		if calls == 2 {
			acquireOutcome = tryCrossProcessRunLockAcquire(t, s.root, run.ID)
		}
		return processDead
	})
	result, err := s.recoverDeadLock(context.Background(), run.ID, probe)
	if err != nil {
		t.Fatalf("recovery failed after blocking competing lock acquisition: result=%#v err=%v", result, err)
	}
	if acquireOutcome != "RUN_LOCK_GUARD_HELD" {
		t.Fatalf("cross-process lock acquisition entered the claim window: outcome=%q", acquireOutcome)
	}
	if !result.LockRecovered || !result.StatusUpdated || result.Status != "interrupted" {
		t.Fatalf("unexpected partial recovery result: %#v", result)
	}
	newLock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatalf("canonical lock did not become available after recovery: %v", err)
	}
	if err := newLock.Release(); err != nil {
		t.Fatalf("release lock acquired after recovery: %v", err)
	}
}

func TestRunLockAcquireChild(t *testing.T) {
	if os.Getenv("REHEARSE_RUN_LOCK_CHILD") != "1" {
		return
	}
	s, err := Open(os.Getenv("REHEARSE_RUN_LOCK_STATE"))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stdout, "STATE_OPEN_FAILED")
		return
	}
	lock, err := s.AcquireRunLock(os.Getenv("REHEARSE_RUN_LOCK_ID"))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stdout, err.Error())
		return
	}
	defer lock.Release()
	_, _ = fmt.Fprintln(os.Stdout, "ACQUIRED")
	if _, err := os.Stdin.Read(make([]byte, 1)); err != nil {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "RELEASED")
}

func tryCrossProcessRunLockAcquire(t *testing.T, stateRoot, runID string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunLockAcquireChild$")
	cmd.Env = appendFilteredEnv(os.Environ(), "REHEARSE_RUN_LOCK_CHILD", "REHEARSE_RUN_LOCK_STATE", "REHEARSE_RUN_LOCK_ID")
	cmd.Env = append(cmd.Env, "REHEARSE_RUN_LOCK_CHILD=1", "REHEARSE_RUN_LOCK_STATE="+stateRoot, "REHEARSE_RUN_LOCK_ID="+runID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start canonical lock acquisition child: %v", err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read lock acquisition child result: %v", err)
	}
	outcome := strings.TrimSpace(line)
	if outcome == "ACQUIRED" {
		if _, err := stdin.Write([]byte("x")); err != nil {
			t.Fatalf("release competing child lock: %v", err)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("canonical lock acquisition child failed: %v", err)
	}
	return outcome
}

func TestRecoverDeadLockRejectsUnsafeRunIDsWithoutFilesystemMutation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../outside", "", strings.Repeat("a", 31), strings.Repeat("A", 32)} {
		if _, err := s.RecoverDeadLock(context.Background(), id); err == nil || err.Error() != "RUN_ID_INVALID" {
			t.Fatalf("accepted unsafe run ID %q: %v", id, err)
		}
	}
}
