package fixture

import "bytes"

// The Forgejo fixture data below is intentionally public and synthetic. It is
// only suitable for disposable test instances; these values must never be
// reused for a real Forgejo account or deployment.
const (
	ForgejoUsername        = "rehearse-fixture"
	ForgejoPassword        = "synthetic-fixture-password"
	ForgejoRepository      = "upgrade-fixture"
	ForgejoDefaultBranch   = "main"
	ForgejoReadTokenName   = "rehearse-fixture-read-only"
	ForgejoReadTokenScopes = "read:user,read:repository"
)

// ForgejoFile is one deterministic regular file on the synthetic default
// branch. Content is held as bytes to preserve exact UTF-8 and newline checks.
type ForgejoFile struct {
	Path    string
	Content []byte
}

// ForgejoDefinition describes the one-user, one-private-repository public
// fixture. The access token itself is generated at runtime and is not part of
// this public data definition.
type ForgejoDefinition struct {
	Username      string
	Password      string
	Repository    string
	Private       bool
	DefaultBranch string
	TokenName     string
	TokenScopes   string
	Files         []ForgejoFile
}

var forgejoFiles = []ForgejoFile{
	{
		Path: "README.md",
		Content: []byte("# Rehearse fixture\n\n" +
			"Synthetic migration data; this repository has no external source.\n\n" +
			"Default branch: `main`.\n" +
			"Unicode: café, 東京, Καλημέρα, 雪.\n"),
	},
	{
		Path:    "nested/hello.txt",
		Content: []byte("Line one — Καλημέρα.\nLine two — 雪 and 東京.\n"),
	},
	{
		Path: "nested/fixture.json",
		Content: []byte("{\n" +
			"  \"adapter\": \"forgejo\",\n" +
			"  \"dataset\": \"synthetic\",\n" +
			"  \"unicode\": [\"café\", \"東京\", \"雪\"],\n" +
			"  \"version\": 1\n" +
			"}\n"),
	},
}

// ForgejoFixture returns a fresh description and defensive copies of all file
// contents so a runner cannot mutate package-global fixture bytes.
func ForgejoFixture() ForgejoDefinition {
	files := make([]ForgejoFile, len(forgejoFiles))
	for i, file := range forgejoFiles {
		files[i] = ForgejoFile{Path: file.Path, Content: bytes.Clone(file.Content)}
	}
	return ForgejoDefinition{
		Username: ForgejoUsername, Password: ForgejoPassword,
		Repository: ForgejoRepository, Private: true,
		DefaultBranch: ForgejoDefaultBranch,
		TokenName:     ForgejoReadTokenName, TokenScopes: ForgejoReadTokenScopes,
		Files: files,
	}
}
