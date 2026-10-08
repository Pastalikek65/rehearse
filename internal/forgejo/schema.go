package forgejo

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
)

var migrationIDPattern = regexp.MustCompile(`^v[0-9]+[a-z]_[a-z0-9_-]+$`)

// ParseSchemaProjection validates the bounded newline-delimited result of
// SchemaSQL. Its migration IDs must already be in the SQL's bytewise order.
func ParseSchemaProjection(r io.Reader) (SchemaSnapshot, error) {
	if r == nil {
		return SchemaSnapshot{}, ErrSchemaInvalid
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(MaxSchemaBytes)+1))
	if err != nil || uint64(len(data)) > MaxSchemaBytes || len(data) == 0 || data[len(data)-1] != '\n' {
		return SchemaSnapshot{}, ErrSchemaInvalid
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) < 2 || len(lines) > 2+len(targetMigrationIDs) {
		return SchemaSnapshot{}, ErrSchemaInvalid
	}
	var snapshot SchemaSnapshot
	var seenVersion, seenForgejoVersion bool
	var lastID string
	for _, line := range lines {
		if len(line) == 0 || len(line) > MaxSchemaLineBytes || !json.Valid(line) {
			return SchemaSnapshot{}, ErrSchemaInvalid
		}
		var fields []json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil || len(fields) == 0 {
			return SchemaSnapshot{}, ErrSchemaInvalid
		}
		var kind string
		if json.Unmarshal(fields[0], &kind) != nil {
			return SchemaSnapshot{}, ErrSchemaInvalid
		}
		switch kind {
		case "version", "forgejo_version":
			if len(fields) != 3 || len(snapshot.MigrationIDs) != 0 {
				return SchemaSnapshot{}, ErrSchemaInvalid
			}
			var id, version int64
			if json.Unmarshal(fields[1], &id) != nil || id != 1 ||
				json.Unmarshal(fields[2], &version) != nil || version <= 0 {
				return SchemaSnapshot{}, ErrSchemaInvalid
			}
			if kind == "version" {
				if seenVersion || seenForgejoVersion {
					return SchemaSnapshot{}, ErrSchemaInvalid
				}
				seenVersion = true
				snapshot.GiteaVersion = version
			} else {
				if !seenVersion || seenForgejoVersion {
					return SchemaSnapshot{}, ErrSchemaInvalid
				}
				seenForgejoVersion = true
				snapshot.ForgejoVersion = version
			}
		case "forgejo_migration":
			if !seenVersion || !seenForgejoVersion || len(fields) != 2 {
				return SchemaSnapshot{}, ErrSchemaInvalid
			}
			var id string
			if json.Unmarshal(fields[1], &id) != nil || !migrationIDPattern.MatchString(id) || id <= lastID {
				return SchemaSnapshot{}, ErrSchemaInvalid
			}
			lastID = id
			snapshot.MigrationIDs = append(snapshot.MigrationIDs, id)
		default:
			return SchemaSnapshot{}, ErrSchemaInvalid
		}
	}
	if !seenVersion || !seenForgejoVersion || len(snapshot.MigrationIDs) == 0 {
		return SchemaSnapshot{}, ErrSchemaInvalid
	}
	return snapshot, nil
}
