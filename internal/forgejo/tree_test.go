package forgejo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTreeEndpointIsFixedAndAllowlisted(t *testing.T) {
	endpoint, err := TreeEndpoint("alice", "upgrade-fixture", strings.Repeat("a", 40), 1)
	if err != nil {
		t.Fatal(err)
	}
	want := "/api/v1/repos/alice/upgrade-fixture/git/trees/" + strings.Repeat("a", 40) + "?recursive=true&page=1&per_page=1000"
	if endpoint != want {
		t.Fatalf("TreeEndpoint=%q, want %q", endpoint, want)
	}
	if !allowedEndpoint(endpoint) {
		t.Fatal("canonical tree endpoint is not allowlisted")
	}
	if _, err := CurlConfig(endpoint, nil); err != nil {
		t.Fatalf("CurlConfig rejected canonical tree endpoint: %v", err)
	}
	for _, input := range []struct {
		owner, repo, sha string
		page             int
	}{
		{"alice", "repo", strings.Repeat("a", 40), 0},
		{"alice", "repo", strings.Repeat("a", 40), 11},
		{"alice", "repo", strings.Repeat("A", 40), 1},
		{"alice", "repo", strings.Repeat("a", 39), 1},
		{"../alice", "repo", strings.Repeat("a", 40), 1},
	} {
		if _, err := TreeEndpoint(input.owner, input.repo, input.sha, input.page); err == nil {
			t.Fatalf("TreeEndpoint accepted unsafe input: %+v", input)
		}
	}
	for _, endpoint := range []string{
		strings.Replace(endpoint, "per_page=1000", "per_page=1001", 1),
		strings.Replace(endpoint, "recursive=true", "recursive=false", 1),
		endpoint + "&format=raw",
		strings.Replace(endpoint, "page=1", "page=01", 1),
		"/api/v1/repos/alice/upgrade-fixture/git/trees/" + strings.Repeat("a", 40) + "?recursive=true&page=1&per_page=1000&per_page=1000",
	} {
		if allowedEndpoint(endpoint) {
			t.Fatalf("noncanonical tree endpoint was allowlisted: %q", endpoint)
		}
	}
}

func TestParseTreePageAndRegularBlobClassification(t *testing.T) {
	data := treePageJSON(1, 5, false, []string{
		treeEntryJSON("README.md", "100644", "blob", 18, strings.Repeat("b", 40), "/api/v1/repos/alice/repo/git/blobs/"+strings.Repeat("b", 40)),
		treeEntryJSON("bin/tool", "100755", "blob", 42, strings.Repeat("c", 40), "/api/v1/repos/alice/repo/git/blobs/"+strings.Repeat("c", 40)),
		treeEntryJSON("docs", "040000", "tree", 0, strings.Repeat("d", 40), "/api/v1/repos/alice/repo/git/trees/"+strings.Repeat("d", 40)),
		treeEntryJSON("link", "120000", "blob", 5, strings.Repeat("e", 40), "/api/v1/repos/alice/repo/git/blobs/"+strings.Repeat("e", 40)),
		treeEntryJSON("vendor", "160000", "commit", 0, strings.Repeat("f", 40), ""),
	})
	page, err := ParseTreePage(data, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Page != 1 || page.TotalCount != 5 || page.Truncated || len(page.Entries) != 5 {
		t.Fatalf("unexpected page metadata: %+v", page)
	}
	for i, want := range []bool{true, true, false, false, false} {
		if got := page.Entries[i].IsRegularBlob(); got != want {
			t.Errorf("entry %q IsRegularBlob()=%t, want %t", page.Entries[i].Path, got, want)
		}
	}
}

func TestParseTreePageUsesForgejoTruncatedAsTotalOverPageSize(t *testing.T) {
	first := make([]string, GitTreePageSize)
	for i := range first {
		name := fmt.Sprintf("file-%04d", i)
		first[i] = treeEntryJSON(name, "100644", "blob", 1, strings.Repeat("b", 40), "/blob")
	}
	page1, err := ParseTreePage(treePageJSON(1, GitTreePageSize+1, true, first), 1)
	if err != nil || !page1.Truncated {
		t.Fatalf("first page with more than one page rejected: truncated=%t err=%v", page1.Truncated, err)
	}
	page2, err := ParseTreePage(treePageJSON(2, GitTreePageSize+1, true, []string{
		treeEntryJSON("last", "100644", "blob", 1, strings.Repeat("c", 40), "/blob"),
	}), 2)
	if err != nil || len(page2.Entries) != 1 || !page2.Truncated {
		t.Fatalf("last page must preserve upstream truncated=true semantics: page=%+v err=%v", page2, err)
	}
	if _, err := ParseTreePage(treePageJSON(1, GitTreePageSize+1, false, first), 1); err == nil {
		t.Fatal("upstream-inconsistent truncated flag accepted")
	}
	if _, err := ParseTreePage(treePageJSON(2, GitTreePageSize+1, true, nil), 2); err == nil {
		t.Fatal("missing final page entry accepted")
	}
}

func TestParseTreePageAllowsOnlyExactEmptyFirstPage(t *testing.T) {
	page, err := ParseTreePage(treePageJSON(1, 0, false, nil), 1)
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("empty page=%+v err=%v", page, err)
	}
	for _, data := range [][]byte{
		treePageJSON(2, 0, false, nil),
		treePageJSON(1, 0, true, nil),
		treePageJSON(1, 1, false, nil),
	} {
		if _, err := ParseTreePage(data, 1); err == nil {
			t.Fatalf("invalid empty/incomplete page accepted: %s", data)
		}
	}
}

