package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/fixture"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestNormalConfigCannotSelectKnownBad230ImageOrVersion(t *testing.T) {
	base := `{"schemaVersion":1,"adapter":"miniflux","sourceVersion":"2.2.19","targetVersion":"2.3.3","postgresVersion":"17.11","backupPath":"synthetic.dump","authEnvRefs":{"username":"FIXTURE_USERNAME","password":"FIXTURE_PASSWORD"}}`
	badVersion := strings.Replace(base, `"targetVersion":"2.3.3"`, `"targetVersion":"2.3.0"`, 1)
	if _, err := spec.Parse(strings.NewReader(badVersion)); err == nil {
		t.Fatal("normal config accepted the known-bad 2.3.0 target")
	}
	badImage := strings.TrimSuffix(base, "}") + `,"targetImage":"` + miniflux.BrokenFixtureImage + `"}`
	if _, err := spec.Parse(strings.NewReader(badImage)); err == nil {
		t.Fatal("normal config accepted an arbitrary target image")
	}
}

// This opt-in integration test isolates the upstream 2.3.0 migration defect.
// The normal runner is not asked to select the known-bad image. Instead, this
// test builds the fixed target Compose document, changes only its migration
// image in test code, restores a synthetic schema-125 archive, and applies the
// fixture's orphaned removed-entry variant before starting that one-shot.
func TestKnownBad230MigrationFailsOnSyntheticOrphanAndCleansOwnedPhase(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_WSL")
	backupPath := os.Getenv("REHEARSE_TEST_BACKUP")
	if distro == "" || backupPath == "" {
		t.Skip("known-bad migration test needs an explicit disposable WSL runtime and synthetic backup")
	}

	backupInfo, err := os.Lstat(backupPath)
	if err != nil || !backupInfo.Mode().IsRegular() {
		t.Fatal("synthetic backup is unavailable")
	}
	backupBefore, err := os.ReadFile(backupPath)
	if err != nil || len(backupBefore) < 5 || string(backupBefore[:5]) != "PGDMP" {
		t.Fatal("synthetic custom-format backup is invalid")
	}
	backupHash := sha256.Sum256(backupBefore)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	e, err := engine.NewWSL(distro)
	if err != nil {
		t.Fatal("explicit disposable WSL runtime is unavailable")
	}
	daemon, err := e.Info(ctx)
	if err != nil {
		t.Fatal("explicit disposable Docker daemon is unavailable")
	}
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal("private state store could not be created")
	}
	run, err := store.Create(daemon.ID)
	if err != nil {
		t.Fatal("private negative-control run intent could not be created")
	}

	cleanup := func() error {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		return e.Cleanup(cleanupCtx, run)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("owned negative-control cleanup failed: %v", err)
		}
	}()

	runDir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal("private run directory could not be resolved")
	}
	if err := createNegativeMigrationPhase(ctx, e, run, runDir); err != nil {
		t.Fatalf("isolated known-bad migration phase could not be prepared: %v", err)
	}
	if err := e.Start(ctx, run, "target", "db"); err != nil {
		t.Fatalf("negative-control database did not start: %v", err)
	}
	if err := e.WaitDatabase(ctx, run, "target"); err != nil {
		t.Fatalf("negative-control database readiness failed: %v", err)
	}
	archive, err := os.Open(backupPath)
	if err != nil {
		t.Fatal("synthetic backup could not be opened")
	}
	restoreErr := e.Inside(ctx, run, "target", "db", []string{"pg_restore", "--username=rehearse", "--dbname=rehearse", "--no-owner", "--no-acl", "--exit-on-error"}, archive, io.Discard)
	closeErr := archive.Close()
	if restoreErr != nil || closeErr != nil {
		t.Fatal("synthetic backup restore failed")
	}
	if schema := negativeSQL(ctx, t, e, run, miniflux.SchemaSQL); strings.TrimSpace(string(schema)) != "125" {
		t.Fatal("negative-control restore was not schema 125")
	}
	if err := negativeSQLExec(ctx, e, run, fixture.BrokenMigrationSQL); err != nil {
		t.Fatalf("synthetic orphan fixture precondition failed: %v", err)
	}
	var brokenCounts fixture.Counts
	if err := json.Unmarshal(negativeSQL(ctx, t, e, run, miniflux.CountsSQL), &brokenCounts); err != nil || brokenCounts != fixture.Expected.BrokenVariant.Counts {
		t.Fatal("negative-control fixture counts do not match the reviewed broken variant")
	}

	startErr := e.Start(ctx, run, "target", "migration")
	waitErr := error(nil)
	if startErr == nil {
		waitErr = e.WaitMigration(ctx, run, "target")
	}
	if !isFixedMigrationFailure(startErr) && !isFixedMigrationFailure(waitErr) {
		t.Fatalf("known-bad 2.3.0 migration did not fail with the expected fixed code (start=%v, wait=%v)", startErr, waitErr)
	}

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	cleanupErr := e.Cleanup(cleanupCtx, run)
	cleanupCancel()
	if cleanupErr != nil {
		t.Fatalf("negative-control resources were not cleaned by ownership: %v", cleanupErr)
	}
	if err := negativeResourcesAbsent(ctx, e, run, "target"); err != nil {
		t.Fatalf("negative-control resources remain after cleanup: %v", err)
	}
	after, err := os.ReadFile(backupPath)
	if err != nil || sha256.Sum256(after) != backupHash {
		t.Fatal("negative-control test changed its synthetic source archive")
	}
	t.Logf("actual pinned Miniflux 2.3.0 migration exited nonzero on the synthetic orphan; owned target resources were cleaned; backup sha256=%x bytes=%d", backupHash, len(backupBefore))
}

