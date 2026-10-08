package state

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStagingSelectsBackupHeaderFromPersistedAdapter(t *testing.T) {
	var payload bytes.Buffer
	writer := zip.NewWriter(&payload)
	member, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte(`{"fixture":"header-only; full archive validation belongs to the adapter"}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		adapter  string
		data     []byte
		accepted bool
	}{
		{"forgejo", payload.Bytes(), true}, {"miniflux", payload.Bytes(), false},
		{"forgejo", []byte("PGDMPsynthetic"), false}, {"miniflux", []byte("PGDMPsynthetic"), true},
	} {
		t.Run(tc.adapter+string(tc.data[:4]), func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			run, err := s.CreateForAdapter("synthetic-daemon", tc.adapter)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := s.AcquireRunLock(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()
			path := filepath.Join(t.TempDir(), "source.backup")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = s.StageBackup(context.Background(), lock, path)
			if !tc.accepted {
				if err == nil || err.Error() != "BACKUP_FORMAT_UNSUPPORTED" {
					t.Fatalf("wrong adapter header accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			file, err := s.OpenVerifiedBackupContext(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(file)
			file.Close()
			if err != nil || !bytes.Equal(got, tc.data) {
				t.Fatalf("opaque staged bytes changed: %v", err)
			}
		})
	}
}
