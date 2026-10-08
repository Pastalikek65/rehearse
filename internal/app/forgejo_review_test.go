package app

import (
	"archive/tar"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

func TestCreateForgejoArchivePublicationRaceIsAtomicAndPrivate(t *testing.T) {
	db, data := archiveInputs(t)
	outputDir := t.TempDir()
	output := filepath.Join(outputDir, "shared.zip")
	const creators = 8
	start := make(chan struct{})
	results := make(chan error, creators)
	var wg sync.WaitGroup
	for i := 0; i < creators; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := CreateForgejoArchive(context.Background(), db, data, output)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes, exists := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, os.ErrExist) || err.Error() == "ARCHIVE_OUTPUT_EXISTS" {
			exists++
		} else {
			t.Fatalf("unexpected competing publication error: %v", err)
		}
	}
	if successes != 1 || exists != creators-1 {
		t.Fatalf("publication outcomes: success=%d exists=%d", successes, exists)
	}

	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if _, err := forgejo.OpenArchive(context.Background(), f, info.Size()); err != nil {
		_ = f.Close()
		t.Fatalf("winner was not a complete validated archive: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("published archive is not private: mode %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "shared.zip" {
		t.Fatalf("partial staging file remained: %v, %v", entries, err)
	}
}

func TestCreateForgejoArchiveDoesNotReplaceSymlinkOrDirectory(t *testing.T) {
	db, data := archiveInputs(t)
	dir := t.TempDir()
	protectedTarget := filepath.Join(dir, "protected")
	if err := os.WriteFile(protectedTarget, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.zip")
	if err := os.Symlink(protectedTarget, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, err := CreateForgejoArchive(context.Background(), db, data, link); err == nil || err.Error() != "ARCHIVE_OUTPUT_EXISTS" {
		t.Fatalf("symlink destination was not rejected: %v", err)
	}
	if got, err := os.ReadFile(protectedTarget); err != nil || string(got) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
	directory := filepath.Join(dir, "directory.zip")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateForgejoArchive(context.Background(), db, data, directory); err == nil || err.Error() != "ARCHIVE_OUTPUT_EXISTS" {
		t.Fatalf("directory destination was not rejected: %v", err)
	}
}

func TestCreateForgejoArchiveCancellationDuringPayloadRemovesPartialOutput(t *testing.T) {
	db, data := archiveInputs(t)
	payload := make([]byte, 16<<20)
	if _, err := cryptorand.Read(payload); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(data)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	if err := tw.WriteHeader(&tar.Header{Name: "gitea/large.bin", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(payload)), Uid: 1000, Gid: 1000}); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	outputDir := t.TempDir()
	output := filepath.Join(outputDir, "canceled.zip")
	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := &cancelWhenArchivePartialGrows{Context: baseCtx, cancel: cancel, dir: outputDir, threshold: 4096}
	_, err = CreateForgejoArchive(ctx, db, data, output)
	if err == nil || err.Error() != "CANCELED" || !ctx.triggered {
		t.Fatalf("mid-copy cancellation result: %v", err)
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled archive was published: %v", err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial output remained after cancellation: %v, %v", entries, err)
	}
}

type cancelWhenArchivePartialGrows struct {
	context.Context
	cancel    context.CancelFunc
	dir       string
	threshold int64
	triggered bool
}

func (c *cancelWhenArchivePartialGrows) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".rehearse-archive-") || !strings.HasSuffix(entry.Name(), ".partial") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.Size() >= c.threshold {
			c.triggered = true
			c.cancel()
			return context.Canceled
		}
	}
	return nil
}

type forgejoReviewClient struct {
	runtimeClient
	responses map[string]string
	requests  []string
}

