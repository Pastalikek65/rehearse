package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/fixture"
	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/state"
)

// These opt-in tests use only a separately created synthetic fixture on an
// explicitly disposable engine. Unit-test skips are not qualification evidence.
func TestCompleteForgejoRehearsalOnExplicitDisposableWSLRuntime(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_FORGEJO_WSL")
	if distro == "" {
		t.Skip("requires explicit disposable WSL engine; creates its own synthetic fixture")
	}
	if distro != "RehearseTest2404-20261008" {
		t.Fatal("qualification requires the isolated RehearseTest2404-20261008 engine")
	}
	e, err := engine.NewWSL(distro)
	if err != nil {
		t.Fatal(err)
	}
	backup, auth, _ := bootstrapForgejoSourceFixture(t, e)
	completeForgejoRehearsal(t, e, backup, auth, "Windows-native Go / explicit WSL")
}

func TestCompleteForgejoRehearsalOnExplicitDisposableLinuxRuntime(t *testing.T) {
	if os.Getenv("REHEARSE_TEST_FORGEJO_LINUX") != "disposable" {
		t.Skip("requires explicit disposable Linux engine; creates its own synthetic fixture")
	}
	e, err := engine.New()
	if err != nil {
		t.Fatal(err)
	}
	backup, auth, _ := bootstrapForgejoSourceFixture(t, e)
	completeForgejoRehearsal(t, e, backup, auth, "native Linux Go / local Unix socket")
}

func completeForgejoRehearsal(t *testing.T, e *engine.Engine, backup string, auth forgejo.Auth, transport string) {
	t.Helper()
	source, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal("synthetic archive unavailable")
	}
	before := sha256.Sum256(source)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, runErr := Run(ctx, forgejoConfig(backup), store, e, miniflux.Auth{APIToken: auth.Token})
	if r == nil {
		t.Fatalf("no report from real Forgejo run: %v", runErr)
	}
	if runErr != nil {
		t.Logf("failed checks: %+v", r.Checks)
		t.Fatalf("real Forgejo rehearsal failed: %v", runErr)
	}
	if r.Result != "passed" || r.Validate() != nil || len(r.Checks) != 25 {
		t.Fatal("real run did not satisfy the complete Forgejo report contract")
	}
	for _, check := range r.Checks {
		if check.Status != "passed" || check.Code != "VALIDATED" {
			t.Fatalf("required check not passed: %+v", check)
		}
	}
	if r.Snapshots["baseline"].Rows != 2 {
		t.Fatal("synthetic global user/repository projection does not contain exactly two rows")
	}
	if r.FileSnapshots["baseline"].Rows == 0 || r.FileSnapshots["baseline"].Bytes == 0 {
		t.Fatal("Git-file projection is empty")
	}
	after, err := os.ReadFile(backup)
	if err != nil || sha256.Sum256(after) != before || r.BackupSHA256 != hex.EncodeToString(before[:]) {
		t.Fatal("source archive identity changed")
	}
	loaded, err := ReadReport(store, r.RunID)
	if err != nil || loaded.Result != "passed" {
		t.Fatalf("persisted passing report is unavailable: %v", err)
	}
	run, err := store.Load(r.RunID)
	if err != nil || run.Status != "completed" || run.SchemaVersion != 2 || len(run.Resources) != 24 {
		t.Fatal("terminal Forgejo state is invalid")
	}
	dir, err := store.RunDir(r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(filepath.Join(dir, "report.html"))
	if err != nil {
		t.Fatal(err)
	}
	private := []string{backup, auth.Token, fixture.ForgejoUsername, fixture.ForgejoPassword, fixture.ForgejoRepository}
	for _, file := range fixture.ForgejoFixture().Files {
		private = append(private, string(file.Content), file.Path)
	}
	for _, value := range private {
		if value != "" && (strings.Contains(string(raw), value) || strings.Contains(string(html), value)) {
			t.Fatal("report exposed private fixture fields, token or input path")
		}
	}
	for _, kind := range []string{"container", "volume", "network"} {
		args := []string{kind, "ls"}
		if kind == "container" {
			args = append(args, "--all")
		}
		args = append(args, "--quiet", "--filter", "label=io.rehearse.run="+r.RunID)
		out, err := e.Bytes(ctx, args, nil, 8192)
		if err != nil || len(strings.TrimSpace(string(out))) != 0 {
			t.Fatalf("owned %s inventory is not empty", kind)
		}
		t.Logf("CLEANUP_INVENTORY kind=%s count=0", kind)
	}
	t.Logf("FORGEJO_PRODUCT_QUALIFIED transport=%s run=%s checks=25 databaseSHA256=%s filesSHA256=%s sourceSHA256=%s backupBytes=%d reportPrivacy=passed",
		transport, r.RunID, r.Snapshots["baseline"].SHA256, r.FileSnapshots["baseline"].SHA256, r.BackupSHA256, r.BackupBytes)
}
