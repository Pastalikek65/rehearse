package forgejo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const apiOrigin = "http://app:3000"

// Auth carries a synthetic Forgejo API token. The value is excluded from JSON
// and has no String method; callers must pass CurlConfig bytes only to curl's
// stdin and must never log them.
type Auth struct {
	Token string `json:"-"`
}

type APIUser struct {
	ID       int64
	Login    string
	FullName string
	IsAdmin  bool
}

type APIRepository struct {
	ID            int64
	OwnerLogin    string
	Name          string
	FullName      string
	DefaultBranch string
	Private       bool
	Empty         bool
	Archived      bool
	Mirror        bool
	Fork          bool
}

type APIBranch struct {
	Name      string
	CommitSHA string
}

// APIContents contains file bytes returned by the bounded contents endpoint.
// Data is deliberately omitted from JSON to prevent accidental report output.
type APIContents struct {
	Path string `json:"-"`
	Size int64  `json:"-"`
	data []byte
}

// Bytes returns a defensive copy of validated file content. Formatting the
// value omits the bytes so accidental logs stay safe.
func (c APIContents) Bytes() []byte { return bytes.Clone(c.data) }

func (APIContents) String() string   { return "APIContents{validated}" }
func (APIContents) GoString() string { return "forgejo.APIContents{validated}" }

func VersionEndpoint() string { return "/api/v1/version" }
func UserEndpoint() string    { return "/api/v1/user" }

func UserRepositoriesEndpoint(page int) (string, error) {
	if page < 1 || page > 10000 {
		return "", ErrAPIEndpoint
	}
	return "/api/v1/user/repos?limit=50&page=" + strconv.Itoa(page), nil
}

func RepositoryEndpoint(owner, repository string) (string, error) {
	ownerPath, err := escapePathComponent(owner)
	if err != nil {
		return "", err
	}
	repositoryPath, err := escapePathComponent(repository)
	if err != nil {
		return "", err
	}
	return "/api/v1/repos/" + ownerPath + "/" + repositoryPath, nil
}

func BranchEndpoint(owner, repository, branch string) (string, error) {
	base, err := RepositoryEndpoint(owner, repository)
	if err != nil {
		return "", err
	}
	branchPath, err := escapeHierarchicalPath(branch)
	if err != nil {
		return "", err
	}
	return base + "/branches/" + branchPath, nil
}

func ContentsEndpoint(owner, repository, filePath, ref string) (string, error) {
	base, err := RepositoryEndpoint(owner, repository)
	if err != nil {
		return "", err
	}
	contentPath, err := escapeHierarchicalPath(filePath)
	if err != nil {
		return "", err
	}
	refPath, err := escapeRef(ref)
	if err != nil {
		return "", err
	}
	return base + "/contents/" + contentPath + "?ref=" + url.QueryEscape(refPath), nil
}

