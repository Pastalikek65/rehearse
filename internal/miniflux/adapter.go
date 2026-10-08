// Package miniflux defines the reviewed Miniflux upgrade contract used by
// Rehearse. It contains no Docker lifecycle or filesystem operations.
package miniflux

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"unicode"
)

const (
	// Image references are immutable Linux/amd64 manifest digests.
	SourceImage        = "docker.io/miniflux/miniflux@sha256:8e1ab958250c86b1ce7081cd5f875e60b11fd2833fcb9177f972e45dce18fade"
	TargetImage        = "docker.io/miniflux/miniflux@sha256:4c24c30b8b420c77e3d074bfb6d78cfe5d37670f895fa68c09c43cb719b9ab04"
	BrokenFixtureImage = "docker.io/miniflux/miniflux@sha256:692c7376cbd42b066697e33201b7afa51bc6fe388b10939c628e4c58647b1670"
	PostgresImage      = "docker.io/library/postgres@sha256:3cec7eb015ba8adb28139fa5c83b8489cdf0e666e53dfdf20f598ae0cc8739e3"
	ProbeImage         = "docker.io/curlimages/curl@sha256:43366cd60f226c7655181a0f7e85c468a41d182fdd2dc2c1c3b872a2b9d05d7a"

	BaselineSchema = 125
	TargetSchema   = 132

	// MaxFingerprintBytes limits the projection stream. A row is buffered only
	// until its newline, and MaxFingerprintRowBytes bounds that per-row buffer.
	MaxFingerprintBytes    uint64 = 1 << 30
	MaxFingerprintRowBytes        = 4 << 20
	MaxAPIResponseBytes           = 8 << 20
)

// ProjectionSQL emits one deterministic JSON array per line. Fields common to
// both reviewed schemas are used; entries with status=removed are projected
// separately because migration 127 converts or deletes them.
const ProjectionSQL = `SELECT line::text
FROM (
    SELECT 'user' AS kind, id::bigint AS sort_id,
           jsonb_build_array('user', id, username, is_admin, language, timezone, theme) AS line
    FROM users
    UNION ALL
    SELECT 'category', id::bigint,
           jsonb_build_array('category', id, user_id, title, hide_globally)
    FROM categories
    UNION ALL
    SELECT 'feed', id::bigint,
           jsonb_build_array('feed', id, user_id, category_id, title, feed_url, site_url, disabled, hide_globally)
    FROM feeds
    UNION ALL
    SELECT 'entry', id::bigint,
           jsonb_build_array(
               'entry', id, user_id, feed_id, hash,
               to_char(published_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
               title, url, author, content, status, starred, tags, comments_url,
               to_char(changed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
           )
    FROM entries
    WHERE status IN ('unread', 'read')
) AS projection
ORDER BY kind, sort_id`

// CountsSQL returns one JSON object with table and entry-state counts. The
// entry_tombstones table is intentionally omitted because it does not exist
// in the source schema.
const CountsSQL = `SELECT jsonb_build_object(
    'users', (SELECT count(*) FROM users),
    'categories', (SELECT count(*) FROM categories),
    'feeds', (SELECT count(*) FROM feeds),
    'entries_unread', (SELECT count(*) FROM entries WHERE status = 'unread'),
    'entries_read', (SELECT count(*) FROM entries WHERE status = 'read'),
    'entries_removed', (SELECT count(*) FROM entries WHERE status = 'removed'),
    'entries_starred', (SELECT count(*) FROM entries WHERE status IN ('unread', 'read') AND starred IS TRUE),
    'entries_tagged', (SELECT count(*) FROM entries WHERE status IN ('unread', 'read') AND cardinality(tags) > 0)
)::text`

// RemovedKeysSQL matches the exact eligibility predicate used by migration
// 127. Orphaned feeds and empty hashes are intentionally excluded.
const RemovedKeysSQL = `SELECT jsonb_build_array(e.feed_id, e.hash)::text
FROM entries AS e
WHERE e.status = 'removed'
  AND e.hash <> ''
  AND e.feed_id IN (SELECT id FROM feeds)
ORDER BY e.feed_id, e.hash`

// TombstoneKeysSQL returns the key set written by migration 127.
const TombstoneKeysSQL = `SELECT jsonb_build_array(feed_id, hash)::text
FROM entry_tombstones
ORDER BY feed_id, hash`

// Miniflux stores its schema version as text, although the migration runner
// scans it as an integer. Casting here keeps psql output a single integer.
const SchemaSQL = `SELECT version::integer FROM schema_version`

// FingerprintResult is the bounded summary of a newline-delimited JSON stream.
// Bytes includes the newline delimiters and SHA256 hashes those exact bytes.
type FingerprintResult struct {
	SHA256 string `json:"sha256"`
	Rows   uint64 `json:"rows"`
	Bytes  uint64 `json:"bytes"`
}

// Auth carries one Miniflux API authentication mechanism. Values are excluded
// from JSON to prevent accidental report/config serialization. Callers must
// not log or format Auth values, and this type intentionally has no String
// method.
type Auth struct {
	APIToken string `json:"-"`
	Username string `json:"-"`
	Password string `json:"-"`
}

type adapterError string

func (e adapterError) Error() string { return string(e) }

