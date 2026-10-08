package main

import (
	"context"
	"strings"
	"testing"
)

func TestArchiveCommandHelpAndSafeFailure(t *testing.T) {
	out, errOut := &strings.Builder{}, &strings.Builder{}
	if status := execute(context.Background(), []string{"archive", "--help"}, out, errOut, testOptions(t.TempDir())); status != 0 || !strings.Contains(out.String(), "--database") || !strings.Contains(out.String(), "consistent") {
		t.Fatalf("help status/output: %d %q %q", status, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if status := execute(context.Background(), []string{"archive", "--database", "private-missing-path", "--data", "private-data-path", "--output", t.TempDir() + "/result.zip"}, out, errOut, testOptions(t.TempDir())); status == 0 || strings.Contains(errOut.String(), "private-") {
		t.Fatalf("unsafe failure: %d %q", status, errOut.String())
	}
}
