package app

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/fixture"
	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestForgejoFixtureMutationRequestIsFixedAndStdinConfigured(t *testing.T) {
	auth := forgejo.Auth{Token: "synthetic-fixture-token"}
	body := []byte(`{"name":"upgrade-fixture"}`)
	config, err := forgejoFixtureMutationConfig("POST", "/api/v1/user/repos", body, auth)
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, want := range []string{
		`url = "http://app:3000/api/v1/user/repos"`,
		`request = "POST"`,
		`header = "Authorization: token synthetic-fixture-token"`,
		`data-binary = "{\"name\":\"upgrade-fixture\"}"`,
		`proxy = ""`, `noproxy = "*"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("mutation config omitted fixed option %q", want)
		}
	}
	for name, tc := range map[string]struct {
		method, endpoint string
	}{
		"arbitrary-endpoint": {"POST", "/api/v1/admin/users"},
		"arbitrary-method":   {"PATCH", "/api/v1/user/repos"},
		"query-injection":    {"POST", "/api/v1/user/repos?redirect=https://outside.invalid"},
		"newline-injection":  {"POST", "/api/v1/user/repos\nurl = https://outside.invalid"},
		"delete-other-user":  {"DELETE", "/api/v1/users/other/tokens/123"},
		"delete-nonnumeric":  {"DELETE", "/api/v1/users/rehearse-fixture/tokens/rehearse-fixture-bootstrap-write"},
		"bearer-delete":      {"DELETE", "/api/v1/users/rehearse-fixture/tokens/123"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := forgejoFixtureMutationConfig(tc.method, tc.endpoint, body, auth); err == nil {
				t.Fatal("unapproved fixture API mutation was accepted")
			}
		})
	}
	if _, err := forgejoFixtureMutationConfig("GET", "/api/v1/users/rehearse-fixture/tokens", nil, auth); err != nil {
		t.Fatalf("fixed token-metadata listing request was rejected: %v", err)
	}
}

func TestForgejoFixtureBasicDeleteConfigOnlyTargetsSyntheticToken(t *testing.T) {
	config, err := forgejoFixtureBasicDeleteConfig("/api/v1/users/rehearse-fixture/tokens/123")
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, want := range []string{
		`url = "http://app:3000/api/v1/users/rehearse-fixture/tokens/123"`,
		`request = "DELETE"`,
		`user = "rehearse-fixture:synthetic-fixture-password"`,
		`proxy = ""`, `noproxy = "*"`, `write-out = "\n%{http_code}\n"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("fixed Basic delete config omitted option %q", want)
		}
	}
	if strings.Contains(text, "Authorization:") || strings.Contains(text, "location") {
		t.Fatal("Basic delete config included bearer auth or redirects")
	}
	for _, endpoint := range []string{
		"/api/v1/users/other/tokens/123",
		"/api/v1/users/rehearse-fixture/tokens/bootstrap-write",
		"/api/v1/users/rehearse-fixture/tokens/0",
		"/api/v1/users/rehearse-fixture/tokens/001",
		"https://outside.invalid/api/v1/users/rehearse-fixture/tokens/123",
	} {
		if _, err := forgejoFixtureBasicDeleteConfig(endpoint); err == nil {
			t.Errorf("unsafe Basic delete endpoint was accepted: %q", endpoint)
		}
	}
}

func TestForgejoFixtureTarFilterDropsOnlyGeneratedRuntimeConfig(t *testing.T) {
	fixtureConfig := []byte("[DEFAULT]\nAPP_NAME = generated-test-config\n")
	sourceConfig := []byte("[DEFAULT]\nAPP_NAME = preserved synthetic source configuration\n")
	input := forgejoFixtureTar(t, []fixtureTarEntry{
		{name: "gitea/", dir: true},
		{name: "gitea/conf/", dir: true},
		{name: "gitea/conf/app.ini", body: sourceConfig},
		{name: "git/", dir: true},
		{name: "git/repositories/", dir: true},
		{name: ".rehearse-runtime/", dir: true},
		{name: ".rehearse-runtime/app.ini", body: fixtureConfig},
	})
	filtered, err := stripForgejoFixtureRuntimeNamespace(context.Background(), bytes.NewReader(input), fixtureConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forgejo.ValidateDataTar(context.Background(), bytes.NewReader(filtered)); err != nil {
		t.Fatalf("filtered source fixture is not a valid archive: %v", err)
	}
	if got := forgejoFixtureTarFile(t, filtered, "gitea/conf/app.ini"); !bytes.Equal(got, sourceConfig) {
		t.Fatal("original synthetic source app.ini changed while generated runtime config was filtered")
	}
	if _, err := forgejoFixtureTarFileIfPresent(filtered, ".rehearse-runtime/app.ini"); err == nil {
		t.Fatal("generated runtime config leaked into the canonical fixture data TAR")
	}
}

func TestForgejoFixtureHTTPStatusSuffixDoesNotExposeBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output []byte
		want   int
		ok     bool
	}{
		{name: "empty 204", output: []byte("\n204\n"), want: 204, ok: true},
		{name: "json 404", output: []byte("{\"message\":\"private diagnostic\"}\n404\n"), want: 404, ok: true},
		{name: "missing delimiter", output: []byte("204\n"), ok: false},
		{name: "nonnumeric", output: []byte("\n2x4\n"), ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := forgejoFixtureHTTPStatusSuffix(tc.output)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("fixture HTTP status parse got=(%d,%v), want=(%d,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestForgejoFixtureConfigKeyDiffOmitsValues(t *testing.T) {
	expected := []byte("[security]\nSECRET_KEY = expected-secret\nINTERNAL_TOKEN = expected-token\n")
	actual := []byte("[security]\nSECRET_KEY = changed-secret\nJWT_SECRET = new-token\n")
	err := forgejoFixtureRuntimeConfigSafeToStrip(expected, actual)
	if err == nil {
		t.Fatal("unexpected generated config changes were accepted")
	}
	diagnostics := err.Error()
	for _, key := range []string{"security.JWT_SECRET", "security.INTERNAL_TOKEN", "security.SECRET_KEY"} {
		if !strings.Contains(diagnostics, key) {
			t.Errorf("fixture config diagnostics omitted key name %q: %s", key, diagnostics)
		}
	}
	for _, secret := range []string{"expected-secret", "expected-token", "changed-secret", "new-token"} {
		if strings.Contains(diagnostics, secret) {
			t.Fatalf("fixture config diagnostics leaked a value: %s", diagnostics)
		}
	}
}

func TestForgejoFixtureConfigAllowlistOnlyPinnedStartupDefaults(t *testing.T) {
	expected := []byte("APP_NAME = Rehearse\nWORK_USER = fixture\n[security]\nSECRET_KEY = old-secret\n")
	generatedSecret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x37}, 32))
	actual := []byte("APP_NAME = Rehearse\nWORK_USER = fixture\nWORK_PATH = /data/gitea\n[security]\nSECRET_KEY = old-secret\n[oauth2]\nJWT_SECRET = " + generatedSecret + "\n")
	if err := forgejoFixtureRuntimeConfigSafeToStrip(expected, actual); err != nil {
		t.Fatalf("known pinned startup defaults were rejected: %v", err)
	}
	changedExisting := bytes.Replace(actual, []byte("SECRET_KEY = old-secret"), []byte("SECRET_KEY = changed-secret"), 1)
	if err := forgejoFixtureRuntimeConfigSafeToStrip(expected, changedExisting); err == nil {
		t.Fatal("changed generated secret was accepted")
	}
	unknownAddition := append(bytes.Clone(actual), []byte("[service]\nDISABLE_REGISTRATION = false\n")...)
	if err := forgejoFixtureRuntimeConfigSafeToStrip(expected, unknownAddition); err == nil {
		t.Fatal("unknown Forgejo startup option was accepted")
	}
	wrongWorkPath := bytes.Replace(actual, []byte("WORK_PATH = /data/gitea"), []byte("WORK_PATH = /production"), 1)
	if err := forgejoFixtureRuntimeConfigSafeToStrip(expected, wrongWorkPath); err == nil {
		t.Fatal("unexpected Forgejo work path was accepted")
	}
	invalidSecret := bytes.Replace(actual, []byte(generatedSecret), []byte("not-base64"), 1)
	if err := forgejoFixtureRuntimeConfigSafeToStrip(expected, invalidSecret); err == nil {
		t.Fatal("invalid generated OAuth2 secret was accepted")
	}
}

