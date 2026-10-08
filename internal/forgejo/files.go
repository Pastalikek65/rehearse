package forgejo

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RepositoryFileSummary is the bounded semantic summary of bare Git
// repositories in a Forgejo /data TAR. It contains no paths or file contents.
type RepositoryFileSummary struct {
	SHA256       string `json:"sha256"`
	Repositories uint64 `json:"repositories"`
	Files        uint64 `json:"files"`
	Bytes        uint64 `json:"bytes"`
}

type repositoryFileRecord struct {
	path string
	root string
	rel  string
	size uint64
	dig  [sha256.Size]byte
}

// ProjectRepositoryFiles hashes normalized file paths, sizes and content
// digests from conventional bare *.git directories. TAR member ordering and
// directory metadata are excluded. Only the exact runner runtime subtree,
// repository-root description files, Forgejo-generated hook scripts and
// documented hook samples are omitted. All other repository files participate.
// The input TAR is validated while each regular file is streamed.
func ProjectRepositoryFiles(ctx context.Context, r io.Reader) (RepositoryFileSummary, error) {
	return projectRepositoryFilesWithLimits(ctx, r, defaultDataTarLimits())
}

func projectRepositoryFiles(ctx context.Context, r io.Reader, maxEntries, maxExpandedBytes, maxPathBytes, maxAncestorRefs uint64) (RepositoryFileSummary, error) {
	limits := defaultDataTarLimits()
	limits.maxEntries = maxEntries
	limits.maxExpandedBytes = maxExpandedBytes
	limits.maxPathBytes = maxPathBytes
	limits.maxAncestorRefs = maxAncestorRefs
	return projectRepositoryFilesWithLimits(ctx, r, limits)
}

