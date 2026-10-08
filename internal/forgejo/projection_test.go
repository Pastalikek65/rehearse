package forgejo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func projectionLines(t *testing.T, rows ...[]any) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func validProjectionRows() []byte {
	return []byte("[\"repository\",10,2,\"fixture-user\",\"fixture-repo\",\"main\",false,false,false,false,false,null,false,null,\"sha1\"]\n" +
		"[\"user\",1,\"admin\",\"Administrator\",true,true]\n" +
		"[\"user\",2,\"fixture-user\",\"Fixture User\",true,false]\n")
}

func TestProjectDatabaseProjectionStreamsStableSortedRows(t *testing.T) {
	input := validProjectionRows()
	got, err := ProjectDatabaseProjection(context.Background(), bytes.NewReader(input))
	if err != nil {
		t.Fatalf("ProjectDatabaseProjection: %v", err)
	}
	h := sha256.Sum256(input)
	if got.SHA256 != hex.EncodeToString(h[:]) || got.Rows != 3 || got.Bytes != uint64(len(input)-3) || got.Users != 2 || got.Repositories != 1 {
		t.Fatalf("unexpected projection: %+v", got)
	}
	counts, err := ParseDatabaseCounts(strings.NewReader(`{"users":2,"repositories":1,"private_repositories":0,"empty_repositories":0,"archived_repositories":0,"mirrors":0,"forks":0}`))
	if err != nil || !got.MatchesCounts(counts) {
		t.Fatalf("counts did not match projection: counts=%+v err=%v", counts, err)
	}
}

func TestProjectionSQLReturnsGlobalRowsWithoutJoinFiltering(t *testing.T) {
	sql := strings.ToUpper(ProjectionSQL)
	if strings.Contains(sql, " JOIN ") || !strings.Contains(sql, "FROM \"USER\"") || !strings.Contains(sql, "FROM REPOSITORY") {
		t.Fatal("ProjectionSQL must retain every user and repository row without joins")
	}
}

func TestSchemaMigrationExpectationsAreBytewiseSortedAndUnique(t *testing.T) {
	for _, expectation := range []SchemaExpectation{SourceSchemaExpectation(), TargetSchemaExpectation()} {
		for i := 1; i < len(expectation.MigrationIDs); i++ {
			if expectation.MigrationIDs[i-1] >= expectation.MigrationIDs[i] {
				t.Fatalf("migration IDs are not bytewise sorted and unique: %q before %q", expectation.MigrationIDs[i-1], expectation.MigrationIDs[i])
			}
		}
	}
}

func TestProjectDatabaseProjectionRejectsMalformedAndMisorderedRows(t *testing.T) {
	valid := validProjectionRows()
	last := bytes.TrimSuffix(valid, []byte("\n"))
	rows := [][]byte{
		[]byte("[\"user\",1,\"admin\",\"Admin\",true,true]\n[\"repository\",10,1,\"fixture-user\",\"repo\",\"main\",false,false,false,false,false,null,false,null,\"sha1\"]\n"),
		[]byte("[\"repository\",10,2,\"fixture-user\",\"repo\",\"main\",false,false,false,false,false,null,false,null,\"sha1\"]\n[\"repository\",10,2,\"fixture-user\",\"duplicate\",\"main\",false,false,false,false,false,null,false,null,\"sha1\"]\n"),
		[]byte("[\"user\",1,\"admin\",\"Admin\",true,true]\n[\"user\",1,\"duplicate\",\"Duplicate\",true,false]\n"),
		[]byte("[\"repository\",10,99,\"fixture-user\",\"repo\",\"main\",false,false,false,false,false,null,false,null,\"sha1\"]\n[\"user\",1,\"admin\",\"Admin\",true,true]\n[\"user\",2,\"fixture-user\",\"Fixture User\",true,false]\n"),
		[]byte("[\"unknown\",1]\n"),
		[]byte("[\"user\",0,\"admin\",\"Admin\",true,true]\n"),
		[]byte("[\"user\",1,\"admin\",\"Admin\",true]\n"),
		[]byte("[\"user\",1,\"admin\",\"Admin\",1,true]\n"),
		[]byte("[\"user\",1,\"admin\",\"Admin\",true,true]"),
		[]byte("null\n"),
		append(bytes.Clone(last), []byte("\n\n")...),
	}
	for i, input := range rows {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			if _, err := ProjectDatabaseProjection(context.Background(), bytes.NewReader(input)); err == nil {
				t.Fatal("malformed projection was accepted")
			}
		})
	}
}

