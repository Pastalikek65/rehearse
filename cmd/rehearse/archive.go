package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/Pastalikek65/rehearse/internal/app"
)

func commandArchive(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("archive")
	database := fs.String("database", "", "consistent PostgreSQL custom-format dump")
	data := fs.String("data", "", "consistent Forgejo data-volume TAR")
	output := fs.String("output", "", "new private archive destination")
	jsonOutput := fs.Bool("json", false, "print archive manifest metadata")
	help, shortHelp := addHelpFlags(fs)
	if fs.Parse(args) != nil {
		return commandArgumentFailure(stderr, "archive")
	}
	if *help || *shortHelp {
		usage, _ := commandUsage("archive")
		return writeText(stdout, stderr, usage)
	}
	if len(fs.Args()) != 0 || strings.TrimSpace(*database) == "" || strings.TrimSpace(*data) == "" || strings.TrimSpace(*output) == "" {
		return commandArgumentFailure(stderr, "archive")
	}
	manifest, err := app.CreateForgejoArchive(ctx, *database, *data, *output)
	if err != nil {
		return writeFailure(stderr, err, "ARCHIVE_WRITE_FAILED")
	}
	if *jsonOutput {
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return writeFailure(stderr, nil, "OUTPUT_WRITE_FAILED")
		}
		return writeBytes(stdout, stderr, append(raw, '\n'))
	}
	return writeText(stdout, stderr, "Forgejo archive created. Keep the archive private; run is required to verify database and application behavior.\n")
}