func (f *forgejoReviewClient) InsideBytes(_ context.Context, _ state.Run, _, role string, args []string, input io.Reader, _ int) ([]byte, error) {
	if role != "probe" || strings.Join(args, " ") != "curl --disable --config -" {
		return nil, errors.New("unexpected API probe invocation")
	}
	config, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	const prefix = `url = "http://app:3000`
	var endpoint string
	for _, line := range strings.Split(string(config), "\n") {
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, `"`) {
			endpoint = strings.TrimSuffix(strings.TrimPrefix(line, prefix), `"`)
			break
		}
	}
	if endpoint == "" {
		return nil, errors.New("API endpoint missing from probe config")
	}
	f.requests = append(f.requests, endpoint)
	response, ok := f.responses[endpoint]
	if !ok {
		return nil, fmt.Errorf("unexpected API endpoint %s", endpoint)
	}
	return []byte(response), nil
}

func reviewRepo(id int, name string) string {
	return fmt.Sprintf(`{"id":%d,"owner":{"login":"synthetic"},"name":%q,"full_name":%q,"default_branch":"","private":true,"empty":true,"archived":false,"mirror":false,"fork":false}`, id, name, "synthetic/"+name)
}

func reviewNonemptyRepo(id int, name string) string {
	return fmt.Sprintf(`{"id":%d,"owner":{"login":"synthetic"},"name":%q,"full_name":%q,"default_branch":"main","private":true,"empty":false,"archived":false,"mirror":false,"fork":false}`, id, name, "synthetic/"+name)
}

func addReviewRepoContent(client *forgejoReviewClient, owner, repo, commit string) {
	client.responses["/api/v1/repos/"+owner+"/"+repo+"/branches/main"] = `{"name":"main","commit":{"id":"` + commit + `"}}` + "\n200\n"
	addContentResponseFixtures(client.responses, owner, repo, commit)
}

func reviewAPIClient(version, user, pageOne, pageTwo string, details map[string]string) *forgejoReviewClient {
	responses := map[string]string{
		forgejo.VersionEndpoint():            version + "\n200\n",
		forgejo.UserEndpoint():               user + "\n200\n",
		"/api/v1/user/repos?limit=50&page=1": pageOne + "\n200\n",
		"/api/v1/user/repos?limit=50&page=2": pageTwo + "\n200\n",
		"/api/v1/user/repos?limit=50&page=3": "[]\n200\n",
	}
	for endpoint, body := range details {
		responses[endpoint] = body + "\n200\n"
	}
	return &forgejoReviewClient{responses: responses}
}

const reviewAPIUser = `{"id":1,"login":"synthetic","full_name":"Synthetic","is_admin":false}`

func TestObserveForgejoAPIWalksAllPagesAndChecksEveryDetail(t *testing.T) {
	const pageSize = 50
	var first []string
	details := make(map[string]string, pageSize+1)
	commit := strings.Repeat("a", 40)
	for i := 1; i <= pageSize+1; i++ {
		name := fmt.Sprintf("repo-%02d", i)
		repository := reviewRepo(i, name)
		if i == 1 {
			repository = reviewNonemptyRepo(i, name)
		}
		if i <= pageSize {
			first = append(first, repository)
		}
		details["/api/v1/repos/synthetic/"+name] = repository
	}
	pageOne := "[" + strings.Join(first, ",") + "]"
	pageTwo := "[" + reviewRepo(pageSize+1, "repo-51") + "]"
	client := reviewAPIClient(`{"version":"15.0.9"}`, reviewAPIUser, pageOne, pageTwo, details)
	addReviewRepoContent(client, "synthetic", "repo-01", commit)
	observation, err := observeForgejoAPI(context.Background(), client, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("x", 40)}, forgejo.SourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Repositories) != pageSize+1 {
		t.Fatalf("observed %d of %d repositories", len(observation.Repositories), pageSize+1)
	}
	pageTwoRequests, detailRequests := 0, 0
	for _, endpoint := range client.requests {
		if endpoint == "/api/v1/user/repos?limit=50&page=2" {
			pageTwoRequests++
		}
		if _, isDetailEndpoint := details[endpoint]; isDetailEndpoint {
			detailRequests++
		}
	}
	if pageTwoRequests != 1 || detailRequests != pageSize+1 {
		t.Fatalf("pagination/detail coverage: page2=%d details=%d", pageTwoRequests, detailRequests)
	}
}

