package app

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/state"
)

// This runtime presents a nonempty, globally consistent database and Git
// archive while the authenticated API account sees no repositories.
type emptyAPIRepositoryRuntime struct {
	apiListCalls    int
	apiDetailCalls  int
	apiContentCalls int
	phaseData       map[string][]byte
	targetMigrated  bool
}

func (*emptyAPIRepositoryRuntime) Info(context.Context) (engine.Daemon, error) {
	return engine.Daemon{ID: "synthetic-empty-api-daemon"}, nil
}

func (*emptyAPIRepositoryRuntime) CreatePhase(context.Context, state.Run, string, string) error {
	return nil
}

func (r *emptyAPIRepositoryRuntime) Start(_ context.Context, _ state.Run, phase, role string) error {
	if phase == "target" && role == "migration" {
		r.targetMigrated = true
	}
	return nil
}

func (r *emptyAPIRepositoryRuntime) Inside(_ context.Context, _ state.Run, _ string, role string, args []string, input io.Reader, output io.Writer) error {
	if role != "db" || len(args) == 0 {
		return code("OPERATION_FAILED")
	}
	if args[0] == "pg_restore" {
		_, err := io.Copy(io.Discard, input)
		return err
	}
	if args[0] == "psql" {
		query, err := io.ReadAll(input)
		if err != nil || !strings.Contains(string(query), forgejo.ProjectionSQL) {
			return code("OPERATION_FAILED")
		}
		_, err = io.WriteString(output,
			`["repository",10,1,"synthetic","example","main",true,false,false,false,false,null,false,null,"sha1"]`+"\n"+
				`["user",1,"synthetic","Synthetic User",true,false]`+"\n")
		return err
	}
	return code("OPERATION_FAILED")
}

func (r *emptyAPIRepositoryRuntime) InsideBytes(_ context.Context, _ state.Run, phase, role string, args []string, input io.Reader, _ int) ([]byte, error) {
	request, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	if role == "db" && len(args) > 0 && args[0] == "psql" {
		query := string(request)
		switch {
		case strings.Contains(query, forgejo.SchemaSQL):
			return emptyAPIReviewSchema(phase, r.targetMigrated)
		case strings.Contains(query, forgejo.CountsSQL):
			return []byte(`{"users":1,"repositories":1,"private_repositories":1,"empty_repositories":0,"archived_repositories":0,"mirrors":0,"forks":0}`), nil
		default:
			return nil, code("OPERATION_FAILED")
		}
	}
	if role != "probe" || strings.Join(args, " ") != "curl --disable --config -" {
		return nil, code("OPERATION_FAILED")
	}
	endpoint := ""
	for _, candidate := range []string{
		"/api/v1/version", "/api/v1/user", "/api/v1/user/repos?limit=50&page=1",
	} {
		if strings.Contains(string(request), `url = "http://app:3000`+candidate+`"`) {
			endpoint = candidate
			break
		}
	}
	if endpoint == "" {
		if strings.Contains(string(request), "/api/v1/repos/") {
			r.apiDetailCalls++
			if strings.Contains(string(request), "/contents/") {
				r.apiContentCalls++
			}
		}
		return nil, code("OPERATION_FAILED")
	}
	switch endpoint {
	case "/api/v1/version":
		version := forgejo.SourceVersion
		if phase == "target" {
			version = forgejo.TargetVersion
		}
		body, _ := json.Marshal(map[string]string{"version": version})
		return append(append(body, '\n'), []byte("200\n")...), nil
	case "/api/v1/user":
		if !bytes.Contains(request, []byte("Authorization: token ")) {
			return []byte("{}\n401\n"), nil
		}
		return []byte(`{"id":1,"login":"synthetic","full_name":"Synthetic User","is_admin":false}` + "\n200\n"), nil
	case "/api/v1/user/repos?limit=50&page=1":
		r.apiListCalls++
		return []byte("[]\n200\n"), nil
	default:
		return nil, code("OPERATION_FAILED")
	}
}

func (*emptyAPIRepositoryRuntime) WaitDatabase(context.Context, state.Run, string) error {
	return nil
}

func (*emptyAPIRepositoryRuntime) WaitMigration(context.Context, state.Run, string) error {
	return nil
}

func (*emptyAPIRepositoryRuntime) VerifyPhase(context.Context, state.Run, string) error {
	return nil
}

