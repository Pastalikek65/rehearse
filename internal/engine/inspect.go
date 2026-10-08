package engine

import (
	"context"
	"encoding/json"
	"github.com/Pastalikek65/rehearse/internal/state"
	"strings"
)

type Network struct {
	LiveResource
	Containers map[string]struct{ Name string }
}

func ParseManagedVolume(raw []byte) (LiveResource, error) {
	var v struct {
		Name    string
		Driver  string
		Scope   string
		Options map[string]string
		Labels  map[string]string
	}
	if json.Unmarshal(raw, &v) != nil || v.Name == "" {
		return LiveResource{}, code("VOLUME_INSPECT_INVALID")
	}
	if v.Driver != "local" || v.Scope != "local" || len(v.Options) != 0 {
		return LiveResource{}, code("VOLUME_BOUNDARY_FAILED")
	}
	return LiveResource{Kind: "volume", Name: v.Name, ID: v.Name, Labels: v.Labels}, nil
}

func ParseIsolatedNetwork(raw []byte) (Network, error) {
	var n struct {
		ID         string `json:"Id"`
		Name       string
		Driver     string
		Internal   bool
		EnableIPv6 bool
		Options    map[string]string
		Labels     map[string]string
		IPAM       struct {
			Config []struct {
				Subnet  string
				Gateway string
			}
		}
		Containers map[string]struct{ Name string }
	}
	if json.Unmarshal(raw, &n) != nil || n.ID == "" || n.Name == "" {
		return Network{}, code("NETWORK_INSPECT_INVALID")
	}
	if n.Driver != "bridge" || !n.Internal || !n.EnableIPv6 || n.Options["com.docker.network.bridge.gateway_mode_ipv4"] != "isolated" || n.Options["com.docker.network.bridge.gateway_mode_ipv6"] != "isolated" || len(n.IPAM.Config) < 2 {
		return Network{}, code("NETWORK_BOUNDARY_FAILED")
	}
	for _, subnet := range n.IPAM.Config {
		if subnet.Gateway != "" {
			return Network{}, code("NETWORK_BOUNDARY_FAILED")
		}
	}
	return Network{LiveResource: LiveResource{Kind: "network", Name: n.Name, ID: n.ID, Labels: n.Labels}, Containers: n.Containers}, nil
}

type ContainerStage string

const (
	ContainerStageCreated         ContainerStage = "created"
	ContainerStageRunning         ContainerStage = "running"
	ContainerStageStopped         ContainerStage = "stopped"
	ContainerStageMigration       ContainerStage = "migration"
	ContainerStageMigrationExited ContainerStage = "migration-exited"
)

type ContainerInspection struct {
	LiveResource
	Status   string
	Running  bool
	ExitCode int
	Image    string
}

func ParseBoundedContainer(raw []byte, role, networkName, networkID, volumeName string, stage ContainerStage) (ContainerInspection, error) {
	return ParseBoundedContainerForAdapter(raw, ContainerBoundary{
		Adapter: "miniflux", Role: role, NetworkName: networkName, NetworkID: networkID,
		DatabaseVolumeName: volumeName, Stage: stage,
	})
}

// ContainerBoundary is a closed inspection contract for a built-in adapter.
// It is populated from persisted run intent and fixed image pins, never user
// supplied Docker options.
type ContainerBoundary struct {
	Adapter            string
	Role               string
	NetworkName        string
	NetworkID          string
	DatabaseVolumeName string
	DataVolumeName     string
	ExpectedImage      string
	ExpectedUser       string
	Stage              ContainerStage
}

