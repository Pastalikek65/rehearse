package forgejo

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"slices"
	"strings"
)

// Archive is a validated view over the caller-owned ReaderAt. It keeps the
// same ReaderAt-backed ZIP index for subsequent member reads and never closes
// the reader supplied to OpenArchive.
type Archive struct {
	reader      *zip.Reader
	manifest    Manifest
	members     map[string]*zip.File
	dataSummary DataSummary
}

// OpenArchive validates the exact ZIP shape, canonical manifest, every
// payload digest, the PGDMP signature, and the complete inner data TAR before
// returning any restore member. The caller owns and must retain r for the
// lifetime of the returned Archive.
func OpenArchive(ctx context.Context, r io.ReaderAt, size int64) (*Archive, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrArchiveCanceled
	}
	if r == nil || size <= 0 {
		return nil, ErrArchiveInvalid
	}
	if size > MaxArchiveBytes {
		return nil, ErrArchiveTooLarge
	}
	if err := preflightZIP(ctx, r, size); err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(r, size)
	if err != nil || len(reader.File) != 3 {
		return nil, ErrArchiveInvalid
	}
	files := make(map[string]*zip.File, 3)
	for _, file := range reader.File {
		if ctx.Err() != nil {
			return nil, ErrArchiveCanceled
		}
		if file == nil || file.Name == "" || file.FileInfo().IsDir() || !file.Mode().IsRegular() ||
			file.Flags&1 != 0 || (file.Method != zip.Store && file.Method != zip.Deflate) {
			return nil, ErrArchiveMember
		}
		if _, duplicate := files[file.Name]; duplicate {
			return nil, ErrArchiveMember
		}
		files[file.Name] = file
	}
	manifestFile, ok := files[ManifestMember]
	if !ok || files[DatabaseMember] == nil || files[DataMember] == nil {
		return nil, ErrArchiveMember
	}
	if manifestFile.UncompressedSize64 > MaxManifestBytes {
		return nil, ErrArchiveTooLarge
	}
	manifestBytes, err := readZipMember(ctx, manifestFile, MaxManifestBytes)
	if err != nil {
		return nil, err
	}
	manifest, err := parseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	databaseFile, dataFile := files[DatabaseMember], files[DataMember]
	if err := validateMemberHeader(databaseFile, manifest.Members[0], MaxDatabaseBytes); err != nil {
		return nil, err
	}
	if err := validateMemberHeader(dataFile, manifest.Members[1], MaxDataTarBytes); err != nil {
		return nil, err
	}
	if databaseFile.UncompressedSize64+dataFile.UncompressedSize64 > uint64(MaxArchiveBytes) {
		return nil, ErrArchiveTooLarge
	}
	if err := verifyDump(ctx, databaseFile, manifest.Members[0]); err != nil {
		return nil, err
	}
	dataSummary, err := verifyData(ctx, dataFile, manifest.Members[1])
	if err != nil {
		return nil, err
	}
	return &Archive{
		reader:      reader,
		manifest:    cloneManifest(manifest),
		members:     files,
		dataSummary: dataSummary,
	}, nil
}

const maxCentralDirectoryBytes = 64 << 10

