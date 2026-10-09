package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
)

type flowRuntime struct {
	events                  []string
	streams                 [][]byte
	migrationErr            error
	migrationRan            bool
	recoveryAnonymousBypass bool
	cleanupErr              error
	cleanupCalls            int
	mutateBackup            string
}

func (f *flowRuntime) Info(context.Context) (engine.Daemon, error) {
	f.events = append(f.events, "info")
	return engine.Daemon{ID: "synthetic-daemon", Version: "29.8.2"}, nil
}

func (f *flowRuntime) CreatePhase(_ context.Context, _ state.Run, _ string, phase string) error {
	f.events = append(f.events, "create:"+phase)
	return nil
}

func (f *flowRuntime) Start(_ context.Context, _ state.Run, phase, role string) error {
	f.events = append(f.events, "start:"+phase+":"+role)
	return nil
}

func (f *flowRuntime) WaitDatabase(_ context.Context, _ state.Run, phase string) error {
	f.events = append(f.events, "database:"+phase)
	return nil
}

func (f *flowRuntime) WaitMigration(_ context.Context, _ state.Run, phase string) error {
	f.events = append(f.events, "migration:"+phase)
	if phase == "target" {
		f.migrationRan = true
	}
	if phase == "target" && f.mutateBackup != "" {
		if err := os.WriteFile(f.mutateBackup, []byte("PGDMPchanged-input"), 0600); err != nil {
			return err
		}
	}
	return f.migrationErr
}

func (f *flowRuntime) VerifyPhase(_ context.Context, _ state.Run, phase string) error {
	f.events = append(f.events, "verify:"+phase)
	return nil
}

func (f *flowRuntime) Inside(_ context.Context, _ state.Run, phase, role string, args []string, input io.Reader, output io.Writer) error {
	if role == "db" && len(args) > 0 && args[0] == "pg_restore" {
		data, err := io.ReadAll(input)
		if err != nil {
			return err
		}
		f.streams = append(f.streams, data)
		if strings.Contains(strings.Join(args, " "), "--list") {
			_, err = io.WriteString(output, "synthetic archive table of contents\n")
		}
		return err
	}
	if role == "db" && len(args) > 0 && args[0] == "psql" {
		query, err := io.ReadAll(input)
		if err != nil {
			return err
		}
		var result string
		switch strings.TrimSpace(string(query)) {
		case strings.TrimSpace(miniflux.SchemaSQL):
			result = "125\n"
			if phase == "target" && f.migrationRan {
				result = "132\n"
			}
		case strings.TrimSpace(miniflux.CountsSQL):
			counts := map[string]int{"users": 1, "categories": 2, "feeds": 2, "entries_unread": 1, "entries_read": 1, "entries_removed": 2, "entries_starred": 1, "entries_tagged": 1}
			if phase == "target" {
				counts["entries_removed"] = 0
			}
			encoded, _ := json.Marshal(counts)
			result = string(encoded) + "\n"
		case strings.TrimSpace(miniflux.ProjectionSQL):
			result = "[\"user\",1,\"reader\"]\n[\"entry\",930001,true,[\"rehearse\"]]\n"
		case strings.TrimSpace(miniflux.RemovedKeysSQL), strings.TrimSpace(miniflux.TombstoneKeysSQL):
			if phase != "target" || strings.TrimSpace(string(query)) == strings.TrimSpace(miniflux.TombstoneKeysSQL) {
				result = "[92001,\"fixture-removed-eligible\"]\n"
			}
		default:
			return errors.New("unexpected synthetic query")
		}
		_, err = io.WriteString(output, result)
		return err
	}
	if role == "probe" && len(args) > 0 && args[0] == "curl" {
		config, err := io.ReadAll(input)
		if err != nil {
			return err
		}
		endpoint := ""
		for _, line := range strings.Split(string(config), "\n") {
			if strings.HasPrefix(line, `url = "http://app:8080`) {
				endpoint = strings.TrimSuffix(strings.TrimPrefix(line, `url = "http://app:8080`), `"`)
			}
		}
		responses := apiObservationFixture()
		if endpoint == "/v1/version" && phase == "target" {
			responses[endpoint] = []byte(`{"version":"2.3.3","commit":"def456","arch":"amd64","os":"linux"}`)
		}
		body, ok := responses[endpoint]
		if !ok {
			return errors.New("unexpected synthetic endpoint")
		}
		status := "200"
		if !bytes.Contains(config, []byte("header = ")) {
			body = []byte(`{"error_message":"Unauthorized"}`)
			status = "401"
			if f.recoveryAnonymousBypass && phase == "recovery" && endpoint == "/v1/me" {
				body = responses[endpoint]
				status = "200"
			}
		}
		_, err = output.Write(append(append(append([]byte(nil), body...), '\n'), []byte(status+"\n")...))
		return err
	}
	return errors.New("unexpected synthetic command")
}

