package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveForgejoFixtureCandidateDirectoryRequiresExactRepositorySibling(t *testing.T) {
	repoRoot := t.TempDir() + "-repo"
	packageDir := filepath.Join(repoRoot, "internal", "app")
	controlRoot := filepath.Join(filepath.Dir(repoRoot), ".control", "rehearse-runtime")
	if err := os.MkdirAll(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(controlRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module github.com/Pastalikek65/rehearse\n"), 0600); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(controlRoot, "candidates")
	foreignSource := `C:\source\rehearse\internal\app\forgejo_fixture_runtime_test.go`

	t.Run("exact absolute candidate override", func(t *testing.T) {
		got, err := resolveForgejoFixtureCandidateDirectory(foreignSource, expected, packageDir)
		if err != nil {
			t.Fatal(err)
		}
		if got != expected {
			t.Fatalf("candidate directory=%q, want %q", got, expected)
		}
	})
	t.Run("relative override rejected without writes", func(t *testing.T) {
		got, err := resolveForgejoFixtureCandidateDirectory(foreignSource, filepath.Join("relative", "candidates"), packageDir)
		if err == nil || got != "" {
			t.Fatalf("relative override returned (%q, %v), want fixed path error", got, err)
		}
		if _, err := os.Lstat(filepath.Join(repoRoot, "relative")); !os.IsNotExist(err) {
			t.Fatalf("relative override created an output path: %v", err)
		}
	})
	t.Run("outside override rejected without writes", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "candidate-output")
		got, err := resolveForgejoFixtureCandidateDirectory(foreignSource, outside, packageDir)
		if err == nil || got != "" {
			t.Fatalf("outside override returned (%q, %v), want fixed path error", got, err)
		}
		if _, err := os.Lstat(outside); !os.IsNotExist(err) {
			t.Fatalf("outside override created an output path: %v", err)
		}
	})
}

func TestForgejoFixtureCandidateDirectoryRefusesNonNativeSourceBeforeCreatingParents(t *testing.T) {
	root := t.TempDir()
	packageDir := filepath.Join(root, "internal", "app")
	if err := os.MkdirAll(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/Pastalikek65/rehearse\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runtime.GOOS, "windows") {
		t.Skip("a Windows source path is native on this platform")
	}
	if _, err := resolveForgejoFixtureCandidateDirectory(`C:\source\rehearse\internal\app\fixture_test.go`, "", packageDir); err == nil {
		t.Fatal("foreign Windows source path without explicit native output override was accepted")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(root), ".control")); !os.IsNotExist(err) {
		t.Fatalf("invalid source path created a control directory: %v", err)
	}
}

func TestCreateForgejoFixtureCandidateDirectoryCreatesOnlyExactChild(t *testing.T) {
	root := t.TempDir()
	controlRoot := filepath.Join(root, ".control", "rehearse-runtime")
	if err := os.MkdirAll(controlRoot, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(controlRoot, "candidates")
	if err := createForgejoFixtureCandidateDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("candidate child was not created as a real directory: info=%v err=%v", info, err)
	}
	missingParent := filepath.Join(root, "missing", "rehearse-runtime", "candidates")
	if err := createForgejoFixtureCandidateDirectory(missingParent); err == nil {
		t.Fatal("candidate creation unexpectedly created missing parent directories")
	}
	if _, err := os.Lstat(filepath.Dir(filepath.Dir(missingParent))); !os.IsNotExist(err) {
		t.Fatalf("missing parent was created: %v", err)
	}
}
