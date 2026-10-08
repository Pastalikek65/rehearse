package engine

import (
	"context"
	"encoding/json"
	"github.com/Pastalikek65/rehearse/internal/state"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func resource(run state.Run, phase, role string) (state.Resource, error) {
	if run.SchemaVersion != 1 || !idPattern.MatchString(run.ID) || !idPattern.MatchString(run.OwnerID) || !reflect.DeepEqual(run.Resources, state.IntendedResources(run.OwnerID, run.ID)) {
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

func (e *Engine) inspectBounded(ctx context.Context, run state.Run, r state.Resource, network Network, volumeName string, stage ContainerStage) (ContainerInspection, error) {
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
	container, err := ParseBoundedContainer(raw, r.Role, network.Name, network.ID, volumeName, stage)
	if err != nil {
		return ContainerInspection{}, err
	}
	if err := VerifyOwnership(r, container.LiveResource, run.DaemonID, daemon.ID); err != nil {
		return ContainerInspection{}, err
	}
	return container, nil
}

func (e *Engine) Start(ctx context.Context, run state.Run, phase, role string) error {
	r, err := resource(run, phase, role)
	if err != nil {
		return err
	}
	if r.Kind != "container" {
		return code("RESOURCE_TYPE_INVALID")
	}
	var live LiveResource
	if role == "migration" {
		network, err := e.phaseNetwork(ctx, run, phase)
		if err != nil {
			return err
		}
		volume, err := resource(run, phase, "volume")
		if err != nil {
			return err
		}
		created, err := e.inspectBounded(ctx, run, r, network, volume.Name, ContainerStageCreated)
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
		volume, err := resource(run, phase, "volume")
		if err != nil {
			return err
		}
		if _, err := e.inspectBounded(ctx, run, r, network, volume.Name, ContainerStageMigration); err != nil {
			return err
		}
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
	volume, err := resource(run, phase, "volume")
	if err != nil {
		return err
	}
	observed, err := e.inspectBounded(ctx, run, r, network, volume.Name, ContainerStageMigration)
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
	finished, err := e.inspectBounded(ctx, run, r, network, volume.Name, ContainerStageMigrationExited)
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