// preflightZIP bounds ZIP metadata before archive/zip allocates its file table.
// Rehearse archives have exactly three regular entries and do not use ZIP64,
// comments, per-entry extras, split disks, or self-extracting prefixes.
func preflightZIP(ctx context.Context, r io.ReaderAt, size int64) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrArchiveCanceled
	}
	if size < 22 {
		return ErrArchiveInvalid
	}
	tailSize := size
	const maxEOCDSearch = 22 + 65535
	if tailSize > maxEOCDSearch {
		tailSize = maxEOCDSearch
	}
	tail := make([]byte, tailSize)
	if _, err := r.ReadAt(tail, size-tailSize); err != nil {
		return ErrArchiveInvalid
	}
	eocd := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if string(tail[i:i+4]) != "PK\x05\x06" {
			continue
		}
		commentSize := int(binary.LittleEndian.Uint16(tail[i+20 : i+22]))
		if i+22+commentSize == len(tail) && commentSize == 0 {
			eocd = i
			break
		}
	}
	if eocd < 0 || ctx.Err() != nil {
		if ctx.Err() != nil {
			return ErrArchiveCanceled
		}
		return ErrArchiveInvalid
	}
	entry := tail[eocd : eocd+22]
	if binary.LittleEndian.Uint16(entry[4:6]) != 0 || binary.LittleEndian.Uint16(entry[6:8]) != 0 ||
		binary.LittleEndian.Uint16(entry[8:10]) != 3 || binary.LittleEndian.Uint16(entry[10:12]) != 3 {
		return ErrArchiveMember
	}
	centralSize := binary.LittleEndian.Uint32(entry[12:16])
	centralOffset := binary.LittleEndian.Uint32(entry[16:20])
	if centralSize == ^uint32(0) || centralOffset == ^uint32(0) || uint64(centralSize) > maxCentralDirectoryBytes {
		return ErrArchiveTooLarge
	}
	eocdAbsolute := uint64(size - tailSize + int64(eocd))
	if uint64(centralOffset)+uint64(centralSize) != eocdAbsolute {
		return ErrArchiveInvalid
	}
	central := make([]byte, int(centralSize))
	if _, err := r.ReadAt(central, int64(centralOffset)); err != nil {
		return ErrArchiveInvalid
	}
	want := []string{ManifestMember, DatabaseMember, DataMember}
	seen := make(map[string]bool, len(want))
	entries := make([]zipPreflightEntry, 0, len(want))
	position := 0
	for i := 0; i < 3; i++ {
		if ctx.Err() != nil {
			return ErrArchiveCanceled
		}
		if len(central)-position < 46 || string(central[position:position+4]) != "PK\x01\x02" {
			return ErrArchiveInvalid
		}
		record := central[position : position+46]
		flags := binary.LittleEndian.Uint16(record[8:10])
		method := binary.LittleEndian.Uint16(record[10:12])
		compressed := binary.LittleEndian.Uint32(record[20:24])
		uncompressed := binary.LittleEndian.Uint32(record[24:28])
		crc := binary.LittleEndian.Uint32(record[16:20])
		nameSize := int(binary.LittleEndian.Uint16(record[28:30]))
		extraSize := int(binary.LittleEndian.Uint16(record[30:32]))
		commentSize := int(binary.LittleEndian.Uint16(record[32:34]))
		disk := binary.LittleEndian.Uint16(record[34:36])
		localOffset := binary.LittleEndian.Uint32(record[42:46])
		if nameSize == 0 || extraSize != 0 || commentSize != 0 || disk != 0 ||
			compressed == ^uint32(0) || uncompressed == ^uint32(0) || localOffset == ^uint32(0) ||
			flags != 8 || (method != zip.Store && method != zip.Deflate) || uint64(localOffset) >= uint64(centralOffset) ||
			position+46+nameSize > len(central) {
			return ErrArchiveMember
		}
		nameBytes := central[position+46 : position+46+nameSize]
		name := string(nameBytes)
		if i >= len(want) || name != want[i] || seen[name] {
			return ErrArchiveMember
		}
		seen[name] = true
		entries = append(entries, zipPreflightEntry{
			name: name, method: method, crc: crc,
			compressed: compressed, uncompressed: uncompressed,
			localOffset: localOffset,
		})
		if uint64(compressed) > uint64(MaxArchiveBytes) || uint64(uncompressed) > uint64(MaxArchiveBytes) {
			return ErrArchiveTooLarge
		}
		position += 46 + nameSize
	}
	if position != len(central) {
		return ErrArchiveInvalid
	}
	if err := validateLocalZIPRecords(ctx, r, entries, uint64(centralOffset)); err != nil {
		return err
	}
	return nil
}

type zipPreflightEntry struct {
	name                     string
	method                   uint16
	crc                      uint32
	compressed, uncompressed uint32
	localOffset              uint32
}

