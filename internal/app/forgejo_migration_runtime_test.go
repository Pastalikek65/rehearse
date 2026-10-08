package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/fixture"
	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/state"
)

const forgejoSyntheticMigrationFailureMarker = "REHEARSE_SYNTHETIC_MIGRATION_FAILURE"

const forgejoSyntheticMigrationFailureSQL = `CREATE FUNCTION public.rehearse_injected_migration_failure() RETURNS event_trigger
LANGUAGE plpgsql
AS $rehearse$
BEGIN
  RAISE EXCEPTION 'REHEARSE_SYNTHETIC_MIGRATION_FAILURE';
END;
$rehearse$;
CREATE EVENT TRIGGER rehearse_injected_migration_failure
ON ddl_command_start
WHEN TAG IN ('ALTER TABLE', 'CREATE TABLE', 'CREATE INDEX')
EXECUTE FUNCTION public.rehearse_injected_migration_failure();
`

// This opt-in failure acceptance test uses the token returned in memory by
// the synthetic fixture bootstrap. It never reads the helper sidecar: on
// Windows that file inherits its candidate-directory ACL and is not treated
// as OS-private. The token is a public synthetic fixture credential only.
func TestForgejoTargetMigrationFailureOnExplicitDisposableWSLRuntime(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_FORGEJO_MIGRATION_FAILURE_WSL")
	if distro == "" {
		t.Skip("requires explicit disposable WSL engine; creates and cleans its own synthetic fixture")
	}
	if distro != "RehearseTest2404-20261008" {
		t.Fatal("migration-failure acceptance is restricted to RehearseTest2404-20261008")
	}
	client, err := engine.NewWSL(distro)
	if err != nil {
		t.Fatal("could not select explicit disposable WSL engine")
	}
	backup, auth, _ := bootstrapForgejoSourceFixture(t, client)
	runForgejoMigrationFailureAcceptance(t, client, backup, auth, "Windows-native Go / explicit WSL")
}

func TestForgejoTargetMigrationFailureOnExplicitDisposableLinuxRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		if os.Getenv("REHEARSE_TEST_FORGEJO_MIGRATION_FAILURE_LINUX") != "" {
			t.Fatal("Linux migration-failure acceptance must run from native Linux Go")
		}
		t.Skip("requires native Linux Go and an explicit disposable local Docker socket")
	}
	if os.Getenv("REHEARSE_TEST_FORGEJO_MIGRATION_FAILURE_LINUX") != "disposable" {
		t.Skip("requires REHEARSE_TEST_FORGEJO_MIGRATION_FAILURE_LINUX=disposable")
	}
	client, err := engine.New()
	if err != nil {
		t.Fatal("could not select explicit local Unix-socket Docker engine")
	}
	backup, auth, _ := bootstrapForgejoSourceFixture(t, client)
	runForgejoMigrationFailureAcceptance(t, client, backup, auth, "native Linux Go / local Unix socket")
}

