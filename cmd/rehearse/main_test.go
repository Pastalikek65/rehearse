package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/app"
	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
)

const validTestRunID = "0123456789abcdef0123456789abcdef"

func TestPlanJSONUsesBackupPathRelativeToConfigFile(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "configs")
	workDir := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workDir, 0700); err != nil {
		t.Fatal(err)
	}
	backup := []byte("PGDMPsynthetic-private-backup")
	if err := os.WriteFile(filepath.Join(configDir, "backup.dump"), backup, 0600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t, configDir, `"backupPath":"backup.dump","authEnvRefs":{"apiToken":"REHEARSE_TOKEN"}`)
	if err := os.WriteFile(filepath.Join(workDir, "backup.dump"), []byte("wrong cwd backup"), 0600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"plan", "--json", configPath}, stdout, stderr, testOptions(t.TempDir()))
	if code != 0 {
		t.Fatalf("plan failed (%d): stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	var got app.Plan
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("plan did not print JSON: %v: %q", err, stdout.String())
	}
	if got.BackupBytes != int64(len(backup)) || got.ArchiveValidated || got.Adapter != "miniflux" {
		t.Fatalf("unexpected plan: %#v", got)
	}
	if !strings.Contains(stdout.String(), "\"runtimeNote\"") {
		t.Fatalf("plan omitted runtime requirements: %q", stdout.String())
	}
}

func TestPlanHumanSummaryIsReadableAndHasUsefulOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backup.dump"), []byte("PGDMPsmall"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t, dir, `"backupPath":"backup.dump","authEnvRefs":{"apiToken":"REHEARSE_TOKEN"}`)
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"plan", configPath}, stdout, stderr, testOptions(t.TempDir()))
	if code != 0 {
		t.Fatalf("plan failed (%d): %q", code, stderr.String())
	}
	for _, fragment := range []string{"Miniflux", "2.2.19", "2.3.3", "PostgreSQL 17.11", "baseline", "target", "recovery", "archive validity is not established", "18"} {
		if !strings.Contains(strings.ToLower(stdout.String()), strings.ToLower(fragment)) {
			t.Errorf("summary omitted %q: %q", fragment, stdout.String())
		}
	}
}

func TestPlanBadInputAndWrongArgumentsDoNotEchoInput(t *testing.T) {
	dir := t.TempDir()
	marker := "private-config-marker-39f4"
	badConfig := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badConfig, []byte(`{"schemaVersion":1,"secret":"`+marker+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"plan", badConfig}, {"plan", "--unknown-flag=" + marker, badConfig}, {"plan", ""}} {
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		code := execute(context.Background(), args, stdout, stderr, testOptions(t.TempDir()))
		if code == 0 {
			t.Errorf("accepted invalid invocation %q", args)
		}
		if strings.Contains(stdout.String()+stderr.String(), marker) {
			t.Errorf("echoed caller data for %q: stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
		if stderr.Len() == 0 {
			t.Errorf("missing useful error output for %q", args)
		}
	}
}

func TestRunRequiresExplicitWSLOnWindowsBeforeCreatingEngine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backup.dump"), []byte("PGDMPsmall"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t, dir, `"backupPath":"backup.dump","authEnvRefs":{"apiToken":"REHEARSE_TOKEN"}`)
	opts := testOptions(t.TempDir())
	opts.goos = "windows"
	opts.getenv = func(string) string { return "synthetic-token" }
	engineCalled := false
	opts.newEngine = func(string) (*engine.Engine, error) {
		engineCalled = true
		return nil, errors.New("must not be called")
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"run", configPath}, stdout, stderr, opts)
	if code == 0 || engineCalled {
		t.Fatalf("Windows run without --wsl-distro proceeded: code=%d engineCalled=%v", code, engineCalled)
	}
	if !strings.Contains(stderr.String(), "WSL_DISTRIBUTION_REQUIRED") || strings.Contains(stderr.String(), "synthetic-token") {
		t.Fatalf("unsafe or unexpected diagnostic: %q", stderr.String())
	}
}

