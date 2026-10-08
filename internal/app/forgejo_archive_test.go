package app

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
)

func archiveInputs(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	db, data := filepath.Join(dir, "db.dump"), filepath.Join(dir, "data.tar")
	if err := os.WriteFile(db, []byte("PGDMPsynthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	tw := tar.NewWriter(&payload)
	if err := tw.WriteHeader(&tar.Header{Name: "gitea", Typeflag: tar.TypeDir, Mode: 0700, Uid: 1000, Gid: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, payload.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return db, data
}

func TestCreateForgejoArchiveCommitsValidatedFileAndPreservesInputs(t *testing.T) {
	db, data := archiveInputs(t)
	dbBefore, _ := os.ReadFile(db)
	dataBefore, _ := os.ReadFile(data)
	dir := t.TempDir()
	output := filepath.Join(dir, "output.zip")
	if _, err := CreateForgejoArchive(context.Background(), db, data, output); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forgejo.OpenArchive(context.Background(), f, info.Size()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("unfinished destination left behind")
	}
	dbAfter, _ := os.ReadFile(db)
	dataAfter, _ := os.ReadFile(data)
	if !bytes.Equal(dbBefore, dbAfter) || !bytes.Equal(dataBefore, dataAfter) {
		t.Fatal("inputs changed")
	}
}

func TestCreateForgejoArchiveNeverOverwritesOrLeavesPartialOutput(t *testing.T) {
	db, data := archiveInputs(t)
	dir := t.TempDir()
	output := filepath.Join(dir, "existing.zip")
	if err := os.WriteFile(output, []byte("keep this"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateForgejoArchive(context.Background(), db, data, output); err == nil || err.Error() != "ARCHIVE_OUTPUT_EXISTS" {
		t.Fatalf("existing output: %v", err)
	}
	if content, _ := os.ReadFile(output); string(content) != "keep this" {
		t.Fatal("existing file changed")
	}
	if _, err := CreateForgejoArchive(context.Background(), "missing-secret-path", data, filepath.Join(dir, "bad.zip")); err == nil {
		t.Fatal("missing input accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CreateForgejoArchive(ctx, db, data, filepath.Join(dir, "canceled.zip")); err == nil || err.Error() != "CANCELED" {
		t.Fatalf("cancellation: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("partial output leaked")
	}
}