func createNegativeMigrationPhase(ctx context.Context, e *engine.Engine, run state.Run, runDir string) error {
	if filepath.Base(runDir) != run.ID {
		return fixedTestError("STATE_DIRECTORY_INVALID")
	}
	daemon, err := e.Info(ctx)
	if err != nil || daemon.ID != run.DaemonID {
		return fixedTestError("DAEMON_ID_MISMATCH")
	}
	for _, resource := range run.Resources {
		if resource.Phase != "target" {
			continue
		}
		exists, err := negativeResourceExists(ctx, e, resource)
		if err != nil || exists {
			return fixedTestError("RESOURCE_ALREADY_EXISTS")
		}
	}

	raw, err := engine.Compose(run, "target")
	if err != nil {
		return fixedTestError("COMPOSE_INVALID")
	}
	var compose map[string]any
	if json.Unmarshal(raw, &compose) != nil {
		return fixedTestError("COMPOSE_INVALID")
	}
	services, ok := compose["services"].(map[string]any)
	if !ok {
		return fixedTestError("COMPOSE_INVALID")
	}
	migration, ok := services["migration"].(map[string]any)
	if !ok || migration["image"] != miniflux.TargetImage {
		return fixedTestError("COMPOSE_IMAGE_UNEXPECTED")
	}
	app, ok := services["app"].(map[string]any)
	if !ok || app["image"] != miniflux.TargetImage {
		return fixedTestError("COMPOSE_IMAGE_UNEXPECTED")
	}
	migration["image"] = miniflux.BrokenFixtureImage
	raw, err = json.MarshalIndent(compose, "", "  ")
	if err != nil {
		return fixedTestError("COMPOSE_INVALID")
	}
	raw = append(raw, '\n')

	composePath := filepath.Join(runDir, "target.compose.json")
	if err := os.WriteFile(composePath, raw, 0600); err != nil {
		return fixedTestError("COMPOSE_WRITE_FAILED")
	}
	envPath := filepath.Join(runDir, "empty.env")
	if err := os.WriteFile(envPath, nil, 0600); err != nil {
		return fixedTestError("COMPOSE_WRITE_FAILED")
	}
	composeHost, err := e.HostPath(ctx, composePath)
	if err != nil {
		return fixedTestError("WSL_PATH_TRANSLATION_FAILED")
	}
	dirHost, err := e.HostPath(ctx, runDir)
	if err != nil {
		return fixedTestError("WSL_PATH_TRANSLATION_FAILED")
	}
	envHost, err := e.HostPath(ctx, envPath)
	if err != nil {
		return fixedTestError("WSL_PATH_TRANSLATION_FAILED")
	}
	args := []string{"compose", "--ansi", "never", "--progress", "quiet", "--project-directory", dirHost, "--env-file", envHost, "--project-name", "rehearse-" + run.ID + "-target", "-f", composeHost, "create", "--pull", "missing"}
	if err := e.Execute(ctx, args, nil, io.Discard); err != nil {
		return fixedTestError("COMPOSE_CREATE_FAILED")
	}
	return verifyNegativeMigrationPhase(ctx, e, run)
}