func TestForgejoFixtureTarFilterRejectsUnexpectedReservedRuntimeFiles(t *testing.T) {
	input := forgejoFixtureTar(t, []fixtureTarEntry{
		{name: "gitea/", dir: true},
		{name: "gitea/conf/", dir: true},
		{name: "gitea/conf/app.ini", body: []byte("synthetic source config\n")},
		{name: ".rehearse-runtime/", dir: true},
		{name: ".rehearse-runtime/app.ini", body: []byte("generated\n")},
		{name: ".rehearse-runtime/extra", body: []byte("must not be silently dropped")},
	})
	if _, err := stripForgejoFixtureRuntimeNamespace(context.Background(), bytes.NewReader(input), []byte("generated\n")); err == nil {
		t.Fatal("unexpected runtime-reserved content was silently stripped")
	}
}

type fixtureTarEntry struct {
	name string
	body []byte
	dir  bool
}

func forgejoFixtureTar(t *testing.T, entries []fixtureTarEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0600, Uid: 1000, Gid: 1000, Format: tar.FormatUSTAR}
		if entry.dir {
			if !strings.HasSuffix(header.Name, "/") {
				header.Name += "/"
			}
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

func forgejoFixtureTarFile(t *testing.T, raw []byte, want string) []byte {
	t.Helper()
	body, err := forgejoFixtureTarFileIfPresent(raw, want)
	if err != nil {
		t.Fatalf("expected TAR member %q is absent", want)
	}
	return body
}

func forgejoFixtureTarFileIfPresent(raw []byte, want string) ([]byte, error) {
	reader := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := reader.Next()
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == want {
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				return nil, io.ErrUnexpectedEOF
			}
			return io.ReadAll(reader)
		}
	}
}

