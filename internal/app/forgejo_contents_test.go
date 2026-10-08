package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestForgejoContentsVerifiesBytesAgainstImmutableGitObject(t *testing.T) {
	commit := strings.Repeat("a", 40)
	content := []byte("Hello synthetic world\n")
	blobSHA := gitBlobDigest(content, 40)
	treeEndpoint, _ := forgejo.TreeEndpoint("synthetic", "example", commit, 1)
	contentsEndpoint, _ := forgejo.ContentsEndpoint("synthetic", "example", "README.md", commit)
	tree := fmt.Sprintf(`{"sha":%q,"url":"http://app:3000/tree","tree":[{"path":"README.md","mode":"100644","type":"blob","size":%d,"sha":%q,"url":"http://app:3000/blob"}],"truncated":false,"page":1,"total_count":1}`, strings.Repeat("b", 40), len(content), blobSHA)
	contents := fmt.Sprintf(`{"type":"file","path":"README.md","size":%d,"encoding":"base64","content":%q}`, len(content), base64.StdEncoding.EncodeToString(content))
	stub := forgejoAPIStub{response: map[string]string{treeEndpoint: tree + "\n200\n", contentsEndpoint: contents + "\n200\n"}}
	repo := forgejo.APIRepository{OwnerLogin: "synthetic", Name: "example"}
	branch := forgejo.APIBranch{Name: "main", CommitSHA: commit}
	observed, err := observeForgejoContents(context.Background(), stub, state.Run{}, "baseline", repo, branch, forgejo.Auth{Token: strings.Repeat("c", 40)})
	if err != nil || len(observed) != 1 || observed[0].Path != "README.md" || observed[0].Size != int64(len(content)) {
		t.Fatalf("valid contents: %v %+v", err, observed)
	}
	stub.response[contentsEndpoint] = fmt.Sprintf(`{"type":"file","path":"README.md","size":%d,"encoding":"base64","content":%q}`, len(content), base64.StdEncoding.EncodeToString(bytesWithFirstChanged(content))) + "\n200\n"
	if _, err := observeForgejoContents(context.Background(), stub, state.Run{}, "target", repo, branch, forgejo.Auth{Token: strings.Repeat("c", 40)}); err == nil {
		t.Fatal("changed contents passed immutable Git blob hash")
	}
}

func bytesWithFirstChanged(input []byte) []byte {
	changed := append([]byte(nil), input...)
	changed[0] ^= 1
	return changed
}

func addContentResponseFixtures(responses map[string]string, owner, repo, commit string) {
	content := []byte("Synthetic README\n")
	treeEndpoint, _ := forgejo.TreeEndpoint(owner, repo, commit, 1)
	contentEndpoint, _ := forgejo.ContentsEndpoint(owner, repo, "README.md", commit)
	responses[treeEndpoint] = fmt.Sprintf(`{"sha":%q,"url":"http://app:3000/tree","tree":[{"path":"README.md","mode":"100644","type":"blob","size":%d,"sha":%q,"url":"http://app:3000/blob"}],"truncated":false,"page":1,"total_count":1}`, strings.Repeat("e", len(commit)), len(content), gitBlobDigest(content, len(commit))) + "\n200\n"
	responses[contentEndpoint] = fmt.Sprintf(`{"type":"file","path":"README.md","size":%d,"encoding":"base64","content":%q}`, len(content), base64.StdEncoding.EncodeToString(content)) + "\n200\n"
}
