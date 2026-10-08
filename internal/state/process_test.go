package state

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLocalProcessProbeTreatsCurrentProcessAsAlive(t *testing.T) {
	if got := probeLocalProcess(os.Getpid()); got != processAlive {
		t.Fatalf("current process probe = %v, want alive", got)
	}
}

func TestLocalProcessProbeTreatsInvalidPIDAsUnknown(t *testing.T) {
	if got := probeLocalProcess(0); got != processUnknown {
		t.Fatalf("invalid process probe = %v, want unknown", got)
	}
}

func TestLocalProcessProbeRecognizesOurTerminatedChild(t *testing.T) {
	child := startOwnedProbeChild(t)
	pid := child.Process.Pid
	if got := probeLocalProcess(pid); got != processAlive {
		stopOwnedProbeChild(t, child)
		t.Fatalf("owned child probe before termination = %v, want alive", got)
	}
	stopOwnedProbeChild(t, child)
	if got := waitForProcessState(t, pid, processDead); got != processDead {
		t.Fatalf("owned child probe after termination = %v, want dead", got)
	}
}

// TestProcessProbeChild is re-executed by tests that need a real process
// handle. It sleeps until the parent explicitly terminates this test-owned
// child; no unrelated PID is ever signaled.
func TestProcessProbeChild(t *testing.T) {
	if os.Getenv("REHEARSE_STATE_PROBE_CHILD") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func startOwnedProbeChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessProbeChild$")
	cmd.Env = append(os.Environ(), "REHEARSE_STATE_PROBE_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start owned process-probe child: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if probeLocalProcess(cmd.Process.Pid) == processAlive {
			return cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("owned child process %d did not become observable as alive", cmd.Process.Pid)
	return nil
}

func stopOwnedProbeChild(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("terminate owned child process %d: %v", cmd.Process.Pid, err)
	}
	_ = cmd.Wait() // A killed test process is expected to exit nonzero.
	if cmd.ProcessState == nil {
		t.Fatalf("owned child process %d did not exit", cmd.Process.Pid)
	}
}

func waitForProcessState(t *testing.T, pid int, want processLiveness) processLiveness {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := probeLocalProcess(pid)
		if got == want {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return probeLocalProcess(pid)
}