func TestRunReturnsNonzeroAndPrintsRunIDForNotRunReport(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backup.dump"), []byte("PGDMPsmall"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t, dir, `"backupPath":"backup.dump","authEnvRefs":{"apiToken":"REHEARSE_TOKEN"}`)
	opts := testOptions(t.TempDir())
	opts.goos = "linux"
	opts.getenv = func(string) string { return "synthetic-token" }
	opts.newEngine = func(string) (*engine.Engine, error) { return nil, nil }
	opts.run = func(_ context.Context, _ spec.Config, store *state.Store, _ *engine.Engine, _ miniflux.Auth) (*report.Report, error) {
		return persistTestReport(store, "not-run")
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"run", configPath}, stdout, stderr, opts)
	if code == 0 {
		t.Fatalf("not-run outcome must be nonzero: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	lines := strings.Split(stdout.String(), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "Run: ") || !validRunID(strings.TrimPrefix(lines[0], "Run: ")) || !strings.Contains(stdout.String(), "Outcome: not-run") || !strings.Contains(stdout.String(), "Report: ") {
		t.Fatalf("missing run ID, outcome, or report location: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "synthetic-token") {
		t.Fatalf("auth value leaked: %q", stderr.String())
	}
}

func TestRunReturnsNonzeroForFailedReport(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backup.dump"), []byte("PGDMPsmall"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t, dir, `"backupPath":"backup.dump","authEnvRefs":{"apiToken":"REHEARSE_TOKEN"}`)
	opts := testOptions(t.TempDir())
	opts.goos = "linux"
	opts.getenv = func(string) string { return "synthetic-token" }
	opts.newEngine = func(string) (*engine.Engine, error) { return nil, nil }
	opts.run = func(_ context.Context, _ spec.Config, store *state.Store, _ *engine.Engine, _ miniflux.Auth) (*report.Report, error) {
		return persistTestReport(store, "failed")
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"run", configPath}, stdout, stderr, opts)
	if code == 0 || !strings.Contains(stdout.String(), "Outcome: failed") || !strings.Contains(stdout.String(), "Report: ") {
		t.Fatalf("failed outcome must be reported with nonzero status: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func persistTestReport(store *state.Store, outcome string) (*report.Report, error) {
	run, err := store.Create("test-daemon")
	if err != nil {
		return nil, err
	}
	result := report.New(run.ID, "linux/amd64", time.Now())
	if outcome == "failed" {
		if err := result.Set("backup.inspect", "failed", "BACKUP_FAILED"); err != nil {
			return nil, err
		}
	}
	dir, err := store.RunDir(run.ID)
	if err != nil {
		return nil, err
	}
	jsonBytes, err := result.JSON()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), jsonBytes, 0600); err != nil {
		return nil, err
	}
	htmlFile, err := os.Create(filepath.Join(dir, "report.html"))
	if err != nil {
		return nil, err
	}
	if err := result.HTML(htmlFile); err != nil {
		_ = htmlFile.Close()
		return nil, err
	}
	if err := htmlFile.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func TestRunNeverPrintsRawAuthenticationResolutionErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backup.dump"), []byte("PGDMPsmall"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t, dir, `"backupPath":"backup.dump","authEnvRefs":{"apiToken":"REHEARSE_TOKEN"}`)
	marker := "raw-auth-secret-marker-a19d"
	opts := testOptions(t.TempDir())
	opts.goos = "windows"
	opts.resolveAuth = func(spec.Config, func(string) string) (miniflux.Auth, error) {
		return miniflux.Auth{}, errors.New("AUTH_INVALID: " + marker)
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"run", "--wsl-distro", "RehearseTest2404", configPath}, stdout, stderr, opts)
	if code == 0 || strings.Contains(stdout.String()+stderr.String(), marker) {
		t.Fatalf("unsafe auth result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "OPERATION_FAILED") {
		t.Fatalf("unknown raw error was not replaced with safe code: %q", stderr.String())
	}
}

func TestRunInvalidEnvironmentCredentialsDoNotEchoAnyValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backup.dump"), []byte("PGDMPsmall"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t, dir, `"backupPath":"backup.dump","authEnvRefs":{"username":"REHEARSE_USER","password":"REHEARSE_PASS"}`)
	marker := "private-username-marker-6fd2"
	opts := testOptions(t.TempDir())
	opts.goos = "windows"
	opts.getenv = func(name string) string {
		if name == "REHEARSE_USER" {
			return marker
		}
		return ""
	}
	engineCalled := false
	opts.newEngine = func(string) (*engine.Engine, error) {
		engineCalled = true
		return nil, errors.New("must not be called")
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"run", "--wsl-distro", "RehearseTest2404", configPath}, stdout, stderr, opts)
	if code == 0 || engineCalled || !strings.Contains(stderr.String(), "AUTH_INVALID") || strings.Contains(stdout.String()+stderr.String(), marker) {
		t.Fatalf("invalid credentials leaked or reached runtime: code=%d engineCalled=%v stdout=%q stderr=%q", code, engineCalled, stdout.String(), stderr.String())
	}
}