// validateLocalZIPRecords requires the fixed writer layout: no prefix, gaps,
// hidden local entries, per-entry extra fields, or data outside the three
// central-directory records. ZIP metadata remains bounded to three small
// headers and descriptors before archive/zip allocates its index.
func validateLocalZIPRecords(ctx context.Context, r io.ReaderAt, entries []zipPreflightEntry, centralOffset uint64) error {
	var expectedOffset uint64
	for _, entry := range entries {
		if ctx == nil || ctx.Err() != nil {
			return ErrArchiveCanceled
		}
		if uint64(entry.localOffset) != expectedOffset {
			return ErrArchiveInvalid
		}
		var header [30]byte
		if _, err := r.ReadAt(header[:], int64(expectedOffset)); err != nil || string(header[:4]) != "PK\x03\x04" {
			return ErrArchiveInvalid
		}
		flags := binary.LittleEndian.Uint16(header[6:8])
		method := binary.LittleEndian.Uint16(header[8:10])
		crc := binary.LittleEndian.Uint32(header[14:18])
		compressed := binary.LittleEndian.Uint32(header[18:22])
		uncompressed := binary.LittleEndian.Uint32(header[22:26])
		nameSize := uint64(binary.LittleEndian.Uint16(header[26:28]))
		extraSize := binary.LittleEndian.Uint16(header[28:30])
		if flags != 8 || method != entry.method || crc != 0 || compressed != 0 || uncompressed != 0 ||
			extraSize != 0 || nameSize != uint64(len(entry.name)) {
			return ErrArchiveMember
		}
		nameBytes := make([]byte, nameSize)
		if _, err := r.ReadAt(nameBytes, int64(expectedOffset)+30); err != nil || string(nameBytes) != entry.name {
			return ErrArchiveMember
		}
		dataOffset := expectedOffset + 30 + nameSize
		descriptorOffset := dataOffset + uint64(entry.compressed)
		if descriptorOffset+16 > centralOffset {
			return ErrArchiveInvalid
		}
		var descriptor [16]byte
		if _, err := r.ReadAt(descriptor[:], int64(descriptorOffset)); err != nil ||
			binary.LittleEndian.Uint32(descriptor[0:4]) != 0x08074b50 ||
			binary.LittleEndian.Uint32(descriptor[4:8]) != entry.crc ||
			binary.LittleEndian.Uint32(descriptor[8:12]) != entry.compressed ||
			binary.LittleEndian.Uint32(descriptor[12:16]) != entry.uncompressed {
			return ErrArchiveInvalid
		}
		expectedOffset = descriptorOffset + 16
	}
	if expectedOffset != centralOffset {
		return ErrArchiveInvalid
	}
	return nil
}

// Manifest returns a copy of the validated manifest.
func (a *Archive) Manifest() Manifest {
	if a == nil {
		return Manifest{}
	}
	return cloneManifest(a.manifest)
}

// DataSummary returns structural counts from the validated data archive.
func (a *Archive) DataSummary() DataSummary {
	if a == nil {
		return DataSummary{}
	}
	return a.dataSummary
}

// OpenMember opens one exact restore payload from the same ReaderAt-backed ZIP
// reader used by OpenArchive. It does not close the caller's original reader.
func (a *Archive) OpenMember(ctx context.Context, name string) (io.ReadCloser, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrArchiveCanceled
	}
	if a == nil || name != DatabaseMember && name != DataMember {
		return nil, ErrArchiveMember
	}
	file := a.members[name]
	if file == nil {
		return nil, ErrArchiveMember
	}
	var expected Member
	for _, member := range a.manifest.Members {
		if member.Name == name {
			expected = member
			break
		}
	}
	if expected.Name != name || expected.Size == 0 || len(expected.SHA256) != 64 {
		return nil, ErrArchiveMember
	}
	r, err := file.Open()
	if err != nil {
		return nil, ErrArchiveInvalid
	}
	return &verifiedMemberReadCloser{
		ctx: ctx, r: r, expectedSize: expected.Size, expectedSHA256: expected.SHA256, hash: sha256.New(),
	}, nil
}

type verifiedMemberReadCloser struct {
	ctx            context.Context
	r              io.ReadCloser
	expectedSize   uint64
	expectedSHA256 string
	hash           hash.Hash
	bytesRead      uint64
	verified       bool
	closed         bool
	terminalErr    error
}

func (r *verifiedMemberReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.closed {
		return 0, ErrArchiveInvalid
	}
	if r.terminalErr != nil {
		return 0, r.terminalErr
	}
	if r.verified {
		return 0, io.EOF
	}
	if r.ctx == nil || r.ctx.Err() != nil {
		r.terminalErr = ErrArchiveCanceled
		return 0, r.terminalErr
	}
	n, err := r.r.Read(p)
	if n > 0 {
		if uint64(n) > r.expectedSize-r.bytesRead {
			r.terminalErr = ErrArchiveInvalid
			return n, r.terminalErr
		}
		_, _ = r.hash.Write(p[:n])
		r.bytesRead += uint64(n)
	}
	if err == io.EOF {
		if r.bytesRead != r.expectedSize || hexDigest(r.hash) != r.expectedSHA256 {
			r.terminalErr = ErrArchiveInvalid
			return n, r.terminalErr
		}
		r.verified = true
		return n, io.EOF
	}
	if err != nil {
		if r.ctx.Err() != nil {
			r.terminalErr = ErrArchiveCanceled
		} else {
			r.terminalErr = ErrArchiveInvalid
		}
		return n, r.terminalErr
	}
	return n, nil
}

