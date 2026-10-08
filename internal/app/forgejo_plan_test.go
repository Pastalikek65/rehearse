package app

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/spec"
)

func forgejoConfig(path string) spec.Config {
	return spec.Config{SchemaVersion: 1, Adapter: "forgejo", SourceVersion: forgejo.SourceVersion, TargetVersion: forgejo.TargetVersion, PostgresVersion: forgejo.PostgresVersion, BackupPath: path, AuthEnvRefs: spec.AuthEnvRefs{APIToken: "FORGEJO_API_TOKEN"}}
}

func forgejoPlanArchive(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	dump := filepath.Join(dir, "db.dump")
	data := filepath.Join(dir, "data.tar")
	if err := os.WriteFile(dump, []byte("PGDMPsynthetic-header-only"), 0600); err != nil {
		t.Fatal(err)
	}
	var tarBytes bytes.Buffer
	tw := tar.NewWriter(&tarBytes)
	if err := tw.WriteHeader(&tar.Header{Name: "gitea", Typeflag: tar.TypeDir, Mode: 0700, Uid: 1000, Gid: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, tarBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := forgejo.WriteArchive(context.Background(), &archive, dump, data); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestForgejoPlanValidatesEnvelopeOfflineWithoutClaimingDatabaseValidity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.zip")
	input := forgejoPlanArchive(t)
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := BuildPlan(context.Background(), forgejoConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	if p.Adapter != "forgejo" || p.ResourceCount != 24 || p.Images[0] != forgejo.SourceImage || p.Images[1] != forgejo.TargetImage || p.ArchiveValidated || p.ValidationNote == "" {
		t.Fatalf("incorrect plan: %+v", p)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("plan created files")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, input) {
		t.Fatal("source changed")
	}
}

func TestForgejoPlanRejectsMalformedAndOversizedBeforeRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(path, []byte("PK\x03\x04not-a-validated-archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(context.Background(), forgejoConfig(path)); err == nil {
		t.Fatal("ZIP signature accepted without validating payload")
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(forgejo.MaxArchiveBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(context.Background(), forgejoConfig(path)); err == nil || err.Error() != "BACKUP_TOO_LARGE" {
		t.Fatalf("size cap: %v", err)
	}
	if _, err := BuildPlan(nil, forgejoConfig(path)); err == nil || err.Error() != "CANCELED" {
		t.Fatalf("nil context: %v", err)
	}
}

func TestResolveForgejoAuthUsesOnlyTokenEnvironmentReference(t *testing.T) {
	cfg := forgejoConfig("backup.zip")
	auth, err := ResolveAuth(cfg, func(name string) string {
		if name != "FORGEJO_API_TOKEN" {
			t.Fatalf("unexpected environment lookup %q", name)
		}
		return "synthetic-token"
	})
	if err != nil || auth.APIToken != "synthetic-token" || auth.Username != "" || auth.Password != "" {
		t.Fatalf("auth rejected/wrong fields: %v", err)
	}
	if _, err := ResolveAuth(cfg, func(string) string { return "secret\nheader" }); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe token accepted or leaked")
	}
}
