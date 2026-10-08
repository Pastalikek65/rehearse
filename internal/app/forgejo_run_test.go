package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

type forgejoFailureRuntime struct {
	runtimeClient
	cleanupCalls int
}

func (f *forgejoFailureRuntime) Info(context.Context) (engine.Daemon, error) {
	return engine.Daemon{ID: "synthetic-daemon"}, nil
}
func (f *forgejoFailureRuntime) CreatePhase(context.Context, state.Run, string, string) error {
	return code("NETWORK_FAILED")
}
func (f *forgejoFailureRuntime) Cleanup(context.Context, state.Run) error {
	f.cleanupCalls++
	return nil
}
func (f *forgejoFailureRuntime) CopyForgejoData(context.Context, state.Run, string, io.Reader) error {
	return code("OPERATION_FAILED")
}
func (f *forgejoFailureRuntime) ReadForgejoData(context.Context, state.Run, string, io.Writer) error {
	return code("OPERATION_FAILED")
}
func (f *forgejoFailureRuntime) StopForgejoApp(context.Context, state.Run, string) error {
	return code("OPERATION_FAILED")
}

func TestForgejoRunFailureFinalizesReportCleansAndPreservesOriginal(t *testing.T) {
	input := forgejoPlanArchive(t)
	path := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := &forgejoFailureRuntime{}
	r, err := runForgejoWithRuntime(context.Background(), forgejoConfig(path), store, client, forgejo.Auth{Token: strings.Repeat("a", 40)})
	if err == nil || r == nil || r.Adapter != "forgejo" || r.Result != "failed" || client.cleanupCalls != 1 {
		t.Fatalf("result/finalization %v %+v cleanup=%d", err, r, client.cleanupCalls)
	}
	loaded, readErr := ReadReport(store, r.RunID)
	if readErr != nil || loaded.Result != "failed" {
		t.Fatalf("failed report unusable: %v", readErr)
	}
	intent, loadErr := store.Load(r.RunID)
	if loadErr != nil || intent.SchemaVersion != 2 || intent.AdapterID() != "forgejo" || intent.Status != "failed" {
		t.Fatalf("wrong stored intent: %v %+v", loadErr, intent)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(input) {
		t.Fatal("source changed")
	}
}