func (r *verifiedMemberReadCloser) Close() error {
	if r.closed {
		if r.terminalErr != nil {
			return r.terminalErr
		}
		if !r.verified {
			return ErrArchiveInvalid
		}
		return nil
	}
	r.closed = true
	closeErr := r.r.Close()
	if r.terminalErr != nil {
		return r.terminalErr
	}
	if !r.verified {
		if r.ctx == nil || r.ctx.Err() != nil {
			return ErrArchiveCanceled
		}
		return ErrArchiveInvalid
	}
	if closeErr != nil {
		return ErrArchiveInvalid
	}
	return nil
}

// WriteArchive creates a canonical offline archive from two caller-supplied
// files. Both source FDs remain open from prevalidation through the ZIP copy;
// hashes and file identity are checked again while writing. The caller must
// first stop Forgejo and PostgreSQL-dependent writers to ensure a consistent
// cross-file snapshot. On error, discard the partial destination.
func WriteArchive(ctx context.Context, dst io.Writer, databasePath, dataTarPath string) (Manifest, error) {
	if ctx == nil || ctx.Err() != nil {
		return Manifest{}, ErrArchiveCanceled
	}
	if dst == nil || databasePath == "" || dataTarPath == "" {
		return Manifest{}, ErrArchiveSource
	}
	database, err := openStableSource(databasePath, MaxDatabaseBytes)
	if err != nil {
		return Manifest{}, err
	}
	defer database.file.Close()
	data, err := openStableSource(dataTarPath, MaxDataTarBytes)
	if err != nil {
		return Manifest{}, err
	}
	defer data.file.Close()
	if os.SameFile(database.initialInfo, data.initialInfo) {
		return Manifest{}, ErrArchiveSource
	}

	databaseMember, err := inspectDumpSource(ctx, database)
	if err != nil {
		return Manifest{}, err
	}
	dataMember, err := inspectTarSource(ctx, data)
	if err != nil {
		return Manifest{}, err
	}
	if err := verifyStableSource(database); err != nil {
		return Manifest{}, err
	}
	if err := verifyStableSource(data); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		Format: ArchiveFormat, FormatVersion: ArchiveFormatVersion,
		SourceVersion: SourceVersion, TargetVersion: TargetVersion,
		PostgresVersion: PostgresVersion, SourceImage: SourceImage,
		TargetImage: TargetImage, PostgresImage: PostgresImage,
		Members: []Member{databaseMember, dataMember},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, ErrArchiveInvalid
	}
	limited := &limitWriter{w: dst, max: uint64(MaxArchiveBytes)}
	zw := zip.NewWriter(limited)
	if err := writeZipEntry(zw, ManifestMember, manifestBytes); err != nil {
		return Manifest{}, ErrArchiveInvalid
	}
	if err := zw.Flush(); err != nil {
		if limited.tooLarge {
			return Manifest{}, ErrArchiveTooLarge
		}
		return Manifest{}, ErrArchiveInvalid
	}
	if err := verifyStableSource(database); err != nil {
		return Manifest{}, err
	}
	if err := verifyStableSource(data); err != nil {
		return Manifest{}, err
	}
	databaseEntry, err := zw.CreateHeader(zipHeader(DatabaseMember))
	if err != nil {
		return Manifest{}, ErrArchiveInvalid
	}
	if err := copyStableZipMember(ctx, databaseEntry, database, databaseMember, MaxDatabaseBytes); err != nil {
		return Manifest{}, err
	}
	dataEntry, err := zw.CreateHeader(zipHeader(DataMember))
	if err != nil {
		return Manifest{}, ErrArchiveInvalid
	}
	if err := zw.Flush(); err != nil {
		return Manifest{}, ErrArchiveInvalid
	}
	if err := verifyStableSource(database); err != nil {
		return Manifest{}, err
	}
	if err := verifyStableSource(data); err != nil {
		return Manifest{}, err
	}
	if err := copyStableZipMember(ctx, dataEntry, data, dataMember, MaxDataTarBytes); err != nil {
		return Manifest{}, err
	}
	if err := zw.Close(); err != nil {
		if limited.tooLarge {
			return Manifest{}, ErrArchiveTooLarge
		}
		return Manifest{}, ErrArchiveInvalid
	}
	if limited.tooLarge {
		return Manifest{}, ErrArchiveTooLarge
	}
	if err := verifyStableSource(database); err != nil {
		return Manifest{}, err
	}
	if err := verifyStableSource(data); err != nil {
		return Manifest{}, err
	}
	return cloneManifest(manifest), nil
}