func forgejoFixtureMutationConfig(method, endpoint string, body []byte, auth forgejo.Auth) ([]byte, error) {
	if method == "DELETE" {
		return nil, errors.New("FIXTURE_BASIC_AUTH_REQUIRED")
	}
	if !allowedForgejoFixtureMutation(method, endpoint) || len(body) > 16<<10 {
		return nil, errors.New("FIXTURE_REQUEST_INVALID")
	}
	base, err := forgejo.CurlConfig(forgejo.UserEndpoint(), &auth)
	if err != nil {
		return nil, errors.New("FIXTURE_AUTH_INVALID")
	}
	lines := make([]string, 0, 16)
	urlReplaced := false
	for _, line := range strings.Split(strings.TrimSuffix(string(base), "\n"), "\n") {
		if strings.HasPrefix(line, "url = ") {
			line = "url = " + strconv.Quote("http://app:3000"+endpoint)
			urlReplaced = true
		}
		lines = append(lines, line)
	}
	if !urlReplaced {
		return nil, errors.New("FIXTURE_REQUEST_INVALID")
	}
	lines = append(lines, "request = "+strconv.Quote(method))
	if body != nil {
		lines = append(lines, `header = "Content-Type: application/json"`, "data-binary = "+strconv.Quote(string(body)))
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func forgejoFixtureBasicDeleteConfig(endpoint string) ([]byte, error) {
	if !allowedForgejoFixtureMutation("DELETE", endpoint) {
		return nil, errors.New("FIXTURE_REQUEST_INVALID")
	}
	lines := []string{
		"silent",
		"show-error",
		`proxy = ""`,
		`noproxy = "*"`,
		"connect-timeout = 3",
		"max-time = 15",
		`write-out = "\n%{http_code}\n"`,
		"url = " + strconv.Quote("http://app:3000"+endpoint),
		`request = "DELETE"`,
		"user = " + strconv.Quote(fixture.ForgejoUsername+":"+fixture.ForgejoPassword),
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func allowedForgejoFixtureMutation(method, endpoint string) bool {
	if method == "POST" && endpoint == "/api/v1/user/repos" {
		return true
	}
	if method == "GET" && endpoint == "/api/v1/users/"+fixture.ForgejoUsername+"/tokens" {
		return true
	}
	deletePrefix := "/api/v1/users/" + fixture.ForgejoUsername + "/tokens/"
	if method == "DELETE" && strings.HasPrefix(endpoint, deletePrefix) {
		id := strings.TrimPrefix(endpoint, deletePrefix)
		parsed, err := strconv.ParseInt(id, 10, 64)
		return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == id
	}
	definition := fixture.ForgejoFixture()
	for _, file := range definition.Files {
		generated, err := forgejo.ContentsEndpoint(definition.Username, definition.Repository, file.Path, definition.DefaultBranch)
		if err != nil {
			continue
		}
		path, _, _ := strings.Cut(generated, "?")
		wantMethod := "POST"
		if file.Path == "README.md" {
			wantMethod = "PUT"
		}
		if method == wantMethod && endpoint == path {
			return true
		}
	}
	return false
}

func stripForgejoFixtureRuntimeNamespace(ctx context.Context, input io.Reader, expectedConfig []byte) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || input == nil || len(expectedConfig) == 0 {
		return nil, errors.New("FIXTURE_TAR_INVALID")
	}
	const maxFixtureTarBytes = 64 << 20
	limited := &fixtureByteLimitReader{reader: input, limit: maxFixtureTarBytes}
	reader := tar.NewReader(limited)
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	seenDir, seenConfig := false, false
	for {
		if ctx.Err() != nil {
			return nil, errors.New("CANCELED")
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("FIXTURE_TAR_INVALID")
		}
		name := strings.TrimPrefix(header.Name, "./")
		cleanName := strings.TrimSuffix(name, "/")
		if cleanName == ".rehearse-runtime" {
			if seenDir || header.Typeflag != tar.TypeDir || header.Size != 0 || header.Uid != 1000 || header.Gid != 1000 || header.Mode&0777 != 0700 {
				return nil, errors.New("FIXTURE_RUNTIME_DIR_INVALID type=" + strconv.Itoa(int(header.Typeflag)) + " size=" + strconv.FormatInt(header.Size, 10) + " uid=" + strconv.Itoa(header.Uid) + " gid=" + strconv.Itoa(header.Gid) + " mode=" + strconv.FormatInt(header.Mode, 8))
			}
			seenDir = true
			continue
		}
		if name == ".rehearse-runtime/app.ini" {
			if seenConfig || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) || header.Size <= 0 || header.Size > 16<<10 || header.Uid != 1000 || header.Gid != 1000 || header.Mode&0777 != 0600 {
				return nil, errors.New("FIXTURE_RUNTIME_CONFIG_HEADER_INVALID type=" + strconv.Itoa(int(header.Typeflag)) + " size=" + strconv.FormatInt(header.Size, 10) + " uid=" + strconv.Itoa(header.Uid) + " gid=" + strconv.Itoa(header.Gid) + " mode=" + strconv.FormatInt(header.Mode, 8))
			}
			config, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(config)) != header.Size {
				return nil, errors.New("FIXTURE_RUNTIME_CONFIG_CONTENT_INVALID")
			}
			if err := forgejoFixtureRuntimeConfigSafeToStrip(expectedConfig, config); err != nil {
				return nil, err
			}
			seenConfig = true
			continue
		}
		if strings.HasPrefix(name, ".rehearse-runtime/") {
			return nil, errors.New("FIXTURE_RUNTIME_UNEXPECTED_ENTRY")
		}
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, errors.New("FIXTURE_TAR_INVALID")
		}
		if header.Size < 0 || header.Size > maxFixtureTarBytes {
			return nil, errors.New("FIXTURE_TAR_TOO_LARGE")
		}
		copyHeader := *header
		if err := writer.WriteHeader(&copyHeader); err != nil {
			return nil, errors.New("FIXTURE_TAR_INVALID")
		}
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			if _, err := io.CopyN(writer, reader, header.Size); err != nil {
				return nil, errors.New("FIXTURE_TAR_INVALID")
			}
		} else if header.Size != 0 {
			return nil, errors.New("FIXTURE_TAR_INVALID")
		}
		if output.Len() > maxFixtureTarBytes {
			return nil, errors.New("FIXTURE_TAR_TOO_LARGE")
		}
	}
	if !seenDir || !seenConfig || limited.overLimit {
		if limited.overLimit {
			return nil, errors.New("FIXTURE_TAR_TOO_LARGE")
		}
		return nil, errors.New("FIXTURE_RUNTIME_CONFIG_MISSING")
	}
	if err := writer.Close(); err != nil || output.Len() > maxFixtureTarBytes {
		return nil, errors.New("FIXTURE_TAR_TOO_LARGE")
	}
	return bytes.Clone(output.Bytes()), nil
}

