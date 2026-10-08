package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/app"
	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/spec"
)

func TestForgejoPlanHumanOutputDescribesEnvelopeValidationWithoutLeakingInputs(t *testing.T) {
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "private-backup.zip")
	if err := os.WriteFile(backupPath, []byte("placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	config := `{"schemaVersion":1,"adapter":"forgejo","sourceVersion":"15.0.9","targetVersion":"16.0.5","postgresVersion":"17.11","backupPath":"private-backup.zip","authEnvRefs":{"apiToken":"PRIVATE_TOKEN_NAME"}}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	const note = "ZIP manifest, member hashes and data TAR were verified offline. PostgreSQL dump validity requires run."
	opts := testOptions(t.TempDir())
	opts.buildPlan = func(_ context.Context, _ spec.Config) (app.Plan, error) {
		return app.Plan{
			Adapter: "forgejo", SourceVersion: forgejo.SourceVersion, TargetVersion: forgejo.TargetVersion,
			PostgresVersion: forgejo.PostgresVersion, BackupBytes: 1024, ValidationNote: note,
			Phases: []string{"baseline", "target", "recovery"}, Images: []string{forgejo.SourceImage},
			ResourceCount: 24, MemoryFloorMiB: 4096,
			RuntimeNote:      "Linux amd64 Docker Engine 28+; explicitly named WSL2 on Windows.",
			DiskPlanningNote: "Private synthetic fixtures only.",
		}, nil
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	status := execute(context.Background(), []string{"plan", configPath}, stdout, stderr, opts)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("plan failed: code=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{"Forgejo 15.0.9 → 16.0.5", note, "PostgreSQL 17.11", "baseline, target, recovery"} {
		if !strings.Contains(text, want) {
			t.Errorf("human plan omitted %q: %q", want, text)
		}
	}
	for _, forbidden := range []string{"Miniflux", "custom-format header recognized", backupPath, "PRIVATE_TOKEN_NAME"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("human plan included irrelevant/private value %q: %q", forbidden, text)
		}
	}
}
