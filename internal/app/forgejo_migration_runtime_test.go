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
	var inventoryRun *state.Run
	inventoryChecked := false
	defer func() {
		if inventoryRun != nil && !inventoryChecked {
			_ = assertForgejoMigrationRunInventoryEmpty(t, ctx, client, *inventoryRun)
			inventoryChecked = true
		}
	}()
	runtimeClient := newForgejoMigrationFailureRuntime(client)
	r, runErr := runForgejoWithRuntime(ctx, forgejoConfig(backup), store, runtimeClient, auth)
	if r == nil {
		t.Fatal("failure acceptance returned no report")
	}
	run, stateErr := store.Load(r.RunID)
	stateStatus, resourcesValid := "unavailable", false
	if stateErr == nil {
		stateStatus = run.Status
		resourcesValid = state.ValidateRunResources(run) == nil
		inventoryRun = &run
	}
	targetStatus, targetCode := forgejoMigrationReportCheck(r, "target.migration")
	cleanupStatus, cleanupCode := forgejoMigrationReportCheck(r, "cleanup.ownership")
	t.Logf("FORGEJO_MIGRATION_DIAGNOSTIC run=%s runErr=%s restoreListed=%t restoreApplied=%t triggerInstalled=%t triggerSetup=%s startError=%s migrationWaited=%t waitError=%s migrationExitInspect=%s exitValid=%t exitCode=%d migrationFailed=%t migrationLogRead=%t migrationLogError=%s migrationLogNonempty=%t sentinelObserved=%t reportResult=%s targetCheck=%s/%s cleanupCheck=%s/%s stateStatus=%s resourcesValid=%t",
		r.RunID, forgejoMigrationFailureSafeCode(runErr), runtimeClient.targetRestoreListed, runtimeClient.targetRestoreApplied,
		runtimeClient.triggerInstalled, runtimeClient.triggerSetupErrorCode, runtimeClient.targetMigrationStartErrorCode,
		runtimeClient.targetMigrationWaited, runtimeClient.targetMigrationWaitErrorCode,
		runtimeClient.targetMigrationExitInspectionErrorCode, runtimeClient.targetMigrationExitInspectionValid,
		runtimeClient.targetMigrationExitCode, runtimeClient.targetMigrationFailed, runtimeClient.targetMigrationLogsRead,
		runtimeClient.targetMigrationLogsErrorCode, runtimeClient.targetMigrationLogsNonempty, runtimeClient.sentinelObserved,
		r.Result, targetStatus, targetCode, cleanupStatus, cleanupCode, stateStatus, resourcesValid)
	if runErr == nil || runErr.Error() != "MIGRATION_FAILED" {
		t.Errorf("target migration did not return the fixed MIGRATION_FAILED code")
	}
	if !forgejoMigrationFailureEvidenceAccepted(runtimeClient) {
		t.Errorf("the pinned target migration did not fail after an actual target restore because of the synthetic event trigger")
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
	run, err = store.Load(r.RunID)
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
	if !t.Failed() {
		_ = assertForgejoMigrationRunInventoryEmpty(t, ctx, client, run)
		inventoryChecked = true
	}
	if !t.Failed() {
		t.Logf("FORGEJO_MIGRATION_FAILURE_ACCEPTED transport=%s run=%s targetMigration=MIGRATION_FAILED sentinelObserved=true sourceSHA256=%s cleanup=empty-owned-inventory report=failed", transport, r.RunID, hex.EncodeToString(sourceDigest[:]))
	}
}

type forgejoMigrationFailureRuntime struct {
	*engine.Engine
	targetRestoreListed                    bool
	targetRestoreApplied                   bool
	triggerInstalled                       bool
	targetMigrationWaited                  bool
	targetMigrationFailed                  bool
	triggerSetupErrorCode                  string
	targetMigrationStartErrorCode          string
	targetMigrationWaitErrorCode           string
	targetMigrationExitInspectionErrorCode string
	targetMigrationExitInspectionValid     bool
	targetMigrationExitCode                int
	targetMigrationLogsRead                bool
	targetMigrationLogsErrorCode           string
	targetMigrationLogsNonempty            bool
	sentinelObserved                       bool
}

func newForgejoMigrationFailureRuntime(client *engine.Engine) *forgejoMigrationFailureRuntime {
	return &forgejoMigrationFailureRuntime{
		Engine: client, triggerSetupErrorCode: "NONE", targetMigrationStartErrorCode: "NONE",
		targetMigrationWaitErrorCode: "NONE", targetMigrationExitInspectionErrorCode: "NOT_RUN",
		targetMigrationLogsErrorCode: "NOT_RUN",
	}
}