func forgejoFixtureRuntimeConfigSafeToStrip(expected, actual []byte) error {
	if bytes.Equal(expected, actual) {
		return nil
	}
	want, wantOK := forgejoFixtureConfigValues(expected)
	got, gotOK := forgejoFixtureConfigValues(actual)
	if !wantOK || !gotOK {
		return errors.New("FIXTURE_RUNTIME_CONFIG_PARSE_INVALID")
	}
	var added, removed, changed []string
	for name, value := range got {
		wantValue, exists := want[name]
		if !exists {
			added = append(added, name)
		} else if wantValue != value {
			changed = append(changed, name)
		}
	}
	for name := range want {
		if _, exists := got[name]; !exists {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	if len(added) != 2 || added[0] != "WORK_PATH" || added[1] != "oauth2.JWT_SECRET" || len(removed) != 0 || len(changed) != 0 {
		return errors.New("FIXTURE_RUNTIME_CONFIG_CHANGED addedKeys=" + strings.Join(added, ",") + " removedKeys=" + strings.Join(removed, ",") + " changedKeys=" + strings.Join(changed, ","))
	}
	if got["WORK_PATH"] != "/data/gitea" {
		return errors.New("FIXTURE_RUNTIME_CONFIG_WORK_PATH_INVALID")
	}
	secret, err := base64.StdEncoding.DecodeString(got["oauth2.JWT_SECRET"])
	if err != nil {
		secret, err = base64.RawStdEncoding.DecodeString(got["oauth2.JWT_SECRET"])
	}
	if err != nil {
		secret, err = base64.RawURLEncoding.DecodeString(got["oauth2.JWT_SECRET"])
	}
	if err != nil {
		secret, err = base64.URLEncoding.DecodeString(got["oauth2.JWT_SECRET"])
	}
	if err != nil || len(secret) != 32 {
		return errors.New("FIXTURE_RUNTIME_CONFIG_OAUTH_SECRET_INVALID")
	}
	return nil
}

func forgejoFixtureConfigValues(raw []byte) (map[string]string, bool) {
	var section string
	values := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && len(line) > 2 {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return nil, false
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, false
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, "[]\r\n\x00") {
			return nil, false
		}
		if section != "" {
			key = section + "." + key
		}
		if _, duplicate := values[key]; duplicate {
			return nil, false
		}
		values[key] = strings.TrimSpace(value)
	}
	return values, true
}

type fixtureByteLimitReader struct {
	reader    io.Reader
	limit     int
	read      int
	overLimit bool
}

func (r *fixtureByteLimitReader) Read(p []byte) (int, error) {
	if r.read >= r.limit {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			r.overLimit = true
			return 0, errors.New("FIXTURE_TAR_TOO_LARGE")
		}
		return 0, err
	}
	if remaining := r.limit - r.read; len(p) > remaining {
		p = p[:remaining]
	}
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

const forgejoFixtureOriginalSourceConfig = "[DEFAULT]\nAPP_NAME = Rehearse public synthetic source fixture\nRUN_USER = git\nRUN_MODE = prod\n\n[database]\nDB_TYPE = postgres\nHOST = source-only.invalid:5432\nNAME = fixture_source\nUSER = fixture_source\nSSL_MODE = disable\n"

const forgejoFixtureBootstrapMaxDataBytes = 64 << 20

// bootstrapForgejoSourceFixture creates a public synthetic source instance,
// captures one stopped and consistent backup, and returns the read-only token
// in memory for a same-process acceptance test. The generated token is also
// written to a private .control candidate sidecar for later explicit review;
// neither this helper nor its logs print the token.
func bootstrapForgejoSourceFixture(t *testing.T, client *engine.Engine) (archivePath string, auth forgejo.Auth, expected fixture.ForgejoDefinition) {
	t.Helper()
	if client == nil {
		t.Fatal("fixture runtime client is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	daemon, err := client.Info(ctx)
	if err != nil {
		t.Fatalf("inspect explicitly selected disposable runtime: %v", err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "forgejo-fixture-state"))
	if err != nil {
		t.Fatalf("open private fixture state: %v", err)
	}
	run, err := store.CreateForAdapter(daemon.ID, "forgejo")
	if err != nil {
		t.Fatalf("persist synthetic fixture ownership: %v", err)
	}
	lock, err := store.AcquireRunLock(run.ID)
	if err != nil {
		t.Fatalf("acquire synthetic fixture run lock: %v", err)
	}
	var runDir string
	var candidateCreated, tokenCreated, keepCandidates bool
	var tokenPath string
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cleanupCancel()
		if cleanupErr := client.Cleanup(cleanupCtx, run); cleanupErr != nil {
			t.Errorf("fixture owned-resource cleanup failed: %v", cleanupErr)
		}
		assertForgejoFixtureRunInventoryEmpty(t, cleanupCtx, client, run)
		if err := lock.Release(); err != nil {
			t.Errorf("fixture run lock release failed: %v", err)
		}
		if !keepCandidates {
			if candidateCreated {
				removeExactFixtureCandidate(t, archivePath)
			}
			if tokenCreated {
				removeExactFixtureCandidate(t, tokenPath)
			}
		}
	}()
	runDir, err = store.RunDir(run.ID)
	if err != nil {
		t.Fatalf("resolve fixture run directory: %v", err)
	}
	expected = fixture.ForgejoFixture()
	t.Logf("FORGEJO_FIXTURE_SCOPE run=%s engine=%s source=%s target=%s database=synthetic-only", run.ID, daemon.Version, forgejo.SourceVersion, forgejo.TargetVersion)

	dataInput := syntheticForgejoFixtureSourceData(t)
	if _, err := forgejo.ValidateDataTar(ctx, bytes.NewReader(dataInput)); err != nil {
		t.Fatalf("validate deterministic synthetic source files: %v", err)
	}
	runtimeConfigTar, err := forgejoRuntimeConfigTar()
	if err != nil {
		t.Fatalf("generate disposable runner config: %v", err)
	}
	runtimeConfig, err := forgejoFixtureRuntimeConfigBytes(runtimeConfigTar)
	if err != nil {
		t.Fatalf("inspect generated runner config archive: %v", err)
	}
	if err := client.CreatePhase(ctx, run, runDir, "baseline"); err != nil {
		t.Fatalf("create fixed source phase: %v", err)
	}
	if err := client.CopyForgejoData(ctx, run, "baseline", bytes.NewReader(dataInput)); err != nil {
		t.Fatalf("copy source fixture data into its owned volume: %v", err)
	}
	if err := client.CopyForgejoData(ctx, run, "baseline", bytes.NewReader(runtimeConfigTar)); err != nil {
		t.Fatalf("copy generated private runner config into its owned volume: %v", err)
	}
	if err := client.Start(ctx, run, "baseline", "db"); err != nil {
		t.Fatalf("start disposable fixture database: %v", err)
	}
	if err := client.WaitDatabase(ctx, run, "baseline"); err != nil {
		t.Fatalf("wait for disposable fixture database: %v", err)
	}
	if err := client.Start(ctx, run, "baseline", "migration"); err != nil {
		t.Fatalf("initialize disposable fixture database: %v", err)
	}
	if err := client.WaitMigration(ctx, run, "baseline"); err != nil {
		t.Fatalf("initialize disposable fixture schema: %v", err)
	}
	if err := client.Start(ctx, run, "baseline", "app"); err != nil {
		t.Fatalf("start source fixture Forgejo: %v", err)
	}
	if err := client.Start(ctx, run, "baseline", "probe"); err != nil {
		t.Fatalf("start owned source fixture API probe: %v", err)
	}
	if err := waitForgejoFixtureAPI(ctx, client, run); err != nil {
		t.Fatalf("wait for exact source fixture API: %v", err)
	}
	if err := createForgejoFixtureUser(ctx, client, run, expected); err != nil {
		t.Fatalf("create one public synthetic fixture user: %v", err)
	}
	writeAuth, err := generateForgejoFixtureToken(ctx, client, run, expected.Username, "rehearse-fixture-bootstrap-write", "read:user,read:repository,write:user,write:repository")
	if err != nil {
		t.Fatalf("generate temporary scoped synthetic write token: %v", err)
	}
	if err := populateForgejoFixtureRepository(ctx, client, run, expected, writeAuth); err != nil {
		t.Fatalf("populate synthetic fixture repository through fixed API calls: %v", err)
	}
	if err := revokeForgejoFixtureBootstrapToken(ctx, client, run, expected.Username, writeAuth); err != nil {
		t.Fatalf("revoke temporary synthetic write token: %v", err)
	}
	auth, err = generateForgejoFixtureToken(ctx, client, run, expected.Username, expected.TokenName, expected.TokenScopes)
	if err != nil {
		t.Fatalf("generate final read-only synthetic fixture token: %v", err)
	}
	if err := verifyForgejoFixtureAPI(ctx, client, run, expected, auth); err != nil {
		t.Fatalf("verify exact synthetic user, private repo, branch and file contents: %v", err)
	}
	if err := client.VerifyPhase(ctx, run, "baseline"); err != nil {
		t.Fatalf("verify exact phase ownership and isolation before backup capture: %v", err)
	}
	if err := client.StopForgejoApp(ctx, run, "baseline"); err != nil {
		t.Fatalf("gracefully stop source Forgejo before consistent backup capture: %v", err)
	}
	dump, err := client.InsideBytes(ctx, run, "baseline", "db", []string{"pg_dump", "--username=rehearse", "--dbname=rehearse", "--format=custom", "--no-owner", "--no-privileges"}, nil, 32<<20)
	if err != nil || len(dump) < 5 || string(dump[:5]) != "PGDMP" {
		t.Fatalf("capture synthetic source custom-format database: %v", safeFixtureFailure(err))
	}
	var copiedData forgejoFixtureLimitBuffer
	copiedData.limit = forgejoFixtureBootstrapMaxDataBytes
	if err := client.ReadForgejoData(ctx, run, "baseline", &copiedData); err != nil {
		t.Fatalf("stream stopped synthetic source data from owned helper: %v", err)
	}
	dataArchive, err := stripForgejoFixtureRuntimeNamespace(ctx, bytes.NewReader(copiedData.Bytes()), runtimeConfig)
	if err != nil {
		t.Fatalf("remove only the exact runner-generated runtime config from fixture archive: %v", err)
	}
	if _, err := forgejo.ValidateDataTar(ctx, bytes.NewReader(dataArchive)); err != nil {
		t.Fatalf("validate canonical synthetic source data TAR: %v", err)
	}
	if sourceConfig, err := forgejoFixtureTarFileIfPresent(dataArchive, "gitea/conf/app.ini"); err != nil || string(sourceConfig) != forgejoFixtureOriginalSourceConfig {
		t.Fatalf("original synthetic source app.ini was not preserved unchanged: %v", safeFixtureFailure(err))
	}
	fileSummary, err := forgejo.ProjectRepositoryFiles(ctx, bytes.NewReader(dataArchive))
	if err != nil || fileSummary.Repositories != 1 || fileSummary.Files == 0 || fileSummary.Bytes == 0 {
		t.Fatalf("synthetic repository data projection was incomplete: %v", safeFixtureFailure(err))
	}
	privateDir := t.TempDir()
	databasePath := filepath.Join(privateDir, "source.dump")
	dataPath := filepath.Join(privateDir, "source-data.tar")
	if err := os.WriteFile(databasePath, dump, 0600); err != nil {
		t.Fatalf("stage synthetic source dump privately: %v", err)
	}
	if err := os.WriteFile(dataPath, dataArchive, 0600); err != nil {
		t.Fatalf("stage synthetic source data privately: %v", err)
	}
	candidateDir, err := forgejoFixtureCandidateDirectory()
	if err != nil {
		t.Fatalf("resolve private fixture candidate directory: %v", err)
	}
	archivePath = filepath.Join(candidateDir, "forgejo-source-fixture-"+run.ID+".zip")
	tokenPath = archivePath + ".read-token.private"
	manifest, err := CreateForgejoArchive(ctx, databasePath, dataPath, archivePath)
	if err != nil {
		t.Fatalf("create validated canonical synthetic source archive: %v", err)
	}
	candidateCreated = true
	archive, size, digest, err := verifyForgejoFixtureArchive(ctx, archivePath)
	if err != nil {
		t.Fatalf("reopen and verify candidate fixture archive: %v", err)
	}
	if manifest.Format != archive.Manifest().Format || len(manifest.Members) != 2 || size == 0 || digest == "" {
		t.Fatalf("canonical candidate manifest did not bind expected payload members")
	}
	if err := writePrivateForgejoFixtureToken(tokenPath, auth.Token); err != nil {
		t.Fatalf("write scoped token to private synthetic-fixture sidecar: %v", err)
	}
	tokenCreated = true
	keepCandidates = true
	t.Logf("FORGEJO_FIXTURE_ARCHIVE_CANDIDATE path=%s bytes=%d sha256=%s tokenCandidate=%s tokenValueLogged=false apiUser=%s repository=%s files=%d", archivePath, size, digest, tokenPath, expected.Username, expected.Repository, len(expected.Files))
	return archivePath, auth, expected
}

func syntheticForgejoFixtureSourceData(t *testing.T) []byte {
	t.Helper()
	return forgejoFixtureTar(t, []fixtureTarEntry{
		{name: "gitea/", dir: true},
		{name: "gitea/conf/", dir: true},
		{name: "gitea/data/", dir: true},
		{name: "git/", dir: true},
		{name: "git/repositories/", dir: true},
		{name: "gitea/conf/app.ini", body: []byte(forgejoFixtureOriginalSourceConfig)},
	})
}

func forgejoFixtureRuntimeConfigBytes(configTar []byte) ([]byte, error) {
	reader := tar.NewReader(bytes.NewReader(configTar))
	var config []byte
	seenDir, seenConfig := false, false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("FIXTURE_RUNTIME_CONFIG_INVALID")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if strings.TrimSuffix(name, "/") == ".rehearse-runtime" && header.Typeflag == tar.TypeDir && header.Uid == 1000 && header.Gid == 1000 && header.Mode&0777 == 0700 {
			if seenDir {
				return nil, errors.New("FIXTURE_RUNTIME_CONFIG_INVALID")
			}
			seenDir = true
			continue
		}
		if name == ".rehearse-runtime/app.ini" && (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) && header.Uid == 1000 && header.Gid == 1000 && header.Mode&0777 == 0600 && header.Size > 0 && header.Size < 16<<10 {
			if seenConfig {
				return nil, errors.New("FIXTURE_RUNTIME_CONFIG_INVALID")
			}
			config, err = io.ReadAll(io.LimitReader(reader, 16<<10))
			if err != nil || int64(len(config)) != header.Size {
				return nil, errors.New("FIXTURE_RUNTIME_CONFIG_INVALID")
			}
			seenConfig = true
			continue
		}
		return nil, errors.New("FIXTURE_RUNTIME_CONFIG_INVALID")
	}
	if !seenDir || !seenConfig {
		return nil, errors.New("FIXTURE_RUNTIME_CONFIG_INVALID")
	}
	return bytes.Clone(config), nil
}

func waitForgejoFixtureAPI(ctx context.Context, client *engine.Engine, run state.Run) error {
	readyCtx, cancel := context.WithTimeout(ctx, apiReadyTimeout)
	defer cancel()
	for {
		status, body, err := requestForgejoAPI(readyCtx, client, run, "baseline", forgejo.VersionEndpoint(), nil)
		if err == nil && status == 200 {
			version, parseErr := forgejo.ParseVersion(body)
			if parseErr == nil && matchesForgejoAPIVersion(version, forgejo.SourceVersion) {
				return nil
			}
		}
		select {
		case <-readyCtx.Done():
			return errors.New("FIXTURE_API_NOT_READY")
		case <-time.After(time.Second):
		}
	}
}

func createForgejoFixtureUser(ctx context.Context, client *engine.Engine, run state.Run, definition fixture.ForgejoDefinition) error {
	args := []string{"/usr/local/bin/forgejo", "--work-path", "/data/gitea", "--config", "/data/.rehearse-runtime/app.ini", "admin", "user", "create",
		"--username", definition.Username, "--password", definition.Password, "--email", "rehearse-fixture@example.invalid", "--must-change-password=false"}
	_, err := client.InsideBytes(ctx, run, "baseline", "app", args, nil, 4096)
	if err != nil {
		return errors.New("FIXTURE_USER_CREATE_FAILED")
	}
	return nil
}

func generateForgejoFixtureToken(ctx context.Context, client *engine.Engine, run state.Run, username, tokenName, scopes string) (forgejo.Auth, error) {
	args := []string{"/usr/local/bin/forgejo", "--work-path", "/data/gitea", "--config", "/data/.rehearse-runtime/app.ini", "admin", "user", "generate-access-token",
		"--username", username, "--token-name", tokenName, "--raw", "--scopes", scopes}
	output, err := client.InsideBytes(ctx, run, "baseline", "app", args, nil, 4096)
	if err != nil {
		return forgejo.Auth{}, errors.New("FIXTURE_TOKEN_CREATE_FAILED")
	}
	auth := forgejo.Auth{Token: strings.TrimSpace(string(output))}
	if _, err := forgejo.CurlConfig(forgejo.UserEndpoint(), &auth); err != nil {
		return forgejo.Auth{}, errors.New("FIXTURE_TOKEN_INVALID")
	}
	return auth, nil
}

func populateForgejoFixtureRepository(ctx context.Context, client *engine.Engine, run state.Run, definition fixture.ForgejoDefinition, auth forgejo.Auth) error {
	createBody, err := json.Marshal(struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		Private       bool   `json:"private"`
		AutoInit      bool   `json:"auto_init"`
		DefaultBranch string `json:"default_branch"`
	}{definition.Repository, "Public synthetic Rehearse fixture", definition.Private, true, definition.DefaultBranch})
	if err != nil {
		return errors.New("FIXTURE_API_REQUEST_INVALID")
	}
	status, _, err := forgejoFixtureMutation(ctx, client, run, "POST", "/api/v1/user/repos", createBody, auth)
	if err != nil || status != 201 {
		return errors.New("FIXTURE_REPOSITORY_CREATE_FAILED")
	}
	readmeEndpoint, err := forgejo.ContentsEndpoint(definition.Username, definition.Repository, "README.md", definition.DefaultBranch)
	if err != nil {
		return errors.New("FIXTURE_API_REQUEST_INVALID")
	}
	status, readmeBody, err := requestForgejoAPI(ctx, client, run, "baseline", readmeEndpoint, &auth)
	if err != nil || status != 200 {
		return errors.New("FIXTURE_REPOSITORY_INITIALIZATION_FAILED")
	}
	var initialFile struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(readmeBody, &initialFile) != nil || !validForgejoFixtureObjectID(initialFile.SHA) {
		return errors.New("FIXTURE_REPOSITORY_INITIALIZATION_FAILED")
	}
	files := append([]fixture.ForgejoFile(nil), definition.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, file := range files {
		endpoint, err := forgejo.ContentsEndpoint(definition.Username, definition.Repository, file.Path, definition.DefaultBranch)
		if err != nil {
			return errors.New("FIXTURE_API_REQUEST_INVALID")
		}
		endpoint, _, _ = strings.Cut(endpoint, "?")
		bodyFields := struct {
			Message string `json:"message"`
			Content string `json:"content"`
			Branch  string `json:"branch"`
			SHA     string `json:"sha,omitempty"`
		}{Message: "Add synthetic fixture " + file.Path, Content: base64.StdEncoding.EncodeToString(file.Content), Branch: definition.DefaultBranch}
		method := "POST"
		if file.Path == "README.md" {
			method = "PUT"
			bodyFields.SHA = initialFile.SHA
		}
		body, err := json.Marshal(bodyFields)
		if err != nil {
			return errors.New("FIXTURE_API_REQUEST_INVALID")
		}
		status, _, err := forgejoFixtureMutation(ctx, client, run, method, endpoint, body, auth)
		wantStatus := 201
		if method == "PUT" {
			wantStatus = 200
		}
		if err != nil || status != wantStatus {
			return errors.New("FIXTURE_FILE_CREATE_FAILED")
		}
	}
	return nil
}

func revokeForgejoFixtureBootstrapToken(ctx context.Context, client *engine.Engine, run state.Run, username string, auth forgejo.Auth) error {
	if username != fixture.ForgejoUsername {
		return errors.New("FIXTURE_WRITE_TOKEN_REVOKE_FAILED")
	}
	listEndpoint := "/api/v1/users/" + fixture.ForgejoUsername + "/tokens"
	status, response, err := forgejoFixtureMutation(ctx, client, run, "GET", listEndpoint, nil, auth)
	if err != nil {
		return errors.New("FIXTURE_TOKEN_LIST_REQUEST_FAILED")
	}
	if status != 200 {
		return errors.New("FIXTURE_TOKEN_LIST_STATUS_" + strconv.Itoa(status))
	}
	var tokens []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(response, &tokens) != nil || len(tokens) != 1 || tokens[0].ID <= 0 || tokens[0].Name != "rehearse-fixture-bootstrap-write" {
		return errors.New("FIXTURE_TOKEN_LIST_CONTENT_INVALID")
	}
	endpoint := listEndpoint + "/" + strconv.FormatInt(tokens[0].ID, 10)
	deleteConfig, err := forgejoFixtureBasicDeleteConfig(endpoint)
	if err != nil {
		return err
	}
	output, err := client.InsideBytes(ctx, run, "baseline", "probe", []string{"curl", "--disable", "--config", "-"}, bytes.NewReader(deleteConfig), forgejo.MaxAPIResponseBytes+8)
	if err != nil {
		return errors.New("FIXTURE_TOKEN_DELETE_REQUEST_FAILED")
	}
	status, ok := forgejoFixtureHTTPStatusSuffix(output)
	if !ok {
		return errors.New("FIXTURE_API_RESPONSE_INVALID")
	}
	if status != 204 {
		return errors.New("FIXTURE_TOKEN_DELETE_STATUS_" + strconv.Itoa(status))
	}
	if string(output) != "\n204\n" {
		return errors.New("FIXTURE_API_RESPONSE_INVALID")
	}
	status, _, err = requestForgejoAPI(ctx, client, run, "baseline", forgejo.UserEndpoint(), &auth)
	if err != nil {
		return errors.New("FIXTURE_TOKEN_REVOKE_VERIFY_REQUEST_FAILED")
	}
	if status != 401 {
		return errors.New("FIXTURE_TOKEN_REVOKE_VERIFY_STATUS_" + strconv.Itoa(status))
	}
	return nil
}

func verifyForgejoFixtureAPI(ctx context.Context, client *engine.Engine, run state.Run, definition fixture.ForgejoDefinition, auth forgejo.Auth) error {
	observed, err := observeForgejoAPI(ctx, client, run, "baseline", auth, forgejo.SourceVersion)
	if err != nil || observed.User.Login != definition.Username || observed.User.IsAdmin || len(observed.Repositories) != 1 {
		return errors.New("FIXTURE_API_DATA_INVALID")
	}
	repository := observed.Repositories[0]
	if repository.Repository.OwnerLogin != definition.Username || repository.Repository.Name != definition.Repository ||
		repository.Repository.FullName != definition.Username+"/"+definition.Repository || !repository.Repository.Private ||
		repository.Repository.Empty || repository.Repository.Archived || repository.Repository.Mirror || repository.Repository.Fork ||
		repository.Repository.DefaultBranch != definition.DefaultBranch || repository.Branch.Name != definition.DefaultBranch ||
		len(repository.Contents) != len(definition.Files) {
		return errors.New("FIXTURE_API_DATA_INVALID")
	}
	wantDigests := make(map[string]string, len(definition.Files))
	wantSizes := make(map[string]int64, len(definition.Files))
	for _, file := range definition.Files {
		digest := sha256.Sum256(file.Content)
		wantDigests[file.Path] = hex.EncodeToString(digest[:])
		wantSizes[file.Path] = int64(len(file.Content))
	}
	for _, content := range repository.Contents {
		digest, ok := wantDigests[content.Path]
		if !ok || digest != content.SHA256 || wantSizes[content.Path] != content.Size {
			return errors.New("FIXTURE_API_DATA_INVALID")
		}
		delete(wantDigests, content.Path)
	}
	if len(wantDigests) != 0 {
		return errors.New("FIXTURE_API_DATA_INVALID")
	}
	return nil
}

func forgejoFixtureMutation(ctx context.Context, client *engine.Engine, run state.Run, method, endpoint string, body []byte, auth forgejo.Auth) (int, []byte, error) {
	config, err := forgejoFixtureMutationConfig(method, endpoint, body, auth)
	if err != nil {
		return 0, nil, err
	}
	output, err := client.InsideBytes(ctx, run, "baseline", "probe", []string{"curl", "--disable", "--config", "-"}, bytes.NewReader(config), forgejo.MaxAPIResponseBytes+8)
	if err != nil {
		return 0, nil, errors.New("FIXTURE_API_REQUEST_FAILED")
	}
	if method == "DELETE" {
		status, ok := forgejoFixtureHTTPStatusSuffix(output)
		if !ok {
			return 0, nil, errors.New("FIXTURE_API_RESPONSE_INVALID")
		}
		if status != 204 || string(output) != "\n204\n" {
			return status, nil, errors.New("FIXTURE_API_DELETE_STATUS_" + strconv.Itoa(status))
		}
		return 204, nil, nil
	}
	status, response, err := forgejo.ParseResponse(output)
	if err != nil {
		return 0, nil, errors.New("FIXTURE_API_RESPONSE_INVALID")
	}
	return status, response, nil
}

func forgejoFixtureHTTPStatusSuffix(output []byte) (int, bool) {
	if len(output) < 5 || output[len(output)-1] != '\n' || output[len(output)-5] != '\n' {
		return 0, false
	}
	status := 0
	for _, digit := range output[len(output)-4 : len(output)-1] {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		status = status*10 + int(digit-'0')
	}
	return status, status >= 100 && status <= 599
}

func validForgejoFixtureObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded)*2 == len(value) && strings.ToLower(value) == value
}