func projectRepositoryFilesWithLimits(ctx context.Context, r io.Reader, limits dataTarLimits) (RepositoryFileSummary, error) {
	if ctx == nil || ctx.Err() != nil {
		return RepositoryFileSummary{}, ErrArchiveCanceled
	}
	if r == nil {
		return RepositoryFileSummary{}, ErrTarInvalid
	}
	observer := &tarObserver{ctx: ctx, r: r}
	tr := tar.NewReader(observer)
	nodes := make(map[string]bool)
	descendants := make(map[string]uint64)
	roots := make(map[string]struct{})
	headRoots := make(map[string]struct{})
	records := make([]repositoryFileRecord, 0)
	var entries, expanded, pathBytes, ancestorRefs uint64
	for {
		if ctx.Err() != nil {
			return RepositoryFileSummary{}, ErrArchiveCanceled
		}
		observer.beginCapture()
		header, err := tr.Next()
		captured := observer.endCapture()
		if err == io.EOF {
			if len(captured) < 512 || !allZero(captured) {
				return RepositoryFileSummary{}, ErrTarInvalid
			}
			if len(captured) < 1024 {
				var second [512]byte
				if _, readErr := io.ReadFull(observer, second[:]); readErr != nil || !allZero(second[:]) {
					return RepositoryFileSummary{}, tarReadError(ctx, readErr)
				}
			}
			var tail [32 * 1024]byte
			for {
				n, readErr := observer.Read(tail[:])
				if !allZero(tail[:n]) {
					return RepositoryFileSummary{}, ErrTarInvalid
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return RepositoryFileSummary{}, tarReadError(ctx, readErr)
				}
			}
			if observer.total < 1024 || observer.total%512 != 0 || observer.total > MaxDataTarBytes {
				return RepositoryFileSummary{}, ErrTarTooLarge
			}
			if entries == 0 {
				return RepositoryFileSummary{}, ErrTarInvalid
			}
			return finishRepositoryProjection(records, roots, headRoots)
		}
		if err != nil {
			return RepositoryFileSummary{}, tarReadError(ctx, err)
		}
		if entries >= limits.maxEntries {
			return RepositoryFileSummary{}, ErrTarTooLarge
		}
		if header == nil || header.Size < 0 || header.Mode < 0 || header.Mode&^0777 != 0 ||
			!supportedOwner(header.Uid) || !supportedOwner(header.Gid) || !validPAX(header.PAXRecords) {
			return RepositoryFileSummary{}, ErrTarInvalid
		}
		isDirectory := header.Typeflag == tar.TypeDir
		if !isDirectory && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return RepositoryFileSummary{}, ErrTarSpecialEntry
		}
		name, runtime, pathErr := normalizeProjectionTarPath(header.Name, isDirectory)
		if pathErr != nil {
			return RepositoryFileSummary{}, pathErr
		}
		if runtime && name == runtimePathName && !isDirectory {
			return RepositoryFileSummary{}, ErrTarInvalid
		}
		depth := strings.Count(name, "/") + 1
		if depth > limits.maxPathDepth {
			return RepositoryFileSummary{}, ErrTarUnsafePath
		}
		pathLength := uint64(len(name))
		newAncestorRefs := uint64(depth - 1)
		if pathBytes > limits.maxPathBytes || pathLength > limits.maxPathBytes-pathBytes ||
			ancestorRefs > limits.maxAncestorRefs || newAncestorRefs > limits.maxAncestorRefs-ancestorRefs {
			return RepositoryFileSummary{}, ErrTarTooLarge
		}
		key := name
		if runtime {
			key = "\x00" + name
		}
		if _, exists := nodes[key]; exists {
			return RepositoryFileSummary{}, ErrTarUnsafePath
		}
		if isDirectory && header.Size != 0 {
			return RepositoryFileSummary{}, ErrTarInvalid
		}
		if !isDirectory {
			if header.Size > MaxDataFileBytes || expanded > limits.maxExpandedBytes || uint64(header.Size) > limits.maxExpandedBytes-expanded {
				return RepositoryFileSummary{}, ErrTarTooLarge
			}
			if descendants[key] != 0 || hasFileAncestor(nodes, key) {
				return RepositoryFileSummary{}, ErrTarUnsafePath
			}
		}
		root, relative, inRepository := "", "", false
		if !runtime && !isDirectory {
			root, relative, inRepository = splitRepositoryFile(name)
		}
		if inRepository {
			roots[root] = struct{}{}
		}
		var contentHash [sha256.Size]byte
		generatedHook := false
		if isDirectory || runtime || !inRepository || relative == "description" || isGeneratedHookSample(relative) {
			if !isDirectory && header.Size > 0 {
				if _, err := io.CopyN(io.Discard, tr, header.Size); err != nil {
					return RepositoryFileSummary{}, tarReadError(ctx, err)
				}
			}
		} else {
			h := sha256.New()
			var marker *generatedHookMatcher
			if isHookFile(relative) {
				marker = &generatedHookMatcher{}
			}
			writer := io.Writer(h)
			if marker != nil {
				writer = io.MultiWriter(h, marker)
			}
			if header.Size > 0 {
				if _, err := io.CopyN(writer, tr, header.Size); err != nil {
					return RepositoryFileSummary{}, tarReadError(ctx, err)
				}
			}
			copy(contentHash[:], h.Sum(nil))
			generatedHook = marker != nil && marker.found
		}
		if !runtime && !isDirectory && inRepository {
			if relative == "HEAD" && header.Size > 0 {
				headRoots[root] = struct{}{}
			}
			if relative != "description" && !isGeneratedHookSample(relative) && !generatedHook {
				records = append(records, repositoryFileRecord{path: name, root: root, rel: relative, size: uint64(header.Size), dig: contentHash})
			}
		}
		if !isDirectory && header.Size > 0 {
			expanded += uint64(header.Size)
		}
		nodes[key] = isDirectory
		for parent := parentTarPath(key); parent != ""; parent = parentTarPath(parent) {
			descendants[parent]++
		}
		pathBytes += pathLength
		ancestorRefs += newAncestorRefs
		entries++
		if observer.total > MaxDataTarBytes {
			return RepositoryFileSummary{}, ErrTarTooLarge
		}
	}
}

