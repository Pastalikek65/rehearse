package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// AdapterID resolves historical schema-1 Miniflux runs without guessing the
// identity of a schema-2 run; the latter must pass ValidateRunResources.
func (run Run) AdapterID() string {
	if run.SchemaVersion == 1 && run.Adapter == "" {
		return "miniflux"
	}
	return run.Adapter
}

func (s *Store) CreateForAdapter(daemonID, adapter string) (Run, error) {
	if adapter == "miniflux" {
		return s.Create(daemonID)
	}
	if adapter != "forgejo" {
		return Run{}, code("ADAPTER_UNSUPPORTED")
	}
	if s == nil || strings.TrimSpace(daemonID) == "" || len(daemonID) > 128 {
		return Run{}, code("DAEMON_ID_INVALID")
	}
	id, err := randomID()
	if err != nil {
		return Run{}, err
	}
	resources, err := IntendedResourcesForAdapter(s.owner, id, adapter)
	if err != nil {
		return Run{}, err
	}
	if err := os.Mkdir(filepath.Join(s.root, id), 0700); err != nil {
		return Run{}, code("RUN_CREATE_FAILED")
	}
	if err := syncDir(s.root); err != nil {
		return Run{}, err
	}
	run := Run{SchemaVersion: 2, Adapter: adapter, AdapterContractVersion: 1, ID: id, OwnerID: s.owner, DaemonID: daemonID, CreatedAt: time.Now().UTC(), Status: "planned", Resources: resources}
	if err := s.save(run); err != nil {
		return Run{}, err
	}
	return run, nil
}

func ValidateRunResources(run Run) error {
	if !validID(run.ID) || !validID(run.OwnerID) {
		return code("RUN_RESOURCES_INVALID")
	}
	switch run.SchemaVersion {
	case 1:
		if run.Adapter != "" || run.AdapterContractVersion != 0 {
			return code("ADAPTER_CONTRACT_UNSUPPORTED")
		}
	case 2:
		if run.Adapter != "forgejo" || run.AdapterContractVersion != 1 {
			return code("ADAPTER_CONTRACT_UNSUPPORTED")
		}
	default:
		return code("STATE_VERSION_UNSUPPORTED")
	}
	expected, err := IntendedResourcesForAdapter(run.OwnerID, run.ID, run.AdapterID())
	if err != nil || !reflect.DeepEqual(run.Resources, expected) {
		return code("RUN_RESOURCES_INVALID")
	}
	return nil
}

// IntendedResourcesForAdapter is a closed built-in contract, never a source of
// user-selected Docker resources or mounts. Each intent precedes runtime work.
func IntendedResourcesForAdapter(owner, id, adapter string) ([]Resource, error) {
	if !validID(owner) || !validID(id) {
		return nil, code("RUN_RESOURCES_INVALID")
	}
	if adapter == "miniflux" {
		return IntendedResources(owner, id), nil
	}
	if adapter != "forgejo" {
		return nil, code("ADAPTER_UNSUPPORTED")
	}
	var resources []Resource
	for _, phase := range []string{"baseline", "target", "recovery"} {
		for _, role := range []string{"network", "volume", "data", "db", "migration", "app", "probe", "data-restore"} {
			kind := "container"
			if role == "network" {
				kind = "network"
			}
			if role == "volume" || role == "data" {
				kind = "volume"
			}
			resources = append(resources, Resource{Kind: kind, Role: role, Phase: phase, Name: "rehearse-" + id + "-" + phase + "-" + role, Labels: map[string]string{"io.rehearse.owner": owner, "io.rehearse.run": id, "io.rehearse.kind": role}})
		}
	}
	return resources, nil
}