func (b *forgejoFixtureLimitBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("FIXTURE_TAR_TOO_LARGE")
	}
	return b.Buffer.Write(p)
}

type forgejoFixtureLimitBuffer struct {
	bytes.Buffer
	limit int
}

func safeFixtureFailure(err error) error {
	if err == nil {
		return errors.New("FIXTURE_VALIDATION_FAILED")
	}
	return err
}

func forgejoFixtureCandidateDirectory() (string, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	directory, err := resolveForgejoFixtureCandidateDirectory(source, os.Getenv("REHEARSE_TEST_FIXTURE_CANDIDATE_DIR"), workingDirectory)
	if err != nil {
		return "", err
	}
	if err := createForgejoFixtureCandidateDirectory(directory); err != nil {
		return "", err
	}
	return directory, nil
}

func resolveForgejoFixtureCandidateDirectory(sourcePath, override, workingDirectory string) (string, error) {
	var repositoryRoot string
	if override != "" {
		var err error
		repositoryRoot, err = findForgejoFixtureRepositoryRoot(workingDirectory)
		if err != nil {
			return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
		}
	} else {
		if !filepath.IsAbs(sourcePath) {
			return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
		}
		repositoryRoot = filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))
	}
	if !filepath.IsAbs(repositoryRoot) {
		return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	expected, err := filepath.Abs(filepath.Join(filepath.Dir(repositoryRoot), ".control", "rehearse-runtime", "candidates"))
	if err != nil {
		return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
		}
		provided, err := filepath.Abs(override)
		if err != nil || !sameForgejoFixturePath(filepath.Clean(provided), filepath.Clean(expected)) {
			return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
		}
	}
	return filepath.Clean(expected), nil
}

