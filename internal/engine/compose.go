package engine

import (
	"encoding/json"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/state"
)

type code string

func (e code) Error() string { return string(e) }

// Compose builds a complete fixed phase. User configuration cannot contribute
// images, commands, environment values, mount definitions or network options.
func Compose(run state.Run, phase string) ([]byte, error) {
	if phase != "baseline" && phase != "target" && phase != "recovery" {
		return nil, code("PHASE_INVALID")
	}
	if state.ValidateRunResources(run) != nil {
		return nil, code("RUN_RESOURCES_INVALID")
	}
	switch run.AdapterID() {
	case "miniflux":
		return composeMiniflux(run, phase)
	case "forgejo":
		return composeForgejo(run, phase)
	default:
		return nil, code("ADAPTER_UNSUPPORTED")
	}
}

func composeMiniflux(run state.Run, phase string) ([]byte, error) {
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

func composeForgejo(run state.Run, phase string) ([]byte, error) {
	resources := map[string]state.Resource{}
	for _, r := range run.Resources {
		if r.Phase == phase {
			resources[r.Role] = r
		}
	}
	appImage := forgejo.SourceImage
	if phase == "target" {
		appImage = forgejo.TargetImage
	}
	service := func(role, image string, memory, pids int, cpu float64, attached bool) map[string]any {
		r := resources[role]
		doc := map[string]any{
			"container_name": r.Name,
			"image":          image,
			"platform":       "linux/amd64",
			"labels":         r.Labels,
			"restart":        "no",
			"mem_limit":      memory << 20,
			"pids_limit":     pids,
			"cpus":           cpu,
			"security_opt":   []string{"no-new-privileges:true"},
			"logging":        map[string]any{"driver": "local", "options": map[string]string{"max-size": "5m", "max-file": "2"}},
		}
		if attached {
			doc["networks"] = []string{"isolated"}
		}
		return doc
	}
	db := service("db", forgejo.PostgresImage, 512, 128, 2, true)
	db["environment"] = map[string]string{"POSTGRES_USER": "rehearse", "POSTGRES_DB": "rehearse", "POSTGRES_HOST_AUTH_METHOD": "trust"}
	db["volumes"] = []any{map[string]any{"type": "volume", "source": "database", "target": "/var/lib/postgresql/data"}}
	db["healthcheck"] = map[string]any{"test": []string{"CMD", "pg_isready", "-U", "rehearse", "-d", "rehearse"}, "interval": "1s", "timeout": "3s", "retries": 60}

	forgejoArgs := []string{"--work-path", "/data/gitea", "--config", "/data/.rehearse-runtime/app.ini"}
	app := service("app", appImage, 512, 256, 2, true)
	app["entrypoint"] = []string{"/usr/local/bin/forgejo"}
	app["command"] = append(append([]string(nil), forgejoArgs...), "web")
	app["user"] = "1000:1000"
	app["volumes"] = []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}}
	app["read_only"] = true
	app["cap_drop"] = []string{"ALL"}
	app["tmpfs"] = []string{"/tmp:rw,noexec,nosuid,size=16777216"}

	migration := service("migration", appImage, 512, 128, 2, true)
	migration["entrypoint"] = []string{"/usr/local/bin/forgejo"}
	migration["command"] = append(append([]string(nil), forgejoArgs...), "migrate")
	migration["user"] = "1000:1000"
	migration["volumes"] = []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}}
	migration["read_only"] = true
	migration["cap_drop"] = []string{"ALL"}
	migration["tmpfs"] = []string{"/tmp:rw,noexec,nosuid,size=16777216"}

	probe := service("probe", forgejo.ProbeImage, 128, 32, 0.5, true)
	probe["entrypoint"] = []string{"/bin/sh"}
	probe["command"] = []string{"-c", "while :; do sleep 3600; done"}
	probe["read_only"] = true
	probe["cap_drop"] = []string{"ALL"}
	probe["tmpfs"] = []string{"/tmp:rw,noexec,nosuid,size=16777216"}

	// Docker's copy API can populate this one-volume stopped container without
	// running an in-image extractor or granting it a network connection.
	dataRestore := service("data-restore", appImage, 512, 64, 1, false)
	dataRestore["entrypoint"] = []string{"/bin/true"}
	dataRestore["network_mode"] = "none"
	dataRestore["user"] = "1000:1000"
	dataRestore["volumes"] = []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}}
	dataRestore["read_only"] = true
	dataRestore["cap_drop"] = []string{"ALL"}

	doc := map[string]any{
		"services": map[string]any{"db": db, "migration": migration, "app": app, "probe": probe, "data-restore": dataRestore},
		"networks": map[string]any{"isolated": map[string]any{"name": resources["network"].Name, "driver": "bridge", "internal": true, "enable_ipv6": true, "driver_opts": map[string]string{"com.docker.network.bridge.gateway_mode_ipv4": "isolated", "com.docker.network.bridge.gateway_mode_ipv6": "isolated"}, "labels": resources["network"].Labels}},
		"volumes": map[string]any{
			"database": map[string]any{"name": resources["volume"].Name, "labels": resources["volume"].Labels},
			"data":     map[string]any{"name": resources["data"].Name, "labels": resources["data"].Labels},
		},
	}
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
