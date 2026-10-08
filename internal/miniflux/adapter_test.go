package miniflux

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestImagePinsAndSchemaContract(t *testing.T) {
	wantImages := map[string]string{
		"source":   "docker.io/miniflux/miniflux@sha256:8e1ab958250c86b1ce7081cd5f875e60b11fd2833fcb9177f972e45dce18fade",
		"target":   "docker.io/miniflux/miniflux@sha256:4c24c30b8b420c77e3d074bfb6d78cfe5d37670f895fa68c09c43cb719b9ab04",
		"broken":   "docker.io/miniflux/miniflux@sha256:692c7376cbd42b066697e33201b7afa51bc6fe388b10939c628e4c58647b1670",
		"postgres": "docker.io/library/postgres@sha256:3cec7eb015ba8adb28139fa5c83b8489cdf0e666e53dfdf20f598ae0cc8739e3",
		"probe":    "docker.io/curlimages/curl@sha256:43366cd60f226c7655181a0f7e85c468a41d182fdd2dc2c1c3b872a2b9d05d7a",
	}
	gotImages := map[string]string{
		"source": SourceImage, "target": TargetImage, "broken": BrokenFixtureImage,
		"postgres": PostgresImage, "probe": ProbeImage,
	}
	for role, want := range wantImages {
		if got := gotImages[role]; got != want || !strings.Contains(got, "@sha256:") {
			t.Errorf("%s image = %q, want immutable %q", role, got, want)
		}
	}
	if BaselineSchema != 125 || TargetSchema != 132 {
		t.Fatalf("schema pair = %d -> %d, want 125 -> 132", BaselineSchema, TargetSchema)
	}
	if MaxFingerprintRowBytes != 4<<20 || MaxFingerprintBytes != 1<<30 || MaxAPIResponseBytes != 8<<20 {
		t.Fatalf("unexpected bounds: row=%d stream=%d api=%d", MaxFingerprintRowBytes, MaxFingerprintBytes, MaxAPIResponseBytes)
	}

	for name, query := range map[string]string{
		"projection": ProjectionSQL,
		"counts":     CountsSQL,
		"removed":    RemovedKeysSQL,
		"tombstones": TombstoneKeysSQL,
		"schema":     SchemaSQL,
	} {
		if strings.TrimSpace(query) == "" || (name != "schema" && !strings.Contains(query, "\n")) {
			t.Errorf("%s SQL must be a fixed query", name)
		}
	}

	for _, field := range []string{"users", "categories", "feeds", "entries", "starred", "tags", "ORDER BY"} {
		if !strings.Contains(ProjectionSQL, field) {
			t.Errorf("projection SQL omits stable field/table %q", field)
		}
	}
	if !strings.Contains(ProjectionSQL, "status IN ('unread', 'read')") ||
		!strings.Contains(ProjectionSQL, "starred, tags") || strings.Contains(ProjectionSQL, "COALESCE(tags") ||
		strings.Count(ProjectionSQL, "AT TIME ZONE 'UTC'") != 2 {
		t.Fatal("projection must retain active rows, preserve star/tag null distinctions, and normalize timestamps")
	}
	for _, field := range []string{"unread", "read", "removed", "starred", "tags"} {
		if !strings.Contains(CountsSQL, field) {
			t.Errorf("counts SQL omits %q", field)
		}
	}
	if !strings.Contains(RemovedKeysSQL, "status = 'removed'") || !strings.Contains(RemovedKeysSQL, "hash <> ''") {
		t.Fatal("removed-key SQL does not match migration eligibility")
	}
	if !strings.Contains(TombstoneKeysSQL, "entry_tombstones") || !strings.Contains(TombstoneKeysSQL, "ORDER BY") {
		t.Fatal("tombstone SQL must return ordered target tombstone keys")
	}
	if got := strings.Join(strings.Fields(SchemaSQL), " "); got != "SELECT version::integer FROM schema_version" {
		t.Fatalf("schema SQL = %q", got)
	}
}

