package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

type forgejoContentsReviewStub struct {
	runtimeClient
	responses map[string][]byte
	calls     []string
	cancelOn  string
	cancel    context.CancelFunc
}

func (s *forgejoContentsReviewStub) InsideBytes(_ context.Context, _ state.Run, _, role string, args []string, input io.Reader, _ int) ([]byte, error) {
	if role != "probe" || strings.Join(args, " ") != "curl --disable --config -" {
		return nil, code("OPERATION_FAILED")
	}
	config, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	endpoint := forgejoContentsReviewEndpoint(string(config))
	if endpoint == "" {
		return nil, code("OPERATION_FAILED")
	}
	body, ok := s.responses[endpoint]
	if !ok {
		return nil, code("OPERATION_FAILED")
	}
	s.calls = append(s.calls, endpoint)
	if endpoint == s.cancelOn && s.cancel != nil {
		s.cancel()
	}
	return append(append([]byte(nil), body...), []byte("\n200\n")...), nil
}

func forgejoContentsReviewEndpoint(config string) string {
	const prefix = `url = "http://app:3000`
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, `"`) {
			return strings.TrimSuffix(strings.TrimPrefix(line, prefix), `"`)
		}
	}
	return ""
}

type forgejoContentsReviewTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
	URL  string `json:"url"`
}

func forgejoContentsReviewBlob(path, sha string, size int64) forgejoContentsReviewTreeEntry {
	return forgejoContentsReviewTreeEntry{Path: path, Mode: "100644", Type: "blob", Size: size, SHA: sha, URL: "/blob/" + sha}
}

func forgejoContentsReviewPage(commitSHA, treeSHA string, page, total int, entries []forgejoContentsReviewTreeEntry) []byte {
	data, err := json.Marshal(struct {
		SHA       string                           `json:"sha"`
		URL       string                           `json:"url"`
		Tree      []forgejoContentsReviewTreeEntry `json:"tree"`
		Truncated bool                             `json:"truncated"`
		Page      int                              `json:"page"`
		Total     int                              `json:"total_count"`
	}{
		SHA: treeSHA, URL: "/trees/" + treeSHA, Tree: entries,
		Truncated: total > forgejo.GitTreePageSize, Page: page, Total: total,
	})
	if err != nil {
		panic(err)
	}
	_ = commitSHA // The requested commit and resolved tree object are intentionally distinct.
	return data
}

func forgejoContentsReviewAddPage(t *testing.T, stub *forgejoContentsReviewStub, repo forgejo.APIRepository, commitSHA, treeSHA string, page, total int, entries []forgejoContentsReviewTreeEntry) string {
	t.Helper()
	endpoint, err := forgejo.TreeEndpoint(repo.OwnerLogin, repo.Name, commitSHA, page)
	if err != nil {
		t.Fatal(err)
	}
	stub.responses[endpoint] = forgejoContentsReviewPage(commitSHA, treeSHA, page, total, entries)
	return endpoint
}

