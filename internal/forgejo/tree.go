package forgejo

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Forgejo 15.0.9 and 16.0.5 cap git-tree pages at 1000 entries.
	GitTreePageSize   = 1000
	MaxGitTreeEntries = 10000
)

// APITreeEntry is one bounded row from Forgejo's recursive git-tree endpoint.
// URL is parsed for shape but must never be followed by a caller.
type APITreeEntry struct {
	Path string
	Mode string
	Type string
	Size int64
	SHA  string
	URL  string
}

// IsRegularBlob reports the regular and executable file modes. Git symlinks
// and submodules are represented as entries but are intentionally excluded
// from file-content sampling.
func (entry APITreeEntry) IsRegularBlob() bool {
	return entry.Type == "blob" && (entry.Mode == "100644" || entry.Mode == "100755")
}

// APITreePage is one page of Forgejo's recursive tree response. SHA is the
// resolved Git tree object ID, not necessarily the commit SHA in the request.
type APITreePage struct {
	SHA        string
	URL        string
	Entries    []APITreeEntry
	Truncated  bool
	Page       int
	TotalCount int
}

// TreeEndpoint builds one fixed recursive page for a resolved branch commit.
// The commit SHA pins the read to an immutable Git object during observation.
func TreeEndpoint(owner, repository, commitSHA string, page int) (string, error) {
	if !validGitObjectID(commitSHA) || page < 1 || page > MaxGitTreeEntries/GitTreePageSize {
		return "", ErrAPIEndpoint
	}
	base, err := RepositoryEndpoint(owner, repository)
	if err != nil {
		return "", err
	}
	return base + "/git/trees/" + commitSHA + "?recursive=true&page=" + strconv.Itoa(page) + "&per_page=" + strconv.Itoa(GitTreePageSize), nil
}

// ParseTreePage strictly validates one fixed-size recursive Forgejo API page.
// In both supported tags, upstream sets `truncated` when total_count exceeds
// one page, and leaves it true on every later page. Completeness must therefore
// be established by checking each expected page and its exact row count, not
// by treating truncated as a per-page continuation flag.
func ParseTreePage(data []byte, expectedPage int) (APITreePage, error) {
	if !validJSONSize(data) || expectedPage < 1 || expectedPage > MaxGitTreeEntries/GitTreePageSize {
		return APITreePage{}, ErrAPIResponse
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "sha", "url", "tree", "truncated", "page", "total_count") {
		return APITreePage{}, ErrAPIResponse
	}

	var page APITreePage
	if decodeRequiredText(fields["sha"], 64, &page.SHA) != nil || !validGitObjectID(page.SHA) ||
		decodeRequiredText(fields["url"], 4096, &page.URL) != nil ||
		decodeRequiredBool(fields["truncated"], &page.Truncated) != nil ||
		decodeRequiredInt(fields["page"], &page.Page) != nil ||
		decodeRequiredInt(fields["total_count"], &page.TotalCount) != nil {
		return APITreePage{}, ErrAPIResponse
	}
	if page.Page != expectedPage || page.TotalCount < 0 || page.TotalCount > MaxGitTreeEntries ||
		page.Truncated != (page.TotalCount > GitTreePageSize) {
		return APITreePage{}, ErrAPIResponse
	}

	var rows []json.RawMessage
	treeJSON := bytes.TrimSpace(fields["tree"])
	if len(treeJSON) == 0 || treeJSON[0] != '[' || json.Unmarshal(treeJSON, &rows) != nil {
		return APITreePage{}, ErrAPIResponse
	}
	expectedRows, ok := expectedTreeRows(page.TotalCount, expectedPage)
	if !ok || len(rows) != expectedRows {
		return APITreePage{}, ErrAPIResponse
	}
	page.Entries = make([]APITreeEntry, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, raw := range rows {
		entry, err := parseTreeEntry(raw)
		if err != nil {
			return APITreePage{}, ErrAPIResponse
		}
		if _, exists := seen[entry.Path]; exists {
			return APITreePage{}, ErrAPIResponse
		}
		seen[entry.Path] = struct{}{}
		page.Entries = append(page.Entries, entry)
	}
	return page, nil
}

func expectedTreeRows(total, page int) (int, bool) {
	if total == 0 {
		return 0, page == 1
	}
	pageCount := (total + GitTreePageSize - 1) / GitTreePageSize
	if page < 1 || page > pageCount {
		return 0, false
	}
	remaining := total - (page-1)*GitTreePageSize
	if remaining > GitTreePageSize {
		remaining = GitTreePageSize
	}
	return remaining, true
}

func parseTreeEntry(data []byte) (APITreeEntry, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || !exactFields(fields, "path", "mode", "type", "size", "sha", "url") {
		return APITreeEntry{}, ErrAPIResponse
	}
	var entry APITreeEntry
	if decodeRequiredText(fields["path"], MaxDataPathBytes, &entry.Path) != nil ||
		decodeRequiredText(fields["mode"], 6, &entry.Mode) != nil ||
		decodeRequiredText(fields["type"], 16, &entry.Type) != nil ||
		decodeRequiredInt64(fields["size"], &entry.Size) != nil ||
		decodeRequiredTextAllowEmpty(fields["url"], 4096, &entry.URL) != nil ||
		decodeRequiredText(fields["sha"], 64, &entry.SHA) != nil ||
		!validGitObjectID(entry.SHA) || !validTreePath(entry.Path) || entry.Size < 0 || !validTreeModeType(entry.Mode, entry.Type) {
		return APITreeEntry{}, ErrAPIResponse
	}
	if entry.Mode == "160000" {
		if entry.URL != "" {
			return APITreeEntry{}, ErrAPIResponse
		}
	} else if !validText(entry.URL, 4096) {
		return APITreeEntry{}, ErrAPIResponse
	}
	return entry, nil
}

func validTreeModeType(mode, kind string) bool {
	switch mode {
	case "040000":
		return kind == "tree"
	case "100644", "100755", "120000":
		return kind == "blob"
	case "160000":
		return kind == "commit"
	default:
		return false
	}
}

func validTreePath(value string) bool {
	if _, err := escapeHierarchicalPath(value); err != nil {
		return false
	}
	if strings.Count(value, "/")+1 > MaxDataPathDepth {
		return false
	}
	return true
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, b := range value {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return false
		}
	}
	return true
}

func decodeRequiredText(raw json.RawMessage, max int, target *string) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, target) != nil || !validText(*target, max) {
		return ErrAPIResponse
	}
	return nil
}

func decodeRequiredTextAllowEmpty(raw json.RawMessage, max int, target *string) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, target) != nil || len(*target) > max || !validUTF8Text(*target) {
		return ErrAPIResponse
	}
	return nil
}

func decodeRequiredBool(raw json.RawMessage, target *bool) error {
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		*target = true
	case "false":
		*target = false
	default:
		return ErrAPIResponse
	}
	return nil
}

func decodeRequiredInt(raw json.RawMessage, target *int) error {
	var value int64
	if err := decodeRequiredInt64(raw, &value); err != nil || int64(int(value)) != value {
		return ErrAPIResponse
	}
	*target = int(value)
	return nil
}

func decodeRequiredInt64(raw json.RawMessage, target *int64) error {
	value, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil {
		return ErrAPIResponse
	}
	*target = value
	return nil
}

func validUTF8Text(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func exactFields(fields map[string]json.RawMessage, names ...string) bool {
	if len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if len(fields[name]) == 0 {
			return false
		}
	}
	return true
}