func TestObserveForgejoAPIRejectsDuplicateIdentityAcrossPageBoundary(t *testing.T) {
	const pageSize = 50
	first := make([]string, 0, pageSize)
	details := make(map[string]string, pageSize)
	for i := 1; i <= pageSize; i++ {
		name := fmt.Sprintf("repo-%02d", i)
		repository := reviewRepo(i, name)
		first = append(first, repository)
		details["/api/v1/repos/synthetic/"+name] = repository
	}
	client := reviewAPIClient(`{"version":"15.0.9"}`, reviewAPIUser,
		"["+strings.Join(first, ",")+"]", "["+reviewRepo(1, "repo-01")+"]", details)
	if _, err := observeForgejoAPI(context.Background(), client, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("x", 40)}, forgejo.SourceVersion); err == nil || err.Error() != "AUTH_FAILED" {
		t.Fatalf("duplicate repository across page boundary accepted: %v", err)
	}
}

func TestObserveForgejoAPIRejectsListDetailMismatchAndWrongBranch(t *testing.T) {
	const listed = `{"id":1,"owner":{"login":"synthetic"},"name":"repo","full_name":"synthetic/repo","default_branch":"main","private":true,"empty":false,"archived":false,"mirror":false,"fork":false}`
	detailMismatch := strings.Replace(listed, `"private":true`, `"private":false`, 1)
	client := reviewAPIClient(`{"version":"15.0.9"}`, reviewAPIUser, "["+listed+"]", "[]", map[string]string{
		"/api/v1/repos/synthetic/repo": detailMismatch,
	})
	if _, err := observeForgejoAPI(context.Background(), client, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("x", 40)}, forgejo.SourceVersion); err == nil || err.Error() != "AUTH_FAILED" {
		t.Fatalf("list/detail mismatch accepted: %v", err)
	}

	client = reviewAPIClient(`{"version":"15.0.9"}`, reviewAPIUser, "["+listed+"]", "[]", map[string]string{
		"/api/v1/repos/synthetic/repo":               listed,
		"/api/v1/repos/synthetic/repo/branches/main": `{"name":"other","commit":{"id":"` + strings.Repeat("a", 40) + `"}}`,
	})
	if _, err := observeForgejoAPI(context.Background(), client, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("x", 40)}, forgejo.SourceVersion); err == nil || err.Error() != "AUTH_FAILED" {
		t.Fatalf("default branch mismatch accepted: %v", err)
	}
}

func TestObserveForgejoAPIComparisonIncludesAuthenticatedUser(t *testing.T) {
	repository := reviewNonemptyRepo(1, "repo")
	detail := map[string]string{"/api/v1/repos/synthetic/repo": repository}
	first := reviewAPIClient(`{"version":"15.0.9"}`, reviewAPIUser, "["+repository+"]", "[]", detail)
	commit := strings.Repeat("a", 40)
	addReviewRepoContent(first, "synthetic", "repo", commit)
	a, err := observeForgejoAPI(context.Background(), first, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("x", 40)}, forgejo.SourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	changedUser := `{"id":2,"login":"synthetic","full_name":"Synthetic","is_admin":false}`
	second := reviewAPIClient(`{"version":"15.0.9"}`, changedUser, "["+repository+"]", "[]", detail)
	addReviewRepoContent(second, "synthetic", "repo", commit)
	b, err := observeForgejoAPI(context.Background(), second, state.Run{}, "target", forgejo.Auth{Token: strings.Repeat("x", 40)}, forgejo.SourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if sameForgejoAPI(a, b) {
		t.Fatal("authenticated API identity drift was not detected")
	}
}

func TestForgejoReviewTestHelpersAreValidJSON(t *testing.T) {
	var value map[string]any
	if err := json.Unmarshal([]byte(reviewRepo(1, "repo")), &value); err != nil || value["full_name"] != "synthetic/repo" {
		t.Fatalf("test fixture malformed: %#v, %v", value, err)
	}
}