func findForgejoFixtureRepositoryRoot(workingDirectory string) (string, error) {
	if !filepath.IsAbs(workingDirectory) {
		return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	current, err := filepath.Abs(workingDirectory)
	if err != nil || !forgejoFixturePathHasNoSymlink(current) {
		return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	for {
		modulePath := filepath.Join(current, "go.mod")
		if info, statErr := os.Lstat(modulePath); statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			module, readErr := os.ReadFile(modulePath)
			if readErr == nil && hasForgejoFixtureModuleLine(module) {
				appPath := filepath.Join(current, "internal", "app")
				if !forgejoFixtureRealDirectory(appPath) {
					return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
				}
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("FIXTURE_OUTPUT_PATH_INVALID")
		}
		current = parent
	}
}

func hasForgejoFixtureModuleLine(goMod []byte) bool {
	for _, line := range strings.Split(string(goMod), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" && fields[1] == "github.com/Pastalikek65/rehearse" {
			return true
		}
	}
	return false
}

func createForgejoFixtureCandidateDirectory(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Base(directory) != "candidates" {
		return errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	parent := filepath.Dir(directory)
	if filepath.Base(parent) != "rehearse-runtime" || filepath.Base(filepath.Dir(parent)) != ".control" || !forgejoFixtureRealDirectory(parent) {
		return errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !forgejoFixturePathHasNoSymlink(directory) {
			return errors.New("FIXTURE_OUTPUT_PATH_INVALID")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			return errors.New("FIXTURE_OUTPUT_PATH_INVALID")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !forgejoFixturePathHasNoSymlink(directory) {
		return errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("FIXTURE_OUTPUT_PATH_INVALID")
	}
	return nil
}

func forgejoFixtureRealDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && forgejoFixturePathHasNoSymlink(path)
}

func forgejoFixturePathHasNoSymlink(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(abs)
	return err == nil && sameForgejoFixturePath(filepath.Clean(abs), filepath.Clean(resolved))
}

func sameForgejoFixturePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func verifyForgejoFixtureArchive(ctx context.Context, path string) (*forgejo.Archive, int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return nil, 0, "", errors.New("FIXTURE_ARCHIVE_INVALID")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, "", errors.New("FIXTURE_ARCHIVE_INVALID")
	}
	defer file.Close()
	archive, err := forgejo.OpenArchive(ctx, file, info.Size())
	if err != nil {
		return nil, 0, "", errors.New("FIXTURE_ARCHIVE_INVALID")
	}
	manifest := archive.Manifest()
	if manifest.SourceVersion != forgejo.SourceVersion || manifest.TargetVersion != forgejo.TargetVersion || len(manifest.Members) != 2 ||
		manifest.Members[0].Name != forgejo.DatabaseMember || manifest.Members[1].Name != forgejo.DataMember {
		return nil, 0, "", errors.New("FIXTURE_ARCHIVE_INVALID")
	}
	hash := sha256.New()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, "", errors.New("FIXTURE_ARCHIVE_INVALID")
	}
	if _, err := io.Copy(hash, file); err != nil {
		return nil, 0, "", errors.New("FIXTURE_ARCHIVE_INVALID")
	}
	return archive, info.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func writePrivateForgejoFixtureToken(path, token string) error {
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("FIXTURE_TOKEN_INVALID")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("FIXTURE_TOKEN_FILE_FAILED")
	}
	_, writeErr := io.WriteString(file, token)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("FIXTURE_TOKEN_FILE_FAILED")
	}
	return nil
}

