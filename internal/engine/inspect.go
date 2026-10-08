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
	ContainerStageMigration       ContainerStage = "migration"
	ContainerStageMigrationExited ContainerStage = "migration-exited"
)

type ContainerInspection struct {
	LiveResource
	Status   string
	Running  bool
	ExitCode int
}

func ParseBoundedContainer(raw []byte, role, networkName, networkID, volumeName string, stage ContainerStage) (ContainerInspection, error) {
	var c struct {
		ID     string `json:"Id"`
		Name   string
		Config struct{ Labels map[string]string }
		State  struct {
			Status   string
			Running  bool
			Dead     bool
			ExitCode *int
		}
		HostConfig struct {
			Privileged   bool
			NetworkMode  string
			PortBindings map[string]json.RawMessage
			Binds        []string
			CapAdd       []string
			Devices      []json.RawMessage
			PidMode      string
			IpcMode      string
			UTSMode      string
			CgroupnsMode string
			UsernsMode   string
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
	if role != "db" && role != "app" && role != "probe" && role != "migration" {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	if h.Privileged || h.NetworkMode != networkName || len(h.PortBindings) != 0 || len(h.CapAdd) != 0 || len(h.Devices) != 0 ||
		!privateNamespace(h.PidMode) || !privateNamespace(h.IpcMode) || !privateNamespace(h.UTSMode) ||
		!privateNamespace(h.CgroupnsMode) || !privateNamespace(h.UsernsMode) {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	// Compose can serialize a named volume through HostConfig.Binds. This
	// legacy field alone does not mean a host bind mount. Accept only the exact
	// managed DB volume mapping and still require Type=volume below.
	if len(h.Binds) != 0 && (role != "db" || len(h.Binds) != 1 || h.Binds[0] != volumeName+":/var/lib/postgresql/data:rw") {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	if !containerStateAllowed(c.State.Status, c.State.Running, c.State.Dead, *c.State.ExitCode, role, stage) {
		return ContainerInspection{}, code("CONTAINER_STATE_FAILED")
	}
	if !containerNetworksAllowed(c.NetworkSettings.Networks, networkName, networkID, c.State.Status, c.State.Running, role, stage) {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	if stage == ContainerStageMigration && role == "migration" && c.State.Status == "exited" && *c.State.ExitCode != 0 {
		return ContainerInspection{}, code("MIGRATION_FAILED")
	}
	for _, bindings := range c.NetworkSettings.Ports {
		if len(bindings) != 0 {
			return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
		}
	}
	volumeCount := 0
	for _, m := range c.Mounts {
		switch m.Type {
		case "volume":
			if role != "db" || m.Name != volumeName || m.Destination != "/var/lib/postgresql/data" {
				return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
			}
			volumeCount++
		case "tmpfs":
			if role == "db" || m.Destination != "/tmp" {
				return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
			}
		default:
			return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
		}
	}
	if role == "db" && volumeCount != 1 {
		return ContainerInspection{}, code("CONTAINER_BOUNDARY_FAILED")
	}
	return ContainerInspection{
		LiveResource: LiveResource{Kind: "container", Name: strings.TrimPrefix(c.Name, "/"), ID: c.ID, Labels: c.Config.Labels},
		Status:       c.State.Status,
		Running:      c.State.Running,
		ExitCode:     *c.State.ExitCode,
	}, nil
}

func privateNamespace(mode string) bool {
	return mode == "" || mode == "private"
}

func containerStateAllowed(status string, running, dead bool, exitCode int, role string, stage ContainerStage) bool {
	if dead {
		return false
	}
	switch stage {
	case ContainerStageCreated:
		return status == "created" && !running && exitCode == 0
	case ContainerStageRunning:
		return status == "running" && running
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

func containerNetworksAllowed(networks map[string]struct{ NetworkID string }, networkName, networkID, status string, running bool, role string, stage ContainerStage) bool {
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
	if len(resources) != 6 {
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
	volumeRaw, err := e.inspect(ctx, resources["volume"])
	if err != nil {
		return err
	}
	volume, err := ParseManagedVolume(volumeRaw)
	if err != nil {
		return err
	}
	if err := VerifyOwnership(resources["volume"], volume, run.DaemonID, daemon.ID); err != nil {
		return err
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
		raw, err := e.inspect(ctx, resources[role])
		if err != nil {
			return err
		}
		live, err := ParseBoundedContainer(raw, role, network.Name, network.ID, resources["volume"].Name, ContainerStageRunning)
		if err != nil {
			return err
		}
		if err := VerifyOwnership(resources[role], live.LiveResource, run.DaemonID, daemon.ID); err != nil {
			return err
		}
	}
	return nil
}