func TestProjectDatabaseProjectionRequiresEveryRepositoryOwner(t *testing.T) {
	input := []byte("[\"repository\",10,99,\"missing-user\",\"repo\",\"main\",false,false,false,false,false,null,false,null,\"sha1\"]\n" +
		"[\"user\",1,\"admin\",\"Admin\",true,true]\n")
	if _, err := ProjectDatabaseProjection(context.Background(), bytes.NewReader(input)); err == nil {
		t.Fatal("repository with an absent owner was accepted")
	}
}

func TestProjectDatabaseProjectionEnforcesStreamingBudgets(t *testing.T) {
	ctx := context.Background()
	input := validProjectionRows()
	if _, err := projectDatabaseProjectionWithLimits(ctx, bytes.NewReader(input), databaseProjectionLimits{maxBytes: 10, maxRows: MaxDatabaseProjectionRows, maxLine: MaxDatabaseProjectionLineBytes}); err == nil {
		t.Fatal("total projection byte budget was not enforced")
	}
	if _, err := projectDatabaseProjectionWithLimits(ctx, bytes.NewReader(input), databaseProjectionLimits{maxBytes: MaxDatabaseProjectionBytes, maxRows: 1, maxLine: MaxDatabaseProjectionLineBytes}); err == nil {
		t.Fatal("projection row-count budget was not enforced")
	}
	if _, err := projectDatabaseProjectionWithLimits(ctx, bytes.NewReader(input), databaseProjectionLimits{maxBytes: MaxDatabaseProjectionBytes, maxRows: MaxDatabaseProjectionRows, maxLine: 10}); err == nil {
		t.Fatal("projection row-size budget was not enforced")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ProjectDatabaseProjection(ctx, bytes.NewReader(input)); err == nil {
		t.Fatal("canceled projection succeeded")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancelOnRead := cancelProjectionReader{reader: bytes.NewReader(input), cancel: cancel}
	if _, err := ProjectDatabaseProjection(ctx, cancelOnRead); err == nil {
		t.Fatal("projection did not observe cancellation during stream consumption")
	}
}

type cancelProjectionReader struct {
	reader *bytes.Reader
	cancel context.CancelFunc
}

func (r cancelProjectionReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.cancel()
	}
	return n, err
}

func TestParseDatabaseCountsRequiresFixedNonnegativeIntegerFields(t *testing.T) {
	valid := `{"users":2,"repositories":1,"private_repositories":0,"empty_repositories":0,"archived_repositories":0,"mirrors":0,"forks":0}`
	if _, err := ParseDatabaseCounts(strings.NewReader(valid)); err != nil {
		t.Fatalf("valid counts rejected: %v", err)
	}
	for name, input := range map[string]string{
		"missing":   `{"users":2}`,
		"unknown":   `{"users":2,"repositories":1,"private_repositories":0,"empty_repositories":0,"archived_repositories":0,"mirrors":0,"forks":0,"extra":0}`,
		"duplicate": `{"users":2,"users":2,"repositories":1,"private_repositories":0,"empty_repositories":0,"archived_repositories":0,"mirrors":0,"forks":0}`,
		"negative":  `{"users":-1,"repositories":1,"private_repositories":0,"empty_repositories":0,"archived_repositories":0,"mirrors":0,"forks":0}`,
		"float":     `{"users":2.0,"repositories":1,"private_repositories":0,"empty_repositories":0,"archived_repositories":0,"mirrors":0,"forks":0}`,
		"string":    `{"users":"2","repositories":1,"private_repositories":0,"empty_repositories":0,"archived_repositories":0,"mirrors":0,"forks":0}`,
		"array":     `[]`,
		"trailing":  valid + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDatabaseCounts(strings.NewReader(input)); err == nil {
				t.Fatal("invalid counts accepted")
			}
		})
	}
}