func (*emptyAPIRepositoryRuntime) Cleanup(context.Context, state.Run) error {
	return nil
}

func (r *emptyAPIRepositoryRuntime) CopyForgejoData(_ context.Context, _ state.Run, phase string, input io.Reader) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	if r.phaseData == nil {
		r.phaseData = make(map[string][]byte)
	}
	if _, ok := r.phaseData[phase]; !ok {
		r.phaseData[phase] = data
	}
	return nil
}

func (r *emptyAPIRepositoryRuntime) ReadForgejoData(_ context.Context, _ state.Run, phase string, output io.Writer) error {
	_, err := output.Write(r.phaseData[phase])
	return err
}

func (*emptyAPIRepositoryRuntime) StopForgejoApp(context.Context, state.Run, string) error {
	return nil
}

func emptyAPIReviewSchema(phase string, targetMigrated bool) ([]byte, error) {
	expectation := forgejo.SourceSchemaExpectation()
	if phase == "target" && targetMigrated {
		expectation = forgejo.TargetSchemaExpectation()
	}
	var output bytes.Buffer
	write := func(row []any) error {
		encoded, err := json.Marshal(row)
		if err != nil {
			return err
		}
		output.Write(encoded)
		output.WriteByte('\n')
		return nil
	}
	if err := write([]any{"version", 1, expectation.GiteaVersion}); err != nil {
		return nil, err
	}
	if err := write([]any{"forgejo_version", 1, expectation.ForgejoVersion}); err != nil {
		return nil, err
	}
	for _, id := range expectation.MigrationIDs {
		if err := write([]any{"forgejo_migration", id}); err != nil {
			return nil, err
		}
	}
	return output.Bytes(), nil
}

func TestEmptyAuthenticatedRepositoryViewCannotProducePassedForgejoRun(t *testing.T) {
	dataTar := emptyAPIReviewDataTar(t)
	backupPath := filepath.Join(t.TempDir(), "synthetic-source.zip")
	if err := writeEmptyAPIReviewArchive(t, backupPath, dataTar); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &emptyAPIRepositoryRuntime{}
	r, runErr := runForgejoWithRuntime(context.Background(), forgejoConfig(backupPath), store, runtime, forgejo.Auth{Token: strings.Repeat("a", 40)})
	if r == nil || runtime.apiListCalls == 0 {
		t.Fatalf("fixture did not reach an authenticated repository-list observation: report=%+v error=%v listCalls=%d", r, runErr, runtime.apiListCalls)
	}
	if runtime.apiDetailCalls != 0 || runtime.apiContentCalls != 0 {
		t.Fatalf("empty authenticated list unexpectedly triggered details/content: detail=%d content=%d", runtime.apiDetailCalls, runtime.apiContentCalls)
	}
	if checkCode(r, "baseline.restore") != "VALIDATED" || checkCode(r, "baseline.schema") != "VALIDATED" {
		t.Fatalf("fixture failed before authenticated repository observation: restore=%q schema=%q", checkCode(r, "baseline.restore"), checkCode(r, "baseline.schema"))
	}
	if r.Result == "passed" {
		if runErr != nil {
			t.Fatalf("passing result was returned with a command error: %v", runErr)
		}
		persisted, readErr := ReadReport(store, r.RunID)
		if readErr != nil || persisted.Result != "passed" || runtime.apiListCalls != 3 || checkCode(r, "baseline.auth") != "VALIDATED" || r.Snapshots["baseline"].Rows == 0 || r.FileSnapshots["baseline"].Rows == 0 {
			t.Fatalf("fixture did not prove a readable pass with independent nonempty SQL/file evidence: report=%+v read=%v listCalls=%d", r, readErr, runtime.apiListCalls)
		}
		t.Fatalf("Forgejo run passed with a readable report and nonempty global SQL/file projections, but the authenticated list was empty in all three phases and made zero repository-detail/content calls")
	}
	if runErr == nil || (checkCode(r, "baseline.auth") != "AUTH_FAILED" && checkCode(r, "baseline.auth") != "NOT_RUN") {
		t.Fatalf("empty authenticated repository view was not explicitly failed/not-run: result=%q auth=%q error=%v", r.Result, checkCode(r, "baseline.auth"), runErr)
	}
}

