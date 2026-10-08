package engine

import (
	"encoding/json"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/state"
	"reflect"
	"regexp"
)

type code string

func (e code) Error() string { return string(e) }

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Compose builds a complete fixed phase. User configuration cannot contribute
// images, commands, environment values, mount definitions or network options.
func Compose(run state.Run, phase string) ([]byte, error) {
	if phase != "baseline" && phase != "target" && phase != "recovery" {
		return nil, code("PHASE_INVALID")
	}
	if run.SchemaVersion != 1 || !idPattern.MatchString(run.ID) || !idPattern.MatchString(run.OwnerID) || !reflect.DeepEqual(run.Resources, state.IntendedResources(run.OwnerID, run.ID)) {
		return nil, code("RUN_RESOURCES_INVALID")
	}
	resources := map[string]state.Resource{}
	for _, r := range run.Resources {
		if r.Phase == phase {
			resources[r.Role] = r
		}
	}
	appImage := miniflux.SourceImage
	if phase == "target" {
		appImage = miniflux.TargetImage
	}
	service := func(role, image string, memory int, pids int, cpu float64) map[string]any {
		r := resources[role]
		return map[string]any{"container_name": r.Name, "image": image, "platform": "linux/amd64", "labels": r.Labels, "networks": []string{"isolated"}, "restart": "no", "mem_limit": memory << 20, "pids_limit": pids, "cpus": cpu, "security_opt": []string{"no-new-privileges:true"}, "logging": map[string]any{"driver": "local", "options": map[string]string{"max-size": "5m", "max-file": "2"}}}
	}
	db := service("db", miniflux.PostgresImage, 512, 128, 2)
	db["environment"] = map[string]string{"POSTGRES_USER": "rehearse", "POSTGRES_DB": "rehearse", "POSTGRES_HOST_AUTH_METHOD": "trust"}
	db["volumes"] = []any{map[string]any{"type": "volume", "source": "database", "target": "/var/lib/postgresql/data"}}
	db["healthcheck"] = map[string]any{"test": []string{"CMD", "pg_isready", "-U", "rehearse", "-d", "rehearse"}, "interval": "1s", "timeout": "3s", "retries": 60}
	appEnv := map[string]string{"DATABASE_URL": "postgres://rehearse@db/rehearse?sslmode=disable", "RUN_MIGRATIONS": "0", "DISABLE_SCHEDULER_SERVICE": "1", "DISABLE_API": "0", "DISABLE_LOCAL_AUTH": "0", "LISTEN_ADDR": "0.0.0.0:8080", "LOG_LEVEL": "error"}
	app := service("app", appImage, 256, 64, 1)
	app["environment"] = appEnv
	migration := service("migration", appImage, 256, 64, 1)
	migration["environment"] = appEnv
	migration["command"] = []string{"/usr/bin/miniflux", "-migrate"}
	for _, s := range []map[string]any{app, migration} {
		s["read_only"] = true
		s["cap_drop"] = []string{"ALL"}
		s["tmpfs"] = []string{"/tmp:rw,noexec,nosuid,size=16777216"}
	}
	probe := service("probe", miniflux.ProbeImage, 128, 32, 0.5)
	probe["command"] = []string{"sh", "-c", "while :; do sleep 3600; done"}
	probe["read_only"] = true
	probe["cap_drop"] = []string{"ALL"}
	probe["tmpfs"] = []string{"/tmp:rw,noexec,nosuid,size=16777216"}
	doc := map[string]any{"services": map[string]any{"db": db, "migration": migration, "app": app, "probe": probe}, "networks": map[string]any{"isolated": map[string]any{"name": resources["network"].Name, "driver": "bridge", "internal": true, "enable_ipv6": true, "driver_opts": map[string]string{"com.docker.network.bridge.gateway_mode_ipv4": "isolated", "com.docker.network.bridge.gateway_mode_ipv6": "isolated"}, "labels": resources["network"].Labels}}, "volumes": map[string]any{"database": map[string]any{"name": resources["volume"].Name, "labels": resources["volume"].Labels}}}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, code("COMPOSE_ENCODE_FAILED")
	}
	return append(raw, '\n'), nil
}

type LiveResource struct {
	Kind   string
	Name   string
	ID     string
	Labels map[string]string
}

func VerifyOwnership(expected state.Resource, live LiveResource, expectedDaemon, currentDaemon string) error {
	if expectedDaemon == "" || currentDaemon != expectedDaemon {
		return code("DAEMON_ID_MISMATCH")
	}
	if live.Kind != expected.Kind || live.Name != expected.Name || live.ID == "" {
		return code("RESOURCE_OWNERSHIP_MISMATCH")
	}
	for _, key := range []string{"io.rehearse.owner", "io.rehearse.run", "io.rehearse.kind"} {
		if expected.Labels[key] == "" || live.Labels[key] != expected.Labels[key] {
			return code("RESOURCE_OWNERSHIP_MISMATCH")
		}
	}
	return nil
}