type stableSource struct {
	path        string
	file        *os.File
	initialInfo os.FileInfo
}

func openStableSource(path string, limit uint64) (*stableSource, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() < 0 || uint64(before.Size()) > limit {
		return nil, ErrArchiveSource
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrArchiveSource
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, ErrArchiveSource
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) {
		_ = file.Close()
		return nil, ErrArchiveSource
	}
	return &stableSource{path: path, file: file, initialInfo: opened}, nil
}

func verifyStableSource(source *stableSource) error {
	opened, err := source.file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(source.initialInfo, opened) ||
		opened.Size() != source.initialInfo.Size() || !opened.ModTime().Equal(source.initialInfo.ModTime()) {
		return ErrSourceChanged
	}
	pathInfo, err := os.Lstat(source.path)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(opened, pathInfo) {
		return ErrSourceChanged
	}
	return nil
}

func inspectDumpSource(ctx context.Context, source *stableSource) (Member, error) {
	if err := seekStart(source.file); err != nil {
		return Member{}, ErrArchiveSource
	}
	h := sha256.New()
	cr := &contextReader{ctx: ctx, r: source.file}
	counted := &countingReader{r: io.TeeReader(cr, h)}
	var header [5]byte
	if _, err := io.ReadFull(counted, header[:]); err != nil || string(header[:]) != "PGDMP" {
		if ctx.Err() != nil {
			return Member{}, ErrArchiveCanceled
		}
		return Member{}, ErrArchiveInvalid
	}
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return Member{}, archiveReadError(cr, err)
	}
	if counted.bytes != uint64(source.initialInfo.Size()) || counted.bytes > MaxDatabaseBytes {
		return Member{}, ErrSourceChanged
	}
	return Member{Name: DatabaseMember, Size: counted.bytes, SHA256: hexDigest(h)}, nil
}

func inspectTarSource(ctx context.Context, source *stableSource) (Member, error) {
	if err := seekStart(source.file); err != nil {
		return Member{}, ErrArchiveSource
	}
	h := sha256.New()
	cr := &contextReader{ctx: ctx, r: source.file}
	counted := &countingReader{r: io.TeeReader(cr, h)}
	if _, err := ValidateDataTar(ctx, counted); err != nil {
		return Member{}, err
	}
	if counted.bytes != uint64(source.initialInfo.Size()) || counted.bytes > MaxDataTarBytes {
		return Member{}, ErrSourceChanged
	}
	return Member{Name: DataMember, Size: counted.bytes, SHA256: hexDigest(h)}, nil
}

func copyStableZipMember(ctx context.Context, entry io.Writer, source *stableSource, expected Member, limit uint64) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrArchiveCanceled
	}
	if err := verifyStableSource(source); err != nil {
		return err
	}
	if err := seekStart(source.file); err != nil {
		return ErrArchiveSource
	}
	h := sha256.New()
	cr := &contextReader{ctx: ctx, r: source.file}
	counted := &countingReader{r: io.TeeReader(cr, h)}
	if _, err := io.Copy(entry, counted); err != nil {
		return archiveReadError(cr, err)
	}
	if counted.bytes != expected.Size || counted.bytes > limit || hexDigest(h) != expected.SHA256 {
		return ErrSourceChanged
	}
	if err := verifyStableSource(source); err != nil {
		return err
	}
	return nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	entry, err := zw.CreateHeader(zipHeader(name))
	if err != nil {
		return err
	}
	_, err = entry.Write(data)
	return err
}

func zipHeader(name string) *zip.FileHeader {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, ModifiedDate: (1 << 5) | 1}
	header.SetMode(0600)
	return header
}

