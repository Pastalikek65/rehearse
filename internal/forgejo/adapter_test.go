package forgejo

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPinnedContractAndSchemaExpectations(t *testing.T) {
	if SourceVersion != "15.0.9" || TargetVersion != "16.0.5" || PostgresVersion != "17.11" {
		t.Fatalf("unexpected reviewed version pair: %q -> %q on %q", SourceVersion, TargetVersion, PostgresVersion)
	}
	for _, image := range []string{SourceImage, TargetImage, PostgresImage, ProbeImage} {
		if !strings.Contains(image, "@sha256:") || strings.Contains(image, ":latest") {
			t.Fatalf("image is not pinned by digest: %q", image)
		}
	}
	source := SourceSchemaExpectation()
	target := TargetSchemaExpectation()
	if source.GiteaVersion != 305 || target.GiteaVersion != 305 ||
		source.ForgejoVersion != 44 || target.ForgejoVersion != 44 {
		t.Fatalf("schema versions differ from reviewed tag source: source=%+v target=%+v", source, target)
	}
	if len(source.MigrationIDs) != 28 || len(target.MigrationIDs) != 39 {
		t.Fatalf("migration ID set sizes differ from reviewed tag source: source=%d target=%d", len(source.MigrationIDs), len(target.MigrationIDs))
	}
	if source.MigrationIDs[0] != "v14a_actions-approval-and-trust" ||
		target.MigrationIDs[len(target.MigrationIDs)-1] != "v17a_add-action-run-workflow-source-commit" {
		t.Fatalf("expected migration IDs were not loaded from tagged registration sources")
	}
}

func TestParseSchemaProjectionRequiresExactOrderedRows(t *testing.T) {
	source := SourceSchemaExpectation()
	var lines []string
	lines = append(lines, `[`+`"version",1,305`+`]`)
	lines = append(lines, `[`+`"forgejo_version",1,44`+`]`)
	for _, id := range source.MigrationIDs {
		encoded, err := json.Marshal([]any{"forgejo_migration", id})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(encoded))
	}
	// SQL emits groups in bytewise order: Gitea version, Forgejo version,
	// then registered migration IDs in bytewise order.
	input := strings.Join(lines, "\n") + "\n"
	got, err := ParseSchemaProjection(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseSchemaProjection: %v", err)
	}
	if !SchemaMatches(got, source) {
		t.Fatalf("schema did not match source expectation: %+v", got)
	}
	if SchemaMatches(got, TargetSchemaExpectation()) {
		t.Fatal("source schema matched target expectation")
	}

	bad := strings.Replace(input, `"version",1,305`, `"version",1,304`, 1)
	badSnapshot, err := ParseSchemaProjection(strings.NewReader(bad))
	if err != nil || SchemaMatches(badSnapshot, source) {
		t.Fatal("unexpected schema version matched source expectation")
	}
	duplicate := input + lines[len(lines)-1] + "\n"
	if _, err := ParseSchemaProjection(strings.NewReader(duplicate)); err == nil {
		t.Fatal("duplicate migration ID was accepted")
	}
}

func TestProjectionSQLUsesOnlyStableSharedIdentityFields(t *testing.T) {
	for _, required := range []string{"\"user\"", "lower_name", "is_active", "is_admin", "repository", "owner_id", "default_branch", "object_format_name"} {
		if !strings.Contains(ProjectionSQL, required) {
			t.Fatalf("ProjectionSQL omitted %q", required)
		}
	}
	for _, unstable := range []string{"password", "passwd", "email", "created_unix", "updated_unix", "size", "num_watches"} {
		if strings.Contains(strings.ToLower(ProjectionSQL), unstable) {
			t.Fatalf("ProjectionSQL unexpectedly includes unstable/private field %q", unstable)
		}
	}
	for _, required := range []string{"version", "forgejo_version", "forgejo_migration", "id", "version"} {
		if !strings.Contains(SchemaSQL, required) {
			t.Fatalf("SchemaSQL omitted tracker %q", required)
		}
	}
	if strings.Contains(SchemaSQL, "created_unix") {
		t.Fatal("SchemaSQL must not fingerprint migration timestamps")
	}
}