func verifyNegativeMigrationPhase(ctx context.Context, e *engine.Engine, run state.Run) error {
	daemon, err := e.Info(ctx)
	if err != nil || daemon.ID != run.DaemonID {
		return fixedTestError("DAEMON_ID_MISMATCH")
	}
	resources := map[string]state.Resource{}
	for _, resource := range run.Resources {
		if resource.Phase == "target" {
			resources[resource.Role] = resource
		}
	}
	networkRaw, err := negativeInspect(ctx, e, resources["network"])
	if err != nil {
		return err
	}
	network, err := engine.ParseIsolatedNetwork(networkRaw)
	if err != nil || engine.VerifyOwnership(resources["network"], network.LiveResource, run.DaemonID, daemon.ID) != nil {
		return fixedTestError("NETWORK_BOUNDARY_FAILED")
	}
	volumeRaw, err := negativeInspect(ctx, e, resources["volume"])
	if err != nil {
		return err
	}
	volume, err := engine.ParseManagedVolume(volumeRaw)
	if err != nil || engine.VerifyOwnership(resources["volume"], volume, run.DaemonID, daemon.ID) != nil {
		return fixedTestError("VOLUME_BOUNDARY_FAILED")
	}
	allowed := map[string]bool{}
	for role, resource := range resources {
		if resource.Kind != "container" {
			continue
		}
		allowed[resource.Name] = true
		raw, err := negativeInspect(ctx, e, resource)
		if err != nil {
			return err
		}
		bounded, err := engine.ParseBoundedContainer(raw, role, network.Name, network.ID, volume.Name, engine.ContainerStageCreated)
		if err != nil || engine.VerifyOwnership(resource, bounded.LiveResource, run.DaemonID, daemon.ID) != nil {
			return fixedTestError("CONTAINER_BOUNDARY_FAILED")
		}
		if role == "migration" {
			var image struct {
				Config struct{ Image string }
			}
			if json.Unmarshal(raw, &image) != nil || image.Config.Image != miniflux.BrokenFixtureImage {
				return fixedTestError("COMPOSE_IMAGE_UNEXPECTED")
			}
		}
	}
	for _, endpoint := range network.Containers {
		if !allowed[endpoint.Name] {
			return fixedTestError("NETWORK_FOREIGN_ATTACHMENT")
		}
	}
	return nil
}

func negativeResourceExists(ctx context.Context, e *engine.Engine, resource state.Resource) (bool, error) {
	args := []string{resource.Kind, "ls"}
	format := "{{.Name}}"
	if resource.Kind == "container" {
		args = append(args, "--all")
		format = "{{.Names}}"
	}
	args = append(args, "--filter", "name="+resource.Name, "--format", format)
	raw, err := e.Bytes(ctx, args, nil, 16<<10)
	if err != nil {
		return false, fixedTestError("DOCKER_INSPECT_FAILED")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == resource.Name {
			return true, nil
		}
	}
	return false, nil
}

func negativeInspect(ctx context.Context, e *engine.Engine, resource state.Resource) ([]byte, error) {
	raw, err := e.Bytes(ctx, []string{resource.Kind, "inspect", resource.Name, "--format", "{{json .}}"}, nil, 2<<20)
	if err != nil {
		return nil, fixedTestError("DOCKER_INSPECT_FAILED")
	}
	return raw, nil
}

func negativeSQL(ctx context.Context, t *testing.T, e *engine.Engine, run state.Run, query string) []byte {
	t.Helper()
	args := []string{"psql", "--username", "rehearse", "--dbname", "rehearse", "--no-psqlrc", "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1"}
	raw, err := e.InsideBytes(ctx, run, "target", "db", args, strings.NewReader(query+"\n"), 1<<20)
	if err != nil {
		t.Fatal("synthetic negative-control SQL query failed")
	}
	return raw
}

func negativeSQLExec(ctx context.Context, e *engine.Engine, run state.Run, query string) error {
	args := []string{"psql", "--username", "rehearse", "--dbname", "rehearse", "--no-psqlrc", "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1"}
	if err := e.Inside(ctx, run, "target", "db", args, strings.NewReader(query+"\n"), io.Discard); err != nil {
		return fixedTestError("FIXTURE_MUTATION_FAILED")
	}
	return nil
}

func negativeResourcesAbsent(ctx context.Context, e *engine.Engine, run state.Run, phase string) error {
	for _, resource := range run.Resources {
		if resource.Phase != phase {
			continue
		}
		exists, err := negativeResourceExists(ctx, e, resource)
		if err != nil || exists {
			return fixedTestError("CLEANUP_INCOMPLETE")
		}
	}
	return nil
}

type fixedTestError string

func (e fixedTestError) Error() string { return string(e) }

func isFixedMigrationFailure(err error) bool {
	return err != nil && err.Error() == "MIGRATION_FAILED"
}