func assertForgejoMigrationRunInventoryEmpty(t *testing.T, ctx context.Context, client *engine.Engine, run state.Run) bool {
	t.Helper()
	allEmpty := true
	for _, kind := range []string{"container", "network", "volume"} {
		args := []string{kind, "ls"}
		if kind == "container" {
			args = append(args, "--all")
		}
		args = append(args, "--filter", "label=io.rehearse.run="+run.ID,
			"--filter", "label=io.rehearse.owner="+run.OwnerID, "--format", "{{.Names}}")
		output, err := client.Bytes(ctx, args, nil, 16<<10)
		empty := err == nil && strings.TrimSpace(string(output)) == ""
		t.Logf("FORGEJO_MIGRATION_INVENTORY run=%s kind=%s queryOK=%t empty=%t", run.ID, kind, err == nil, empty)
		if !empty {
			t.Errorf("migration failure exact-run %s inventory is not empty", kind)
			allEmpty = false
		}
		for i := range output {
			output[i] = 0
		}
	}
	return allEmpty
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
			c.triggerSetupErrorCode = "SYNTHETIC_MIGRATION_INJECTION_ORDER_INVALID"
			return errors.New("SYNTHETIC_MIGRATION_INJECTION_ORDER_INVALID")
		}
		input := strings.NewReader(forgejoSyntheticMigrationFailureSQL)
		_, err := c.Engine.InsideBytes(ctx, run, "target", "db", []string{"psql", "--username=rehearse", "--dbname=rehearse", "--no-psqlrc", "--quiet", "--single-transaction", "--set=ON_ERROR_STOP=1"}, input, 4096)
		if err != nil {
			c.triggerSetupErrorCode = forgejoMigrationFailureSafeCode(err)
			return errors.New("SYNTHETIC_MIGRATION_TRIGGER_SETUP_FAILED")
		}
		c.triggerInstalled = true
	}
	err := c.Engine.Start(ctx, run, phase, role)
	if phase == "target" && role == "migration" {
		c.targetMigrationStartErrorCode = forgejoMigrationFailureSafeCode(err)
		if err != nil {
			c.inspectTargetMigrationFailure(ctx, run)
		}
	}
	return err
}

func (c *forgejoMigrationFailureRuntime) WaitMigration(ctx context.Context, run state.Run, phase string) error {
	if phase == "target" {
		c.targetMigrationWaited = true
	}
	err := c.Engine.WaitMigration(ctx, run, phase)
	if phase != "target" {
		return err
	}
	c.targetMigrationWaitErrorCode = forgejoMigrationFailureSafeCode(err)
	if err != nil {
		c.inspectTargetMigrationFailure(ctx, run)
	}
	return err
}

