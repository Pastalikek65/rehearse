package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
)

// CreateForgejoArchive packages an already consistent offline database/data
// snapshot. It never contacts Docker or verifies that the two source captures
// came from the same instant; the operator must stop Forgejo before capturing.
// The destination must be new. Publication uses a same-directory hard link
// so a competing creator is never overwritten on either supported platform.
func CreateForgejoArchive(ctx context.Context, databasePath, dataPath, outputPath string) (forgejo.Manifest, error) {
	if ctx == nil || ctx.Err() != nil {
		return forgejo.Manifest{}, code("CANCELED")
	}
	if outputPath == "" {
		return forgejo.Manifest{}, code("ARCHIVE_OUTPUT_INVALID")
	}
	outputPath, err := filepath.Abs(outputPath)
	if err != nil {
		return forgejo.Manifest{}, code("ARCHIVE_OUTPUT_INVALID")
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return forgejo.Manifest{}, code("ARCHIVE_OUTPUT_EXISTS")
	} else if !errors.Is(err, os.ErrNotExist) {
		return forgejo.Manifest{}, code("ARCHIVE_OUTPUT_INVALID")
	}
	tmp, err := os.CreateTemp(filepath.Dir(outputPath), ".rehearse-archive-*.partial")
	if err != nil {
		return forgejo.Manifest{}, code("ARCHIVE_WRITE_FAILED")
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()
	manifest, err := forgejo.WriteArchive(ctx, tmp, databasePath, dataPath)
	if err != nil {
		if ctx.Err() != nil {
			return forgejo.Manifest{}, code("CANCELED")
		}
		return forgejo.Manifest{}, code("BACKUP_FORMAT_UNSUPPORTED")
	}
	info, err := tmp.Stat()
	if err != nil {
		return forgejo.Manifest{}, code("ARCHIVE_WRITE_FAILED")
	}
	if _, err := forgejo.OpenArchive(ctx, tmp, info.Size()); err != nil {
		if ctx.Err() != nil {
			return forgejo.Manifest{}, code("CANCELED")
		}
		return forgejo.Manifest{}, code("ARCHIVE_WRITE_FAILED")
	}
	if err := tmp.Sync(); err != nil {
		return forgejo.Manifest{}, code("ARCHIVE_WRITE_FAILED")
	}
	if err := tmp.Close(); err != nil {
		return forgejo.Manifest{}, code("ARCHIVE_WRITE_FAILED")
	}
	if ctx.Err() != nil {
		return forgejo.Manifest{}, code("CANCELED")
	}
	if err := os.Link(tmpPath, outputPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return forgejo.Manifest{}, code("ARCHIVE_OUTPUT_EXISTS")
		}
		return forgejo.Manifest{}, code("ARCHIVE_WRITE_FAILED")
	}
	return manifest, nil
}