func TestForgejoAPIRejectsAllVisibleRepositoriesBeingEmpty(t *testing.T) {
	const emptyRepository = `{"id":1,"owner":{"login":"synthetic"},"name":"empty","full_name":"synthetic/empty","default_branch":"","private":true,"empty":true,"archived":false,"mirror":false,"fork":false}`
	stub := forgejoAPIStub{response: map[string]string{
		"/api/v1/version":                    `{"version":"15.0.9"}` + "\n200\n",
		"/api/v1/user":                       `{"id":1,"login":"synthetic","full_name":"Synthetic User","is_admin":false}` + "\n200\n",
		"/api/v1/user/repos?limit=50&page=1": "[" + emptyRepository + "]\n200\n",
		"/api/v1/repos/synthetic/empty":      emptyRepository + "\n200\n",
	}}
	if _, err := observeForgejoAPI(context.Background(), stub, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("a", 40)}, forgejo.SourceVersion); err == nil || err.Error() != "AUTH_FAILED" {
		t.Fatalf("all-empty authenticated repository view was not rejected with AUTH_FAILED: %v", err)
	}
}

func TestForgejoAPIAcceptsContentFromOneNonemptyRepositoryAlongsideEmptyRepository(t *testing.T) {
	const emptyRepository = `{"id":1,"owner":{"login":"synthetic"},"name":"empty","full_name":"synthetic/empty","default_branch":"","private":true,"empty":true,"archived":false,"mirror":false,"fork":false}`
	const contentRepository = `{"id":2,"owner":{"login":"synthetic"},"name":"example","full_name":"synthetic/example","default_branch":"main","private":true,"empty":false,"archived":false,"mirror":false,"fork":false}`
	commit := strings.Repeat("a", 40)
	responses := map[string]string{
		"/api/v1/version":                               `{"version":"15.0.9"}` + "\n200\n",
		"/api/v1/user":                                  `{"id":1,"login":"synthetic","full_name":"Synthetic User","is_admin":false}` + "\n200\n",
		"/api/v1/user/repos?limit=50&page=1":            "[" + emptyRepository + "," + contentRepository + "]\n200\n",
		"/api/v1/repos/synthetic/empty":                 emptyRepository + "\n200\n",
		"/api/v1/repos/synthetic/example":               contentRepository + "\n200\n",
		"/api/v1/repos/synthetic/example/branches/main": `{"name":"main","commit":{"id":"` + commit + `"}}` + "\n200\n",
	}
	addContentResponseFixtures(responses, "synthetic", "example", commit)
	observed, err := observeForgejoAPI(context.Background(), forgejoAPIStub{response: responses}, state.Run{}, "baseline", forgejo.Auth{Token: strings.Repeat("a", 40)}, forgejo.SourceVersion)
	if err != nil {
		t.Fatalf("a verified nonempty repository was rejected beside an empty repository: %v", err)
	}
	if len(observed.Repositories) != 2 || len(observed.Repositories[0].Contents) != 0 || len(observed.Repositories[1].Contents) != 1 {
		t.Fatalf("expected one empty repository and one content-verified repository, got %+v", observed.Repositories)
	}
}

func emptyAPIReviewDataTar(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	w := tar.NewWriter(&output)
	for _, name := range []string{
		"gitea", "gitea/data", "gitea/data/gitea-repositories", "gitea/data/gitea-repositories/synthetic",
		"gitea/data/gitea-repositories/synthetic/example.git",
	} {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0700, Uid: 1000, Gid: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	head := []byte("ref: refs/heads/main\n")
	if err := w.WriteHeader(&tar.Header{Name: "gitea/data/gitea-repositories/synthetic/example.git/HEAD", Typeflag: tar.TypeReg, Mode: 0600, Uid: 1000, Gid: 1000, Size: int64(len(head))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(head); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func writeEmptyAPIReviewArchive(t *testing.T, output string, dataTar []byte) error {
	t.Helper()
	dir := t.TempDir()
	dump := filepath.Join(dir, "database.dump")
	data := filepath.Join(dir, "data.tar")
	if err := os.WriteFile(dump, []byte("PGDMPsynthetic-dump-header"), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(data, dataTar, 0600); err != nil {
		return err
	}
	file, err := os.Create(output)
	if err != nil {
		return err
	}
	_, err = forgejo.WriteArchive(context.Background(), file, dump, data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
