package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

// This opt-in test qualifies the Forgejo engine boundary against one explicitly
// selected disposable WSL2 Docker engine. It uses synthetic bytes and exact
// Rehearse run names; no production account, backup, bind mount, or host port.
func TestForgejoEngineRuntimeRestoreMigrationAndRecovery(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_WSL")
	if distro == "" {
		t.Skip("set REHEARSE_TEST_WSL to the explicitly approved disposable WSL2 runtime")
	}
	if distro != "RehearseTest2404-20261008" {
		t.Fatalf("runtime qualification is restricted to RehearseTest2404-20261008")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	e, err := NewWSL(distro)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := e.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "forgejo-runtime-state"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateForAdapter(daemon.ID, "forgejo")
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := store.RunDir(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("RUNTIME_SCOPE distro=%s engine=%s adapter=%s source=%s target=%s postgres=%s run=%s", distro, daemon.Version, run.AdapterID(), forgejo.SourceVersion, forgejo.TargetVersion, forgejo.PostgresVersion, run.ID)

	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cleanupCancel()
		if err := e.Cleanup(cleanupCtx, run); err != nil {
			t.Errorf("owned resource cleanup failed: %v", err)
		}
		assertForgejoRunInventoryEmpty(t, cleanupCtx, e, run)
	}()

	dataTar := syntheticForgejoDataTar(t)
	if _, err := forgejo.ValidateDataTar(ctx, bytes.NewReader(dataTar)); err != nil {
		t.Fatalf("synthetic data tar failed the production validator: %v", err)
	}
	configTar := syntheticForgejoRuntimeConfigTar(t, syntheticForgejoRuntimeConfig())
	var sourceDump []byte

	for _, phase := range []struct {
		name               string
		version            string
		migrate            bool
		restoreSourceDB    bool
		expectSourceSchema bool
	}{
		{name: "baseline", version: forgejo.SourceVersion, migrate: true, expectSourceSchema: true},
		{name: "target", version: forgejo.TargetVersion, migrate: true, restoreSourceDB: true},
		{name: "recovery", version: forgejo.SourceVersion, restoreSourceDB: true, expectSourceSchema: true},
	} {
		t.Run(phase.name, func(t *testing.T) {
			if err := e.CreatePhase(ctx, run, runDir, phase.name); err != nil {
				t.Fatalf("create fixed phase: %v", err)
			}
			if err := e.CopyForgejoData(ctx, run, phase.name, bytes.NewReader(dataTar)); err != nil {
				t.Fatalf("copy validated synthetic data archive into stopped helper: %v", err)
			}
			if err := e.CopyForgejoData(ctx, run, phase.name, bytes.NewReader(configTar)); err != nil {
				t.Fatalf("copy generated private runtime config into stopped helper: %v", err)
			}
			var copiedOut bytes.Buffer
			if err := e.ReadForgejoData(ctx, run, phase.name, &copiedOut); err != nil {
				t.Fatalf("stream phase data back from stopped helper: %v", err)
			}
			verifyForgejoCopyOutHeaders(t, copiedOut.Bytes())
			t.Logf("COPY_OUT_QUALIFIED phase=%s tarBytes=%d reservedConfigHeaderPresent=true payloadLogged=false", phase.name, copiedOut.Len())
			if err := e.Start(ctx, run, phase.name, "db"); err != nil {
				t.Fatalf("start owned database: %v", err)
			}
			if err := e.WaitDatabase(ctx, run, phase.name); err != nil {
				t.Fatalf("wait for owned database: %v", err)
			}
			if phase.restoreSourceDB {
				if len(sourceDump) < 5 || string(sourceDump[:5]) != "PGDMP" {
					t.Fatal("source custom-format dump was not available")
				}
				if err := restoreSyntheticForgejoDatabase(ctx, e, run, phase.name, sourceDump); err != nil {
					t.Fatalf("restore synthetic source database into fresh phase volume: %v", err)
				}
			}
			if phase.migrate {
				if err := e.Start(ctx, run, phase.name, "migration"); err != nil {
					t.Fatalf("start pinned Forgejo migration: %v", err)
				}
				if err := e.WaitMigration(ctx, run, phase.name); err != nil {
					t.Fatalf("wait for pinned Forgejo migration: %v", err)
				}
			}
			if phase.expectSourceSchema || phase.name == "target" {
				if err := verifySyntheticForgejoSchema(ctx, e, run, phase.name, phase.expectSourceSchema); err != nil {
					t.Fatalf("schema projection mismatch: %v", err)
				}
			}
			if phase.name == "baseline" {
				dump, err := e.InsideBytes(ctx, run, phase.name, "db", []string{"pg_dump", "--username=rehearse", "--dbname=rehearse", "--format=custom", "--no-owner", "--no-privileges"}, nil, 32<<20)
				if err != nil {
					t.Fatalf("create synthetic source-format database dump: %v", err)
				}
				if len(dump) < 5 || string(dump[:5]) != "PGDMP" {
					t.Fatal("pg_dump did not produce the custom format")
				}
				sourceDump = bytes.Clone(dump)
			}
			if err := e.Start(ctx, run, phase.name, "app"); err != nil {
				t.Fatalf("start pinned Forgejo web process: %v", err)
			}
			if err := e.Start(ctx, run, phase.name, "probe"); err != nil {
				t.Fatalf("start owned API probe: %v", err)
			}
			if err := verifySyntheticForgejoRuntimeUser(ctx, e, run, phase.name); err != nil {
				t.Fatalf("Forgejo runtime user/private config check: %v", err)
			}
			version, err := waitSyntheticForgejoVersion(ctx, e, run, phase.name)
			if err != nil {
				t.Fatalf("Forgejo API did not start: %v", err)
			}
			if !forgejoRuntimeVersionMatches(version, phase.version) {
				t.Fatalf("Forgejo reported version %s, expected %s", version, phase.version)
			}
			if err := e.VerifyPhase(ctx, run, phase.name); err != nil {
				t.Fatalf("phase effective boundary verification: %v", err)
			}
			if err := e.StopForgejoApp(ctx, run, phase.name); err != nil {
				t.Fatalf("gracefully stop the verified Forgejo app before consistent reads: %v", err)
			}
			var stoppedCopy bytes.Buffer
			if err := e.ReadForgejoData(ctx, run, phase.name, &stoppedCopy); err != nil {
				t.Fatalf("stopped app prevented a consistent data-helper copy: %v", err)
			}
			verifyForgejoCopyOutHeaders(t, stoppedCopy.Bytes())
			t.Logf("PHASE_QUALIFIED phase=%s apiVersion=%s migration=%t cleanOwnedVolumes=true apiFixture=not-run", phase.name, version, phase.migrate)
		})
		if t.Failed() {
			return
		}
		if err := e.Cleanup(ctx, run); err != nil {
			t.Fatalf("cleanup after %s before next clean phase: %v", phase.name, err)
		}
		assertForgejoRunInventoryEmpty(t, ctx, e, run)
		t.Logf("PHASE_CLEANED phase=%s exactRunInventory=empty", phase.name)
	}
}

func restoreSyntheticForgejoDatabase(ctx context.Context, e *Engine, run state.Run, phase string, dump []byte) error {
	return e.Inside(ctx, run, phase, "db", []string{"pg_restore", "--exit-on-error", "--no-owner", "--no-privileges", "--username=rehearse", "--dbname=rehearse"}, bytes.NewReader(dump), io.Discard)
}

func verifySyntheticForgejoSchema(ctx context.Context, e *Engine, run state.Run, phase string, source bool) error {
	result, err := e.InsideBytes(ctx, run, phase, "db", []string{"psql", "--username=rehearse", "--dbname=rehearse", "--no-psqlrc", "--quiet", "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1", "--command", forgejo.SchemaSQL}, nil, int(forgejo.MaxSchemaBytes)+1)
	if err != nil {
		return err
	}
	snapshot, err := forgejo.ParseSchemaProjection(bytes.NewReader(result))
	if err != nil {
		return err
	}
	expected := forgejo.TargetSchemaExpectation()
	if source {
		expected = forgejo.SourceSchemaExpectation()
	}
	if !forgejo.SchemaMatches(snapshot, expected) {
		return fmt.Errorf("fixed schema expectation mismatch (giteaVersion=%d forgejoVersion=%d migrationIDs=%q)", snapshot.GiteaVersion, snapshot.ForgejoVersion, snapshot.MigrationIDs)
	}
	return nil
}

func verifySyntheticForgejoRuntimeUser(ctx context.Context, e *Engine, run state.Run, phase string) error {
	user, err := e.InsideBytes(ctx, run, phase, "app", []string{"id", "-u"}, nil, 64)
	if err != nil || strings.TrimSpace(string(user)) != "1000" {
		return code("CONTAINER_USER_MISMATCH")
	}
	for _, check := range []struct{ path, expected string }{
		{path: "/data/.rehearse-runtime", expected: "1000:1000:700"},
		{path: "/data/.rehearse-runtime/app.ini", expected: "1000:1000:600"},
	} {
		metadata, err := e.InsideBytes(ctx, run, phase, "app", []string{"stat", "-c", "%u:%g:%a", check.path}, nil, 128)
		if err != nil || strings.TrimSpace(string(metadata)) != check.expected {
			return code("FORGEJO_RUNTIME_CONFIG_BOUNDARY_FAILED")
		}
	}
	return nil
}

func waitSyntheticForgejoVersion(ctx context.Context, e *Engine, run state.Run, phase string) (string, error) {
	request, err := forgejo.CurlConfig(forgejo.VersionEndpoint(), nil)
	if err != nil {
		return "", err
	}
	for {
		response, requestErr := e.InsideBytes(ctx, run, phase, "probe", []string{"curl", "--disable", "--config", "-"}, bytes.NewReader(request), forgejo.MaxAPIResponseBytes+8)
		if requestErr == nil {
			status, body, parseErr := forgejo.ParseResponse(response)
			if parseErr == nil && status == 200 {
				version, parseErr := forgejo.ParseVersion(body)
				if parseErr == nil {
					return version, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", code("FORGEJO_API_NOT_READY")
		case <-time.After(time.Second):
		}
	}
}

func forgejoRuntimeVersionMatches(actual, expected string) bool {
	if actual == expected {
		return expected == forgejo.SourceVersion || expected == forgejo.TargetVersion
	}
	// These exact build strings were observed from the current immutable image
	// pins. Do not treat arbitrary build metadata as proof of a pinned release.
	return (expected == forgejo.SourceVersion && actual == "15.0.9+gitea-1.22.0") ||
		(expected == forgejo.TargetVersion && actual == "16.0.5+gitea-1.22.0")
}

func TestForgejoRuntimeVersionMatcherAllowsOnlyExactPinnedAPIStrings(t *testing.T) {
	for _, tc := range []struct {
		actual, expected string
		want             bool
	}{
		{"15.0.9", "15.0.9", true},
		{"15.0.9+gitea-1.22.0", "15.0.9", true},
		{"16.0.5+gitea-1.22.0", "16.0.5", true},
		{"16.0.5+gitea-1.23.0", "16.0.5", false},
		{"15.0.8+gitea-1.22.0", "15.0.9", false},
		{"15.0.9+gitea-1.23.0", "15.0.9", false},
		{"15.0.9+forgejo-1.22.0", "15.0.9", false},
		{"16.0.5+gitea-1.22.0", "15.0.9", false},
		{"15.0.9", "other", false},
	} {
		if got := forgejoRuntimeVersionMatches(tc.actual, tc.expected); got != tc.want {
			t.Errorf("forgejoRuntimeVersionMatches(%q, %q) = %t, want %t", tc.actual, tc.expected, got, tc.want)
		}
	}
}

func assertForgejoRunInventoryEmpty(t *testing.T, ctx context.Context, e *Engine, run state.Run) {
	t.Helper()
	for _, kind := range []string{"container", "network", "volume"} {
		args := []string{kind, "ls"}
		if kind == "container" {
			args = append(args, "--all")
		}
		args = append(args, "--filter", "label=io.rehearse.run="+run.ID, "--filter", "label=io.rehearse.owner="+run.OwnerID, "--format", "{{.Names}}")
		output, err := e.Bytes(ctx, args, nil, 16<<10)
		if err != nil {
			t.Errorf("owned %s inventory query failed: %v", kind, err)
			continue
		}
		if strings.TrimSpace(string(output)) != "" {
			t.Errorf("owned %s remain after cleanup", kind)
		}
		t.Logf("CLEANUP_INVENTORY kind=%s count=0", kind)
	}
}

type runtimeTarEntry struct {
	name string
	body []byte
	dir  bool
}

func syntheticForgejoDataTar(t *testing.T) []byte {
	t.Helper()
	return makeRuntimeTar(t, []runtimeTarEntry{
		{name: "gitea", dir: true},
		{name: "gitea/conf", dir: true},
		{name: "gitea/data", dir: true},
		{name: "git", dir: true},
		{name: "git/repositories", dir: true},
		{name: "gitea/conf/app.ini", body: []byte("[database]\nDB_TYPE=synthetic-invalid\n")},
	})
}

func syntheticForgejoRuntimeConfigTar(t *testing.T, config string) []byte {
	t.Helper()
	return makeRuntimeTar(t, []runtimeTarEntry{
		{name: ".rehearse-runtime", dir: true},
		{name: ".rehearse-runtime/app.ini", body: []byte(config)},
	})
}

func makeRuntimeTar(t *testing.T, entries []runtimeTarEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0600, Uid: 1000, Gid: 1000, Format: tar.FormatUSTAR}
		if entry.dir {
			header.Name += "/"
			header.Typeflag = tar.TypeDir
			header.Mode = 0700
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.body))
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if !entry.dir {
			if _, err := writer.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(output.Bytes())
}

func verifyForgejoCopyOutHeaders(t *testing.T, raw []byte) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(raw))
	seenRuntimeConfig, seenOriginalConfig := false, false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("copy-out stream is not a complete tar archive: %v", err)
		}
		name := strings.TrimPrefix(header.Name, "./")
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			t.Fatalf("copy-out tar has unexpected special header type %d", header.Typeflag)
		}
		if header.Mode&^0777 != 0 || (header.Uid != 0 && header.Uid != 1000) || (header.Gid != 0 && header.Gid != 1000) {
			t.Fatalf("copy-out tar has unsupported metadata for a synthetic entry")
		}
		if name == ".rehearse-runtime/app.ini" {
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Uid != 1000 || header.Gid != 1000 || header.Mode&0777 != 0600 {
				t.Fatalf("generated private config metadata changed")
			}
			seenRuntimeConfig = true
		}
		if name == "gitea/conf/app.ini" {
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				t.Fatalf("synthetic source config is not a regular file")
			}
			seenOriginalConfig = true
		}
	}
	if !seenRuntimeConfig || !seenOriginalConfig {
		t.Fatalf("copy-out tar omitted expected synthetic config headers (runtime=%t original=%t)", seenRuntimeConfig, seenOriginalConfig)
	}
}

func syntheticForgejoRuntimeConfig() string {
	secret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	return fmt.Sprintf(`[DEFAULT]
APP_NAME = Rehearse synthetic runtime qualification
RUN_USER = git
RUN_MODE = prod

[database]
DB_TYPE = postgres
HOST = db:5432
NAME = rehearse
USER = rehearse
PASSWD =
SSL_MODE = disable
PATH = /data/gitea/forgejo.db

[repository]
ROOT = /data/git/repositories

[server]
APP_DATA_PATH = /data/gitea
PROTOCOL = http
DOMAIN = app
ROOT_URL = http://app:3000/
HTTP_ADDR = 0.0.0.0
HTTP_PORT = 3000
DISABLE_SSH = true
START_SSH_SERVER = false
LFS_START_SERVER = false
OFFLINE_MODE = true

[security]
INSTALL_LOCK = true
SECRET_KEY = %s
INTERNAL_TOKEN = %s

[service]
DISABLE_REGISTRATION = true
ENABLE_NOTIFY_MAIL = false

[session]
PROVIDER = memory

[actions]
ENABLED = false

[log]
MODE = console
LEVEL = warn
`, secret, secret)
}
