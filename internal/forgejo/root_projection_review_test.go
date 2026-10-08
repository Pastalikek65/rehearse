package forgejo

import (
	"archive/tar"
	"bytes"
	"context"
	"testing"
)

func TestProjectRepositoryFilesAcceptsDotRootDirectoryConsistently(t *testing.T) {
	for _, rootName := range []string{".", "./"} {
		t.Run(rootName, func(t *testing.T) {
			data := makeTar(t,
				tarEntry{header: tar.Header{Name: rootName, Typeflag: tar.TypeDir}},
				tarEntry{header: tar.Header{Name: "gitea", Typeflag: tar.TypeDir}},
				tarEntry{header: tar.Header{Name: "gitea/conf", Typeflag: tar.TypeDir}},
				tarEntry{header: tar.Header{Name: "gitea/conf/app.ini"}, body: []byte("synthetic config\n")},
				tarEntry{header: tar.Header{Name: "gitea/repositories/fixture/sample.git/HEAD"}, body: []byte("ref: refs/heads/main\n")},
			)
			if _, err := ValidateDataTar(context.Background(), bytes.NewReader(data)); err != nil {
				t.Fatalf("validator rejected root directory %q: %v", rootName, err)
			}
			if _, err := ProjectRepositoryFiles(context.Background(), bytes.NewReader(data)); err != nil {
				t.Fatalf("repository projection rejected root directory %q accepted by validator: %v", rootName, err)
			}
		})
	}
}

func TestProjectionRootExceptionDoesNotAdmitUnsafePathsOrLinks(t *testing.T) {
	for name, tc := range map[string]struct {
		path      string
		directory bool
	}{
		"empty regular filename": {path: ""},
		"dot-root regular file":  {path: "./"},
		"traversal":              {path: "../outside"},
		"internal dot component": {path: "gitea/data/gitea-repositories/user/repo.git/refs/./main"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := normalizeProjectionTarPath(tc.path, tc.directory); err == nil {
				t.Fatalf("unsafe projection path %q accepted", tc.path)
			}
		})
	}

	symlink := tarEntry{header: tar.Header{
		Name:     "gitea/data/gitea-repositories/synthetic-user/upgrade-fixture.git/refs/link",
		Typeflag: tar.TypeSymlink,
		Linkname: "../outside",
	}}
	if _, err := ProjectRepositoryFiles(context.Background(), bytes.NewReader(syntheticGitTar(t, symlink))); err == nil {
		t.Fatal("repository projection accepted a symlink")
	}
}
