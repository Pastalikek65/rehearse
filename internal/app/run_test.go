package app

import (
	"context"
	"crypto/sha256"
	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/fixture"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanceledRunCannotCreateDockerResources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, config("unused"), nil, nil, miniflux.Auth{}); err == nil || err.Error() != "CANCELED" {
		t.Fatalf("canceled run not rejected: %v", err)
	}
}

func TestMissingRuntimeCannotPanicOnOtherwiseValidInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic.dump")
	if err := os.WriteFile(path, []byte("PGDMPsynthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), config(path), store, nil, miniflux.Auth{Username: "synthetic-user", Password: "synthetic-password"}); err == nil {
		t.Fatal("nil runtime accepted")
	}
}

func TestCompleteMinifluxRehearsalOnExplicitDisposableWSLRuntime(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_WSL")
	backup := os.Getenv("REHEARSE_TEST_BACKUP")
	if distro == "" || backup == "" {
		t.Skip("actual rehearsal needs explicitly prepared disposable WSL runtime and synthetic backup")
	}
	e, err := engine.NewWSL(distro)
	if err != nil {
		t.Fatal(err)
	}
	completeRehearsal(t, e, backup, "Windows-native Go / explicit WSL")
}

func TestCompleteMinifluxRehearsalOnExplicitDisposableLinuxRuntime(t *testing.T) {
	backup := os.Getenv("REHEARSE_TEST_BACKUP")
	if os.Getenv("REHEARSE_TEST_LINUX") != "disposable" || backup == "" {
		t.Skip("actual native Linux rehearsal requires explicit disposable engine opt-in and synthetic backup")
	}
	e, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	completeRehearsal(t, e, backup, "native Linux Go / local Unix socket")
}

func completeRehearsal(t *testing.T, e *engine.Engine, backup, transport string) {
	t.Helper()
	source, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(source)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(ctx, config(backup), store, e, miniflux.Auth{Username: fixture.AdminUsername, Password: fixture.AdminPassword})
	if err != nil {
		if r != nil {
			t.Logf("failed checks: %#v", r.Checks)
		}
		t.Fatalf("full real rehearsal failed: %v", err)
	}
	if r == nil || r.Outcome() != "passed" {
		t.Fatal("full run not qualified")
	}
	for _, id := range []string{"backup.inspect", "baseline.restore", "baseline.schema", "baseline.data", "baseline.auth", "baseline.unauthenticated", "target.migration", "target.schema", "target.data", "target.removed-transformation", "target.auth", "target.unauthenticated", "recovery.restore", "recovery.schema", "recovery.data", "recovery.auth", "recovery.unauthenticated", "source.unchanged", "cleanup.ownership", "baseline.network", "target.network", "recovery.network"} {
		found := false
		for _, c := range r.Checks {
			if c.ID == id {
				found = c.Status == "passed"
			}
		}
		if !found {
			t.Fatalf("required real check absent/failed: %s", id)
		}
	}
	after, err := os.ReadFile(backup)
	if err != nil || sha256.Sum256(after) != before {
		t.Fatal("source backup changed")
	}
	reopened, err := ReadReport(store, r.RunID)
	if err != nil || reopened.Outcome() != "passed" {
		t.Fatalf("persisted report not usable: %v", err)
	}
	dir, err := store.RunDir(r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(filepath.Join(dir, "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{fixture.AdminPassword, backup} {
		if strings.Contains(string(raw), private) || strings.Contains(string(html), private) {
			t.Fatal("report exposed credential or private source path")
		}
	}
	t.Logf("complete %s rehearsal passed: run %s; core digest %s; backup bytes %d", transport, r.RunID, r.Snapshots["baseline"].SHA256, r.BackupBytes)
}
func TestAPIObservationRejectsReadyHealthWithoutAuthenticatedData(t *testing.T) {
	if _, err := decodeAPIObservation(map[string][]byte{"/v1/me": []byte(`{"status":"ok"}`)}, "2.2.19"); err == nil {
		t.Fatal("health-only response passed real API checks")
	}
}
