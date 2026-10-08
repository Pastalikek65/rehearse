package spec

import (
	"strings"
	"testing"
)

const forgejoConfigJSON = `{"schemaVersion":1,"adapter":"forgejo","sourceVersion":"15.0.9","targetVersion":"16.0.5","postgresVersion":"17.11","backupPath":"./backup.rehearse.zip","authEnvRefs":{"apiToken":"FORGEJO_API_TOKEN"}}`

func TestParseForgejoFixedContract(t *testing.T) {
	cfg, err := Parse(strings.NewReader(forgejoConfigJSON))
	if err != nil || cfg.Adapter != "forgejo" {
		t.Fatalf("fixed Forgejo contract rejected: %v", err)
	}
	for _, mutation := range []struct{ from, to, code string }{
		{`"15.0.9"`, `"15.0.8"`, "VERSION_PAIR_UNSUPPORTED"},
		{`"16.0.5"`, `"17.0.0"`, "VERSION_PAIR_UNSUPPORTED"},
		{`"17.11"`, `"18.0"`, "POSTGRES_VERSION_UNSUPPORTED"},
		{`{"apiToken":"FORGEJO_API_TOKEN"}`, `{"username":"FORGEJO_USER","password":"FORGEJO_PASSWORD"}`, "AUTH_ENV_REFS_INVALID"},
		{`"forgejo"`, `"custom"`, "ADAPTER_UNSUPPORTED"},
	} {
		assertParseError(t, strings.Replace(forgejoConfigJSON, mutation.from, mutation.to, 1), mutation.code)
	}
}