func (c *forgejoMigrationFailureRuntime) inspectTargetMigrationFailure(ctx context.Context, run state.Run) {
	c.targetMigrationExitInspectionValid = false
	c.targetMigrationExitInspectionErrorCode = "RUN_RESOURCES_INVALID"
	if state.ValidateRunResources(run) != nil || run.AdapterID() != "forgejo" {
		return
	}
	daemon, err := c.Engine.Info(ctx)
	if err != nil {
		c.targetMigrationExitInspectionErrorCode = forgejoMigrationFailureSafeCode(err)
		return
	}
	if daemon.ID != run.DaemonID {
		c.targetMigrationExitInspectionErrorCode = "DAEMON_ID_MISMATCH"
		return
	}
	var networkResource, migrationResource, dataResource state.Resource
	for _, resource := range run.Resources {
		if resource.Phase != "target" {
			continue
		}
		switch resource.Role {
		case "network":
			networkResource = resource
		case "migration":
			migrationResource = resource
		case "data":
			dataResource = resource
		}
	}
	if networkResource.Name == "" || migrationResource.Name == "" || dataResource.Name == "" {
		c.targetMigrationExitInspectionErrorCode = "RESOURCE_UNKNOWN"
		return
	}
	networkRaw, err := c.Engine.Bytes(ctx, []string{"network", "inspect", networkResource.Name, "--format", "{{json .}}"}, nil, 2<<20)
	if err != nil {
		c.targetMigrationExitInspectionErrorCode = forgejoMigrationFailureSafeCode(err)
		return
	}
	network, err := engine.ParseIsolatedNetwork(networkRaw)
	for i := range networkRaw {
		networkRaw[i] = 0
	}
	if err != nil {
		c.targetMigrationExitInspectionErrorCode = forgejoMigrationFailureSafeCode(err)
		return
	}
	if err := engine.VerifyOwnership(networkResource, network.LiveResource, run.DaemonID, daemon.ID); err != nil {
		c.targetMigrationExitInspectionErrorCode = forgejoMigrationFailureSafeCode(err)
		return
	}
	allowedNames := make(map[string]bool)
	for _, resource := range run.Resources {
		if resource.Phase == "target" && resource.Kind == "container" {
			allowedNames[resource.Name] = true
		}
	}
	for _, endpoint := range network.Containers {
		if !allowedNames[endpoint.Name] {
			c.targetMigrationExitInspectionErrorCode = "NETWORK_FOREIGN_ATTACHMENT"
			return
		}
	}
	containerRaw, err := c.Engine.Bytes(ctx, []string{"container", "inspect", migrationResource.Name, "--format", "{{json .}}"}, nil, 2<<20)
	if err != nil {
		c.targetMigrationExitInspectionErrorCode = forgejoMigrationFailureSafeCode(err)
		return
	}
	container, err := engine.ParseBoundedContainerForAdapter(containerRaw, engine.ContainerBoundary{
		Adapter: "forgejo", Role: "migration", NetworkName: network.Name, NetworkID: network.ID,
		DataVolumeName: dataResource.Name, ExpectedImage: forgejo.TargetImage,
		ExpectedUser: "1000:1000", Stage: engine.ContainerStageMigrationExited,
	})
	for i := range containerRaw {
		containerRaw[i] = 0
	}
	if err != nil {
		c.targetMigrationExitInspectionErrorCode = forgejoMigrationFailureSafeCode(err)
		return
	}
	if err := engine.VerifyOwnership(migrationResource, container.LiveResource, run.DaemonID, daemon.ID); err != nil {
		c.targetMigrationExitInspectionErrorCode = forgejoMigrationFailureSafeCode(err)
		return
	}
	c.targetMigrationExitCode = container.ExitCode
	if container.Status != "exited" || container.Running || container.ExitCode == 0 {
		c.targetMigrationExitInspectionErrorCode = "CONTAINER_STATE_FAILED"
		return
	}
	c.targetMigrationExitInspectionValid = true
	c.targetMigrationExitInspectionErrorCode = "NONE"
	c.targetMigrationFailed = (c.targetMigrationStartErrorCode == "MIGRATION_FAILED" || c.targetMigrationWaitErrorCode == "MIGRATION_FAILED")
	output, logErr := c.Engine.Bytes(ctx, []string{"container", "logs", "--tail", "100", container.ID}, nil, 64<<10)
	c.targetMigrationLogsRead = logErr == nil
	c.targetMigrationLogsErrorCode = forgejoMigrationFailureSafeCode(logErr)
	if logErr == nil {
		c.targetMigrationLogsNonempty = len(output) > 0
		c.sentinelObserved = bytes.Contains(output, []byte(forgejoSyntheticMigrationFailureMarker))
	}
	for i := range output {
		output[i] = 0
	}
}

func forgejoMigrationFailureEvidenceAccepted(c *forgejoMigrationFailureRuntime) bool {
	if c == nil || !c.targetRestoreListed || !c.targetRestoreApplied || !c.triggerInstalled ||
		!c.targetMigrationFailed || !c.targetMigrationExitInspectionValid || c.targetMigrationExitCode <= 0 ||
		c.targetMigrationExitInspectionErrorCode != "NONE" || !c.targetMigrationLogsRead ||
		c.targetMigrationLogsErrorCode != "NONE" || !c.targetMigrationLogsNonempty || !c.sentinelObserved {
		return false
	}
	failedAtStart := c.targetMigrationStartErrorCode == "MIGRATION_FAILED" &&
		!c.targetMigrationWaited && c.targetMigrationWaitErrorCode == "NONE"
	failedAtWait := c.targetMigrationStartErrorCode == "NONE" &&
		c.targetMigrationWaited && c.targetMigrationWaitErrorCode == "MIGRATION_FAILED"
	return failedAtStart || failedAtWait
}

func forgejoMigrationReportCheck(r *report.Report, id string) (status, code string) {
	if r == nil {
		return "unavailable", "unavailable"
	}
	for _, check := range r.Checks {
		if check.ID == id {
			return check.Status, check.Code
		}
	}
	return "missing", "missing"
}

func forgejoMigrationFailureSafeCode(err error) string {
	if err == nil {
		return "NONE"
	}
	switch err.Error() {
	case "MIGRATION_FAILED", "SYNTHETIC_MIGRATION_TRIGGER_SETUP_FAILED", "SYNTHETIC_MIGRATION_INJECTION_ORDER_INVALID",
		"CONTAINER_STATE_FAILED", "CONTAINER_INSPECT_INVALID", "MIGRATION_EXIT_STATUS_INVALID", "MIGRATION_EXIT_STATUS_MISMATCH",
		"DATABASE_NOT_READY", "DOCKER_COMMAND_FAILED", "CONTAINER_EXEC_FAILED", "CONTAINER_START_FAILED",
		"RESOURCE_OWNERSHIP_FAILED", "RESOURCE_OWNERSHIP_MISMATCH", "RESOURCE_UNKNOWN", "RUN_RESOURCES_INVALID",
		"NETWORK_FAILED", "NETWORK_BOUNDARY_FAILED", "NETWORK_INSPECT_INVALID", "NETWORK_FOREIGN_ATTACHMENT",
		"CONTAINER_BOUNDARY_FAILED", "CONTAINER_IMAGE_MISMATCH", "CONTAINER_USER_MISMATCH", "DAEMON_ID_MISMATCH",
		"OPERATION_FAILED", "CANCELED":
		return err.Error()
	default:
		return "OTHER"
	}
}