// CurlConfig returns stdin-only curl configuration for one generated,
// allowlisted GET endpoint. Invoke curl with --disable --config -; no redirects
// or proxies are enabled and the URL is fixed to the internal app service.
func CurlConfig(endpoint string, auth *Auth) ([]byte, error) {
	if !allowedEndpoint(endpoint) {
		return nil, ErrAPIEndpoint
	}
	lines := []string{
		"silent",
		"show-error",
		`proxy = ""`,
		`noproxy = "*"`,
		"connect-timeout = 3",
		"max-time = 15",
		fmt.Sprintf("max-filesize = %d", MaxAPIResponseBytes),
		`write-out = "\n%{http_code}\n"`,
		`url = "` + curlQuote(apiOrigin+endpoint) + `"`,
	}
	if auth != nil {
		if !validToken(auth.Token) {
			return nil, ErrAPIAuth
		}
		lines = append(lines, `header = "Authorization: token `+auth.Token+`"`)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// ParseResponse splits the status code appended by CurlConfig and checks the
// response body is bounded JSON. It returns no raw error text or status body in
// errors; callers must keep the returned body out of reports and logs.
func ParseResponse(output []byte) (status int, body []byte, err error) {
	if len(output) > MaxAPIResponseBytes+8 {
		return 0, nil, ErrAPIResponseTooLarge
	}
	if len(output) < 6 || output[len(output)-1] != '\n' {
		return 0, nil, ErrAPIResponse
	}
	codeEnd := len(output) - 1
	codeStart := codeEnd - 3
	if codeStart <= 0 || output[codeStart-1] != '\n' {
		return 0, nil, ErrAPIResponse
	}
	for _, digit := range output[codeStart:codeEnd] {
		if digit < '0' || digit > '9' {
			return 0, nil, ErrAPIResponse
		}
		status = status*10 + int(digit-'0')
	}
	if status < 100 || status > 599 {
		return 0, nil, ErrAPIResponse
	}
	body = output[:codeStart-1]
	if len(body) > MaxAPIResponseBytes || !json.Valid(body) {
		return 0, nil, ErrAPIResponse
	}
	return status, bytes.Clone(body), nil
}

func ParseVersion(data []byte) (string, error) {
	if !validJSONSize(data) {
		return "", ErrAPIResponse
	}
	var response struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &response) != nil || !validText(response.Version, 128) {
		return "", ErrAPIResponse
	}
	return response.Version, nil
}

func ParseUser(data []byte) (APIUser, error) {
	if !validJSONSize(data) {
		return APIUser{}, ErrAPIResponse
	}
	var response struct {
		ID       int64  `json:"id"`
		Login    string `json:"login"`
		FullName string `json:"full_name"`
		IsAdmin  *bool  `json:"is_admin"`
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(data, &shape) != nil || json.Unmarshal(data, &response) != nil ||
		response.ID <= 0 || !validText(response.Login, 100) || response.IsAdmin == nil || !hasFields(shape, "id", "login", "is_admin") ||
		response.FullName != "" && !validText(response.FullName, 200) {
		return APIUser{}, ErrAPIResponse
	}
	return APIUser{ID: response.ID, Login: response.Login, FullName: response.FullName, IsAdmin: *response.IsAdmin}, nil
}

func ParseRepository(data []byte) (APIRepository, error) {
	if !validJSONSize(data) {
		return APIRepository{}, ErrAPIResponse
	}
	var response struct {
		ID    int64 `json:"id"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name          string  `json:"name"`
		FullName      string  `json:"full_name"`
		DefaultBranch *string `json:"default_branch"`
		Private       *bool   `json:"private"`
		Empty         *bool   `json:"empty"`
		Archived      *bool   `json:"archived"`
		Mirror        *bool   `json:"mirror"`
		Fork          *bool   `json:"fork"`
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(data, &shape) != nil || json.Unmarshal(data, &response) != nil ||
		response.ID <= 0 || !validText(response.Owner.Login, 100) || !validText(response.Name, 100) ||
		!validText(response.FullName, 205) || response.DefaultBranch == nil ||
		(*response.DefaultBranch != "" && !validText(*response.DefaultBranch, 255)) ||
		(*response.DefaultBranch == "" && (response.Empty == nil || !*response.Empty)) ||
		!strings.EqualFold(response.FullName, response.Owner.Login+"/"+response.Name) ||
		response.Private == nil || response.Empty == nil || response.Archived == nil ||
		response.Mirror == nil || response.Fork == nil || !hasFields(shape, "id", "owner", "name", "full_name", "default_branch", "private", "empty", "archived", "mirror", "fork") {
		return APIRepository{}, ErrAPIResponse
	}
	return APIRepository{
		ID: response.ID, OwnerLogin: response.Owner.Login, Name: response.Name,
		FullName: response.FullName, DefaultBranch: *response.DefaultBranch,
		Private: *response.Private, Empty: *response.Empty, Archived: *response.Archived,
		Mirror: *response.Mirror, Fork: *response.Fork,
	}, nil
}

func ParseRepoList(data []byte) ([]APIRepository, error) {
	if !validJSONSize(data) || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '[' {
		return nil, ErrAPIResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var raw []json.RawMessage
	if decoder.Decode(&raw) != nil || len(raw) > MaxAPIListItems {
		return nil, ErrAPIResponse
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, ErrAPIResponse
	}
	repositories := make([]APIRepository, 0, len(raw))
	ids := make(map[int64]struct{}, len(raw))
	fullNames := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		repository, err := ParseRepository(item)
		if err != nil {
			return nil, ErrAPIResponse
		}
		if _, exists := ids[repository.ID]; exists {
			return nil, ErrAPIResponse
		}
		key := strings.ToLower(repository.FullName)
		if _, exists := fullNames[key]; exists {
			return nil, ErrAPIResponse
		}
		ids[repository.ID] = struct{}{}
		fullNames[key] = struct{}{}
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

func ParseBranch(data []byte) (APIBranch, error) {
	if !validJSONSize(data) {
		return APIBranch{}, ErrAPIResponse
	}
	var response struct {
		Name   string `json:"name"`
		Commit struct {
			ID  string `json:"id"`
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if json.Unmarshal(data, &response) != nil || !validText(response.Name, 255) {
		return APIBranch{}, ErrAPIResponse
	}
	sha := response.Commit.ID
	if sha == "" {
		sha = response.Commit.SHA
	}
	if !validText(sha, 128) {
		return APIBranch{}, ErrAPIResponse
	}
	return APIBranch{Name: response.Name, CommitSHA: sha}, nil
}

func ParseContents(data []byte, expectedPath string) (APIContents, error) {
	if !validJSONSize(data) {
		return APIContents{}, ErrAPIResponse
	}
	if _, err := escapeHierarchicalPath(expectedPath); err != nil {
		return APIContents{}, ErrAPIResponse
	}
	var response struct {
		Type     string  `json:"type"`
		Path     string  `json:"path"`
		Size     int64   `json:"size"`
		Encoding *string `json:"encoding"`
		Content  *string `json:"content"`
	}
	if json.Unmarshal(data, &response) != nil || response.Type != "file" || response.Path != expectedPath ||
		response.Size < 0 || response.Size > MaxFileContentBytes || response.Encoding == nil ||
		*response.Encoding != "base64" || response.Content == nil {
		return APIContents{}, ErrAPIResponse
	}
	decoded, err := base64.StdEncoding.DecodeString(*response.Content)
	if err != nil || int64(len(decoded)) != response.Size {
		return APIContents{}, ErrAPIResponse
	}
	return APIContents{Path: response.Path, Size: response.Size, data: decoded}, nil
}

func allowedEndpoint(endpoint string) bool {
	if endpoint == VersionEndpoint() || endpoint == UserEndpoint() {
		return true
	}
	u, err := url.ParseRequestURI(endpoint)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || u.User != nil ||
		strings.Contains(endpoint, "\\") || strings.ContainsAny(endpoint, "\r\n\x00") || !strings.HasPrefix(u.Path, "/api/v1/") {
		return false
	}
	pathParts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	decoded := make([]string, len(pathParts))
	for i, part := range pathParts {
		decoded[i], err = url.PathUnescape(part)
		if err != nil || !validPathComponent(decoded[i]) || strings.Contains(decoded[i], "%") || strings.Contains(decoded[i], "/") {
			return false
		}
	}
	if len(decoded) == 4 && decoded[0] == "api" && decoded[1] == "v1" && decoded[2] == "user" && decoded[3] == "repos" {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query) != 2 || len(query["limit"]) != 1 || query["limit"][0] != "50" || len(query["page"]) != 1 {
			return false
		}
		page, err := strconv.Atoi(query["page"][0])
		return err == nil && page >= 1 && page <= 10000 && strconv.Itoa(page) == query["page"][0]
	}
	if len(u.RawQuery) == 0 {
		if len(decoded) == 5 && decoded[0] == "api" && decoded[1] == "v1" && decoded[2] == "repos" {
			return true
		}
		if len(decoded) >= 7 && decoded[0] == "api" && decoded[1] == "v1" && decoded[2] == "repos" && decoded[5] == "branches" {
			return true
		}
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	if len(decoded) == 8 && decoded[0] == "api" && decoded[1] == "v1" && decoded[2] == "repos" &&
		decoded[5] == "git" && decoded[6] == "trees" && validGitObjectID(decoded[7]) {
		if len(query) != 3 || len(query["recursive"]) != 1 || query["recursive"][0] != "true" ||
			len(query["page"]) != 1 || len(query["per_page"]) != 1 || query["per_page"][0] != strconv.Itoa(GitTreePageSize) {
			return false
		}
		page, err := strconv.Atoi(query["page"][0])
		return err == nil && page >= 1 && page <= MaxGitTreeEntries/GitTreePageSize && strconv.Itoa(page) == query["page"][0]
	}
	return err == nil && len(query) == 1 && len(query["ref"]) == 1 &&
		validText(query["ref"][0], 255) && len(decoded) >= 7 &&
		decoded[0] == "api" && decoded[1] == "v1" && decoded[2] == "repos" && decoded[5] == "contents"
}

func escapePathComponent(value string) (string, error) {
	if !validPathComponent(value) || strings.ContainsAny(value, "/\\%") {
		return "", ErrAPIEndpoint
	}
	return url.PathEscape(value), nil
}

func escapeHierarchicalPath(value string) (string, error) {
	if !validText(value, MaxDataPathBytes) || strings.Contains(value, "\\") {
		return "", ErrAPIEndpoint
	}
	parts := strings.Split(value, "/")
	for i := range parts {
		if !validPathComponent(parts[i]) || strings.Contains(parts[i], "%") {
			return "", ErrAPIEndpoint
		}
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/"), nil
}

func escapeRef(value string) (string, error) {
	if !validText(value, 255) || strings.ContainsAny(value, "\\\x00") || strings.TrimSpace(value) != value {
		return "", ErrAPIEndpoint
	}
	return value, nil
}

func validPathComponent(value string) bool {
	if !validText(value, MaxDataPathBytes) || value == "." || value == ".." || strings.ContainsAny(value, "/\\:") {
		return false
	}
	return true
}

func validToken(value string) bool {
	if len(value) < 8 || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._~+-/=", r)) {
			return false
		}
	}
	return true
}

func validText(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validJSONSize(data []byte) bool {
	if len(data) == 0 || len(data) > MaxAPIResponseBytes || !utf8.Valid(data) || !json.Valid(data) {
		return false
	}
	return !hasDuplicateJSONKeys(data)
}

func hasDuplicateJSONKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var readValue func(int) error
	readValue = func(depth int) error {
		if depth > 64 {
			return ErrAPIResponse
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return ErrAPIResponse
				}
				if _, duplicate := keys[key]; duplicate {
					return ErrAPIResponse
				}
				keys[key] = struct{}{}
				if err := readValue(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return ErrAPIResponse
			}
		case '[':
			for decoder.More() {
				if err := readValue(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return ErrAPIResponse
			}
		default:
			return ErrAPIResponse
		}
		return nil
	}
	if err := readValue(0); err != nil {
		return true
	}
	if _, err := decoder.Token(); err != io.EOF {
		return true
	}
	return false
}

func hasFields(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if len(fields[name]) == 0 {
			return false
		}
	}
	return true
}

func curlQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