func TestFingerprintStreamsCompleteJSONLines(t *testing.T) {
	input := "[\"category\",3,\"A\"]\n[\"entry\",9,true,[\"news\"]]\n"
	wantHash := sha256.Sum256([]byte(input))
	got, err := Fingerprint(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != hex.EncodeToString(wantHash[:]) || got.Rows != 2 || got.Bytes != uint64(len(input)) {
		t.Fatalf("Fingerprint() = %+v", got)
	}

	empty, err := Fingerprint(strings.NewReader(""))
	if err != nil || empty.Rows != 0 || empty.Bytes != 0 {
		t.Fatalf("empty fingerprint = %+v, %v", empty, err)
	}
}

func TestFingerprintRejectsTruncatedOrInvalidRows(t *testing.T) {
	for _, input := range []string{
		`["entry",1]`,
		"not-json\n",
		"\n",
	} {
		if _, err := Fingerprint(strings.NewReader(input)); err == nil {
			t.Errorf("Fingerprint(%q) unexpectedly succeeded", input)
		}
	}
}

func TestFingerprintRejectsOverlongSingleRow(t *testing.T) {
	input := strings.Repeat(" ", MaxFingerprintRowBytes) + "\n"
	if _, err := Fingerprint(strings.NewReader(input)); err == nil || err.Error() != "FINGERPRINT_ROW_TOO_LARGE" {
		t.Fatalf("overlong row error = %v", err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestFingerprintDoesNotExposeReaderErrors(t *testing.T) {
	secret := errors.New("credential-looking-private-data")
	_, err := Fingerprint(failingReader{err: secret})
	if err == nil || strings.Contains(err.Error(), secret.Error()) {
		t.Fatalf("reader failure was not converted to a safe error: %v", err)
	}
}

func TestCurlConfigUsesOnlyFixedGETEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"/v1/me",
		"/v1/version",
		"/v1/categories?counts=true",
		"/v1/feeds",
		"/v1/entries?limit=100&status=unread",
		"/v1/entries?limit=100&status=read",
		"/v1/entries?limit=100&starred=true",
	} {
		config, err := CurlConfig(endpoint, nil)
		if err != nil {
			t.Fatalf("CurlConfig(%q): %v", endpoint, err)
		}
		text := string(config)
		if !strings.Contains(text, "url = \"http://app:8080"+endpoint+"\"") ||
			!strings.Contains(text, `write-out = "\n%{http_code}\n"`) ||
			!strings.Contains(text, `proxy = ""`) ||
			!strings.Contains(text, `noproxy = "*"`) ||
			!strings.Contains(text, "max-time = ") ||
			!strings.Contains(text, "max-filesize = 8388608") ||
			strings.Contains(text, "location") {
			t.Errorf("unexpected curl config for %s: %q", endpoint, text)
		}
	}

	for _, endpoint := range []string{
		"https://attacker.example/v1/me",
		"/v1/me\nurl = \"http://attacker.example\"",
		"/v1/entries?starred=true&limit=0",
	} {
		if _, err := CurlConfig(endpoint, nil); err == nil {
			t.Errorf("CurlConfig accepted unapproved endpoint %q", endpoint)
		}
	}
}

func TestCurlConfigAuthValidationAndSecretHandling(t *testing.T) {
	if _, err := CurlConfig("/v1/me", &Auth{}); err == nil {
		t.Fatal("empty non-nil auth must be rejected")
	}
	for _, auth := range []*Auth{
		{APIToken: "token\r\nheader: injected"},
		{APIToken: "token\u0085header"},
		{APIToken: "token", Username: "user"},
		{Username: "user"},
		{Password: "pass"},
		{Username: "user", Password: "pass\x00"},
		{Username: "user:name", Password: "pass"},
	} {
		if _, err := CurlConfig("/v1/me", auth); err == nil {
			t.Errorf("CurlConfig accepted invalid auth %#v", auth)
		}
	}

	token := "synthetic-token-123"
	config, err := CurlConfig("/v1/me", &Auth{APIToken: token})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "X-Auth-Token: "+token) {
		t.Fatal("API-token header missing from stdin config")
	}

	password := "synthetic-private-password"
	config, err = CurlConfig("/v1/me", &Auth{Username: "synthetic-user", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	encoded := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("synthetic-user:"+password))
	if !strings.Contains(string(config), encoded) || strings.Contains(string(config), password) {
		t.Fatal("basic auth must be encoded in config, without plaintext password")
	}

	serialized, err := json.Marshal(Auth{APIToken: token, Username: "user", Password: password})
	if err != nil || strings.Contains(string(serialized), token) || strings.Contains(string(serialized), password) {
		t.Fatalf("Auth serialized secret values: %s (%v)", serialized, err)
	}
}

func TestParseResponseSeparatesStatusAndReturnsJSONBody(t *testing.T) {
	status, body, err := ParseResponse([]byte("{\"version\":\"2.3.3\"}\n\n200\n"))
	if err != nil || status != 200 || string(body) != "{\"version\":\"2.3.3\"}\n" {
		t.Fatalf("ParseResponse() = %d %q %v", status, body, err)
	}

	status, body, err = ParseResponse([]byte("{\"error\":\"unauthorized\"}\n401\n"))
	if err != nil || status != 401 || !json.Valid(body) {
		t.Fatalf("unauthenticated response = %d %q %v", status, body, err)
	}
}

func TestParseResponseRejectsMalformedAndOversizedOutputSafely(t *testing.T) {
	secret := []byte("private response body")
	for _, output := range [][]byte{
		append(append([]byte(nil), secret...), []byte("\nnot-status\n")...),
		[]byte("not-json\n\n200\n"),
		[]byte("{}\n200"),
		bytes.Repeat([]byte("x"), MaxAPIResponseBytes+16),
	} {
		_, _, err := ParseResponse(output)
		if err == nil || strings.Contains(err.Error(), string(secret)) || strings.Contains(err.Error(), "not-json") {
			t.Errorf("ParseResponse(%d bytes) did not fail safely: %v", len(output), err)
		}
	}
}

func TestFingerprintPreservesCallerReadCancellation(t *testing.T) {
	ctxErr := errors.New("caller cancelled")
	reader := &cancelAfterBytes{prefix: []byte("[\"entry\",1]\n"), err: ctxErr}
	_, err := Fingerprint(reader)
	if err == nil || strings.Contains(err.Error(), ctxErr.Error()) {
		t.Fatalf("cancellation should stop work with a safe error: %v", err)
	}
}

type cancelAfterBytes struct {
	prefix []byte
	err    error
}

func (r *cancelAfterBytes) Read(p []byte) (int, error) {
	if len(r.prefix) == 0 {
		return 0, r.err
	}
	n := copy(p, r.prefix)
	r.prefix = r.prefix[n:]
	return n, nil
}

var _ io.Reader = (*cancelAfterBytes)(nil)