func TestReportPrintsStoredReportAsJSONOrHTMLAndRejectsUnknownFormat(t *testing.T) {
	opts := testOptions(t.TempDir())
	opts.readReport = func(*state.Store, string) (*report.Report, error) {
		return report.New(validTestRunID, "linux/amd64", time.Now()), nil
	}
	for _, tc := range []struct {
		format string
		want   string
	}{{"json", `"outcome": "not-run"`}, {"html", "<h1>Rehearse: not-run</h1>"}} {
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		code := execute(context.Background(), []string{"report", "--format", tc.format, validTestRunID}, stdout, stderr, opts)
		if code != 0 || !strings.Contains(stdout.String(), tc.want) {
			t.Errorf("report format %q: code=%d stdout=%q stderr=%q", tc.format, code, stdout.String(), stderr.String())
		}
	}
	marker := "format-secret-4883"
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"report", "--format", marker, validTestRunID}, stdout, stderr, opts)
	if code == 0 || strings.Contains(stdout.String()+stderr.String(), marker) || !strings.Contains(stderr.String(), "REPORT_FORMAT_UNSUPPORTED") {
		t.Fatalf("unsafe format diagnostic: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestReportDefaultReadsOnlyTheRunReportFromPrivateState(t *testing.T) {
	root := t.TempDir()
	store, err := state.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("test-daemon")
	if err != nil {
		t.Fatal(err)
	}
	r := report.New(run.ID, "windows/amd64", time.Now())
	if err := r.Set("backup.inspect", "failed", "BACKUP_FAILED"); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	r.FinishedAt = &finished
	r.Result = r.Outcome()
	raw, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var html strings.Builder
	if err := r.HTML(&html); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte(html.String()), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "failed"
	if err := store.Save(lock, run); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(root)
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"report", run.ID}, stdout, stderr, opts)
	if code != 0 || !strings.Contains(stdout.String(), `"runId": "`+run.ID+`"`) || !strings.Contains(stdout.String(), `"outcome": "failed"`) {
		t.Fatalf("report did not use stored JSON: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestCleanupPassesNamedWSLDistroAndUsesSafeErrors(t *testing.T) {
	opts := testOptions(t.TempDir())
	opts.goos = "windows"
	distroSeen := ""
	cleanupCalled := false
	opts.newEngine = func(distro string) (*engine.Engine, error) {
		distroSeen = distro
		return nil, nil
	}
	opts.cleanup = func(context.Context, *state.Store, *engine.Engine, string) error {
		cleanupCalled = true
		return errors.New("OPERATION_FAILED: private-resource-name")
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"cleanup", "--wsl-distro", "RehearseTest2404", validTestRunID}, stdout, stderr, opts)
	if code == 0 || !cleanupCalled || distroSeen != "RehearseTest2404" {
		t.Fatalf("cleanup did not use selected runtime: code=%d called=%v distro=%q", code, cleanupCalled, distroSeen)
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-resource-name") || !strings.Contains(stderr.String(), "OPERATION_FAILED") {
		t.Fatalf("unsafe cleanup diagnostic: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCleanupRequiresExplicitWSLAndRejectsUnsupportedLinuxArch(t *testing.T) {
	for _, tc := range []struct {
		name string
		goos string
		arch string
		want string
	}{{"windows missing distro", "windows", "amd64", "WSL_DISTRIBUTION_REQUIRED"}, {"linux arm64", "linux", "arm64", "DOCKER_PLATFORM_UNSUPPORTED"}} {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(t.TempDir())
			opts.goos, opts.goarch = tc.goos, tc.arch
			engineCalled := false
			opts.newEngine = func(string) (*engine.Engine, error) {
				engineCalled = true
				return nil, errors.New("must not be called")
			}
			stdout, stderr := &strings.Builder{}, &strings.Builder{}
			code := execute(context.Background(), []string{"cleanup", validTestRunID}, stdout, stderr, opts)
			if code == 0 || engineCalled || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("runtime boundary not enforced: code=%d engineCalled=%v stderr=%q", code, engineCalled, stderr.String())
			}
		})
	}
}

func TestReportRejectsExtraFileArgumentWithoutReadingIt(t *testing.T) {
	opts := testOptions(t.TempDir())
	readCalled := false
	opts.readReport = func(*state.Store, string) (*report.Report, error) {
		readCalled = true
		return report.New(validTestRunID, "linux/amd64", time.Now()), nil
	}
	marker := "private-report-path-018c"
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	code := execute(context.Background(), []string{"report", validTestRunID, marker}, stdout, stderr, opts)
	if code == 0 || readCalled || strings.Contains(stdout.String()+stderr.String(), marker) {
		t.Fatalf("extra path was used or echoed: code=%d readCalled=%v stdout=%q stderr=%q", code, readCalled, stdout.String(), stderr.String())
	}
}

func TestVersionAndHelpAreConcise(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--help"}, {"help"}} {
		stdout, stderr := &strings.Builder{}, &strings.Builder{}
		code := execute(context.Background(), args, stdout, stderr, testOptions(t.TempDir()))
		if code != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
			t.Errorf("args %q: code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	if code := execute(context.Background(), []string{"--version"}, stdout, stderr, testOptions(t.TempDir())); code != 0 || stderr.Len() != 0 || stdout.String() != "rehearse "+version+"\n" {
		t.Fatalf("unexpected version result: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func writeConfig(t *testing.T, dir, tail string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	config := `{"schemaVersion":1,"adapter":"miniflux","sourceVersion":"2.2.19","targetVersion":"2.3.3","postgresVersion":"17.11",` + tail + `}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testOptions(stateRoot string) cliOptions {
	return cliOptions{
		stateRoot:   stateRoot,
		goos:        runtime.GOOS,
		goarch:      "amd64",
		getenv:      func(string) string { return "" },
		buildPlan:   app.BuildPlan,
		resolveAuth: app.ResolveAuth,
		readInput:   func(path string) (io.ReadCloser, error) { return os.Open(path) },
	}
}