func removeExactFixtureCandidate(t *testing.T, path string) {
	t.Helper()
	directory, err := forgejoFixtureCandidateDirectory()
	if err != nil {
		t.Errorf("fixture candidate cleanup path invalid")
		return
	}
	parent, err := filepath.Abs(filepath.Dir(path))
	if err != nil || !strings.EqualFold(filepath.Clean(parent), filepath.Clean(directory)) {
		t.Errorf("fixture candidate cleanup refused unexpected path")
		return
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("fixture candidate cleanup refused nonregular output")
		return
	}
	if err := os.Remove(path); err != nil {
		t.Errorf("fixture candidate cleanup failed")
	}
}

func assertForgejoFixtureRunInventoryEmpty(t *testing.T, ctx context.Context, client *engine.Engine, run state.Run) {
	t.Helper()
	for _, kind := range []string{"container", "network", "volume"} {
		args := []string{kind, "ls"}
		if kind == "container" {
			args = append(args, "--all")
		}
		args = append(args, "--filter", "label=io.rehearse.run="+run.ID, "--filter", "label=io.rehearse.owner="+run.OwnerID, "--format", "{{.Names}}")
		output, err := client.Bytes(ctx, args, nil, 16<<10)
		if err != nil || strings.TrimSpace(string(output)) != "" {
			t.Errorf("synthetic fixture exact-run %s inventory is not empty", kind)
		}
	}
}

