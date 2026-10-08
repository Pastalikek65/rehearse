package engine

import (
	"context"
	"github.com/Pastalikek65/rehearse/internal/state"
	"os"
	"testing"
	"time"
)

func TestOwnedPhaseOnExplicitDisposableWSLRuntime(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_WSL")
	if distro == "" {
		t.Skip("actual phase qualification requires explicit disposable WSL runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := e.Cleanup(cleanupCtx, run); err != nil {
			t.Errorf("owned cleanup failed: %v", err)
		}
	}()
	if err := e.CreatePhase(ctx, run, dir, "baseline"); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(ctx, run, "baseline", "db"); err != nil {
		t.Fatal(err)
	}
	if err := e.WaitDatabase(ctx, run, "baseline"); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(ctx, run, "baseline", "probe"); err != nil {
		t.Fatal(err)
	}
	raw, err := e.InsideBytes(ctx, run, "baseline", "probe", []string{"curl", "--version"}, nil, 4096)
	if err != nil || len(raw) == 0 {
		t.Fatalf("actual probe output missing: %v", err)
	}
	if err := e.CreatePhase(ctx, run, dir, "baseline"); err == nil {
		t.Fatal("silently adopted existing phase")
	}
	if err := e.Cleanup(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := e.Cleanup(ctx, run); err != nil {
		t.Fatalf("cleanup not idempotent: %v", err)
	}
	t.Log("actual fixed Compose phase, DB readiness, WSL probe stdout and repeated owned cleanup passed")
}