func (f *flowRuntime) InsideBytes(ctx context.Context, run state.Run, phase, role string, args []string, input io.Reader, limit int) ([]byte, error) {
	var output bytes.Buffer
	if err := f.Inside(ctx, run, phase, role, args, input, &output); err != nil {
		return nil, err
	}
	if limit <= 0 || output.Len() > limit {
		return nil, errors.New("bounded synthetic output exceeded")
	}
	return output.Bytes(), nil
}

func (f *flowRuntime) Cleanup(_ context.Context, _ state.Run) error {
	f.cleanupCalls++
	f.events = append(f.events, "cleanup")
	return f.cleanupErr
}

func flowConfig(t *testing.T) (spec.Config, *state.Store, string) {
	t.Helper()
	root := t.TempDir()
	backup := filepath.Join(root, "fixture.dump")
	if err := os.WriteFile(backup, []byte("PGDMPsynthetic-archive-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config(backup)
	return cfg, store, backup
}

func fixtureAuth() miniflux.Auth {
	return miniflux.Auth{Username: "synthetic-reader", Password: "synthetic-password"}
}

func TestRunWithRuntimeRestoresThreeFreshPhasesAndWritesQualifiedReport(t *testing.T) {
	cfg, store, backup := flowConfig(t)
	runtime := &flowRuntime{}
	r, err := runWithRuntime(context.Background(), cfg, store, runtime, fixtureAuth())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if r == nil || r.Result != "passed" || r.Validate() != nil {
		t.Fatalf("report = %#v, validation error %v", r, r.Validate())
	}
	if r.Snapshots["baseline"] != r.Snapshots["target"] || r.Snapshots["baseline"] != r.Snapshots["recovery"] {
		t.Fatalf("phase snapshots differ: %#v", r.Snapshots)
	}
	if runtime.cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want exactly one", runtime.cleanupCalls)
	}
	wantCreates := []string{"create:baseline", "create:target", "create:recovery"}
	var creates []string
	for _, event := range runtime.events {
		if strings.HasPrefix(event, "create:") {
			creates = append(creates, event)
		}
	}
	if !reflect.DeepEqual(creates, wantCreates) {
		t.Fatalf("created phases = %v, want %v", creates, wantCreates)
	}
	var migrations []string
	for _, event := range runtime.events {
		if strings.HasPrefix(event, "migration:") {
			migrations = append(migrations, event)
		}
	}
	if !reflect.DeepEqual(migrations, []string{"migration:target"}) {
		t.Fatalf("migration executions = %v, want only target", migrations)
	}
	if len(runtime.streams) != 4 {
		t.Fatalf("backup streams = %d, want one TOC read and three restores", len(runtime.streams))
	}
	for _, data := range runtime.streams {
		if string(data) != "PGDMPsynthetic-archive-fixture" {
			t.Fatal("restore did not receive the staged original backup bytes")
		}
	}
	loaded, err := store.Load(r.RunID)
	if err != nil || loaded.Status != "completed" || loaded.Backup == nil {
		t.Fatalf("saved run = %#v, %v", loaded, err)
	}
	if _, err := ReadReport(store, r.RunID); err != nil {
		t.Fatalf("saved report cannot be read: %v", err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Size() != int64(len("PGDMPsynthetic-archive-fixture")) {
		t.Fatal("source archive changed during successful rehearsal")
	}
}

func TestRunFailureLeavesDependentChecksNotRunAndCleansOwnedResources(t *testing.T) {
	cfg, store, _ := flowConfig(t)
	runtime := &flowRuntime{migrationErr: errors.New("synthetic private migration diagnostic")}
	r, err := runWithRuntime(context.Background(), cfg, store, runtime, fixtureAuth())
	if err == nil || err.Error() != "MIGRATION_FAILED" {
		t.Fatalf("migration failure = %v (%s), want MIGRATION_FAILED", err, safeOperationDiagnostic(err))
	}
	if r == nil || r.Result != "failed" || runtime.cleanupCalls != 1 {
		t.Fatalf("failure report/cleanup = %#v / %d", r, runtime.cleanupCalls)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("raw migration diagnostic escaped")
	}
	check := func(id string) report.Check {
		t.Helper()
		for _, item := range r.Checks {
			if item.ID == id {
				return item
			}
		}
		t.Fatalf("missing check %q", id)
		return report.Check{}
	}
	if got := check("target.migration"); got.Status != "failed" || got.Code != "MIGRATION_FAILED" {
		t.Fatalf("migration check = %#v", got)
	}
	if got := check("target.schema"); got.Status != "not-run" || got.Code != "NOT_RUN" {
		t.Fatalf("dependent schema check = %#v", got)
	}
	if got := check("recovery.restore"); got.Status != "not-run" {
		t.Fatalf("later recovery check = %#v", got)
	}
	if got := check("cleanup.ownership"); got.Status != "passed" {
		t.Fatalf("cleanup check = %#v", got)
	}
	loaded, loadErr := store.Load(r.RunID)
	if loadErr != nil || loaded.Status != "failed" {
		t.Fatalf("failed run status = %q, %v", loaded.Status, loadErr)
	}
}

func TestRecoveryAnonymousAccessBypassIsRecordedAsFailedReport(t *testing.T) {
	cfg, store, _ := flowConfig(t)
	runtime := &flowRuntime{recoveryAnonymousBypass: true}
	r, err := runWithRuntime(context.Background(), cfg, store, runtime, fixtureAuth())
	if err == nil || err.Error() != "AUTH_BYPASS" {
		t.Fatalf("recovery anonymous-access probe error = %v, want AUTH_BYPASS", err)
	}
	if r == nil || r.Result != "failed" {
		t.Fatalf("recovery anonymous-access report outcome = %#v, want failed", r)
	}
	var found bool
	for _, check := range r.Checks {
		if check.ID == "recovery.unauthenticated" {
			found = check.Status == "failed" && check.Code == "AUTH_BYPASS"
		}
	}
	if !found {
		t.Fatalf("recovery anonymous-access failure absent from report: %#v", r.Checks)
	}
	loaded, loadErr := store.Load(r.RunID)
	if loadErr != nil || loaded.Status != "failed" {
		t.Fatalf("stored status = %q, %v; want failed", loaded.Status, loadErr)
	}
	stored, readErr := ReadReport(store, r.RunID)
	if readErr != nil || stored.Result != "failed" {
		t.Fatalf("persisted failure report = %#v, %v", stored, readErr)
	}
}

func TestRunReportsChangedSourceAndPreservesCleanupFailure(t *testing.T) {
	cfg, store, backup := flowConfig(t)
	runtime := &flowRuntime{mutateBackup: backup, cleanupErr: errors.New("synthetic cleanup diagnostic")}
	r, err := runWithRuntime(context.Background(), cfg, store, runtime, fixtureAuth())
	if err == nil || err.Error() != "SOURCE_CHANGED" {
		t.Fatalf("changed source = %v, want SOURCE_CHANGED", err)
	}
	if r == nil || r.Result != "failed" {
		t.Fatalf("changed source report = %#v", r)
	}
	var sourceCheck, cleanupCheck report.Check
	for _, check := range r.Checks {
		switch check.ID {
		case "source.unchanged":
			sourceCheck = check
		case "cleanup.ownership":
			cleanupCheck = check
		}
	}
	if sourceCheck.Status != "failed" || sourceCheck.Code != "SOURCE_CHANGED" {
		t.Fatalf("source check = %#v", sourceCheck)
	}
	if cleanupCheck.Status != "failed" || cleanupCheck.Code != "CLEANUP_HELD" {
		t.Fatalf("cleanup check = %#v", cleanupCheck)
	}
	loaded, loadErr := store.Load(r.RunID)
	if loadErr != nil || loaded.Status != "cleanup-held" {
		t.Fatalf("cleanup-held status = %q, %v", loaded.Status, loadErr)
	}
}

func TestCleanupRejectsTraversalBeforeReachingRuntime(t *testing.T) {
	if err := Cleanup(context.Background(), nil, nil, "../owner-id"); err == nil || err.Error() != "RUN_ID_INVALID" {
		t.Fatalf("traversal cleanup error = %v, want RUN_ID_INVALID", err)
	}
}
