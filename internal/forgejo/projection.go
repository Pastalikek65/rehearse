package forgejo

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const (
	MaxDatabaseProjectionBytes     uint64 = 1 << 30
	MaxDatabaseProjectionRows      uint64 = 1_000_000
	MaxDatabaseProjectionLineBytes        = 4 << 20
	MaxDatabaseCountsBytes         int64  = 16 << 10
)

var (
	ErrDatabaseProjectionInvalid  = errors.New("DATABASE_PROJECTION_INVALID")
	ErrDatabaseProjectionTooLarge = errors.New("DATABASE_PROJECTION_TOO_LARGE")
	ErrDatabaseProjectionCanceled = errors.New("DATABASE_PROJECTION_CANCELED")
)

// DatabaseProjection is the bounded digest and row counts from ProjectionSQL.
// Bytes counts JSON payload bytes and excludes the newline separators; SHA256
// covers each exact validated JSON row followed by its newline.
type DatabaseProjection struct {
	SHA256       string `json:"sha256"`
	Rows         uint64 `json:"rows"`
	Bytes        uint64 `json:"bytes"`
	Users        uint64 `json:"users"`
	Repositories uint64 `json:"repositories"`
}

// DatabaseCounts contains only the fixed integer aggregates emitted by
// CountsSQL. It contains no user or repository names.
type DatabaseCounts struct {
	Users                uint64 `json:"users"`
	Repositories         uint64 `json:"repositories"`
	PrivateRepositories  uint64 `json:"private_repositories"`
	EmptyRepositories    uint64 `json:"empty_repositories"`
	ArchivedRepositories uint64 `json:"archived_repositories"`
	Mirrors              uint64 `json:"mirrors"`
	Forks                uint64 `json:"forks"`
}

// MatchesCounts checks the row projection's global user and repository counts
// against the independent aggregate query.
func (p DatabaseProjection) MatchesCounts(counts DatabaseCounts) bool {
	return p.Users == counts.Users && p.Repositories == counts.Repositories
}

type databaseProjectionLimits struct {
	maxBytes uint64
	maxRows  uint64
	maxLine  int
}

func defaultDatabaseProjectionLimits() databaseProjectionLimits {
	return databaseProjectionLimits{
		maxBytes: MaxDatabaseProjectionBytes,
		maxRows:  MaxDatabaseProjectionRows,
		maxLine:  MaxDatabaseProjectionLineBytes,
	}
}

// ProjectDatabaseProjection validates and hashes the newline-delimited JSON
// rows emitted by ProjectionSQL. It retains only the ordered user IDs and
// repository owner IDs needed to ensure no repository row refers to a missing
// user; projected record bodies are streamed directly into SHA-256. With the
// one-million-row cap, the retained int64 values are limited to at most
// sixteen MiB, plus slice capacity overhead and one four-MiB input row.
func ProjectDatabaseProjection(ctx context.Context, r io.Reader) (DatabaseProjection, error) {
	return projectDatabaseProjectionWithLimits(ctx, r, defaultDatabaseProjectionLimits())
}

