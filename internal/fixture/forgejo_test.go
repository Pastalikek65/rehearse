package fixture

import (
	"bytes"
	"encoding/json"
	"testing"
	"unicode/utf8"
)

func TestForgejoFixtureIsOnePrivateSyntheticRepositoryWithThreeFiles(t *testing.T) {
	fixture := ForgejoFixture()
	if fixture.Username != ForgejoUsername || fixture.Password != ForgejoPassword ||
		fixture.Repository != ForgejoRepository || !fixture.Private || fixture.DefaultBranch != "main" ||
		fixture.TokenName == "" || fixture.TokenScopes != "read:user,read:repository" {
		t.Fatalf("unexpected Forgejo fixture contract: %+v", fixture)
	}
	wantPaths := []string{"README.md", "nested/hello.txt", "nested/fixture.json"}
	if len(fixture.Files) != len(wantPaths) {
		t.Fatalf("fixture file count=%d, want %d", len(fixture.Files), len(wantPaths))
	}
	for i, file := range fixture.Files {
		if file.Path != wantPaths[i] || len(file.Content) == 0 || !utf8.Valid(file.Content) {
			t.Errorf("fixture file %d is malformed: path=%q bytes=%d", i, file.Path, len(file.Content))
		}
	}
	if !bytes.Contains(fixture.Files[0].Content, []byte("café, 東京, Καλημέρα, 雪")) ||
		!bytes.Contains(fixture.Files[1].Content, []byte("Line one — Καλημέρα.\nLine two — 雪 and 東京.\n")) {
		t.Fatal("README or nested UTF-8/newline fixture content changed")
	}
	var jsonValue struct {
		Adapter string   `json:"adapter"`
		Dataset string   `json:"dataset"`
		Unicode []string `json:"unicode"`
		Version int      `json:"version"`
	}
	if err := json.Unmarshal(fixture.Files[2].Content, &jsonValue); err != nil ||
		jsonValue.Adapter != "forgejo" || jsonValue.Dataset != "synthetic" ||
		jsonValue.Version != 1 || len(jsonValue.Unicode) != 3 {
		t.Fatalf("nested JSON fixture is invalid: %+v %v", jsonValue, err)
	}
}

func TestForgejoFixtureReturnsDefensiveFileCopies(t *testing.T) {
	first := ForgejoFixture()
	first.Files[0].Content[0] ^= 0xff
	first.Files[1].Path = "changed"
	second := ForgejoFixture()
	if second.Files[0].Content[0] != '#' || second.Files[1].Path != "nested/hello.txt" {
		t.Fatal("fixture caller mutation changed package fixture values")
	}
}
