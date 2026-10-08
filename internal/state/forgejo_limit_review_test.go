package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
)

func TestForgejoBackupMetadataCannotExceedArchiveLimit(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "backup"
		if pending {
			name = "pending backup"
		}
		t.Run(name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			run, err := s.CreateForAdapter("synthetic-daemon", "forgejo")
			if err != nil {
				t.Fatal(err)
			}
			lock, err := s.AcquireRunLock(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()

			manifest := Backup{
				SourcePath: "synthetic-source",
				SHA256:     strings.Repeat("a", 64),
				Bytes:      forgejo.MaxArchiveBytes + 1,
			}
			if pending {
				run.Status = "staging"
				run.PendingBackup = &manifest
			} else {
				run.Backup = &manifest
			}
			if err := s.Save(lock, run); err == nil || err.Error() != "RUN_BACKUP_INVALID" {
				t.Fatalf("oversized Forgejo %s metadata was not rejected with RUN_BACKUP_INVALID: %v", name, err)
			}
		})
	}
}

func TestMinifluxBackupMetadataRetainsLegacyLimit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("synthetic-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	run.Backup = &Backup{
		SourcePath: "synthetic-source",
		SHA256:     strings.Repeat("b", 64),
		Bytes:      forgejo.MaxArchiveBytes + 1,
	}
	if err := s.Save(lock, run); err != nil {
		t.Fatalf("legacy Miniflux metadata limit changed: %v", err)
	}
	loaded, err := s.Load(run.ID)
	if err != nil || loaded.Backup == nil || loaded.Backup.Bytes != forgejo.MaxArchiveBytes+1 {
		t.Fatalf("Miniflux metadata did not persist at the legacy limit: loaded=%+v err=%v", loaded.Backup, err)
	}
}

func TestForgejoArchiveLimitMatchesArchiveContract(t *testing.T) {
	if forgejo.MaxArchiveBytes != 2<<30 {
		t.Fatalf("Forgejo archive limit changed: got %d bytes, want %d", forgejo.MaxArchiveBytes, int64(2<<30))
	}
	if MaxForgejoBackupBytes != forgejo.MaxArchiveBytes {
		t.Fatalf("state Forgejo backup limit differs from archive contract: state=%d contract=%d", MaxForgejoBackupBytes, forgejo.MaxArchiveBytes)
	}
	if got := backupLimitForAdapter("forgejo"); got != forgejo.MaxArchiveBytes {
		t.Fatalf("Forgejo staging limit differs from archive contract: staging=%d contract=%d", got, forgejo.MaxArchiveBytes)
	}
}

func TestForgejoCopyRejectsOversizedSourceBeforeCreatingPartial(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.zip")
	partial := filepath.Join(dir, "backup.partial")
	if err := os.WriteFile(source, []byte("PK\x03\x04payload"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := copyBackupToPartialWithLimit(context.Background(), source, partial, "forgejo", 10)
	if err == nil || err.Error() != "BACKUP_TOO_LARGE" {
		t.Fatalf("oversized source was not rejected before staging: %v", err)
	}
	if _, err := os.Lstat(partial); !os.IsNotExist(err) {
		t.Fatalf("oversized preflight created a partial file: %v", err)
	}
}

func TestCopyBackupAcceptsExactSmallAdapterBudget(t *testing.T) {
	for _, tc := range []struct {
		adapter string
		name    string
		payload []byte
	}{
		{adapter: "forgejo", name: "zip header", payload: []byte("PK\x03\x04abcdefghijkl")},
		{adapter: "miniflux", name: "pgdump header", payload: []byte("PGDMPabcdefghijk")},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.backup")
			partial := filepath.Join(dir, "backup.partial")
			if err := os.WriteFile(source, tc.payload, 0600); err != nil {
				t.Fatal(err)
			}
			backup, err := copyBackupToPartialWithLimit(context.Background(), source, partial, tc.adapter, int64(len(tc.payload)))
			if err != nil {
				t.Fatalf("valid %s at its exact budget was rejected: %v", tc.name, err)
			}
			got, err := os.ReadFile(partial)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(tc.payload) || backup.Bytes != int64(len(tc.payload)) {
				t.Fatalf("exact-budget copy changed bytes or length: bytes=%q manifest=%d", got, backup.Bytes)
			}
			sum := sha256.Sum256(tc.payload)
			if backup.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("exact-budget copy digest mismatch: got %q", backup.SHA256)
			}
		})
	}
}

type appendSourceOnSecondErrContext struct {
	context.Context
	sourcePath string
	calls      int
	grew       bool
	appendErr  error
}

func (c *appendSourceOnSecondErrContext) Err() error {
	c.calls++
	if c.calls == 2 {
		f, err := os.OpenFile(c.sourcePath, os.O_WRONLY|os.O_APPEND, 0)
		if err == nil {
			_, err = f.Write([]byte{'x'})
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
		c.appendErr = err
		c.grew = err == nil
	}
	return c.Context.Err()
}

func TestForgejoCopyBoundsSourceThatGrowsAfterInitialStat(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.zip")
	partial := filepath.Join(dir, "backup.partial")
	payload := []byte("PK\x03\x04abcdefghijkl") // 16 bytes, exactly at the test budget.
	if len(payload) != 16 {
		t.Fatal("test fixture length changed")
	}
	if err := os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := &appendSourceOnSecondErrContext{Context: context.Background(), sourcePath: source}
	_, err := copyBackupToPartialWithLimit(ctx, source, partial, "forgejo", int64(len(payload)))
	if ctx.appendErr != nil || !ctx.grew {
		t.Fatalf("failed to grow source after initial stat: grew=%t err=%v", ctx.grew, ctx.appendErr)
	}
	if err == nil || err.Error() != "BACKUP_TOO_LARGE" {
		t.Fatalf("grown source was not rejected at the adapter limit: %v", err)
	}
	if _, err := os.Lstat(partial); !os.IsNotExist(err) {
		t.Fatalf("grown source left a partial backup: %v", err)
	}
}
