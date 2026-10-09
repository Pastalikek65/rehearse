package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteDiagnosticSeparatesOpenAndRenameWithoutPathOrContent(t *testing.T) {
	root := t.TempDir()
	secretPath := filepath.Join(root, "private-source-path-sentinel")
	secretContent := "private-payload-content-sentinel"
	goodPath := filepath.Join(root, "successful-write.json")
	if err := atomicWrite(root, filepath.Base(goodPath), []byte(secretContent)); err != nil {
		t.Fatalf("successful atomic write failed: %v", err)
	}
	written, err := os.ReadFile(goodPath)
	if err != nil || string(written) != secretContent {
		t.Fatalf("successful atomic write changed contents: %q, %v", written, err)
	}

	openErr := atomicWrite(filepath.Join(secretPath, "missing-parent"), "run.json", []byte(secretContent))
	if openErr == nil || openErr.Error() != "STATE_WRITE_FAILED" {
		t.Fatalf("open failure public code = %v", openErr)
	}

	destination := filepath.Join(root, "occupied-directory")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	renameErr := atomicWrite(root, filepath.Base(destination), []byte(secretContent))
	if renameErr == nil || renameErr.Error() != "STATE_WRITE_FAILED" {
		t.Fatalf("rename failure public code = %v", renameErr)
	}

	read := func(err error) (string, string, int) {
		t.Helper()
		diagnostic, ok := DiagnosticFor(err)
		if !ok {
			t.Fatalf("error has no typed internal diagnostic: %T %v", err, err)
		}
		if rendered := fmt.Sprintf("%+v", diagnostic); strings.Contains(rendered, secretPath) || strings.Contains(rendered, secretContent) {
			t.Fatalf("structured diagnostic exposed sensitive input: %q", rendered)
		}
		return diagnostic.Code, diagnostic.Operation, diagnostic.Errno
	}
	openCode, openOperation, openErrno := read(openErr)
	renameCode, renameOperation, renameErrno := read(renameErr)
	if openCode != "STATE_WRITE_FAILED" || renameCode != "STATE_WRITE_FAILED" || openOperation != "open" || renameOperation != "rename" {
		t.Fatalf("diagnostic stages: open=(%q,%q), rename=(%q,%q)", openCode, openOperation, renameCode, renameOperation)
	}
	if openErrno == 0 || renameErrno == 0 {
		t.Fatalf("missing numeric OS error: open=%d rename=%d", openErrno, renameErrno)
	}
	for _, err := range []error{openErr, renameErr} {
		for _, rendered := range []string{err.Error(), strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", ""), "\n", ""))} {
			if strings.Contains(rendered, secretPath) || strings.Contains(rendered, secretContent) {
				t.Fatalf("diagnostic exposed sensitive input: %q", rendered)
			}
		}
	}
}

func TestDiagnosticForRejectsPlainTextThatResemblesStateCode(t *testing.T) {
	if diagnostic, ok := DiagnosticFor(errors.New("STATE_WRITE_FAILED")); ok {
		t.Fatalf("untyped text was accepted as state diagnostic: %+v", diagnostic)
	}
}
