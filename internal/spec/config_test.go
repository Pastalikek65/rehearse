package spec

import (
	"strings"
	"testing"
)

const approvedConfigJSON = `{"schemaVersion":1,"adapter":"miniflux","sourceVersion":"2.2.19","targetVersion":"2.3.3","postgresVersion":"17.11","backupPath":"./fixtures/miniflux.sql","authEnvRefs":{"apiToken":"MINIFLUX_API_TOKEN"}}`

func TestParseAcceptsApprovedMinifluxConfig(t *testing.T) {
	got, err := Parse(strings.NewReader(approvedConfigJSON))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.SchemaVersion != 1 || got.Adapter != "miniflux" || got.SourceVersion != "2.2.19" || got.TargetVersion != "2.3.3" || got.PostgresVersion != "17.11" || got.BackupPath != "./fixtures/miniflux.sql" || got.AuthEnvRefs.APIToken != "MINIFLUX_API_TOKEN" {
		t.Fatalf("Parse() returned unexpected config: %#v", got)
	}
}

func TestParseAcceptsUsernameAndPasswordEnvironmentReferences(t *testing.T) {
	input := strings.Replace(approvedConfigJSON, `{"apiToken":"MINIFLUX_API_TOKEN"}`, `{"username":"MINIFLUX_USER","password":"MINIFLUX_PASSWORD"}`, 1)
	got, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.AuthEnvRefs.Username != "MINIFLUX_USER" || got.AuthEnvRefs.Password != "MINIFLUX_PASSWORD" || got.AuthEnvRefs.APIToken != "" {
		t.Fatalf("Parse() returned unexpected auth refs: %#v", got.AuthEnvRefs)
	}
}

func TestParseRejectsUnknownFieldsAtEveryLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "top-level command injection field",
			input: strings.Replace(approvedConfigJSON, `"backupPath"`, `"command":"echo unsafe","backupPath"`, 1),
		},
		{
			name:  "custom image field",
			input: strings.Replace(approvedConfigJSON, `"backupPath"`, `"image":"custom:latest","backupPath"`, 1),
		},
		{
			name:  "raw compose field",
			input: strings.Replace(approvedConfigJSON, `"backupPath"`, `"rawCompose":"services: {}","backupPath"`, 1),
		},
		{
			name:  "mounts field",
			input: strings.Replace(approvedConfigJSON, `"backupPath"`, `"mounts":[],"backupPath"`, 1),
		},
		{
			name:  "nested authentication field",
			input: strings.Replace(approvedConfigJSON, `{"apiToken":"MINIFLUX_API_TOKEN"}`, `{"apiToken":"MINIFLUX_API_TOKEN","extra":"inline"}`, 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseError(t, tt.input, "UNKNOWN_FIELD")
		})
	}
}

func TestParseRejectsDuplicateFieldsAtEveryLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "top-level",
			input: strings.Replace(approvedConfigJSON, `"adapter":"miniflux"`, `"adapter":"miniflux","adapter":"miniflux"`, 1),
		},
		{
			name:  "nested authentication",
			input: strings.Replace(approvedConfigJSON, `{"apiToken":"MINIFLUX_API_TOKEN"}`, `{"apiToken":"MINIFLUX_API_TOKEN","apiToken":"OTHER_TOKEN"}`, 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseError(t, tt.input, "DUPLICATE_FIELD")
		})
	}
}