const (
	errFingerprintReader    adapterError = "FINGERPRINT_READ_FAILED"
	errFingerprintTooLarge  adapterError = "FINGERPRINT_TOO_LARGE"
	errFingerprintRowLarge  adapterError = "FINGERPRINT_ROW_TOO_LARGE"
	errFingerprintTruncated adapterError = "FINGERPRINT_TRUNCATED_ROW"
	errFingerprintInvalid   adapterError = "FINGERPRINT_INVALID_ROW"
	errEndpointUnsupported  adapterError = "API_ENDPOINT_UNSUPPORTED"
	errAuthInvalid          adapterError = "API_AUTH_INVALID"
	errResponseInvalid      adapterError = "API_RESPONSE_INVALID"
	errResponseTooLarge     adapterError = "API_RESPONSE_TOO_LARGE"
)

// Fingerprint incrementally hashes complete JSON lines. It retains at most one
// row (bounded by MaxFingerprintRowBytes) plus a fixed reader buffer. If the
// caller's reader is cancellable, its read error stops processing immediately.
func Fingerprint(r io.Reader) (FingerprintResult, error) {
	if r == nil {
		return FingerprintResult{}, errFingerprintReader
	}

	hash := sha256.New()
	reader := bufio.NewReaderSize(r, 64*1024)
	var result FingerprintResult
	var row []byte

	for {
		part, readErr := reader.ReadSlice('\n')
		if len(part) > 0 {
			if uint64(len(part)) > MaxFingerprintBytes-result.Bytes-uint64(len(row)) {
				return FingerprintResult{}, errFingerprintTooLarge
			}
			if len(row)+len(part) > MaxFingerprintRowBytes {
				return FingerprintResult{}, errFingerprintRowLarge
			}
			row = append(row, part...)
			if row[len(row)-1] == '\n' {
				jsonLine := row[:len(row)-1]
				if len(jsonLine) == 0 || !json.Valid(jsonLine) {
					return FingerprintResult{}, errFingerprintInvalid
				}
				_, _ = hash.Write(row)
				result.Bytes += uint64(len(row))
				result.Rows++
				row = row[:0]
			}
		}

		if readErr != nil {
			if readErr == bufio.ErrBufferFull {
				continue
			}
			if readErr == io.EOF {
				if len(row) != 0 {
					return FingerprintResult{}, errFingerprintTruncated
				}
				result.SHA256 = hex.EncodeToString(hash.Sum(nil))
				return result, nil
			}
			return FingerprintResult{}, errFingerprintReader
		}
	}
}

var allowedEndpoints = map[string]struct{}{
	"/v1/me":                              {},
	"/v1/version":                         {},
	"/v1/categories?counts=true":          {},
	"/v1/feeds":                           {},
	"/v1/entries?limit=100&status=unread": {},
	"/v1/entries?limit=100&status=read":   {},
	"/v1/entries?limit=100&starred=true":  {},
}

// CurlConfig returns curl's stdin configuration for one fixed GET endpoint.
// The caller must invoke curl with --disable --config - and pass these bytes
// over stdin. Redirect following is left disabled (curl's default), proxy
// environment variables are explicitly bypassed, and request time is bounded.
func CurlConfig(endpoint string, auth *Auth) ([]byte, error) {
	if _, ok := allowedEndpoints[endpoint]; !ok {
		return nil, errEndpointUnsupported
	}

	lines := []string{
		"silent",
		"show-error",
		`proxy = ""`,
		`noproxy = "*"`,
		"connect-timeout = 3",
		"max-time = 15",
		"max-filesize = 8388608",
		`write-out = "\n%{http_code}\n"`,
		`url = "` + curlQuote("http://app:8080"+endpoint) + `"`,
	}
	if auth != nil {
		header, err := authHeader(auth)
		if err != nil {
			return nil, err
		}
		lines = append(lines, `header = "`+curlQuote(header)+`"`)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func authHeader(auth *Auth) (string, error) {
	if auth.APIToken != "" {
		if auth.Username != "" || auth.Password != "" || strings.TrimSpace(auth.APIToken) == "" || hasControl(auth.APIToken) {
			return "", errAuthInvalid
		}
		return "X-Auth-Token: " + auth.APIToken, nil
	}
	if auth.Username == "" || auth.Password == "" || strings.Contains(auth.Username, ":") ||
		hasControl(auth.Username) || hasControl(auth.Password) {
		return "", errAuthInvalid
	}
	credentials := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
	return "Authorization: Basic " + credentials, nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func curlQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

// ParseResponse splits curl's appended newline-delimited HTTP status code from
// stdout and validates that the body is bounded JSON. Errors are fixed codes;
// response text is returned only on success and is never included in errors.
func ParseResponse(output []byte) (status int, body []byte, err error) {
	if len(output) > MaxAPIResponseBytes+8 {
		return 0, nil, errResponseTooLarge
	}
	if len(output) < 6 || output[len(output)-1] != '\n' {
		return 0, nil, errResponseInvalid
	}
	codeEnd := len(output) - 1
	codeStart := codeEnd - 3
	if codeStart <= 0 || output[codeStart-1] != '\n' {
		return 0, nil, errResponseInvalid
	}
	for _, digit := range output[codeStart:codeEnd] {
		if digit < '0' || digit > '9' {
			return 0, nil, errResponseInvalid
		}
		status = status*10 + int(digit-'0')
	}
	if status < 100 || status > 599 {
		return 0, nil, errResponseInvalid
	}
	body = output[:codeStart-1]
	if len(body) > MaxAPIResponseBytes || !json.Valid(body) {
		return 0, nil, errResponseInvalid
	}
	return status, append([]byte(nil), body...), nil
}
