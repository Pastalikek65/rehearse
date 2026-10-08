package engine

import (
	"context"
	"github.com/Pastalikek65/rehearse/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This explicitly opted-in characterization uses the actual Windows process,
// named WSL2 distribution, Compose binary and Docker engine. An ordinary unit
// suite does not count this skip as runtime qualification.
func TestExplicitWSLRuntimeInfoAndCompose(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_WSL")
	if distro == "" {
		t.Skip("set REHEARSE_TEST_WSL to a disposable prepared WSL2 distribution")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e, err := NewWSL(distro)
	if err != nil {
		t.Fatal(err)
	}
	info, err := e.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(filepath.Join(t.TempDir(), "state with spaces"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Compose(run, "target")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "target.compose.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	linuxPath, err := e.HostPath(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.Bytes(ctx, []string{"compose", "-f", linuxPath, "--project-name", "rehearse-" + run.ID, "config", "--format", "json"}, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"internal": true`) || !strings.Contains(string(result), "gateway_mode_ipv4") {
		t.Fatal("Compose lost isolation")
	}
	t.Logf("Windows process used explicitly selected WSL Docker %s; Compose validated generated isolated stack; no resources created", info.Version)
}