func runForgejoMigrationFailureAcceptance(t *testing.T, client *engine.Engine, backup string, auth forgejo.Auth, transport string) {
	t.Helper()
	if client == nil || auth.Token == "" {
		t.Fatal("synthetic migration-failure fixture is unavailable")
	}
	source, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal("synthetic archive candidate is unavailable")
	}
	sourceDigest := sha256.Sum256(source)
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal("could not open isolated migration-failure state")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	runtimeClient := &forgejoMigrationFailureRuntime{Engine: client}
	r, runErr := runForgejoWithRuntime(ctx, forgejoConfig(backup), store, runtimeClient, auth)
	if r == nil {
		t.Fatal("failure acceptance returned no report")
	}
	if runErr == nil || runErr.Error() != "MIGRATION_FAILED" {
		t.Fatal("target migration did not return the fixed MIGRATION_FAILED code")
	}
	if !runtimeClient.targetRestoreListed || !runtimeClient.targetRestoreApplied || !runtimeClient.triggerInstalled ||
		!runtimeClient.targetMigrationWaited || !runtimeClient.targetMigrationFailed || !runtimeClient.sentinelObserved {
		t.Fatal("the pinned target migration did not fail after an actual target restore because of the synthetic event trigger")
	}
	assertForgejoMigrationFailureChecks(t, r)
	if r.Adapter != "forgejo" || r.SchemaVersion != 2 || r.AdapterContractVersion != 1 || r.Result != "failed" || r.Validate() != nil {
		t.Fatal("migration-failure report is not a valid failed Forgejo report")
	}
	if r.BackupSHA256 != hex.EncodeToString(sourceDigest[:]) || r.BackupBytes != uint64(len(source)) {
		t.Fatal("report does not bind the original synthetic archive")
	}
	if _, ok := r.Snapshots["baseline"]; !ok || r.FileSnapshots["baseline"].Rows == 0 || r.FileSnapshots["baseline"].Bytes == 0 {
		t.Fatal("baseline migration-failure checks lack nonempty synthetic data evidence")
	}

	persisted, err := ReadReport(store, r.RunID)
	if err != nil || persisted.Result != "failed" || persisted.Validate() != nil {
		t.Fatal("no valid failed report was readable after migration failure")
	}
	if persisted.Result == "passed" || r.Outcome() == "passed" {
		t.Fatal("a passed report was published for a failed target migration")
	}
	run, err := store.Load(r.RunID)
	if err != nil || state.ValidateRunResources(run) != nil || run.Status != "failed" || run.AdapterID() != "forgejo" {
		t.Fatal("terminal state does not record the owned failed Forgejo run")
	}
	if run.Backup == nil || run.Backup.SHA256 != hex.EncodeToString(sourceDigest[:]) || run.Backup.Bytes != int64(len(source)) {
		t.Fatal("terminal state does not bind the unchanged synthetic archive")
	}
	after, err := os.ReadFile(backup)
	if err != nil || sha256.Sum256(after) != sourceDigest {
		t.Fatal("source archive changed during failed migration rehearsal")
	}
	runDir, err := store.RunDir(r.RunID)
	if err != nil {
		t.Fatal("failed report directory is unavailable")
	}
	html, err := os.ReadFile(filepath.Join(runDir, "report.html"))
	if err != nil || !bytes.Contains(html, []byte("<h1>Rehearse: failed</h1>")) || bytes.Contains(html, []byte("<h1>Rehearse: passed</h1>")) {
		t.Fatal("published HTML does not clearly describe the failed run")
	}
	for _, secret := range append([]string{auth.Token, backup}, forgejoMigrationFixtureDisclosureValues()...) {
		if secret != "" && (bytes.Contains(html, []byte(secret)) || reportContainsValue(persisted, secret)) {
			t.Fatal("failed report disclosed a fixture credential, content, or archive path")
		}
	}
	assertForgejoFixtureRunInventoryEmpty(t, ctx, client, run)
	t.Logf("FORGEJO_MIGRATION_FAILURE_ACCEPTED transport=%s run=%s targetMigration=MIGRATION_FAILED sentinelObserved=true sourceSHA256=%s cleanup=empty-owned-inventory report=failed", transport, r.RunID, hex.EncodeToString(sourceDigest[:]))
}

type forgejoMigrationFailureRuntime struct {
	*engine.Engine
	targetRestoreListed   bool
	targetRestoreApplied  bool
	triggerInstalled      bool
	targetMigrationWaited bool
	targetMigrationFailed bool
	sentinelObserved      bool
}

func (c *forgejoMigrationFailureRuntime) Inside(ctx context.Context, run state.Run, phase, role string, args []string, input io.Reader, output io.Writer) error {
	err := c.Engine.Inside(ctx, run, phase, role, args, input, output)
	if err == nil && phase == "target" && role == "db" && len(args) > 0 && args[0] == "pg_restore" {
		if hasForgejoArgument(args, "--list") {
			c.targetRestoreListed = true
		} else if hasForgejoArgument(args, "--dbname=rehearse") {
			c.targetRestoreApplied = true
		}
	}
	return err
}

