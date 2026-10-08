package spec

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
)

const (
	// MaxConfigBytes is the maximum encoded size of a Rehearse configuration.
	MaxConfigBytes = 64 * 1024
	maxJSONDepth   = 32

	approvedAdapter         = "miniflux"
	approvedSourceVersion   = "2.2.19"
	approvedTargetVersion   = "2.3.3"
	approvedPostgresVersion = "17.11"
)

// Config is the complete user-controlled Rehearse v1 configuration. Runtime
// image selection, Compose services, and commands are intentionally not part
// of this schema.
type Config struct {
	SchemaVersion   int         `json:"schemaVersion"`
	Adapter         string      `json:"adapter"`
	SourceVersion   string      `json:"sourceVersion"`
	TargetVersion   string      `json:"targetVersion"`
	PostgresVersion string      `json:"postgresVersion"`
	BackupPath      string      `json:"backupPath"`
	AuthEnvRefs     AuthEnvRefs `json:"authEnvRefs"`
}

// AuthEnvRefs contains names of environment variables. Values are resolved by
// the runner at execution time and are never part of the serialized config.
type AuthEnvRefs struct {
	APIToken string `json:"apiToken,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type configError string

func (e configError) Error() string { return string(e) }

const (
	errConfigReadFailed   configError = "CONFIG_READ_FAILED"
	errConfigTooLarge     configError = "CONFIG_TOO_LARGE"
	errInvalidJSON        configError = "INVALID_JSON"
	errTrailingData       configError = "TRAILING_DATA"
	errDuplicateField     configError = "DUPLICATE_FIELD"
	errUnknownField       configError = "UNKNOWN_FIELD"
	errNullNotAllowed     configError = "NULL_NOT_ALLOWED"
	errInvalidFieldType   configError = "INVALID_FIELD_TYPE"
	errJSONTooDeep        configError = "JSON_TOO_DEEP"
	errSchemaVersion      configError = "SCHEMA_VERSION_UNSUPPORTED"
	errAdapter            configError = "ADAPTER_UNSUPPORTED"
	errVersionInvalid     configError = "VERSION_INVALID"
	errVersionPair        configError = "VERSION_PAIR_UNSUPPORTED"
	errPostgresVersion    configError = "POSTGRES_VERSION_UNSUPPORTED"
	errBackupPathRequired configError = "BACKUP_PATH_REQUIRED"
	errAuthEnvRefsInvalid configError = "AUTH_ENV_REFS_INVALID"
)

var configFields = map[string]struct{}{
	"schemaVersion":   {},
	"adapter":         {},
	"sourceVersion":   {},
	"targetVersion":   {},
	"postgresVersion": {},
	"backupPath":      {},
	"authEnvRefs":     {},
}

var authEnvRefFields = map[string]struct{}{
	"apiToken": {},
	"username": {},
	"password": {},
}

// Parse reads and validates one Rehearse v1 JSON configuration. It returns
// only fixed error codes so malformed input and credentials are not echoed.
func Parse(r io.Reader) (Config, error) {
	if r == nil {
		return Config{}, errConfigReadFailed
	}

	raw, err := io.ReadAll(io.LimitReader(r, MaxConfigBytes+1))
	if err != nil {
		return Config{}, errConfigReadFailed
	}
	if len(raw) > MaxConfigBytes {
		return Config{}, errConfigTooLarge
	}
	if !utf8.Valid(raw) {
		return Config{}, errInvalidJSON
	}
	if err := inspectJSON(raw); err != nil {
		return Config{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Config{}, errInvalidJSON
	}
	for field := range fields {
		if _, ok := configFields[field]; !ok {
			return Config{}, errUnknownField
		}
	}

	var cfg Config
	if err := decodeField(fields, "schemaVersion", &cfg.SchemaVersion); err != nil {
		return Config{}, err
	}
	if err := decodeField(fields, "adapter", &cfg.Adapter); err != nil {
		return Config{}, err
	}
	if err := decodeField(fields, "sourceVersion", &cfg.SourceVersion); err != nil {
		return Config{}, err
	}
	if err := decodeField(fields, "targetVersion", &cfg.TargetVersion); err != nil {
		return Config{}, err
	}
	if err := decodeField(fields, "postgresVersion", &cfg.PostgresVersion); err != nil {
		return Config{}, err
	}
	if err := decodeField(fields, "backupPath", &cfg.BackupPath); err != nil {
		return Config{}, err
	}
	if err := decodeAuthEnvRefs(fields, &cfg.AuthEnvRefs); err != nil {
		return Config{}, err
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks semantic constraints that do not require filesystem or
// environment access. The runner must separately verify that BackupPath names
// a readable regular file and resolve the selected environment variables.
func Validate(cfg Config) error {
	if cfg.SchemaVersion != 1 {
		return errSchemaVersion
	}
	if cfg.Adapter != approvedAdapter && cfg.Adapter != "forgejo" {
		return errAdapter
	}
	if !canonicalStableSemver(cfg.SourceVersion) || !canonicalStableSemver(cfg.TargetVersion) {
		return errVersionInvalid
	}
	source, target := approvedSourceVersion, approvedTargetVersion
	if cfg.Adapter == "forgejo" {
		source, target = forgejo.SourceVersion, forgejo.TargetVersion
	}
	if cfg.SourceVersion != source || cfg.TargetVersion != target {
		return errVersionPair
	}
	if cfg.PostgresVersion != approvedPostgresVersion {
		return errPostgresVersion
	}
	if strings.TrimSpace(cfg.BackupPath) == "" {
		return errBackupPathRequired
	}
	if !validAuthEnvRefs(cfg.AuthEnvRefs) {
		return errAuthEnvRefsInvalid
	}
	if cfg.Adapter == "forgejo" && cfg.AuthEnvRefs.APIToken == "" {
		return errAuthEnvRefsInvalid
	}
	return nil
}

func decodeField[T any](fields map[string]json.RawMessage, name string, dst *T) error {
	raw, ok := fields[name]
	if !ok {
		// Missing required values are reported by semantic validation.
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return errInvalidFieldType
	}
	return nil
}

func decodeAuthEnvRefs(fields map[string]json.RawMessage, dst *AuthEnvRefs) error {
	raw, ok := fields["authEnvRefs"]
	if !ok {
		return nil
	}
	var refs map[string]json.RawMessage
	if err := json.Unmarshal(raw, &refs); err != nil || refs == nil {
		return errInvalidFieldType
	}
	for field := range refs {
		if _, ok := authEnvRefFields[field]; !ok {
			return errUnknownField
		}
	}
	_, hasAPIToken := refs["apiToken"]
	_, hasUsername := refs["username"]
	_, hasPassword := refs["password"]
	if hasAPIToken {
		if len(refs) != 1 {
			return errAuthEnvRefsInvalid
		}
	} else if len(refs) != 2 || !hasUsername || !hasPassword {
		return errAuthEnvRefsInvalid
	}
	if err := decodeField(refs, "apiToken", &dst.APIToken); err != nil {
		return err
	}
	if err := decodeField(refs, "username", &dst.Username); err != nil {
		return err
	}
	if err := decodeField(refs, "password", &dst.Password); err != nil {
		return err
	}
	return nil
}

func canonicalStableSemver(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func validAuthEnvRefs(refs AuthEnvRefs) bool {
	if refs.APIToken != "" {
		return refs.Username == "" && refs.Password == "" && validEnvName(refs.APIToken)
	}
	return validEnvName(refs.Username) && validEnvName(refs.Password)
}

func validEnvName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i, r := range name {
		if i == 0 {
			if !((r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
			continue
		}
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func inspectJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return errInvalidJSON
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return errInvalidJSON
	}
	if err := scanObject(dec, 1); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errTrailingData
	}
	return nil
}

func scanValue(dec *json.Decoder, depth int) error {
	token, err := dec.Token()
	if err != nil {
		return errInvalidJSON
	}
	if token == nil {
		return errNullNotAllowed
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if depth > maxJSONDepth {
		return errJSONTooDeep
	}
	switch delim {
	case '{':
		return scanObject(dec, depth)
	case '[':
		return scanArray(dec, depth)
	default:
		return errInvalidJSON
	}
}

func scanObject(dec *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errJSONTooDeep
	}
	seen := make(map[string]struct{})
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return errInvalidJSON
		}
		key, ok := token.(string)
		if !ok {
			return errInvalidJSON
		}
		if _, ok := seen[key]; ok {
			return errDuplicateField
		}
		seen[key] = struct{}{}
		if err := scanValue(dec, depth+1); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return errInvalidJSON
	}
	if end != json.Delim('}') {
		return errInvalidJSON
	}
	return nil
}

func scanArray(dec *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errJSONTooDeep
	}
	for dec.More() {
		if err := scanValue(dec, depth+1); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return errInvalidJSON
	}
	if end != json.Delim(']') {
		return errInvalidJSON
	}
	return nil
}