func TestParseRejectsNullAndWrongTypes(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		code string
	}{
		{name: "null schema version", from: `"schemaVersion":1`, to: `"schemaVersion":null`, code: "NULL_NOT_ALLOWED"},
		{name: "null adapter", from: `"adapter":"miniflux"`, to: `"adapter":null`, code: "NULL_NOT_ALLOWED"},
		{name: "null source version", from: `"sourceVersion":"2.2.19"`, to: `"sourceVersion":null`, code: "NULL_NOT_ALLOWED"},
		{name: "null target version", from: `"targetVersion":"2.3.3"`, to: `"targetVersion":null`, code: "NULL_NOT_ALLOWED"},
		{name: "null postgres version", from: `"postgresVersion":"17.11"`, to: `"postgresVersion":null`, code: "NULL_NOT_ALLOWED"},
		{name: "null backup path", from: `"backupPath":"./fixtures/miniflux.sql"`, to: `"backupPath":null`, code: "NULL_NOT_ALLOWED"},
		{name: "null auth refs", from: `"authEnvRefs":{"apiToken":"MINIFLUX_API_TOKEN"}`, to: `"authEnvRefs":null`, code: "NULL_NOT_ALLOWED"},
		{name: "null token ref", from: `"apiToken":"MINIFLUX_API_TOKEN"`, to: `"apiToken":null`, code: "NULL_NOT_ALLOWED"},
		{name: "string schema version", from: `"schemaVersion":1`, to: `"schemaVersion":"1"`, code: "INVALID_FIELD_TYPE"},
		{name: "number adapter", from: `"adapter":"miniflux"`, to: `"adapter":7`, code: "INVALID_FIELD_TYPE"},
		{name: "array auth refs", from: `"authEnvRefs":{"apiToken":"MINIFLUX_API_TOKEN"}`, to: `"authEnvRefs":[]`, code: "INVALID_FIELD_TYPE"},
		{name: "number token ref", from: `"apiToken":"MINIFLUX_API_TOKEN"`, to: `"apiToken":7`, code: "INVALID_FIELD_TYPE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Replace(approvedConfigJSON, tt.from, tt.to, 1)
			assertParseError(t, input, tt.code)
		})
	}
}

func TestParseRejectsInvalidJSONAndTrailingValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		code  string
	}{
		{name: "malformed", input: `{`, code: "INVALID_JSON"},
		{name: "non-object root", input: `[]`, code: "INVALID_JSON"},
		{name: "trailing object", input: approvedConfigJSON + `{}`, code: "TRAILING_DATA"},
		{name: "trailing garbage", input: approvedConfigJSON + `xyz`, code: "TRAILING_DATA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseError(t, tt.input, tt.code)
		})
	}
}

func TestParseRejectsInputOverMaximumSize(t *testing.T) {
	if MaxConfigBytes != 64*1024 {
		t.Fatalf("MaxConfigBytes = %d, want %d", MaxConfigBytes, 64*1024)
	}
	input := approvedConfigJSON + strings.Repeat(" ", MaxConfigBytes-len(approvedConfigJSON)+1)
	assertParseError(t, input, "CONFIG_TOO_LARGE")
}

func TestParseRejectsUnqualifiedVersionPairs(t *testing.T) {
	input := strings.Replace(approvedConfigJSON, `"targetVersion":"2.3.3"`, `"targetVersion":"2.3.0"`, 1)
	assertParseError(t, input, "VERSION_PAIR_UNSUPPORTED")
}

