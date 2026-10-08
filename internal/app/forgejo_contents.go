package app

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"slices"
	"strconv"
	"strings"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

// Only private in-memory comparisons retain sample paths. Reports contain
// the separate global repository fingerprint, never these paths or bytes.
type forgejoContentObservation struct {
	Path   string
	Size   int64
	SHA256 string
}

func observeForgejoContents(ctx context.Context, client runtimeClient, run state.Run, phase string, repo forgejo.APIRepository, branch forgejo.APIBranch, auth forgejo.Auth) ([]forgejoContentObservation, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, code("CANCELED")
	}
	get := func(endpoint string) ([]byte, error) {
		status, body, err := requestForgejoAPI(ctx, client, run, phase, endpoint, &auth)
		if err != nil || status != 200 {
			return nil, code("AUTH_FAILED")
		}
		return body, nil
	}
	var treeSHA string
	total := -1
	seen := map[string]bool{}
	var candidates []forgejo.APITreeEntry
	for page := 1; page <= forgejo.MaxGitTreeEntries/forgejo.GitTreePageSize; page++ {
		endpoint, err := forgejo.TreeEndpoint(repo.OwnerLogin, repo.Name, branch.CommitSHA, page)
		if err != nil {
			return nil, code("AUTH_FAILED")
		}
		body, err := get(endpoint)
		if err != nil {
			return nil, err
		}
		tree, err := forgejo.ParseTreePage(body, page)
		if err != nil || len(tree.SHA) != len(branch.CommitSHA) {
			return nil, code("AUTH_FAILED")
		}
		if page == 1 {
			treeSHA = tree.SHA
			total = tree.TotalCount
		} else if tree.SHA != treeSHA || tree.TotalCount != total {
			return nil, code("AUTH_FAILED")
		}
		for _, entry := range tree.Entries {
			if seen[entry.Path] || len(entry.SHA) != len(branch.CommitSHA) {
				return nil, code("AUTH_FAILED")
			}
			seen[entry.Path] = true
			if entry.IsRegularBlob() && entry.Size <= forgejo.MaxFileContentBytes {
				candidates = append(candidates, entry)
			}
		}
		pages := (total + forgejo.GitTreePageSize - 1) / forgejo.GitTreePageSize
		if pages == 0 {
			pages = 1
		}
		if page == pages {
			break
		}
	}
	if len(seen) != total || len(candidates) == 0 {
		return nil, code("AUTH_FAILED")
	}
	slices.SortFunc(candidates, func(a, b forgejo.APITreeEntry) int { return strings.Compare(a.Path, b.Path) })
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}
	observations := make([]forgejoContentObservation, 0, len(candidates))
	for _, entry := range candidates {
		if ctx.Err() != nil {
			return nil, code("CANCELED")
		}
		endpoint, err := forgejo.ContentsEndpoint(repo.OwnerLogin, repo.Name, entry.Path, branch.CommitSHA)
		if err != nil {
			return nil, code("AUTH_FAILED")
		}
		body, err := get(endpoint)
		if err != nil {
			return nil, err
		}
		contents, err := forgejo.ParseContents(body, entry.Path)
		if err != nil {
			return nil, code("AUTH_FAILED")
		}
		data := contents.Bytes()
		if int64(len(data)) != entry.Size || gitBlobDigest(data, len(entry.SHA)) != entry.SHA {
			return nil, code("AUTH_FAILED")
		}
		digest := sha256.Sum256(data)
		observations = append(observations, forgejoContentObservation{Path: entry.Path, Size: entry.Size, SHA256: hex.EncodeToString(digest[:])})
	}
	if ctx.Err() != nil {
		return nil, code("CANCELED")
	}
	return observations, nil
}

func gitBlobDigest(data []byte, hexLength int) string {
	var h hash.Hash
	switch hexLength {
	case 40:
		h = sha1.New()
	case 64:
		h = sha256.New()
	default:
		return ""
	}
	_, _ = h.Write([]byte("blob " + strconv.Itoa(len(data)) + "\x00"))
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
