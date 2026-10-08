package state

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cancel deterministically after the verifier has entered its read loop.
// This avoids a scheduler/timer race and still exercises the real file opener.
type cancelAfterChecks struct {
	context.Context
	checks    int
	remaining int
}

func (c *cancelAfterChecks) Err() error {
	c.checks++
	if c.checks >= c.remaining {
		return context.Canceled
	}
	return nil
}

func TestVerifiedBackupContextCancellationAndRewind(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := s.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	data := "PGDMP" + strings.Repeat("bounded-synthetic-", 32768)
	source := filepath.Join(t.TempDir(), "source.dump")
	if err := os.WriteFile(source, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageBackup(context.Background(), lock, source); err != nil {
		t.Fatal(err)
	}
	for _, after := range []int{1, 4} {
		ctx := &cancelAfterChecks{Context: context.Background(), remaining: after}
		file, err := s.OpenVerifiedBackupContext(ctx, run.ID)
		if file != nil {
			file.Close()
		}
		if err == nil || err.Error() != "CANCELED" {
			t.Fatalf("cancel after %d context checks returned %v", after, err)
		}
		if ctx.checks < after {
			t.Fatal("verification did not observe cancellation")
		}
	}
	file, err := s.OpenVerifiedBackupContext(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || string(got) != data {
		t.Fatalf("verified descriptor not rewound: bytes=%d err=%v", len(got), err)
	}
}
