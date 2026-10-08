package app

import (
	"context"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
	"io"
	"os"
)

type code string

func (e code) Error() string { return string(e) }

type Plan struct {
	SchemaVersion    int      `json:"schemaVersion"`
	Adapter          string   `json:"adapter"`
	SourceVersion    string   `json:"sourceVersion"`
	TargetVersion    string   `json:"targetVersion"`
	PostgresVersion  string   `json:"postgresVersion"`
	BackupBytes      int64    `json:"backupBytes"`
	ArchiveValidated bool     `json:"archiveValidated"`
	Phases           []string `json:"phases"`
	Images           []string `json:"images"`
	ResourceCount    int      `json:"resourceCount"`
	MemoryFloorMiB   int      `json:"memoryFloorMiB"`
	DiskPlanningNote string   `json:"diskPlanningNote"`
	RuntimeNote      string   `json:"runtimeNote"`
}

// BuildPlan is read-only and offline. Recognizing a custom-format header does
// not establish archive validity: pg_restore inspection happens during run.
func BuildPlan(ctx context.Context, cfg spec.Config) (Plan, error) {
	if ctx.Err() != nil {
		return Plan{}, code("CANCELED")
	}
	if err := spec.Validate(cfg); err != nil {
		return Plan{}, code("CONFIG_INVALID")
	}
	info, err := os.Lstat(cfg.BackupPath)
	if err != nil || !info.Mode().IsRegular() {
		return Plan{}, code("BACKUP_NOT_REGULAR")
	}
	if info.Size() > state.MaxBackupBytes {
		return Plan{}, code("BACKUP_TOO_LARGE")
	}
	f, err := os.Open(cfg.BackupPath)
	if err != nil {
		return Plan{}, code("BACKUP_UNREADABLE")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return Plan{}, code("BACKUP_NOT_REGULAR")
	}
	var prefix [5]byte
	if _, err := io.ReadFull(f, prefix[:]); err != nil || string(prefix[:]) != "PGDMP" {
		return Plan{}, code("BACKUP_FORMAT_UNSUPPORTED")
	}
	if ctx.Err() != nil {
		return Plan{}, code("CANCELED")
	}
	return Plan{SchemaVersion: 1, Adapter: cfg.Adapter, SourceVersion: cfg.SourceVersion, TargetVersion: cfg.TargetVersion, PostgresVersion: cfg.PostgresVersion, BackupBytes: opened.Size(), ArchiveValidated: false, Phases: []string{"baseline", "target", "recovery"}, Images: []string{miniflux.SourceImage, miniflux.TargetImage, miniflux.PostgresImage, miniflux.ProbeImage}, ResourceCount: 18, MemoryFloorMiB: 4096, DiskPlanningNote: "Allow space for a staged backup, three restored databases and pinned images. A compressed archive can expand substantially; input size does not predict restored database size.", RuntimeNote: "Linux amd64 Docker Engine 28+ with Compose and isolated dual-stack bridge support. Windows requires an explicitly named WSL2 distribution. All phase volumes are fresh; no app ports are published."}, nil
}

func ResolveAuth(cfg spec.Config, getenv func(string) string) (miniflux.Auth, error) {
	if getenv == nil || spec.Validate(cfg) != nil {
		return miniflux.Auth{}, code("AUTH_INVALID")
	}
	refs := cfg.AuthEnvRefs
	auth := miniflux.Auth{APIToken: getenv(refs.APIToken), Username: getenv(refs.Username), Password: getenv(refs.Password)}
	if refs.APIToken != "" {
		auth.Username = ""
		auth.Password = ""
	} else {
		auth.APIToken = ""
	}
	if _, err := miniflux.CurlConfig("/v1/me", &auth); err != nil {
		return miniflux.Auth{}, code("AUTH_INVALID")
	}
	return auth, nil
}