type tarEntry struct {
	header tar.Header
	body   []byte
}

func makeTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, entry := range entries {
		h := entry.header
		if h.Mode == 0 {
			h.Mode = 0600
		}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Size == 0 && len(entry.body) > 0 {
			h.Size = int64(len(entry.body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) > 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func validDataTar(t *testing.T) []byte {
	t.Helper()
	return makeTar(t,
		tarEntry{header: tar.Header{Name: "gitea", Typeflag: tar.TypeDir}},
		tarEntry{header: tar.Header{Name: "gitea/conf", Typeflag: tar.TypeDir}},
		tarEntry{header: tar.Header{Name: "gitea/conf/app.ini"}, body: []byte("synthetic config\n")},
		tarEntry{header: tar.Header{Name: "gitea/repositories/fixture/sample.git/HEAD"}, body: []byte("ref: refs/heads/main\n")},
	)
}

func validDump() []byte { return []byte("PGDMP\x01\x0f\x00synthetic custom dump") }

func archiveManifest(db, data []byte) Manifest {
	return Manifest{
		Format:          ArchiveFormat,
		FormatVersion:   ArchiveFormatVersion,
		SourceVersion:   SourceVersion,
		TargetVersion:   TargetVersion,
		PostgresVersion: PostgresVersion,
		SourceImage:     SourceImage,
		TargetImage:     TargetImage,
		PostgresImage:   PostgresImage,
		Members: []Member{
			{Name: DatabaseMember, Size: uint64(len(db)), SHA256: shaHex(db)},
			{Name: DataMember, Size: uint64(len(data)), SHA256: shaHex(data)},
		},
	}
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func makeArchive(t *testing.T, db, data []byte, manifest Manifest, extra ...string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	entries := []struct {
		name string
		data []byte
	}{{ManifestMember, manifestBytes}, {DatabaseMember, db}, {DataMember, data}}
	for _, name := range extra {
		entries = append(entries, struct {
			name string
			data []byte
		}{name, []byte("extra")})
	}
	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestArchiveRoundTripAndCallerOwnsReader(t *testing.T) {
	ctx := context.Background()
	db, data := validDump(), validDataTar(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "database.pgdump")
	dataPath := filepath.Join(dir, "forgejo-data.tar")
	if err := os.WriteFile(dbPath, db, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	manifest, err := WriteArchive(ctx, &out, dbPath, dataPath)
	if err != nil {
		t.Fatalf("WriteArchive: %v", err)
	}
	if manifest.SourceVersion != SourceVersion || len(manifest.Members) != 2 {
		t.Fatalf("unexpected returned manifest: %+v", manifest)
	}
	reader := bytes.NewReader(out.Bytes())
	archive, err := OpenArchive(ctx, reader, int64(reader.Len()))
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	if got := archive.Manifest(); got.FormatVersion != ArchiveFormatVersion || len(got.Members) != 2 {
		t.Fatalf("unexpected parsed manifest: %+v", got)
	}
	for name, expected := range map[string][]byte{DatabaseMember: db, DataMember: data} {
		member, err := archive.OpenMember(ctx, name)
		if err != nil {
			t.Fatalf("OpenMember(%q): %v", name, err)
		}
		got, readErr := io.ReadAll(member)
		closeErr := member.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(got, expected) {
			t.Fatalf("member %q mismatch: read=%v close=%v", name, readErr, closeErr)
		}
	}
	if _, err := archive.OpenMember(ctx, ManifestMember); err == nil {
		t.Fatal("manifest member was exposed as a restore payload")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("OpenArchive took ownership of caller reader: %v", err)
	}
}

func TestOpenArchiveRejectsMalformedAndMismatchedMembers(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenArchive(ctx, bytes.NewReader([]byte("not a zip")), int64(len("not a zip"))); err == nil {
		t.Fatal("malformed ZIP accepted")
	}
	db, data := validDump(), validDataTar(t)
	manifest := archiveManifest(db, data)
	valid := makeArchive(t, db, data, manifest)
	for name, archiveBytes := range map[string][]byte{
		"extra member":            makeArchive(t, db, data, manifest, "unexpected.bin"),
		"bad custom dump":         makeArchive(t, []byte("not pg_dump"), data, archiveManifest([]byte("not pg_dump"), data)),
		"payload digest mismatch": makeArchive(t, append([]byte(nil), db...), data, func() Manifest { m := manifest; m.Members[0].SHA256 = strings.Repeat("0", 64); return m }()),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := OpenArchive(ctx, bytes.NewReader(archiveBytes), int64(len(archiveBytes))); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
	wrongPin := manifest
	wrongPin.TargetVersion = "16.0.4"
	wrongPinBytes := makeArchive(t, db, data, wrongPin)
	if _, err := OpenArchive(ctx, bytes.NewReader(wrongPinBytes), int64(len(wrongPinBytes))); err == nil {
		t.Fatal("archive for another target pair accepted")
	}
	if _, err := OpenArchive(ctx, bytes.NewReader(valid), int64(len(valid))); err != nil {
		t.Fatalf("valid archive rejected: %v", err)
	}
	badLocalFlags := append([]byte(nil), valid...)
	badLocalFlags[6] = 0
	if _, err := OpenArchive(ctx, bytes.NewReader(badLocalFlags), int64(len(badLocalFlags))); err == nil {
		t.Fatal("noncanonical local ZIP header was accepted")
	}
	withTrailingByte := append(append([]byte(nil), valid...), 1)
	if _, err := OpenArchive(ctx, bytes.NewReader(withTrailingByte), int64(len(withTrailingByte))); err == nil {
		t.Fatal("trailing ZIP payload was accepted")
	}

	// archive/zip accepts self-extracting prefixes; the rehearsal format does
	// not. Preflight must reject one before constructing the ZIP index.
	prefixed := append([]byte("prefix"), valid...)
	if _, err := OpenArchive(ctx, bytes.NewReader(prefixed), int64(len(prefixed))); err == nil {
		t.Fatal("self-extracting prefix was accepted")
	}
}

func TestZIPPreflightRejectsHugeCentralDirectoryClaimsBeforeParsing(t *testing.T) {
	for name, entries := range map[string]uint16{"too many entries": 0xffff, "three entries with huge CD": 3} {
		eocd := make([]byte, 22)
		copy(eocd, "PK\x05\x06")
		binary.LittleEndian.PutUint16(eocd[8:10], entries)
		binary.LittleEndian.PutUint16(eocd[10:12], entries)
		if entries == 3 {
			binary.LittleEndian.PutUint32(eocd[12:16], 0xffffffff)
			binary.LittleEndian.PutUint32(eocd[16:20], 0)
		}
		t.Run(name, func(t *testing.T) {
			if err := preflightZIP(context.Background(), bytes.NewReader(eocd), int64(len(eocd))); err == nil {
				t.Fatal("oversized or ZIP64 central directory claim was accepted")
			}
		})
	}
}

func TestValidateDataTarRejectsUnsafeAndSpecialEntries(t *testing.T) {
	ctx := context.Background()
	for name, header := range map[string]tar.Header{
		"parent traversal": {Name: "gitea/../../outside"},
		"absolute path":    {Name: "/etc/passwd"},
		"backslash":        {Name: `gitea\conf\app.ini`},
		"dot path":         {Name: "gitea/./app.ini"},
		"duplicate path":   {Name: "gitea/conf/app.ini"},
		"symlink":          {Name: "gitea/conf/link", Typeflag: tar.TypeSymlink, Linkname: "app.ini"},
		"hardlink":         {Name: "gitea/conf/hard", Typeflag: tar.TypeLink, Linkname: "gitea/conf/app.ini"},
		"fifo":             {Name: "gitea/run/queue", Typeflag: tar.TypeFifo},
		"device":           {Name: "gitea/device", Typeflag: tar.TypeChar},
	} {
		t.Run(name, func(t *testing.T) {
			entries := []tarEntry{{header: tar.Header{Name: "gitea", Typeflag: tar.TypeDir}}, {header: header}}
			if name == "duplicate path" {
				entries = append(entries, tarEntry{header: tar.Header{Name: "gitea/conf/app.ini"}})
			}
			data := makeTar(t, entries...)
			if _, err := ValidateDataTar(ctx, bytes.NewReader(data)); err == nil {
				t.Fatal("unsafe TAR accepted")
			}
		})
	}
	valid := validDataTar(t)
	if _, err := ValidateDataTar(ctx, bytes.NewReader(valid)); err != nil {
		t.Fatalf("valid TAR rejected: %v", err)
	}
	rootPrefixed := makeTar(t,
		tarEntry{header: tar.Header{Name: ".", Typeflag: tar.TypeDir}},
		tarEntry{header: tar.Header{Name: "./gitea/", Typeflag: tar.TypeDir}},
		tarEntry{header: tar.Header{Name: "./gitea/conf/app.ini"}, body: []byte("synthetic config\n")},
	)
	if _, err := ValidateDataTar(ctx, bytes.NewReader(rootPrefixed)); err != nil {
		t.Fatalf("standard dot-root TAR rejected: %v", err)
	}

	for name, header := range map[string]tar.Header{
		"reserved runtime directory": {Name: ".rehearse-runtime/app.ini"},
		"Windows device path":        {Name: "gitea/NUL.txt"},
		"setuid mode":                {Name: "gitea/bin/tool", Mode: 04755},
		"unsupported owner":          {Name: "gitea/private", Uid: 1234},
		"PAX xattr":                  {Name: "gitea/private", PAXRecords: map[string]string{"SCHILY.xattr.user.secret": "value"}},
	} {
		t.Run(name, func(t *testing.T) {
			data := makeTar(t, tarEntry{header: header, body: []byte("x")})
			if _, err := ValidateDataTar(ctx, bytes.NewReader(data)); err == nil {
				t.Fatal("unsafe TAR entry accepted")
			}
		})
	}
	if _, err := normalizeTarPath(string([]byte{0xff}), false); err == nil {
		t.Fatal("invalid UTF-8 path accepted")
	}
	if _, err := validateDataTar(ctx, bytes.NewReader(valid), 1); err == nil {
		t.Fatal("injected small entry limit was not enforced")
	}
	oversized := makeTar(t,
		tarEntry{header: tar.Header{Name: "gitea", Typeflag: tar.TypeDir}},
		tarEntry{header: tar.Header{Name: "gitea/large.bin"}},
	)
	setTARHeaderSize(t, oversized, 512, uint64(MaxDataFileBytes)+1)
	if _, err := ValidateDataTar(ctx, bytes.NewReader(oversized)); err == nil || !strings.Contains(err.Error(), ErrTarTooLarge.Error()) {
		t.Fatalf("oversized TAR file was not rejected with fixed size code: %v", err)
	}
	withTrailingPayload := append([]byte(nil), valid...)
	withTrailingPayload[len(withTrailingPayload)-1] = 'x'
	if _, err := ValidateDataTar(ctx, bytes.NewReader(withTrailingPayload)); err == nil {
		t.Fatal("nonzero data after TAR end markers was accepted")
	}
}

func setTARHeaderSize(t *testing.T, data []byte, offset int, size uint64) {
	t.Helper()
	if offset < 0 || offset+512 > len(data) || size > 077777777777 {
		t.Fatal("invalid synthetic TAR header mutation")
	}
	header := data[offset : offset+512]
	copy(header[124:136], fmt.Sprintf("%011o\x00", size))
	for i := 148; i < 156; i++ {
		header[i] = ' '
	}
	var checksum int64
	for _, value := range header {
		checksum += int64(value)
	}
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", checksum))
	if _, err := strconv.ParseUint(strings.TrimRight(string(header[148:154]), "\x00 "), 8, 64); err != nil {
		t.Fatalf("bad synthetic checksum field: %v", err)
	}
}

func TestArchiveAndTarCancellationAreFixed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ValidateDataTar(ctx, bytes.NewReader(validDataTar(t))); err == nil {
		t.Fatal("canceled TAR validation succeeded")
	}
	db, data := validDump(), validDataTar(t)
	archiveBytes := makeArchive(t, db, data, archiveManifest(db, data))
	if _, err := OpenArchive(ctx, bytes.NewReader(archiveBytes), int64(len(archiveBytes))); err == nil {
		t.Fatal("canceled archive validation succeeded")
	}
}

func TestOpenMemberRechecksPayloadAgainstFrozenManifest(t *testing.T) {
	ctx := context.Background()
	db, data := validDump(), validDataTar(t)
	archiveBytes := makeArchive(t, db, data, archiveManifest(db, data))
	mutable := &mutableReaderAt{data: bytes.Clone(archiveBytes)}
	archive, err := OpenArchive(ctx, mutable, int64(len(mutable.data)))
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatal(err)
	}
	var databaseOffset int64
	for _, file := range reader.File {
		if file.Name == DatabaseMember {
			databaseOffset, err = file.DataOffset()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if databaseOffset <= 0 || databaseOffset >= int64(len(mutable.data)) {
		t.Fatal("database member offset was not found")
	}
	mutable.data[databaseOffset] ^= 0xff
	member, err := archive.OpenMember(ctx, DatabaseMember)
	if err != nil {
		t.Fatalf("OpenMember: %v", err)
	}
	_, readErr := io.ReadAll(member)
	closeErr := member.Close()
	if !errors.Is(readErr, ErrArchiveInvalid) || !errors.Is(closeErr, ErrArchiveInvalid) {
		t.Fatalf("mutated backing bytes were not mapped to a fixed verification error: read=%v close=%v", readErr, closeErr)
	}

	validArchive, err := OpenArchive(ctx, bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatal(err)
	}
	validArchive.manifest.Members[0].SHA256 = strings.Repeat("0", 64)
	member, err = validArchive.OpenMember(ctx, DatabaseMember)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr = io.ReadAll(member)
	closeErr = member.Close()
	if !errors.Is(readErr, ErrArchiveInvalid) || !errors.Is(closeErr, ErrArchiveInvalid) {
		t.Fatalf("manifest digest mismatch was not detected at EOF: read=%v close=%v", readErr, closeErr)
	}

	validArchive, err = OpenArchive(ctx, bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatal(err)
	}
	validArchive.manifest.Members[0].Size++
	member, err = validArchive.OpenMember(ctx, DatabaseMember)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr = io.ReadAll(member)
	closeErr = member.Close()
	if !errors.Is(readErr, ErrArchiveInvalid) || !errors.Is(closeErr, ErrArchiveInvalid) {
		t.Fatalf("manifest size mismatch was not detected at EOF: read=%v close=%v", readErr, closeErr)
	}
}

type mutableReaderAt struct {
	data []byte
}

func (r *mutableReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestValidateDataTarEnforcesPathMetadataBudgets(t *testing.T) {
	ctx := context.Background()
	data := makeTar(t,
		tarEntry{header: tar.Header{Name: "root", Typeflag: tar.TypeDir}},
		tarEntry{header: tar.Header{Name: "root/child/file"}, body: []byte("x")},
	)
	limits := defaultDataTarLimits()
	limits.maxPathBytes = 5
	if _, err := validateDataTarWithLimits(ctx, bytes.NewReader(data), limits); err == nil {
		t.Fatal("aggregate path-byte budget was not enforced")
	}
	limits = defaultDataTarLimits()
	limits.maxAncestorRefs = 1
	if _, err := validateDataTarWithLimits(ctx, bytes.NewReader(data), limits); err == nil {
		t.Fatal("aggregate ancestor-reference budget was not enforced")
	}
	limits = defaultDataTarLimits()
	limits.maxPathDepth = 2
	if _, err := validateDataTarWithLimits(ctx, bytes.NewReader(data), limits); err == nil {
		t.Fatal("maximum path depth was not enforced")
	}
	deep := strings.TrimSuffix(strings.Repeat("x/", MaxDataPathDepth+1), "/")
	if _, err := normalizeTarPath(deep, false); err == nil {
		t.Fatal("maximum path depth was not enforced")
	}
}

func BenchmarkValidateDataTarManyEntries(b *testing.B) {
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for i := 0; i < 8000; i++ {
		h := &tar.Header{Name: fmt.Sprintf("repositories/user/repo.git/objects/ab/%08x", i), Mode: 0600, Typeflag: tar.TypeReg, Size: 1, Uid: 1000, Gid: 1000}
		if err := tw.WriteHeader(h); err != nil {
			b.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			b.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		b.Fatal(err)
	}
	archive := bytes.Clone(out.Bytes())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ValidateDataTar(context.Background(), bytes.NewReader(archive)); err != nil {
			b.Fatal(err)
		}
	}
}

func TestWriteArchiveRejectsSourceMutationAfterOpen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "database.pgdump")
	dataPath := filepath.Join(dir, "forgejo-data.tar")
	if err := os.WriteFile(dbPath, validDump(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, validDataTar(t), 0600); err != nil {
		t.Fatal(err)
	}
	mutating := &mutatingWriter{mutate: func() {
		if err := os.WriteFile(dbPath, []byte("PGDMP\x01\x0fchanged source"), 0600); err != nil {
			t.Errorf("mutate source: %v", err)
		}
	}}
	_, err := WriteArchive(context.Background(), mutating, dbPath, dataPath)
	if err == nil || !strings.Contains(err.Error(), "SOURCE_CHANGED") {
		t.Fatalf("source mutation was not rejected with a fixed code: %v", err)
	}
}

type mutatingWriter struct {
	mutate func()
	once   bool
	data   bytes.Buffer
}

func (w *mutatingWriter) Write(p []byte) (int, error) {
	if !w.once {
		w.once = true
		w.mutate()
	}
	return w.data.Write(p)
}

func TestForgejoAPIParsingAndCredentialConfig(t *testing.T) {
	if endpoint, err := RepositoryEndpoint("alice", "upgrade-fixture"); err != nil || endpoint != "/api/v1/repos/alice/upgrade-fixture" {
		t.Fatalf("RepositoryEndpoint=%q err=%v", endpoint, err)
	}
	if _, err := RepositoryEndpoint("../outside", "repo"); err == nil {
		t.Fatal("path traversal owner accepted")
	}
	contentsEndpoint, err := ContentsEndpoint("alice", "upgrade-fixture", "nested/hello.txt", "main")
	if err != nil || contentsEndpoint != "/api/v1/repos/alice/upgrade-fixture/contents/nested/hello.txt?ref=main" {
		t.Fatalf("ContentsEndpoint=%q err=%v", contentsEndpoint, err)
	}
	token := "synthetic-token-value"
	config, err := CurlConfig(contentsEndpoint, &Auth{Token: token})
	if err != nil || !bytes.Contains(config, []byte("Authorization: token "+token)) || !bytes.Contains(config, []byte(`url = "http://app:3000`)) {
		t.Fatalf("unexpected curl config or error: %v", err)
	}
	if _, err := CurlConfig(contentsEndpoint, &Auth{Token: "bad\r\nheader"}); err == nil {
		t.Fatal("control character in token accepted")
	}
	if _, err := CurlConfig("/api/v1/user?next=http://attacker.invalid", &Auth{Token: token}); err == nil {
		t.Fatal("non-allowlisted endpoint accepted")
	}
	authJSON, err := json.Marshal(Auth{Token: token})
	if err != nil || strings.Contains(string(authJSON), token) {
		t.Fatalf("Auth serialization exposed its token: %s (%v)", authJSON, err)
	}

	version, err := ParseVersion([]byte(`{"version":"16.0.5"}`))
	if err != nil || version != "16.0.5" {
		t.Fatalf("ParseVersion=%q err=%v", version, err)
	}
	user, err := ParseUser([]byte(`{"id":7,"login":"synthetic-user","full_name":"Fixture","is_admin":false}`))
	if err != nil || user.ID != 7 || user.Login != "synthetic-user" {
		t.Fatalf("ParseUser=%+v err=%v", user, err)
	}
	page := []byte(`[{"id":10,"owner":{"login":"synthetic-user"},"name":"upgrade-fixture","full_name":"synthetic-user/upgrade-fixture","default_branch":"main","private":true,"empty":false,"archived":false,"mirror":false,"fork":false}]`)
	repos, err := ParseRepoList(page)
	if err != nil || len(repos) != 1 || repos[0].ID != 10 || !repos[0].Private {
		t.Fatalf("ParseRepoList=%+v err=%v", repos, err)
	}
	duplicate := []byte(`[{"id":10,"owner":{"login":"synthetic-user"},"name":"a","full_name":"synthetic-user/a","default_branch":"main","private":false,"empty":false,"archived":false,"mirror":false,"fork":false},{"id":10,"owner":{"login":"synthetic-user"},"name":"b","full_name":"synthetic-user/b","default_branch":"main","private":false,"empty":false,"archived":false,"mirror":false,"fork":false}]`)
	if _, err := ParseRepoList(duplicate); err == nil {
		t.Fatal("duplicate repository IDs accepted")
	}
	branch, err := ParseBranch([]byte(`{"name":"main","commit":{"id":"abc123","sha":"abc123"}}`))
	if err != nil || branch.Name != "main" || branch.CommitSHA != "abc123" {
		t.Fatalf("ParseBranch=%+v err=%v", branch, err)
	}
	fileBytes := []byte("nested fixture\n")
	encoded := "bmVzdGVkIGZpeHR1cmUK"
	file, err := ParseContents([]byte(`{"type":"file","path":"nested/hello.txt","size":15,"encoding":"base64","content":"`+encoded+`"}`), "nested/hello.txt")
	if err != nil || !bytes.Equal(file.Bytes(), fileBytes) {
		t.Fatalf("ParseContents=%+v err=%v", file, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", file), string(fileBytes)) || strings.Contains(fmt.Sprintf("%#v", file), string(fileBytes)) {
		t.Fatal("APIContents formatting exposed private file contents")
	}
	if _, err := ParseRepository([]byte(`{"id":11,"owner":{"login":"synthetic-user"},"name":"empty","full_name":"synthetic-user/empty","default_branch":"","private":false,"empty":true,"archived":false,"mirror":false,"fork":false}`)); err != nil {
		t.Fatalf("empty repository with no default branch rejected: %v", err)
	}
	if _, err := ParseRepoList([]byte(`null`)); err == nil {
		t.Fatal("null repository list accepted as an empty list")
	}
	if _, err := ContentsEndpoint("synthetic-user", "upgrade-fixture", "../outside", "main"); err == nil {
		t.Fatal("traversal file path accepted")
	}
	if _, _, err := ParseResponse([]byte("{\"version\":\"16.0.5\"}\n200\n")); err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if _, err := ParseVersion([]byte(`{"version":"15.0.9","version":"16.0.5"}`)); err == nil {
		t.Fatal("duplicate API JSON keys were accepted")
	}
	if _, err := ParseUser([]byte(`{"id":7,"login":"synthetic-user","full_name":"Fixture","is_admin":null}`)); err == nil {
		t.Fatal("null required API boolean was accepted")
	}
	if _, err := UserRepositoriesEndpoint(1); err != nil {
		t.Fatalf("canonical user repository page endpoint rejected: %v", err)
	}
	if allowedEndpoint("/api/v1/user/repos?limit=50&page=01") {
		t.Fatal("noncanonical repository page accepted")
	}
}