func projectDatabaseProjectionWithLimits(ctx context.Context, r io.Reader, limits databaseProjectionLimits) (DatabaseProjection, error) {
	if ctx == nil || ctx.Err() != nil {
		return DatabaseProjection{}, ErrDatabaseProjectionCanceled
	}
	if r == nil || limits.maxBytes == 0 || limits.maxRows == 0 || limits.maxLine <= 0 {
		return DatabaseProjection{}, ErrDatabaseProjectionInvalid
	}
	reader := bufio.NewReaderSize(r, 32<<10)
	hash := sha256.New()
	line := make([]byte, 0, 32<<10)
	var projection DatabaseProjection
	var consumed uint64
	var lastID [2]int64
	var lastKind = -1
	userIDs := make([]int64, 0)
	ownerIDs := make([]int64, 0)
	for {
		if ctx.Err() != nil {
			return DatabaseProjection{}, ErrDatabaseProjectionCanceled
		}
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if consumed > limits.maxBytes || uint64(len(fragment)) > limits.maxBytes-consumed {
				return DatabaseProjection{}, ErrDatabaseProjectionTooLarge
			}
			consumed += uint64(len(fragment))
			if len(fragment) > limits.maxLine+1-len(line) {
				return DatabaseProjection{}, ErrDatabaseProjectionTooLarge
			}
			line = append(line, fragment...)
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if readErr == nil {
			if len(line) < 2 || line[len(line)-1] != '\n' || bytes.IndexByte(line[:len(line)-1], '\r') >= 0 {
				return DatabaseProjection{}, ErrDatabaseProjectionInvalid
			}
			row := line[:len(line)-1]
			if projection.Rows >= limits.maxRows {
				return DatabaseProjection{}, ErrDatabaseProjectionTooLarge
			}
			kind, id, ownerID, err := parseDatabaseProjectionRow(row)
			if err != nil || kind < lastKind || kind == lastKind && id <= lastID[kind] {
				return DatabaseProjection{}, ErrDatabaseProjectionInvalid
			}
			lastKind, lastID[kind] = kind, id
			if kind == 0 {
				projection.Repositories++
				ownerIDs = append(ownerIDs, ownerID)
			} else {
				projection.Users++
				userIDs = append(userIDs, id)
			}
			_, _ = hash.Write(row)
			_, _ = hash.Write([]byte{'\n'})
			projection.Rows++
			projection.Bytes += uint64(len(row))
			line = line[:0]
			continue
		}
		if errors.Is(readErr, io.EOF) {
			if len(line) != 0 {
				return DatabaseProjection{}, ErrDatabaseProjectionInvalid
			}
			break
		}
		if ctx.Err() != nil {
			return DatabaseProjection{}, ErrDatabaseProjectionCanceled
		}
		return DatabaseProjection{}, ErrDatabaseProjectionInvalid
	}
	if !repositoryOwnersExist(ownerIDs, userIDs) {
		return DatabaseProjection{}, ErrDatabaseProjectionInvalid
	}
	if ctx.Err() != nil {
		return DatabaseProjection{}, ErrDatabaseProjectionCanceled
	}
	projection.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return projection, nil
}

func parseDatabaseProjectionRow(line []byte) (kind int, id, ownerID int64, err error) {
	if len(line) == 0 || len(line) > MaxDatabaseProjectionLineBytes || !utf8.Valid(line) || !json.Valid(line) {
		return 0, 0, 0, ErrDatabaseProjectionInvalid
	}
	var fields []json.RawMessage
	if json.Unmarshal(line, &fields) != nil || len(fields) == 0 {
		return 0, 0, 0, ErrDatabaseProjectionInvalid
	}
	var name string
	if decodeProjectionString(fields[0], 32, false, &name) != nil {
		return 0, 0, 0, ErrDatabaseProjectionInvalid
	}
	switch name {
	case "repository":
		if len(fields) != 15 || decodeProjectionInt(fields[1], true, &id) != nil ||
			decodeProjectionInt(fields[2], true, &ownerID) != nil ||
			decodeProjectionString(fields[3], 255, false, nil) != nil ||
			decodeProjectionString(fields[4], 255, false, nil) != nil ||
			decodeProjectionString(fields[5], 255, true, nil) != nil ||
			decodeProjectionBool(fields[6]) != nil || decodeProjectionBool(fields[7]) != nil ||
			decodeProjectionBool(fields[8]) != nil || decodeProjectionBool(fields[9]) != nil ||
			decodeProjectionBool(fields[10]) != nil || decodeProjectionNullableID(fields[11]) != nil ||
			decodeProjectionBool(fields[12]) != nil || decodeProjectionNullableID(fields[13]) != nil ||
			decodeProjectionString(fields[14], 32, false, nil) != nil {
			return 0, 0, 0, ErrDatabaseProjectionInvalid
		}
		return 0, id, ownerID, nil
	case "user":
		if len(fields) != 6 || decodeProjectionInt(fields[1], true, &id) != nil ||
			decodeProjectionString(fields[2], 255, false, nil) != nil ||
			decodeProjectionString(fields[3], 255, true, nil) != nil ||
			decodeProjectionBool(fields[4]) != nil || decodeProjectionBool(fields[5]) != nil {
			return 0, 0, 0, ErrDatabaseProjectionInvalid
		}
		return 1, id, 0, nil
	default:
		return 0, 0, 0, ErrDatabaseProjectionInvalid
	}
}

