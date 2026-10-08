package forgejo

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DataSummary contains only bounded structural counts; file contents and
// member paths are never returned or logged.
type DataSummary struct {
	Files       uint64 `json:"files"`
	Directories uint64 `json:"directories"`
	Bytes       uint64 `json:"bytes"`
}

type tarObserver struct {
	ctx       context.Context
	r         io.Reader
	total     uint64
	capturing bool
	capture   []byte
}

type dataTarLimits struct {
	maxEntries       uint64
	maxExpandedBytes uint64
	maxPathBytes     uint64
	maxAncestorRefs  uint64
	maxPathDepth     int
}

func defaultDataTarLimits() dataTarLimits {
	return dataTarLimits{
		maxEntries: MaxDataEntries, maxExpandedBytes: MaxDataExpandedBytes,
		maxPathBytes: MaxDataPathBytesTotal, maxAncestorRefs: MaxDataAncestorRefs,
		maxPathDepth: MaxDataPathDepth,
	}
}

func (r *tarObserver) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.total >= MaxDataTarBytes {
		var probe [1]byte
		n, err := r.r.Read(probe[:])
		if n > 0 {
			return 0, ErrTarTooLarge
		}
		return 0, err
	}
	remaining := MaxDataTarBytes - r.total
	if uint64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		r.total += uint64(n)
		if r.capturing {
			left := 1024 - len(r.capture)
			if left > n {
				left = n
			}
			if left > 0 {
				r.capture = append(r.capture, p[:left]...)
			}
		}
	}
	return n, err
}

func (r *tarObserver) beginCapture() {
	r.capturing = true
	r.capture = r.capture[:0]
}

func (r *tarObserver) endCapture() []byte {
	r.capturing = false
	return append([]byte(nil), r.capture...)
}

// ValidateDataTar checks the complete archive before it can be restored. It
// accepts regular files and directories only, normalizes one leading "./",
// and rejects unsafe paths, links, special modes, special owners and expansion
// beyond the fixed resource limits.
func ValidateDataTar(ctx context.Context, r io.Reader) (DataSummary, error) {
	return validateDataTarWithLimits(ctx, r, defaultDataTarLimits())
}

func validateDataTar(ctx context.Context, r io.Reader, maxEntries uint64) (DataSummary, error) {
	limits := defaultDataTarLimits()
	limits.maxEntries = maxEntries
	return validateDataTarWithLimits(ctx, r, limits)
}