const runtimeRootName = ".rehearse-runtime"
const runtimePathName = "rehearse-runtime"

func normalizeProjectionTarPath(name string, directory bool) (string, bool, error) {
	if !utf8.ValidString(name) || name == "" || len(name) > MaxDataPathBytes || strings.ContainsRune(name, '\\') {
		return "", false, ErrTarUnsafePath
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false, ErrTarUnsafePath
		}
	}
	if name == "." || name == "./" {
		if directory {
			return ".", false, nil
		}
		return "", false, ErrTarUnsafePath
	}
	runtimePath := name
	if strings.HasPrefix(runtimePath, "./") {
		runtimePath = strings.TrimPrefix(runtimePath, "./")
	}
	first := runtimePath
	if i := strings.IndexByte(first, '/'); i >= 0 {
		first = first[:i]
	}
	runtime := strings.EqualFold(first, runtimeRootName)
	if runtime {
		rest := strings.TrimPrefix(runtimePath, first)
		name = runtimePathName + rest
	}
	normalized, err := normalizeTarPath(name, directory)
	if err != nil {
		return "", false, err
	}
	return normalized, runtime, nil
}

func splitRepositoryFile(name string) (root, relative string, ok bool) {
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if i == len(parts)-1 || len(part) <= len(".git") || !strings.HasSuffix(strings.ToLower(part), ".git") {
			continue
		}
		return strings.Join(parts[:i+1], "/"), strings.Join(parts[i+1:], "/"), true
	}
	return "", "", false
}

func isHookFile(relative string) bool { return strings.HasPrefix(relative, "hooks/") }

func isGeneratedHookSample(relative string) bool {
	return isHookFile(relative) && strings.HasSuffix(strings.ToLower(relative), ".sample")
}

type generatedHookMatcher struct {
	suffix []byte
	found  bool
}

var generatedHookMarker = []byte("AUTO GENERATED BY GITEA")

func (m *generatedHookMatcher) Write(p []byte) (int, error) {
	if m.found {
		return len(p), nil
	}
	combined := make([]byte, 0, len(m.suffix)+len(p))
	combined = append(combined, m.suffix...)
	combined = append(combined, p...)
	if bytes.Contains(combined, generatedHookMarker) {
		m.found = true
	} else if len(combined) >= len(generatedHookMarker)-1 {
		m.suffix = append(m.suffix[:0], combined[len(combined)-(len(generatedHookMarker)-1):]...)
	} else {
		m.suffix = append(m.suffix[:0], combined...)
	}
	return len(p), nil
}

func finishRepositoryProjection(records []repositoryFileRecord, roots, headRoots map[string]struct{}) (RepositoryFileSummary, error) {
	for root := range roots {
		if _, ok := headRoots[root]; !ok {
			return RepositoryFileSummary{}, ErrTarInvalid
		}
	}
	for _, record := range records {
		if _, ok := headRoots[record.root]; !ok {
			return RepositoryFileSummary{}, ErrTarInvalid
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].path < records[j].path })
	h := sha256.New()
	_, _ = h.Write([]byte("rehearse-forgejo-repository-files-v1\x00"))
	var encoded [8]byte
	var summary RepositoryFileSummary
	for _, record := range records {
		binary.BigEndian.PutUint64(encoded[:], uint64(len(record.path)))
		_, _ = h.Write(encoded[:])
		_, _ = h.Write([]byte(record.path))
		binary.BigEndian.PutUint64(encoded[:], record.size)
		_, _ = h.Write(encoded[:])
		_, _ = h.Write(record.dig[:])
		summary.Files++
		if record.size > ^uint64(0)-summary.Bytes {
			return RepositoryFileSummary{}, ErrTarTooLarge
		}
		summary.Bytes += record.size
	}
	summary.Repositories = uint64(len(headRoots))
	summary.SHA256 = hex.EncodeToString(h.Sum(nil))
	return summary, nil
}
