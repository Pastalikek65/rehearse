package engine

import (
	"context"
	"encoding/json"
	"github.com/Pastalikek65/rehearse/internal/state"
	"io"
	"os"
	"testing"
	"time"
)

func TestCleanupPreservesForeignSameNameVolumeOnDisposableRuntime(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_WSL")
	if distro == "" {
		t.Skip("explicit disposable runtime required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e, err := NewWSL(distro)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := e.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(daemon.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := resource(run, "baseline", "volume")
	// A synthetic sentinel deliberately lacks Rehearse's ownership labels.
	// Test teardown has its own nonce proof and cannot borrow product authority.
	if err := e.Execute(ctx, []string{"volume", "create", "--name", r.Name, "--label", "io.rehearse.test-sentinel=" + run.ID}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		raw, err := e.inspect(c, r)
		if err != nil {
			t.Errorf("sentinel disappeared before test teardown: %v", err)
			return
		}
		var doc struct {
			Name   string
			Labels map[string]string
		}
		if json.Unmarshal(raw, &doc) != nil || doc.Name != r.Name || doc.Labels["io.rehearse.test-sentinel"] != run.ID {
			t.Error("test cannot verify its sentinel")
			return
		}
		if err := e.Execute(c, []string{"volume", "rm", r.Name}, nil, io.Discard); err != nil {
			t.Errorf("test sentinel teardown: %v", err)
		}
	}()
	if err := e.Cleanup(ctx, run); err == nil || err.Error() != "CLEANUP_OWNERSHIP_HELD" {
		t.Fatalf("foreign object not held: %v", err)
	}
	if exists, err := e.exists(ctx, r); err != nil || !exists {
		t.Fatalf("foreign volume removed: %v", err)
	}
	otherDaemon := run
	otherDaemon.DaemonID = "other-daemon"
	if err := e.Cleanup(ctx, otherDaemon); err == nil || err.Error() != "DAEMON_ID_MISMATCH" {
		t.Fatalf("wrong daemon accepted: %v", err)
	}
	t.Log("foreign same-name volume preserved; wrong daemon refused; nonce-owned sentinel removed only by test teardown")
}
