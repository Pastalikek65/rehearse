package forgejo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicForgejoExampleTokenSupportsPOSIXCommandSubstitution(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "forgejo", "fixture-read-token.txt"))
	if err != nil {
		t.Fatal("public synthetic token fixture is unavailable")
	}
	// POSIX command substitution removes trailing LF bytes, not CR bytes.
	token := strings.TrimRight(string(raw), "\n")
	if len(token) != 40 {
		t.Fatal("public synthetic token has an invalid command-substitution length")
	}
	if _, err := CurlConfig(UserEndpoint(), &Auth{Token: token}); err != nil {
		t.Fatal("documented POSIX example does not produce a valid read-only token")
	}
}