func TestParseRejectsNoncanonicalSemver(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
	}{
		{name: "v prefix", from: `"sourceVersion":"2.2.19"`, to: `"sourceVersion":"v2.2.19"`},
		{name: "leading zero", from: `"sourceVersion":"2.2.19"`, to: `"sourceVersion":"02.2.19"`},
		{name: "prerelease", from: `"targetVersion":"2.3.3"`, to: `"targetVersion":"2.3.3-rc.1"`},
		{name: "missing patch", from: `"targetVersion":"2.3.3"`, to: `"targetVersion":"2.3"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Replace(approvedConfigJSON, tt.from, tt.to, 1)
			assertParseError(t, input, "VERSION_INVALID")
		})
	}
}

func TestParseRejectsMissingVersionFields(t *testing.T) {
	tests := []struct {
		name string
		from string
	}{
		{name: "source version", from: `"sourceVersion":"2.2.19",`},
		{name: "target version", from: `"targetVersion":"2.3.3",`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Replace(approvedConfigJSON, tt.from, "", 1)
			assertParseError(t, input, "VERSION_INVALID")
		})
	}
}

func TestParseRejectsUnsupportedSchemaAdapterAndPostgresVersion(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		code string
	}{
		{name: "schema version", from: `"schemaVersion":1`, to: `"schemaVersion":2`, code: "SCHEMA_VERSION_UNSUPPORTED"},
		{name: "adapter", from: `"adapter":"miniflux"`, to: `"adapter":"forgejo"`, code: "ADAPTER_UNSUPPORTED"},
		{name: "postgres version", from: `"postgresVersion":"17.11"`, to: `"postgresVersion":"17.10"`, code: "POSTGRES_VERSION_UNSUPPORTED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseError(t, strings.Replace(approvedConfigJSON, tt.from, tt.to, 1), tt.code)
		})
	}
}

func TestParseRejectsEmptyBackupPathAndInvalidAuthReferenceSets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		code  string
	}{
		{
			name:  "blank backup path",
			input: strings.Replace(approvedConfigJSON, `"backupPath":"./fixtures/miniflux.sql"`, `"backupPath":"   "`, 1),
			code:  "BACKUP_PATH_REQUIRED",
		},
		{
			name:  "blank auth refs",
			input: strings.Replace(approvedConfigJSON, `{"apiToken":"MINIFLUX_API_TOKEN"}`, `{}`, 1),
			code:  "AUTH_ENV_REFS_INVALID",
		},
		{
			name:  "inline secret is not an env name",
			input: strings.Replace(approvedConfigJSON, "MINIFLUX_API_TOKEN", "example-secret-value", 1),
			code:  "AUTH_ENV_REFS_INVALID",
		},
		{
			name:  "partial basic auth",
			input: strings.Replace(approvedConfigJSON, `{"apiToken":"MINIFLUX_API_TOKEN"}`, `{"username":"MINIFLUX_USER"}`, 1),
			code:  "AUTH_ENV_REFS_INVALID",
		},
		{
			name:  "mixed auth modes",
			input: strings.Replace(approvedConfigJSON, `{"apiToken":"MINIFLUX_API_TOKEN"}`, `{"apiToken":"MINIFLUX_API_TOKEN","username":"MINIFLUX_USER","password":"MINIFLUX_PASSWORD"}`, 1),
			code:  "AUTH_ENV_REFS_INVALID",
		},
		{
			name:  "empty token field mixed with basic auth",
			input: strings.Replace(approvedConfigJSON, `{"apiToken":"MINIFLUX_API_TOKEN"}`, `{"apiToken":"","username":"MINIFLUX_USER","password":"MINIFLUX_PASSWORD"}`, 1),
			code:  "AUTH_ENV_REFS_INVALID",
		},
		{
			name:  "empty basic auth field mixed with token",
			input: strings.Replace(approvedConfigJSON, `{"apiToken":"MINIFLUX_API_TOKEN"}`, `{"apiToken":"MINIFLUX_API_TOKEN","username":""}`, 1),
			code:  "AUTH_ENV_REFS_INVALID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseError(t, tt.input, tt.code)
		})
	}
}

func TestValidationErrorsDoNotEchoSecretsOrInput(t *testing.T) {
	secret := "example-secret-value"
	input := strings.Replace(approvedConfigJSON, "MINIFLUX_API_TOKEN", secret, 1)
	_, err := Parse(strings.NewReader(input))
	if err == nil {
		t.Fatal("Parse() error = nil, want invalid auth env reference")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), input) {
		t.Fatalf("Parse() error leaked input: %q", err.Error())
	}
}

func TestParseRejectsExcessiveJSONNesting(t *testing.T) {
	input := `{"extra":` + strings.Repeat("[", 80) + `0` + strings.Repeat("]", 80) + `}`
	assertParseError(t, input, "JSON_TOO_DEEP")
}

func assertParseError(t *testing.T, input, want string) {
	t.Helper()
	_, err := Parse(strings.NewReader(input))
	if err == nil {
		t.Fatalf("Parse() error = nil, want %s", want)
	}
	if got := err.Error(); got != want {
		t.Fatalf("Parse() error = %q, want %q", got, want)
	}
}