type limitWriter struct {
	w        io.Writer
	max      uint64
	written  uint64
	tooLarge bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if uint64(len(p)) > w.max-w.written {
		w.tooLarge = true
		return 0, ErrArchiveTooLarge
	}
	n, err := w.w.Write(p)
	w.written += uint64(n)
	return n, err
}

func parseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, ErrArchiveInvalid
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Manifest{}, ErrArchiveInvalid
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, data) {
		return Manifest{}, ErrArchiveInvalid
	}
	if manifest.Format != ArchiveFormat || manifest.FormatVersion != ArchiveFormatVersion ||
		manifest.SourceVersion != SourceVersion || manifest.TargetVersion != TargetVersion ||
		manifest.PostgresVersion != PostgresVersion || manifest.SourceImage != SourceImage ||
		manifest.TargetImage != TargetImage || manifest.PostgresImage != PostgresImage || len(manifest.Members) != 2 {
		return Manifest{}, ErrArchiveInvalid
	}
	if manifest.Members[0].Name != DatabaseMember || manifest.Members[1].Name != DataMember {
		return Manifest{}, ErrArchiveInvalid
	}
	for _, member := range manifest.Members {
		if len(member.SHA256) != 64 || strings.ToLower(member.SHA256) != member.SHA256 {
			return Manifest{}, ErrArchiveInvalid
		}
		if _, err := hex.DecodeString(member.SHA256); err != nil {
			return Manifest{}, ErrArchiveInvalid
		}
	}
	return manifest, nil
}

func validateMemberHeader(file *zip.File, member Member, limit uint64) error {
	if file == nil || member.Name != file.Name || member.Size == 0 || member.Size > limit ||
		file.UncompressedSize64 != member.Size {
		if member.Size > limit || file != nil && file.UncompressedSize64 > limit {
			return ErrArchiveTooLarge
		}
		return ErrArchiveMember
	}
	return nil
}

func readZipMember(ctx context.Context, file *zip.File, limit uint64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ErrArchiveCanceled
	}
	r, err := file.Open()
	if err != nil {
		return nil, ErrArchiveInvalid
	}
	cr := &contextReader{ctx: ctx, r: r}
	data, readErr := io.ReadAll(io.LimitReader(cr, int64(limit)+1))
	closeErr := r.Close()
	if ctx.Err() != nil {
		return nil, ErrArchiveCanceled
	}
	if readErr != nil || closeErr != nil {
		return nil, ErrArchiveInvalid
	}
	if uint64(len(data)) > limit || uint64(len(data)) != file.UncompressedSize64 {
		return nil, ErrArchiveTooLarge
	}
	return data, nil
}

func verifyDump(ctx context.Context, file *zip.File, member Member) error {
	r, err := file.Open()
	if err != nil {
		return ErrArchiveInvalid
	}
	defer r.Close()
	h := sha256.New()
	cr := &contextReader{ctx: ctx, r: r}
	counted := &countingReader{r: io.TeeReader(cr, h)}
	var header [5]byte
	if _, err := io.ReadFull(counted, header[:]); err != nil || string(header[:]) != "PGDMP" {
		if ctx.Err() != nil {
			return ErrArchiveCanceled
		}
		return ErrArchiveInvalid
	}
	if _, err := io.Copy(io.Discard, counted); err != nil {
		return archiveReadError(cr, err)
	}
	if err := r.Close(); err != nil || counted.bytes != member.Size || hexDigest(h) != member.SHA256 {
		return ErrArchiveInvalid
	}
	return nil
}

func verifyData(ctx context.Context, file *zip.File, member Member) (DataSummary, error) {
	r, err := file.Open()
	if err != nil {
		return DataSummary{}, ErrArchiveInvalid
	}
	defer r.Close()
	h := sha256.New()
	cr := &contextReader{ctx: ctx, r: r}
	counted := &countingReader{r: io.TeeReader(cr, h)}
	summary, err := ValidateDataTar(ctx, counted)
	if err != nil {
		return DataSummary{}, err
	}
	if err := r.Close(); err != nil || counted.bytes != member.Size || hexDigest(h) != member.SHA256 {
		return DataSummary{}, ErrArchiveInvalid
	}
	return summary, nil
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.Members = slices.Clone(manifest.Members)
	return manifest
}

func seekStart(file *os.File) error {
	_, err := file.Seek(0, io.SeekStart)
	return err
}

func hexDigest(hash hash.Hash) string { return hex.EncodeToString(hash.Sum(nil)) }