func ParseBoundedContainerForAdapter(raw []byte, boundary ContainerBoundary) (ContainerInspection, error) {
	var c struct {
		ID     string `json:"Id"`
		Name   string
		Config struct {
			Image  string
			User   string
			Labels map[string]string
		}
		State struct {
			Status   string
			Running  bool
			Dead     bool
			ExitCode *int
		}
		HostConfig struct {
			Privileged     bool
			NetworkMode    string
			ReadonlyRootfs bool
			PortBindings   map[string]json.RawMessage
			Binds          []string
			CapDrop        []string
			CapAdd         []string
			SecurityOpt    []string
			Devices        []json.RawMessage
			PidMode        string
			IpcMode        string
			UTSMode        string
			CgroupnsMode   string
			UsernsMode     string
		}
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
			Ports    map[string][]json.RawMessage
		}
		Mounts []struct {
			Type        string
			Name        string
			Destination string
		}
	}
	if json.Unmarshal(raw, &c) != nil || c.ID == "" || c.Name == "" {
		return ContainerInspection{}, code("CONTAINER_INSPECT_INVALID")
	}
	if c.State.ExitCode == nil {
		return ContainerInspection{}, code("CONTAINER_INSPECT_INVALID")
	}
	h := c.HostConfig
	role := boundary.Role
	if !supportedContainerRole(boundary.Adapter, role) {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	expectedNetworkMode := boundary.NetworkName
	if boundary.Adapter == "forgejo" && role == "data-restore" {
		expectedNetworkMode = "none"
	}
	if h.Privileged || h.NetworkMode != expectedNetworkMode || len(h.PortBindings) != 0 || len(h.CapAdd) != 0 || len(h.Devices) != 0 ||
		!privateNamespace(h.PidMode) || !privateNamespace(h.IpcMode) || !privateNamespace(h.UTSMode) ||
		!privateNamespace(h.CgroupnsMode) || !privateNamespace(h.UsernsMode) {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	if boundary.ExpectedImage != "" && c.Config.Image != boundary.ExpectedImage {
		return ContainerInspection{}, code("CONTAINER_IMAGE_MISMATCH")
	}
	if boundary.ExpectedUser != "" && c.Config.User != boundary.ExpectedUser {
		return ContainerInspection{}, code("CONTAINER_USER_MISMATCH")
	}
	if boundary.Adapter == "forgejo" {
		if !exactStrings(c.HostConfig.SecurityOpt, "no-new-privileges:true") {
			return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
		}
		if role != "db" && (!c.HostConfig.ReadonlyRootfs || !exactStrings(c.HostConfig.CapDrop, "ALL")) {
			return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
		}
	}
	volumeName, volumeDestination := expectedContainerVolume(boundary)
	// Compose serializes named volumes through HostConfig.Binds on some engine
	// versions. Accept only the exact managed volume mapping and still require
	// an inspected Type=volume at the same destination.
	if len(h.Binds) != 0 && (volumeName == "" || len(h.Binds) != 1 || h.Binds[0] != volumeName+":"+volumeDestination+":rw") {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	if !containerStateAllowed(c.State.Status, c.State.Running, c.State.Dead, *c.State.ExitCode, role, boundary.Stage, boundary.Adapter) {
		return ContainerInspection{}, code("CONTAINER_STATE_FAILED")
	}
	if !containerNetworksAllowed(c.NetworkSettings.Networks, boundary.NetworkName, boundary.NetworkID, c.State.Status, c.State.Running, role, boundary.Stage, boundary.Adapter) {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	if boundary.Stage == ContainerStageMigration && role == "migration" && c.State.Status == "exited" && *c.State.ExitCode != 0 {
		return ContainerInspection{}, code("MIGRATION_FAILED")
	}
	for _, bindings := range c.NetworkSettings.Ports {
		if len(bindings) != 0 {
			return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
		}
	}
	volumeCount := 0
	tmpfsCount := 0
	for _, m := range c.Mounts {
		switch m.Type {
		case "volume":
			if volumeName == "" || m.Name != volumeName || m.Destination != volumeDestination {
				return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
			}
			volumeCount++
		case "tmpfs":
			if !tmpfsAllowed(boundary.Adapter, role, m.Name, m.Destination) {
				return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
			}
			tmpfsCount++
		default:
			return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
		}
	}
	if (volumeName != "" && volumeCount != 1) || (volumeName == "" && volumeCount != 0) || tmpfsCount > 1 {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	return ContainerInspection{
		LiveResource: LiveResource{Kind: "container", Name: strings.TrimPrefix(c.Name, "/"), ID: c.ID, Labels: c.Config.Labels},
		Status:       c.State.Status,
		Running:      c.State.Running,
		ExitCode:     *c.State.ExitCode,
		Image:        c.Config.Image,
	}, nil
}

func supportedContainerRole(adapter, role string) bool {
	switch adapter {
	case "miniflux":
		return role == "db" || role == "app" || role == "probe" || role == "migration"
	case "forgejo":
		return role == "db" || role == "app" || role == "probe" || role == "migration" || role == "data-restore"
	default:
		return false
	}
}

func expectedContainerVolume(boundary ContainerBoundary) (string, string) {
	if boundary.Role == "db" {
		return boundary.DatabaseVolumeName, "/var/lib/postgresql/data"
	}
	if boundary.Adapter == "forgejo" && (boundary.Role == "app" || boundary.Role == "migration" || boundary.Role == "data-restore") {
		return boundary.DataVolumeName, "/data"
	}
	return "", ""
}

func tmpfsAllowed(adapter, role, name, destination string) bool {
	if name != "" || destination != "/tmp" {
		return false
	}
	return (adapter == "miniflux" || adapter == "forgejo") && (role == "app" || role == "migration" || role == "probe")
}

func exactStrings(values []string, expected string) bool {
	return len(values) == 1 && values[0] == expected
}

func privateNamespace(mode string) bool {
	return mode == "" || mode == "private"
}

func containerStateAllowed(status string, running, dead bool, exitCode int, role string, stage ContainerStage, adapter string) bool {
	if dead {
		return false
	}
	switch stage {
	case ContainerStageCreated:
		return (role == "migration" || adapter == "forgejo") && status == "created" && !running && exitCode == 0
	case ContainerStageRunning:
		return status == "running" && running
	case ContainerStageStopped:
		return adapter == "forgejo" && role == "app" && status == "exited" && !running && exitCode == 0
	case ContainerStageMigration:
		if role != "migration" {
			return false
		}
		return (status == "running" && running) || (status == "exited" && !running)
	case ContainerStageMigrationExited:
		return role == "migration" && status == "exited" && !running
	default:
		return false
	}
}

func containerNetworksAllowed(networks map[string]struct{ NetworkID string }, networkName, networkID, status string, running bool, role string, stage ContainerStage, adapter string) bool {
	if stage == ContainerStageStopped {
		if adapter != "forgejo" || role != "app" || status != "exited" || running {
			return false
		}
		if len(networks) == 0 {
			return true
		}
		if len(networks) == 1 {
			attached, ok := networks[networkName]
			return ok && attached.NetworkID == networkID
		}
		return false
	}
	if adapter == "forgejo" && role == "data-restore" {
		if stage != ContainerStageCreated || status != "created" || running {
			return false
		}
		if len(networks) == 0 {
			return true
		}
		if len(networks) == 1 {
			none, ok := networks["none"]
			return ok && none.NetworkID == ""
		}
		return false
	}
	if stage == ContainerStageRunning || (stage == ContainerStageMigration && running) {
		return len(networks) == 1 && networks[networkName].NetworkID == networkID
	}
	if stage == ContainerStageCreated || stage == ContainerStageMigration || stage == ContainerStageMigrationExited {
		if role == "migration" && (stage == ContainerStageMigration || stage == ContainerStageMigrationExited) && status == "exited" {
			if len(networks) == 0 {
				return true
			}
			if len(networks) == 1 {
				attached, ok := networks[networkName]
				return ok && (attached.NetworkID == "" || attached.NetworkID == networkID)
			}
			return false
		}
		if len(networks) == 0 {
			return true
		}
		if len(networks) == 1 {
			attached, ok := networks[networkName]
			return ok && (attached.NetworkID == "" || attached.NetworkID == networkID)
		}
	}
	return false
}

func (e *Engine) inspect(ctx context.Context, r state.Resource) ([]byte, error) {
	return e.Bytes(ctx, []string{r.Kind, "inspect", r.Name, "--format", "{{json .}}"}, nil, 2<<20)
}

func (e *Engine) VerifyPhase(ctx context.Context, run state.Run, phase string) error {
	if err := state.ValidateRunResources(run); err != nil {
		return code("RUN_RESOURCES_INVALID")
	}
	daemon, err := e.Info(ctx)
	if err != nil {
		return err
	}
	if daemon.ID != run.DaemonID {
		return code("DAEMON_ID_MISMATCH")
	}
	resources := map[string]state.Resource{}
	allowedNames := map[string]bool{}
	for _, r := range run.Resources {
		if r.Phase == phase {
			resources[r.Role] = r
			if r.Kind == "container" {
				allowedNames[r.Name] = true
			}
		}
	}
	expectedRoles := 6
	if run.AdapterID() == "forgejo" {
		expectedRoles = 8
	}
	if len(resources) != expectedRoles {
		return code("PHASE_INVALID")
	}
	raw, err := e.inspect(ctx, resources["network"])
	if err != nil {
		return err
	}
	network, err := ParseIsolatedNetwork(raw)
	if err != nil {
		return err
	}
	if err := VerifyOwnership(resources["network"], network.LiveResource, run.DaemonID, daemon.ID); err != nil {
		return err
	}
	volumes := []string{"volume"}
	if run.AdapterID() == "forgejo" {
		volumes = append(volumes, "data")
	}
	for _, role := range volumes {
		volumeRaw, err := e.inspect(ctx, resources[role])
		if err != nil {
			return err
		}
		volume, err := ParseManagedVolume(volumeRaw)
		if err != nil {
			return err
		}
		if err := VerifyOwnership(resources[role], volume, run.DaemonID, daemon.ID); err != nil {
			return err
		}
	}
	for _, endpoint := range network.Containers {
		if !allowedNames[endpoint.Name] {
			return code("NETWORK_FOREIGN_ATTACHMENT")
		}
	}
	// Database, app and probe must be running and attached when this check is
	// executed. The one-shot migration container is checked separately before
	// starting; its exit can detach its endpoint.
	for _, role := range []string{"db", "app", "probe"} {
		if _, err := e.inspectBounded(ctx, run, resources[role], network, ContainerStageRunning); err != nil {
			return err
		}
	}
	if run.AdapterID() == "forgejo" {
		if _, err := e.inspectBounded(ctx, run, resources["data-restore"], network, ContainerStageCreated); err != nil {
			return err
		}
	}
	return nil
}