func hasForgejoArgument(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func TestForgejoMigrationFailureEvidenceAcceptsFastStartAndWaitFailures(t *testing.T) {
	base := newForgejoMigrationFailureRuntime(nil)
	if base.targetMigrationStartErrorCode != "NONE" || base.targetMigrationWaitErrorCode != "NONE" {
		t.Fatal("unattempted migration lifecycle operations must be represented by NONE")
	}
	base.targetRestoreListed = true
	base.targetRestoreApplied = true
	base.triggerInstalled = true
	base.targetMigrationFailed = true
	base.targetMigrationExitInspectionValid = true
	base.targetMigrationExitInspectionErrorCode = "NONE"
	base.targetMigrationExitCode = 1
	base.targetMigrationLogsRead = true
	base.targetMigrationLogsErrorCode = "NONE"
	base.targetMigrationLogsNonempty = true
	base.sentinelObserved = true
	t.Run("fast nonzero exit from start", func(t *testing.T) {
		candidate := *base
		candidate.targetMigrationStartErrorCode = "MIGRATION_FAILED"
		if !forgejoMigrationFailureEvidenceAccepted(&candidate) {
			t.Fatal("strictly inspected fast migration failure was rejected")
		}
	})
	t.Run("nonzero exit from wait", func(t *testing.T) {
		candidate := *base
		candidate.targetMigrationStartErrorCode = "NONE"
		candidate.targetMigrationWaited = true
		candidate.targetMigrationWaitErrorCode = "MIGRATION_FAILED"
		if !forgejoMigrationFailureEvidenceAccepted(&candidate) {
			t.Fatal("strictly inspected wait migration failure was rejected")
		}
	})
	for name, mutate := range map[string]func(*forgejoMigrationFailureRuntime){
		"missing restore list":    func(c *forgejoMigrationFailureRuntime) { c.targetRestoreListed = false },
		"missing applied restore": func(c *forgejoMigrationFailureRuntime) { c.targetRestoreApplied = false },
		"missing trigger":         func(c *forgejoMigrationFailureRuntime) { c.triggerInstalled = false },
		"unexpected start error":  func(c *forgejoMigrationFailureRuntime) { c.targetMigrationStartErrorCode = "CONTAINER_STATE_FAILED" },
		"missing inspected exit":  func(c *forgejoMigrationFailureRuntime) { c.targetMigrationExitInspectionValid = false },
		"inspection error": func(c *forgejoMigrationFailureRuntime) {
			c.targetMigrationExitInspectionErrorCode = "CONTAINER_BOUNDARY_FAILED"
		},
		"zero exit code":            func(c *forgejoMigrationFailureRuntime) { c.targetMigrationExitCode = 0 },
		"missing sentinel":          func(c *forgejoMigrationFailureRuntime) { c.sentinelObserved = false },
		"unreadable migration logs": func(c *forgejoMigrationFailureRuntime) { c.targetMigrationLogsRead = false },
		"log read error":            func(c *forgejoMigrationFailureRuntime) { c.targetMigrationLogsErrorCode = "DOCKER_COMMAND_FAILED" },
		"wait error after successful start": func(c *forgejoMigrationFailureRuntime) {
			c.targetMigrationStartErrorCode = "NONE"
			c.targetMigrationWaited = true
			c.targetMigrationWaitErrorCode = "CONTAINER_STATE_FAILED"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *base
			candidate.targetMigrationStartErrorCode = "MIGRATION_FAILED"
			mutate(&candidate)
			if forgejoMigrationFailureEvidenceAccepted(&candidate) {
				t.Fatal("incomplete or unrelated migration failure evidence was accepted")
			}
		})
	}
}

func assertForgejoMigrationFailureChecks(t *testing.T, r *report.Report) {
	t.Helper()
	for _, id := range []string{
		"backup.inspect", "baseline.network", "baseline.restore", "baseline.schema", "baseline.data", "baseline.files", "baseline.auth", "baseline.unauthenticated", "target.restore", "source.unchanged", "cleanup.ownership",
	} {
		assertForgejoMigrationFailureCheck(t, r, id, "passed", "VALIDATED")
	}
	assertForgejoMigrationFailureCheck(t, r, "target.migration", "failed", "MIGRATION_FAILED")
	for _, id := range []string{
		"target.network",
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