func forgejoContentsReviewAddFile(t *testing.T, stub *forgejoContentsReviewStub, repo forgejo.APIRepository, commitSHA, path string, content []byte) string {
	t.Helper()
	endpoint, err := forgejo.ContentsEndpoint(repo.OwnerLogin, repo.Name, path, commitSHA)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(struct {
		Type     string `json:"type"`
		Path     string `json:"path"`
		Size     int    `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}{Type: "file", Path: path, Size: len(content), Encoding: "base64", Content: base64.StdEncoding.EncodeToString(content)})
	if err != nil {
		t.Fatal(err)
	}
	stub.responses[endpoint] = body
	return endpoint
}

func forgejoContentsReviewInput(commitSHA string) (forgejo.APIRepository, forgejo.APIBranch) {
	return forgejo.APIRepository{OwnerLogin: "synthetic", Name: "upgrade-fixture"}, forgejo.APIBranch{Name: "main", CommitSHA: commitSHA}
}

func TestObserveForgejoContentsCompletesEveryPageThenSamplesSortedFirstThree(t *testing.T) {
	const (
		commitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		treeSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		blobSHA   = "ce013625030ba8dba906f756967f9e9ca394464a" // Git SHA-1 of "hello\n".
	)
	content := []byte("hello\n")
	repo, branch := forgejoContentsReviewInput(commitSHA)
	stub := &forgejoContentsReviewStub{responses: map[string][]byte{}}
	pageOne := make([]forgejoContentsReviewTreeEntry, 0, forgejo.GitTreePageSize)
	for i := forgejo.GitTreePageSize - 1; i >= 0; i-- {
		path := fmt.Sprintf("file-%04d", i)
		pageOne = append(pageOne, forgejoContentsReviewBlob(path, blobSHA, int64(len(content))))
	}
	page1Endpoint := forgejoContentsReviewAddPage(t, stub, repo, commitSHA, treeSHA, 1, forgejo.GitTreePageSize+1, pageOne)
	page2Endpoint := forgejoContentsReviewAddPage(t, stub, repo, commitSHA, treeSHA, 2, forgejo.GitTreePageSize+1,
		[]forgejoContentsReviewTreeEntry{forgejoContentsReviewBlob("file-1000", blobSHA, int64(len(content)))})
	contentEndpoints := make([]string, 0, 3)
	for _, path := range []string{"file-0000", "file-0001", "file-0002"} {
		contentEndpoints = append(contentEndpoints, forgejoContentsReviewAddFile(t, stub, repo, commitSHA, path, content))
	}

	observed, err := observeForgejoContents(context.Background(), stub, state.Run{}, "baseline", repo, branch, forgejo.Auth{Token: strings.Repeat("c", 40)})
	if err != nil {
		t.Fatalf("complete multi-page tree rejected (tree SHA is resolved tree ID, not commit SHA): %v calls=%v", err, stub.calls)
	}
	if len(observed) != 3 {
		t.Fatalf("sample count=%d, want bounded maximum 3", len(observed))
	}
	for i, path := range []string{"file-0000", "file-0001", "file-0002"} {
		if observed[i].Path != path || observed[i].Size != int64(len(content)) {
			t.Fatalf("sample %d=%+v, want sorted %q with exact size", i, observed[i], path)
		}
		h := sha256.Sum256(content)
		if observed[i].SHA256 != hex.EncodeToString(h[:]) {
			t.Fatalf("sample %q SHA-256=%q, want content SHA-256", path, observed[i].SHA256)
		}
	}
	wantCalls := append([]string{page1Endpoint, page2Endpoint}, contentEndpoints...)
	if len(stub.calls) != len(wantCalls) {
		t.Fatalf("requested %d endpoints, want exactly both complete tree pages and three bounded samples: %v", len(stub.calls), stub.calls)
	}
	for i := range wantCalls {
		if stub.calls[i] != wantCalls[i] {
			t.Fatalf("request %d=%q, want %q", i, stub.calls[i], wantCalls[i])
		}
	}
}

func TestObserveForgejoContentsRejectsCrossPageInconsistency(t *testing.T) {
	const (
		commitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		treeSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		blobSHA   = "ce013625030ba8dba906f756967f9e9ca394464a"
	)
	for _, tc := range []struct {
		name        string
		secondSHA   string
		secondTotal int
		secondRows  []forgejoContentsReviewTreeEntry
	}{
		{name: "duplicate path across page boundary", secondSHA: treeSHA, secondTotal: forgejo.GitTreePageSize + 1, secondRows: []forgejoContentsReviewTreeEntry{forgejoContentsReviewBlob("file-0000", blobSHA, 6)}},
		{name: "changed page total", secondSHA: treeSHA, secondTotal: forgejo.GitTreePageSize + 2, secondRows: []forgejoContentsReviewTreeEntry{forgejoContentsReviewBlob("file-1000", blobSHA, 6), forgejoContentsReviewBlob("file-1001", blobSHA, 6)}},
		{name: "changed resolved tree SHA", secondSHA: strings.Repeat("d", 40), secondTotal: forgejo.GitTreePageSize + 1, secondRows: []forgejoContentsReviewTreeEntry{forgejoContentsReviewBlob("file-1000", blobSHA, 6)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, branch := forgejoContentsReviewInput(commitSHA)
			stub := &forgejoContentsReviewStub{responses: map[string][]byte{}}
			first := make([]forgejoContentsReviewTreeEntry, forgejo.GitTreePageSize)
			for i := range first {
				first[i] = forgejoContentsReviewBlob(fmt.Sprintf("file-%04d", i), blobSHA, 6)
			}
			forgejoContentsReviewAddPage(t, stub, repo, commitSHA, treeSHA, 1, forgejo.GitTreePageSize+1, first)
			forgejoContentsReviewAddPage(t, stub, repo, commitSHA, tc.secondSHA, 2, tc.secondTotal, tc.secondRows)
			if _, err := observeForgejoContents(context.Background(), stub, state.Run{}, "baseline", repo, branch, forgejo.Auth{Token: strings.Repeat("c", 40)}); err == nil {
				t.Fatal("inconsistent paginated tree was accepted")
			}
		})
	}
}

func TestObserveForgejoContentsSkipsSymlinksSubmodulesAndOversizedBlobs(t *testing.T) {
	const (
		commitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		treeSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		blobSHA   = "ce013625030ba8dba906f756967f9e9ca394464a"
	)
	content := []byte("hello\n")
	repo, branch := forgejoContentsReviewInput(commitSHA)
	stub := &forgejoContentsReviewStub{responses: map[string][]byte{}}
	entries := []forgejoContentsReviewTreeEntry{
		forgejoContentsReviewBlob("README.md", blobSHA, int64(len(content))),
		{Path: "link", Mode: "120000", Type: "blob", Size: 4, SHA: blobSHA, URL: "/blob/" + blobSHA},
		{Path: "vendor", Mode: "160000", Type: "commit", Size: 0, SHA: blobSHA, URL: ""},
		forgejoContentsReviewBlob("large.bin", blobSHA, forgejo.MaxFileContentBytes+1),
	}
	forgejoContentsReviewAddPage(t, stub, repo, commitSHA, treeSHA, 1, len(entries), entries)
	wantContentEndpoint := forgejoContentsReviewAddFile(t, stub, repo, commitSHA, "README.md", content)

	observed, err := observeForgejoContents(context.Background(), stub, state.Run{}, "baseline", repo, branch, forgejo.Auth{Token: strings.Repeat("c", 40)})
	if err != nil || len(observed) != 1 || observed[0].Path != "README.md" {
		t.Fatalf("regular-blob sample: observations=%+v err=%v calls=%v", observed, err, stub.calls)
	}
	if len(stub.calls) != 2 || stub.calls[1] != wantContentEndpoint {
		t.Fatalf("sampler fetched excluded entries: calls=%v", stub.calls)
	}
}

func TestObserveForgejoContentsRejectsReportedSizeOrGitBlobHashMismatch(t *testing.T) {
	const (
		commitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		treeSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		blobSHA   = "ce013625030ba8dba906f756967f9e9ca394464a"
		wrongSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	content := []byte("hello\n")
	for _, tc := range []struct {
		name     string
		treeSize int64
		treeBlob string
	}{
		{name: "reported tree size differs from body", treeSize: int64(len(content) + 1), treeBlob: blobSHA},
		{name: "contents bytes differ from Git blob object", treeSize: int64(len(content)), treeBlob: wrongSHA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, branch := forgejoContentsReviewInput(commitSHA)
			stub := &forgejoContentsReviewStub{responses: map[string][]byte{}}
			entry := forgejoContentsReviewBlob("README.md", tc.treeBlob, tc.treeSize)
			forgejoContentsReviewAddPage(t, stub, repo, commitSHA, treeSHA, 1, 1, []forgejoContentsReviewTreeEntry{entry})
			forgejoContentsReviewAddFile(t, stub, repo, commitSHA, "README.md", content)
			if _, err := observeForgejoContents(context.Background(), stub, state.Run{}, "baseline", repo, branch, forgejo.Auth{Token: strings.Repeat("c", 40)}); err == nil {
				t.Fatal("tree metadata or Git object mismatch was accepted")
			}
		})
	}
}

func TestObserveForgejoContentsSupportsSHA256GitObjectIDs(t *testing.T) {
	const (
		commitSHA = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		treeSHA   = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		blobSHA   = "2cf8d83d9ee29543b34a87727421fdecb7e3f3a183d337639025de576db9ebb4" // Git SHA-256 of "hello\n".
	)
	content := []byte("hello\n")
	repo, branch := forgejoContentsReviewInput(commitSHA)
	stub := &forgejoContentsReviewStub{responses: map[string][]byte{}}
	forgejoContentsReviewAddPage(t, stub, repo, commitSHA, treeSHA, 1, 1, []forgejoContentsReviewTreeEntry{forgejoContentsReviewBlob("README.md", blobSHA, int64(len(content)))})
	forgejoContentsReviewAddFile(t, stub, repo, commitSHA, "README.md", content)
	observed, err := observeForgejoContents(context.Background(), stub, state.Run{}, "baseline", repo, branch, forgejo.Auth{Token: strings.Repeat("c", 40)})
	if err != nil || len(observed) != 1 || observed[0].Path != "README.md" {
		t.Fatalf("SHA-256 Git object rejected: observations=%+v err=%v calls=%v", observed, err, stub.calls)
	}
}

func TestObserveForgejoContentsDoesNotReturnPassAfterCancellation(t *testing.T) {
	const (
		commitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		blobSHA   = "ce013625030ba8dba906f756967f9e9ca394464a"
	)
	treeSHA := strings.Repeat("b", 40)
	content := []byte("hello\n")
	repo, branch := forgejoContentsReviewInput(commitSHA)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := &forgejoContentsReviewStub{responses: map[string][]byte{}, cancel: cancel}
	forgejoContentsReviewAddPage(t, stub, repo, commitSHA, treeSHA, 1, 1, []forgejoContentsReviewTreeEntry{forgejoContentsReviewBlob("README.md", blobSHA, int64(len(content)))})
	contentsEndpoint := forgejoContentsReviewAddFile(t, stub, repo, commitSHA, "README.md", content)
	stub.cancelOn = contentsEndpoint

	observed, err := observeForgejoContents(ctx, stub, state.Run{}, "baseline", repo, branch, forgejo.Auth{Token: strings.Repeat("c", 40)})
	if ctx.Err() == nil || len(stub.calls) != 2 || stub.calls[1] != contentsEndpoint {
		t.Fatalf("test did not cancel on the content response: calls=%v ctxErr=%v", stub.calls, ctx.Err())
	}
	if err == nil || observed != nil {
		t.Fatalf("canceled contents observation returned usable samples: %+v err=%v", observed, err)
	}
}
