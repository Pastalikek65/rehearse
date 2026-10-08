package engine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func resource(run state.Run, phase, role string) (state.Resource, error) {
	if state.ValidateRunResources(run) != nil {
		return state.Resource{}, code("RUN_RESOURCES_INVALID")
	}
	for _, r := range run.Resources {
		if r.Phase == phase && r.Role == role {
			return r, nil
		}
	}
	return state.Resource{}, code("RESOURCE_UNKNOWN")
}
func (e *Engine) exists(ctx context.Context, r state.Resource) (bool, error) {
	format := "{{.Name}}"
	args := []string{r.Kind, "ls"}
	if r.Kind == "container" {
		format = "{{.Names}}"
		args = append(args, "--all")
	}
	args = append(args, "--filter", "name="+r.Name, "--format", format)
	raw, err := e.Bytes(ctx, args, nil, 16<<10)
	if err != nil {
		return false, err
	}
	for _, name := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(name) == r.Name {
			return true, nil
		}
	}
	return false, nil
}
func (e *Engine) owned(ctx context.Context, run state.Run, r state.Resource) (LiveResource, error) {
	daemon, err := e.Info(ctx)
	if err != nil {
		return LiveResource{}, err
	}
	if daemon.ID != run.DaemonID {
		return LiveResource{}, code("DAEMON_ID_MISMATCH")
	}
	raw, err := e.inspect(ctx, r)
	if err != nil {
		return LiveResource{}, err
	}
	var doc struct {
		ID     string `json:"Id"`
		Name   string
		Labels map[string]string
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal(raw, &doc) != nil {
		return LiveResource{}, code("RESOURCE_INSPECT_INVALID")
	}
	labels := doc.Labels
	name := doc.Name
	id := doc.ID
	if r.Kind == "container" {
		labels = doc.Config.Labels
		name = strings.TrimPrefix(name, "/")
	}
	if r.Kind == "volume" {
		id = name
	}
	live := LiveResource{Kind: r.Kind, Name: name, ID: id, Labels: labels}
	if err := VerifyOwnership(r, live, run.DaemonID, daemon.ID); err != nil {
		return LiveResource{}, err
	}
	return live, nil
}

// CreatePhase cannot adopt an existing object, including a same-name volume.
// All names/labels must have been persisted by state.Create before this call.
func (e *Engine) CreatePhase(ctx context.Context, run state.Run, runDir, phase string) error {
	raw, err := Compose(run, phase)
	if err != nil {
		return err
	}
	if filepath.Base(runDir) != run.ID {
		return code("STATE_DIRECTORY_INVALID")
	}
	daemon, err := e.Info(ctx)
	if err != nil {
		return err
	}
	if daemon.ID != run.DaemonID {
		return code("DAEMON_ID_MISMATCH")
	}
	for _, r := range run.Resources {
		if r.Phase == phase {
			exists, err := e.exists(ctx, r)
			if err != nil {
				return err
			}
			if exists {
				return code("RESOURCE_ALREADY_EXISTS")
			}
		}
	}
	file := filepath.Join(runDir, phase+".compose.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		return code("COMPOSE_WRITE_FAILED")
	}
	empty := filepath.Join(runDir, "empty.env")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		return code("COMPOSE_WRITE_FAILED")
	}
	filePath, err := e.HostPath(ctx, file)
	if err != nil {
		return err
	}
	dirPath, err := e.HostPath(ctx, runDir)
	if err != nil {
		return err
	}
	envPath, err := e.HostPath(ctx, empty)
	if err != nil {
		return err
	}
	args := []string{"compose", "--ansi", "never", "--progress", "quiet", "--project-directory", dirPath, "--env-file", envPath, "--project-name", "rehearse-" + run.ID + "-" + phase, "-f", filePath, "create", "--pull", "missing"}
	if err := e.Execute(ctx, args, nil, io.Discard); err != nil {
		return err
	}
	for _, r := range run.Resources {
		if r.Phase == phase {
			if _, err := e.owned(ctx, run, r); err != nil {
				return err
			}
			if r.Kind == "network" {
				raw, err := e.inspect(ctx, r)
				if err != nil {
					return err
				}
				if _, err := ParseIsolatedNetwork(raw); err != nil {
					return err
				}
			}
			if r.Kind == "volume" {
				raw, err := e.inspect(ctx, r)
				if err != nil {
					return err
				}
				if _, err := ParseManagedVolume(raw); err != nil {
					return err
				}
			}
		}
	}
	if run.AdapterID() == "forgejo" {
		network, err := e.phaseNetwork(ctx, run, phase)
		if err != nil {
			return err
		}
		for _, r := range run.Resources {
			if r.Phase == phase && r.Kind == "container" {
				if _, err := e.inspectBounded(ctx, run, r, network, ContainerStageCreated); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (e *Engine) phaseNetwork(ctx context.Context, run state.Run, phase string) (Network, error) {
	r, err := resource(run, phase, "network")
	if err != nil {
		return Network{}, err
	}
	daemon, err := e.Info(ctx)
	if err != nil {
		return Network{}, err
	}
	if daemon.ID != run.DaemonID {
		return Network{}, code("DAEMON_ID_MISMATCH")
	}
	raw, err := e.inspect(ctx, r)
	if err != nil {
		return Network{}, err
	}
	network, err := ParseIsolatedNetwork(raw)
	if err != nil {
		return Network{}, err
	}
	if err := VerifyOwnership(r, network.LiveResource, run.DaemonID, daemon.ID); err != nil {
		return Network{}, err
	}
	return network, nil
}

func (e *Engine) inspectBounded(ctx context.Context, run state.Run, r state.Resource, network Network, stage ContainerStage) (ContainerInspection, error) {
	daemon, err := e.Info(ctx)
	if err != nil {
		return ContainerInspection{}, err
	}
	if daemon.ID != run.DaemonID {
		return ContainerInspection{}, code("DAEMON_ID_MISMATCH")
	}
	raw, err := e.inspect(ctx, r)
	if err != nil {
		return ContainerInspection{}, err
	}
	dbVolume, err := resource(run, r.Phase, "volume")
	if err != nil {
		return ContainerInspection{}, err
	}
	dataVolumeName := ""
	if run.AdapterID() == "forgejo" {
		dataVolume, err := resource(run, r.Phase, "data")
		if err != nil {
			return ContainerInspection{}, err
		}
		dataVolumeName = dataVolume.Name
	}
	expectedImage, err := imageForResource(run, r)
	if err != nil {
		return ContainerInspection{}, err
	}
	expectedUser := ""
	if run.AdapterID() == "forgejo" && (r.Role == "app" || r.Role == "migration" || r.Role == "data-restore") {
		expectedUser = "1000:1000"
	}
	container, err := ParseBoundedContainerForAdapter(raw, ContainerBoundary{
		Adapter: run.AdapterID(), Role: r.Role, NetworkName: network.Name, NetworkID: network.ID,
		DatabaseVolumeName: dbVolume.Name, DataVolumeName: dataVolumeName,
		ExpectedImage: expectedImage, ExpectedUser: expectedUser, Stage: stage,
	})
	if err != nil {
		return ContainerInspection{}, err
	}
	if err := VerifyOwnership(r, container.LiveResource, run.DaemonID, daemon.ID); err != nil {
		return ContainerInspection{}, err
	}
	return container, nil
}

func imageForResource(run state.Run, r state.Resource) (string, error) {
	if r.Kind != "container" {
		return "", code("RESOURCE_TYPE_INVALID")
	}
	switch run.AdapterID() {
	case "miniflux":
		switch r.Role {
		case "db":
			return miniflux.PostgresImage, nil
		case "probe":
			return miniflux.ProbeImage, nil
		case "app", "migration":
			if r.Phase == "target" {
				return miniflux.TargetImage, nil
			}
			return miniflux.SourceImage, nil
		}
	case "forgejo":
		switch r.Role {
		case "db":
			return forgejo.PostgresImage, nil
		case "probe":
			return forgejo.ProbeImage, nil
		case "app", "migration", "data-restore":
			if r.Phase == "target" {
				return forgejo.TargetImage, nil
			}
			return forgejo.SourceImage, nil
		}
	}
	return "", code("RESOURCE_UNKNOWN")
}

func (e *Engine) Start(ctx context.Context, run state.Run, phase, role string) error {
	r, err := resource(run, phase, role)
	if err != nil {
		return err
	}
	if r.Kind != "container" {
		return code("RESOURCE_TYPE_INVALID")
	}
	if run.AdapterID() == "forgejo" && role == "data-restore" {
		return code("CONTAINER_START_FORBIDDEN")
	}
	var live LiveResource
	if role == "migration" {
		network, err := e.phaseNetwork(ctx, run, phase)
		if err != nil {
			return err
		}
		created, err := e.inspectBounded(ctx, run, r, network, ContainerStageCreated)
		if err != nil {
			return err
		}
		live = created.LiveResource
	} else if run.AdapterID() == "forgejo" {
		network, err := e.phaseNetwork(ctx, run, phase)
		if err != nil {
			return err
		}
		created, err := e.inspectBounded(ctx, run, r, network, ContainerStageCreated)
		if err != nil {
			return err
		}
		live = created.LiveResource
	} else {
		live, err = e.owned(ctx, run, r)
		if err != nil {
			return err
		}
	}
	if err := e.Execute(ctx, []string{"container", "start", live.ID}, nil, io.Discard); err != nil {
		return err
	}
	if role == "migration" {
		network, err := e.phaseNetwork(ctx, run, phase)
		if err != nil {
			return err
		}
		if _, err := e.inspectBounded(ctx, run, r, network, ContainerStageMigration); err != nil {
			return err
		}
	} else if run.AdapterID() == "forgejo" {
		network, err := e.phaseNetwork(ctx, run, phase)
		if err != nil {
			return err
		}
		if _, err := e.inspectBounded(ctx, run, r, network, ContainerStageRunning); err != nil {
			return err
		}
	}
	return nil
}

// StopForgejoApp requests a graceful stop of the exact running app container
// in one validated Forgejo phase. It proves ownership and effective boundary
// before and after stopping; it never removes, creates, or adopts a resource.
func (e *Engine) StopForgejoApp(ctx context.Context, run state.Run, phase string) error {
	if run.AdapterID() != "forgejo" {
		return code("FORGEJO_STOP_FORBIDDEN")
	}
	app, err := resource(run, phase, "app")
	if err != nil {
		return err
	}
	network, err := e.phaseNetwork(ctx, run, phase)
	if err != nil {
		return err
	}
	before, err := e.inspectBounded(ctx, run, app, network, ContainerStageRunning)
	if err != nil {
		return err
	}
	if !before.Running || before.Status != "running" {
		return code("CONTAINER_STATE_FAILED")
	}
	stopErr := e.Execute(ctx, []string{"container", "stop", "--time", "15", before.ID}, nil, io.Discard)
	stopped, inspectErr := e.inspectBounded(ctx, run, app, network, ContainerStageStopped)
	if inspectErr != nil {
		return inspectErr
	}
	if err := verifySameContainerIdentity(before, stopped); err != nil {
		return err
	}
	if stopErr != nil {
		return stopErr
	}
	return nil
}

func (e *Engine) Inside(ctx context.Context, run state.Run, phase, role string, args []string, input io.Reader, output io.Writer) error {
	r, err := resource(run, phase, role)
	if err != nil {
		return err
	}
	if r.Kind != "container" || len(args) == 0 {
		return code("RESOURCE_TYPE_INVALID")
	}
	if run.AdapterID() == "forgejo" && role == "data-restore" {
		return code("CONTAINER_EXEC_FORBIDDEN")
	}
	live, err := e.owned(ctx, run, r)
	if err != nil {
		return err
	}
	command := append([]string{"exec", "-i", live.ID}, args...)
	return e.Execute(ctx, command, input, output)
}
func (e *Engine) InsideBytes(ctx context.Context, run state.Run, phase, role string, args []string, input io.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, code("DOCKER_OUTPUT_LIMIT_INVALID")
	}
	b := &boundedBuffer{limit: limit}
	err := e.Inside(ctx, run, phase, role, args, input, b)
	if b.overflow {
		return nil, code("DOCKER_OUTPUT_TOO_LARGE")
	}
	if err != nil {
		return nil, err
	}
	return b.buffer.Bytes(), nil
}

// CopyForgejoData streams a host-validated tar archive into the exact stopped,
// owned Forgejo data-restore container for a phase. The helper has no network,
// no shell invocation, and exactly one inspected named volume at /data.
func (e *Engine) CopyForgejoData(ctx context.Context, run state.Run, phase string, validatedTar io.Reader) error {
	if run.AdapterID() != "forgejo" || validatedTar == nil {
		return code("FORGEJO_ARCHIVE_INVALID")
	}
	helper, err := resource(run, phase, "data-restore")
	if err != nil {
		return err
	}
	network, err := e.phaseNetwork(ctx, run, phase)
	if err != nil {
		return err
	}
	before, err := e.inspectBounded(ctx, run, helper, network, ContainerStageCreated)
	if err != nil {
		return err
	}
	if before.Status != "created" || before.Running {
		return code("CONTAINER_STATE_FAILED")
	}
	copyErr := e.Execute(ctx, []string{"container", "cp", "--archive", "-", before.ID + ":/data"}, validatedTar, io.Discard)
	// Inspect after both successful and failed copy attempts. A partial copy is
	// still only in the run-owned volume; cleanup removes it by verified intent.
	after, inspectErr := e.inspectBounded(ctx, run, helper, network, ContainerStageCreated)
	if inspectErr != nil {
		return inspectErr
	}
	if err := verifySameContainerIdentity(before, after); err != nil {
		return err
	}
	return copyErr
}

// ReadForgejoData streams a tar archive of the exact owned phase's /data tree
// from its stopped data-restore helper. It never starts or execs the helper,
// writes no archive to disk, and never accepts a caller-selected container or
// path. Callers must consume the stream through the bounded Forgejo projector.
func (e *Engine) ReadForgejoData(ctx context.Context, run state.Run, phase string, output io.Writer) error {
	if run.AdapterID() != "forgejo" || output == nil {
		return code("FORGEJO_ARCHIVE_INVALID")
	}
	helper, err := resource(run, phase, "data-restore")
	if err != nil {
		return err
	}
	network, err := e.phaseNetwork(ctx, run, phase)
	if err != nil {
		return err
	}
	before, err := e.inspectBounded(ctx, run, helper, network, ContainerStageCreated)
	if err != nil {
		return err
	}
	if before.Status != "created" || before.Running {
		return code("CONTAINER_STATE_FAILED")
	}
	args, err := forgejoDataCopyOutArgs(before.ID)
	if err != nil {
		return err
	}
	copyErr := e.Execute(ctx, args, nil, output)
	// The helper must remain stopped, isolated, and on the same owned data
	// volume even if the copy fails or the consumer closes its pipe early.
	after, inspectErr := e.inspectBounded(ctx, run, helper, network, ContainerStageCreated)
	if inspectErr != nil {
		return inspectErr
	}
	if err := verifySameContainerIdentity(before, after); err != nil {
		return err
	}
	return copyErr
}

func verifySameContainerIdentity(before, after ContainerInspection) error {
	if before.ID == "" || after.ID == "" || before.ID != after.ID {
		return code("CONTAINER_IDENTITY_CHANGED")
	}
	return nil
}

func forgejoDataCopyOutArgs(containerID string) ([]string, error) {
	if len(containerID) != 64 {
		return nil, code("CONTAINER_INSPECT_INVALID")
	}
	if _, err := hex.DecodeString(containerID); err != nil {
		return nil, code("CONTAINER_INSPECT_INVALID")
	}
	return []string{"container", "cp", "--archive", containerID + ":/data/.", "-"}, nil
}

func (e *Engine) WaitDatabase(ctx context.Context, run state.Run, phase string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		raw, err := e.InsideBytes(ctx, run, phase, "db", []string{"psql", "--username", "rehearse", "--dbname", "rehearse", "--no-psqlrc", "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1", "--command", "SELECT 1"}, nil, 4096)
		if err == nil && strings.TrimSpace(string(raw)) == "1" {
			return nil
		}
		select {
		case <-ctx.Done():
			return code("DATABASE_NOT_READY")
		case <-time.After(time.Second):
		}
	}
}
func (e *Engine) WaitMigration(ctx context.Context, run state.Run, phase string) error {
	r, err := resource(run, phase, "migration")
	if err != nil {
		return err
	}
	network, err := e.phaseNetwork(ctx, run, phase)
	if err != nil {
		return err
	}
	observed, err := e.inspectBounded(ctx, run, r, network, ContainerStageMigration)
	if err != nil {
		return err
	}
	waitOutput, err := e.Bytes(ctx, []string{"container", "wait", observed.ID}, nil, 4096)
	if err != nil {
		return err
	}
	network, err = e.phaseNetwork(ctx, run, phase)
	if err != nil {
		return err
	}
	finished, err := e.inspectBounded(ctx, run, r, network, ContainerStageMigrationExited)
	if err != nil {
		return err
	}
	return verifyMigrationExitCode(waitOutput, finished)
}

func verifyMigrationExitCode(waitOutput []byte, inspected ContainerInspection) error {
	status := strings.TrimSpace(string(waitOutput))
	waitCode, err := strconv.Atoi(status)
	if err != nil || waitCode < 0 || waitCode > 255 || strconv.Itoa(waitCode) != status {
		return code("MIGRATION_EXIT_STATUS_INVALID")
	}
	if inspected.Status != "exited" || inspected.Running {
		return code("CONTAINER_STATE_FAILED")
	}
	if waitCode != inspected.ExitCode {
		return code("MIGRATION_EXIT_STATUS_MISMATCH")
	}
	if waitCode != 0 {
		return code("MIGRATION_FAILED")
	}
	return nil
}

// Cleanup inspects exact names and live ownership immediately before each
// removal. A foreign object is preserved, while other verified owned objects
// can still be cleaned. It never uses Compose down or a global prune.
func (e *Engine) Cleanup(ctx context.Context, run state.Run) error {
	if _, err := resource(run, "baseline", "db"); err != nil {
		return err
	}
	held := false
	for _, kind := range []string{"container", "volume", "network"} {
		for _, r := range run.Resources {
			if r.Kind != kind {
				continue
			}
			daemon, err := e.Info(ctx)
			if err != nil {
				return err
			}
			if daemon.ID != run.DaemonID {
				return code("DAEMON_ID_MISMATCH")
			}
			exists, err := e.exists(ctx, r)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			live, err := e.owned(ctx, run, r)
			if err != nil {
				held = true
				continue
			}
			args := []string{kind, "rm"}
			if kind == "container" {
				args = append(args, "--force")
			}
			args = append(args, live.ID)
			if err := e.Execute(ctx, args, nil, io.Discard); err != nil {
				held = true
			}
		}
	}
	if held {
		return code("CLEANUP_OWNERSHIP_HELD")
	}
	return nil
}