func TestParseTreePageRejectsMalformedAndUnsafeRows(t *testing.T) {
	valid := treeEntryJSON("safe/file", "100644", "blob", 1, strings.Repeat("b", 40), "/blob")
	badRows := []string{
		treeEntryJSON("../outside", "100644", "blob", 1, strings.Repeat("b", 40), "/blob"),
		treeEntryJSON("/absolute", "100644", "blob", 1, strings.Repeat("b", 40), "/blob"),
		treeEntryJSON("safe\\file", "100644", "blob", 1, strings.Repeat("b", 40), "/blob"),
		treeEntryJSON("safe/%2e%2e", "100644", "blob", 1, strings.Repeat("b", 40), "/blob"),
		treeEntryJSON(strings.Repeat("x/", MaxDataPathDepth)+"file", "100644", "blob", 1, strings.Repeat("b", 40), "/blob"),
		treeEntryJSON("safe/file", "100644", "tree", 1, strings.Repeat("b", 40), "/blob"),
		treeEntryJSON("safe/file", "100644", "blob", -1, strings.Repeat("b", 40), "/blob"),
		treeEntryJSON("safe/file", "100644", "blob", 1, "bad", "/blob"),
		treeEntryJSON("safe/file", "040000", "tree", 1, strings.Repeat("b", 40), ""),
	}
	for _, row := range badRows {
		data := treePageJSON(1, 1, false, []string{row})
		if _, err := ParseTreePage(data, 1); err == nil {
			t.Errorf("unsafe/malformed row accepted: %s", row)
		}
	}
	duplicate := treePageJSON(1, 2, false, []string{valid, valid})
	if _, err := ParseTreePage(duplicate, 1); err == nil {
		t.Fatal("duplicate tree paths accepted")
	}
	unknown := []byte(strings.Replace(string(treePageJSON(1, 0, false, nil)), `"truncated":false`, `"truncated":false,"extra":true`, 1))
	if _, err := ParseTreePage(unknown, 1); err == nil {
		t.Fatal("unknown root field accepted")
	}
	unknownEntry := []byte(strings.Replace(valid, `"url":"/blob"}`, `"url":"/blob","extra":true}`, 1))
	if _, err := ParseTreePage(treePageJSON(1, 1, false, []string{string(unknownEntry)}), 1); err == nil {
		t.Fatal("unknown tree entry field accepted")
	}
	duplicateKey := []byte(strings.Replace(string(treePageJSON(1, 0, false, nil)), `"page":1`, `"page":1,"page":1`, 1))
	if _, err := ParseTreePage(duplicateKey, 1); err == nil {
		t.Fatal("duplicate JSON key accepted")
	}
}

func TestParseTreePageRejectsIncorrectExpectedPageAndBounds(t *testing.T) {
	page := treePageJSON(1, 1, false, []string{
		treeEntryJSON("one", "100644", "blob", 1, strings.Repeat("b", 40), "/blob"),
	})
	if _, err := ParseTreePage(page, 2); err == nil {
		t.Fatal("unexpected page number accepted")
	}
	if _, err := ParseTreePage(treePageJSON(1, MaxGitTreeEntries+1, true, nil), 1); err == nil {
		t.Fatal("oversized total_count accepted")
	}
	if _, err := ParseTreePage(bytes.Repeat([]byte(" "), MaxAPIResponseBytes+1), 1); err == nil {
		t.Fatal("oversized response accepted")
	}
	if _, err := ParseTreePage([]byte(`{"sha":"bad"}`), 1); err == nil {
		t.Fatal("missing response fields accepted")
	}
}

func treePageJSON(page, total int, truncated bool, entries []string) []byte {
	rows := "[" + strings.Join(entries, ",") + "]"
	return []byte(fmt.Sprintf(`{"sha":"%s","url":"/api/v1/repos/alice/repo/git/trees/%s","tree":%s,"truncated":%t,"page":%d,"total_count":%d}`,
		strings.Repeat("a", 40), strings.Repeat("a", 40), rows, truncated, page, total))
}

func treeEntryJSON(path, mode, kind string, size int64, sha, rawURL string) string {
	data, _ := json.Marshal(map[string]any{
		"path": path, "mode": mode, "type": kind, "size": size, "sha": sha, "url": rawURL,
	})
	return string(data)
}