func validateDataTarWithLimits(ctx context.Context, r io.Reader, limits dataTarLimits) (DataSummary, error) {
	if ctx == nil || ctx.Err() != nil {
		return DataSummary{}, ErrArchiveCanceled
	}
	if r == nil {
		return DataSummary{}, ErrTarInvalid
	}
	observer := &tarObserver{ctx: ctx, r: r}
	tr := tar.NewReader(observer)
	var summary DataSummary
	nodes := make(map[string]bool)
	descendants := make(map[string]uint64)
	var pathBytes, ancestorRefs uint64
	for {
		if err := ctx.Err(); err != nil {
			return DataSummary{}, ErrArchiveCanceled
		}
		observer.beginCapture()
		h, err := tr.Next()
		captured := observer.endCapture()
		if err == io.EOF {
			// Go's TAR reader stops at its first all-zero end block. Require the
			// second 512-byte block and zero-only padding as well.
			if len(captured) < 512 || !allZero(captured) {
				return DataSummary{}, ErrTarInvalid
			}
			if len(captured) < 1024 {
				var second [512]byte
				if _, readErr := io.ReadFull(observer, second[:]); readErr != nil || !allZero(second[:]) {
					return DataSummary{}, tarReadError(ctx, readErr)
				}
			}
			var tail [32 * 1024]byte
			for {
				n, readErr := observer.Read(tail[:])
				if !allZero(tail[:n]) {
					return DataSummary{}, ErrTarInvalid
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return DataSummary{}, tarReadError(ctx, readErr)
				}
			}
			if observer.total < 1024 || observer.total%512 != 0 || observer.total > MaxDataTarBytes {
				return DataSummary{}, ErrTarTooLarge
			}
			if summary.Files+summary.Directories == 0 {
				return DataSummary{}, ErrTarInvalid
			}
			return summary, nil
		}
		if err != nil {
			return DataSummary{}, tarReadError(ctx, err)
		}
		if summary.Files+summary.Directories >= limits.maxEntries {
			return DataSummary{}, ErrTarTooLarge
		}
		if h == nil || h.Size < 0 || h.Mode < 0 || h.Mode&^0777 != 0 ||
			!supportedOwner(h.Uid) || !supportedOwner(h.Gid) || !validPAX(h.PAXRecords) {
			return DataSummary{}, ErrTarInvalid
		}
		isDir := h.Typeflag == tar.TypeDir
		if !isDir && h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return DataSummary{}, ErrTarSpecialEntry
		}
		name, pathErr := normalizeTarPathWithDepth(h.Name, isDir, limits.maxPathDepth)
		if pathErr != nil {
			return DataSummary{}, pathErr
		}
		depth := uint64(strings.Count(name, "/") + 1)
		newPathBytes := uint64(len(name))
		newAncestorRefs := depth - 1
		if pathBytes > limits.maxPathBytes || newPathBytes > limits.maxPathBytes-pathBytes ||
			ancestorRefs > limits.maxAncestorRefs || newAncestorRefs > limits.maxAncestorRefs-ancestorRefs {
			return DataSummary{}, ErrTarTooLarge
		}
		if _, exists := nodes[name]; exists {
			return DataSummary{}, ErrTarUnsafePath
		}
		if isDir {
			if h.Size != 0 {
				return DataSummary{}, ErrTarInvalid
			}
		} else {
			if h.Size > MaxDataFileBytes || summary.Bytes > limits.maxExpandedBytes || uint64(h.Size) > limits.maxExpandedBytes-summary.Bytes {
				return DataSummary{}, ErrTarTooLarge
			}
			if descendants[name] != 0 {
				return DataSummary{}, ErrTarUnsafePath
			}
			if _, err := io.CopyN(io.Discard, tr, h.Size); err != nil {
				return DataSummary{}, tarReadError(ctx, err)
			}
			summary.Files++
			summary.Bytes += uint64(h.Size)
		}
		if hasFileAncestor(nodes, name) {
			return DataSummary{}, ErrTarUnsafePath
		}
		nodes[name] = isDir
		for parent := parentTarPath(name); parent != ""; parent = parentTarPath(parent) {
			descendants[parent]++
		}
		pathBytes += newPathBytes
		ancestorRefs += newAncestorRefs
		if isDir {
			summary.Directories++
		}
		if observer.total > MaxDataTarBytes {
			return DataSummary{}, ErrTarTooLarge
		}
	}
}

func tarReadError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ErrArchiveCanceled
	}
	if errors.Is(err, ErrTarTooLarge) {
		return ErrTarTooLarge
	}
	return ErrTarInvalid
}

func supportedOwner(id int) bool { return id == 0 || id == 1000 }

func validPAX(records map[string]string) bool {
	for key := range records {
		switch key {
		case "path", "size", "mtime", "atime", "ctime", "comment", "charset", "hdrcharset":
		default:
			return false
		}
	}
	return true
}

func normalizeTarPath(name string, directory bool) (string, error) {
	return normalizeTarPathWithDepth(name, directory, MaxDataPathDepth)
}

func normalizeTarPathWithDepth(name string, directory bool, maxDepth int) (string, error) {
	if !utf8.ValidString(name) || len(name) == 0 || len(name) > MaxDataPathBytes || strings.ContainsRune(name, '\\') {
		return "", ErrTarUnsafePath
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrTarUnsafePath
		}
	}
	if name == "." || name == "./" {
		if directory {
			return ".", nil
		}
		return "", ErrTarUnsafePath
	}
	if directory && strings.HasSuffix(name, "/") {
		name = strings.TrimSuffix(name, "/")
	}
	if strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	if name == "" || strings.HasPrefix(name, "/") || path.IsAbs(name) || path.Clean(name) != name {
		return "", ErrTarUnsafePath
	}
	parts := strings.Split(name, "/")
	if maxDepth <= 0 || len(parts) > maxDepth {
		return "", ErrTarUnsafePath
	}
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") {
			return "", ErrTarUnsafePath
		}
		if i == 0 && (strings.EqualFold(part, ".rehearse-runtime") || strings.EqualFold(part, ".rehearse-runtime/")) {
			return "", ErrTarUnsafePath
		}
		if isWindowsDeviceName(part) {
			return "", ErrTarUnsafePath
		}
	}
	return name, nil
}

func isWindowsDeviceName(part string) bool {
	base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

func hasFileAncestor(nodes map[string]bool, name string) bool {
	for parent := parentTarPath(name); parent != ""; parent = parentTarPath(parent) {
		if isDir, ok := nodes[parent]; ok && !isDir {
			return true
		}
	}
	return false
}

func parentTarPath(name string) string {
	separator := strings.LastIndexByte(name, '/')
	if separator <= 0 {
		return ""
	}
	return name[:separator]
}

func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}