func decodeProjectionString(raw []byte, max int, allowEmpty bool, target *string) error {
	raw = bytes.TrimSpace(raw)
	var value string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil ||
		len(value) > max || !utf8.ValidString(value) || !allowEmpty && value == "" {
		return ErrDatabaseProjectionInvalid
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return ErrDatabaseProjectionInvalid
		}
	}
	if target != nil {
		*target = value
	}
	return nil
}

func decodeProjectionInt(raw []byte, positive bool, target *int64) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' {
		return ErrDatabaseProjectionInvalid
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || positive && value <= 0 || !positive && value < 0 {
		return ErrDatabaseProjectionInvalid
	}
	if target != nil {
		*target = value
	}
	return nil
}

func decodeProjectionNullableID(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	return decodeProjectionInt(raw, false, nil)
}

func decodeProjectionBool(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false")) {
		return nil
	}
	return ErrDatabaseProjectionInvalid
}

func repositoryOwnersExist(owners, users []int64) bool {
	if len(owners) == 0 {
		return true
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i] < owners[j] })
	for _, ownerID := range owners {
		index := sort.Search(len(users), func(i int) bool { return users[i] >= ownerID })
		if index == len(users) || users[index] != ownerID {
			return false
		}
	}
	return true
}

// ParseDatabaseCounts parses the one JSON object emitted by CountsSQL. It
// requires exactly the seven known, nonnegative integer count fields and
// rejects duplicate keys, even though ordinary map unmarshalling would hide
// them.
func ParseDatabaseCounts(r io.Reader) (DatabaseCounts, error) {
	if r == nil {
		return DatabaseCounts{}, ErrDatabaseProjectionInvalid
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxDatabaseCountsBytes+1))
	if err != nil {
		return DatabaseCounts{}, ErrDatabaseProjectionInvalid
	}
	if int64(len(data)) > MaxDatabaseCountsBytes {
		return DatabaseCounts{}, ErrDatabaseProjectionTooLarge
	}
	if len(data) == 0 || !utf8.Valid(data) || !json.Valid(data) {
		return DatabaseCounts{}, ErrDatabaseProjectionInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return DatabaseCounts{}, ErrDatabaseProjectionInvalid
	}
	values := make(map[string]uint64, 7)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return DatabaseCounts{}, ErrDatabaseProjectionInvalid
		}
		if _, exists := values[key]; exists {
			return DatabaseCounts{}, ErrDatabaseProjectionInvalid
		}
		switch key {
		case "users", "repositories", "private_repositories", "empty_repositories", "archived_repositories", "mirrors", "forks":
		default:
			return DatabaseCounts{}, ErrDatabaseProjectionInvalid
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return DatabaseCounts{}, ErrDatabaseProjectionInvalid
		}
		count, err := parseUnsignedCount(raw)
		if err != nil {
			return DatabaseCounts{}, err
		}
		values[key] = count
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || len(values) != 7 {
		return DatabaseCounts{}, ErrDatabaseProjectionInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return DatabaseCounts{}, ErrDatabaseProjectionInvalid
	}
	return DatabaseCounts{
		Users: values["users"], Repositories: values["repositories"],
		PrivateRepositories: values["private_repositories"], EmptyRepositories: values["empty_repositories"],
		ArchivedRepositories: values["archived_repositories"], Mirrors: values["mirrors"], Forks: values["forks"],
	}, nil
}

func parseUnsignedCount(raw []byte) (uint64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' {
		return 0, ErrDatabaseProjectionInvalid
	}
	value, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return 0, ErrDatabaseProjectionInvalid
	}
	return value, nil
}
