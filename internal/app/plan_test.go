package app

import (
	"context"
	"encoding/json"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func config(path string) spec.Config {
	return spec.Config{SchemaVersion: 1, Adapter: "miniflux", SourceVersion: "2.2.19", TargetVersion: "2.3.3", PostgresVersion: "17.11", BackupPath: path, AuthEnvRefs: spec.AuthEnvRefs{Username: "FIXTURE_USERNAME", Password: "FIXTURE_PASSWORD"}}
}
func TestPlanInspectsBackupWithoutCreatingStateOrChangingOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.dump")
	data := []byte("PGDMPsynthetic-header-only")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := BuildPlan(context.Background(), config(path))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if p.BackupBytes != int64(len(data)) || p.ArchiveValidated || len(p.Phases) != 3 || len(p.Images) != 4 {
		t.Fatalf("misleading plan: %s", raw)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("planning created state")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("planning altered backup")
	}
}
func TestPlanRejectsBadBackupAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.dump")
	os.WriteFile(path, []byte("not a backup"), 0600)
	if _, err := BuildPlan(context.Background(), config(path)); err == nil {
		t.Fatal("bad header accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BuildPlan(ctx, config(path)); err == nil || err.Error() != "CANCELED" {
		t.Fatalf("cancel hidden: %v", err)
	}
}
func TestResolvedAuthCannotSerializeValuesOrLeakInvalidHeader(t *testing.T) {
	cfg := config("backup.dump")
	env := map[string]string{"FIXTURE_USERNAME": "public-user", "FIXTURE_PASSWORD": "synthetic-secret"}
	auth, err := ResolveAuth(cfg, func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(auth)
	if err != nil || strings.Contains(string(raw), "synthetic-secret") {
		t.Fatalf("secret serialized: %s", raw)
	}
	env["FIXTURE_PASSWORD"] = "private\nheader"
	if _, err := ResolveAuth(cfg, func(name string) string { return env[name] }); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("unsafe auth accepted or echoed")
	}
}