func (c *forgejoMigrationFailureRuntime) Start(ctx context.Context, run state.Run, phase, role string) error {
	if phase == "target" && role == "migration" {
		if c.triggerInstalled || !c.targetRestoreListed || !c.targetRestoreApplied || run.AdapterID() != "forgejo" || state.ValidateRunResources(run) != nil {
			return errors.New("SYNTHETIC_MIGRATION_INJECTION_ORDER_INVALID")
		}
		input := strings.NewReader(forgejoSyntheticMigrationFailureSQL)
		_, err := c.Engine.InsideBytes(ctx, run, "target", "db", []string{"psql", "--username=rehearse", "--dbname=rehearse", "--no-psqlrc", "--quiet", "--single-transaction", "--set=ON_ERROR_STOP=1"}, input, 4096)
		if err != nil {
			return errors.New("SYNTHETIC_MIGRATION_TRIGGER_SETUP_FAILED")
		}
		c.triggerInstalled = true
	}
	return c.Engine.Start(ctx, run, phase, role)
}

func (c *forgejoMigrationFailureRuntime) WaitMigration(ctx context.Context, run state.Run, phase string) error {
	err := c.Engine.WaitMigration(ctx, run, phase)
	if phase != "target" {
		return err
	}
	c.targetMigrationWaited = true
	if err == nil || err.Error() != "MIGRATION_FAILED" || state.ValidateRunResources(run) != nil {
		return err
	}
	c.targetMigrationFailed = true
	name := ""
	for _, resource := range run.Resources {
		if resource.Kind == "container" && resource.Phase == "target" && resource.Role == "migration" {
			name = resource.Name
			break
		}
	}
	if name == "" {
		return err
	}
	output, logErr := c.Engine.Bytes(ctx, []string{"container", "logs", "--tail", "100", name}, nil, 64<<10)
	if logErr == nil {
		c.sentinelObserved = bytes.Contains(output, []byte(forgejoSyntheticMigrationFailureMarker))
	}
	for i := range output {
		output[i] = 0
	}
	return err
}

func hasForgejoArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func assertForgejoMigrationFailureChecks(t *testing.T, r *report.Report) {
	t.Helper()
	for _, id := range []string{
		"backup.inspect", "baseline.network", "baseline.restore", "baseline.schema", "baseline.data", "baseline.files", "baseline.auth", "baseline.unauthenticated", "target.network", "target.restore", "source.unchanged", "cleanup.ownership",
	} {
		assertForgejoMigrationFailureCheck(t, r, id, "passed", "VALIDATED")
	}
	assertForgejoMigrationFailureCheck(t, r, "target.migration", "failed", "MIGRATION_FAILED")
	for _, id := range []string{
		"target.schema", "target.data", "target.files", "target.auth", "target.unauthenticated",
		"recovery.network", "recovery.restore", "recovery.schema", "recovery.data", "recovery.files", "recovery.auth", "recovery.unauthenticated",
	} {
		assertForgejoMigrationFailureCheck(t, r, id, "not-run", "NOT_RUN")
	}
}

func assertForgejoMigrationFailureCheck(t *testing.T, r *report.Report, id, status, code string) {
	t.Helper()
	for _, check := range r.Checks {
		if check.ID == id {
			if check.Status != status || check.Code != code {
				t.Fatalf("check %s = %s/%s, want %s/%s", id, check.Status, check.Code, status, code)
			}
			return
		}
	}
	t.Fatalf("required report check %s is missing", id)
}

func reportContainsValue(r *report.Report, value string) bool {
	if r == nil || value == "" {
		return false
	}
	raw, err := r.JSON()
	return err != nil || bytes.Contains(raw, []byte(value))
}

func forgejoMigrationFixtureDisclosureValues() []string {
	values := []string{fixture.ForgejoUsername, fixture.ForgejoPassword, fixture.ForgejoRepository}
	for _, file := range fixture.ForgejoFixture().Files {
		values = append(values, string(file.Content), file.Path)
	}
	return values
}