func TestForgejoSourceFixtureBootstrapOnExplicitDisposableRuntime(t *testing.T) {
	distro := os.Getenv("REHEARSE_TEST_FORGEJO_BOOTSTRAP_WSL")
	if distro == "" {
		t.Skip("set REHEARSE_TEST_FORGEJO_BOOTSTRAP_WSL to the approved disposable WSL2 runtime")
	}
	if distro != "RehearseTest2404-20261008" {
		t.Fatal("source fixture bootstrap is restricted to RehearseTest2404-20261008")
	}
	client, err := engine.NewWSL(distro)
	if err != nil {
		t.Fatal(err)
	}
	archivePath, auth, expected := bootstrapForgejoSourceFixture(t, client)
	if _, err := forgejo.CurlConfig(forgejo.UserEndpoint(), &auth); err != nil {
		t.Fatal("returned synthetic read-only token failed validation")
	}
	if expected.Username != fixture.ForgejoUsername || len(expected.Files) != 3 {
		t.Fatal("fixture definition changed during source bootstrap")
	}
	if data, err := os.ReadFile(archivePath + ".read-token.private"); err != nil || string(data) != auth.Token {
		t.Fatal("private fixture token candidate did not match the in-memory synthetic token")
	}
	t.Logf("FORGEJO_SOURCE_FIXTURE_READY archive=%s privateTokenCandidate=%s user=%s repository=%s files=%d tokenValueLogged=false", archivePath, archivePath+".read-token.private", expected.Username, expected.Repository, len(expected.Files))
}
