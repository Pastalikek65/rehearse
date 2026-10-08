package app

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strings"
	"time"

	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
)

type forgejoRuntimeClient interface {
	runtimeClient
	CopyForgejoData(context.Context, state.Run, string, io.Reader) error
	ReadForgejoData(context.Context, state.Run, string, io.Writer) error
	StopForgejoApp(context.Context, state.Run, string) error
}
type forgejoDatabaseObservation struct {
	Counts     forgejo.DatabaseCounts
	Projection forgejo.DatabaseProjection
}

func preflightForgejo(ctx context.Context, cfg spec.Config, auth forgejo.Auth) error {
	if ctx == nil || ctx.Err() != nil {
		return code("CANCELED")
	}
	if cfg.Adapter != "forgejo" || spec.Validate(cfg) != nil {
		return code("CONFIG_INVALID")
	}
	if _, err := BuildPlan(ctx, cfg); err != nil {
		return err
	}
	if _, err := forgejo.CurlConfig(forgejo.UserEndpoint(), &auth); err != nil {
		return code("AUTH_INVALID")
	}
	return nil
}

func runForgejoWithRuntime(ctx context.Context, cfg spec.Config, store *state.Store, client forgejoRuntimeClient, auth forgejo.Auth) (result *report.Report, returnErr error) {
	if err := preflightForgejo(ctx, cfg, auth); err != nil {
		return nil, err
	}
	if store == nil || client == nil {
		return nil, code("OPERATION_FAILED")
	}
	platform, err := hostPlatform()
	if err != nil {
		return nil, err
	}
	daemon, err := client.Info(ctx)
	if err != nil || strings.TrimSpace(daemon.ID) == "" {
		return nil, code("OPERATION_FAILED")
	}
	intent, err := store.CreateForAdapter(daemon.ID, "forgejo")
	if err != nil {
		return nil, code("OPERATION_FAILED")
	}
	result = report.NewForgejo(intent.ID, platform, time.Now().UTC())
	lock, err := store.AcquireRunLock(intent.ID)
	if err != nil {
		return result, code("OPERATION_FAILED")
	}
	locked := true
	defer func() {
		if locked {
			if err := lock.Release(); err != nil && returnErr == nil {
				returnErr = code("OPERATION_FAILED")
			}
		}
	}()
	runDir, err := store.RunDir(intent.ID)
	if err != nil {
		return result, code("OPERATION_FAILED")
	}
	var staged *state.Backup
	defer func() {
		finalErr, released := finalizeForgejoRun(ctx, store, client, intent.ID, runDir, lock, result, staged)
		if released {
			locked = false
		}
		if returnErr == nil {
			returnErr = finalErr
		}
	}()
	fail := func(id, failure string) error {
		if ctx.Err() != nil {
			failure = "CANCELED"
		}
		_ = result.Set(id, "failed", failure)
		return code(failure)
	}
	pass := func(id string) { _ = result.Set(id, "passed", "VALIDATED") }
	backup, err := store.StageBackup(ctx, lock, cfg.BackupPath)
	if err != nil {
		return result, fail("backup.inspect", "BACKUP_FAILED")
	}
	staged = &backup
	result.BackupSHA256 = backup.SHA256
	result.BackupBytes = uint64(backup.Bytes)
	run, err := store.Load(intent.ID)
	if err != nil {
		return result, fail("backup.inspect", "OPERATION_FAILED")
	}
	run.Status = "running"
	if err := store.Save(lock, run); err != nil {
		return result, fail("backup.inspect", "OPERATION_FAILED")
	}
	config, err := forgejoRuntimeConfigTar()
	if err != nil {
		return result, fail("backup.inspect", "OPERATION_FAILED")
	}
	var baselineDB forgejoDatabaseObservation
	var baselineFiles forgejo.RepositoryFileSummary
	var baselineAPI forgejoAPIObservation
	for _, phase := range []string{"baseline", "target", "recovery"} {
		if err := client.CreatePhase(ctx, run, runDir, phase); err != nil {
			return result, fail(phase+".network", "NETWORK_FAILED")
		}
		if err := client.Start(ctx, run, phase, "db"); err != nil {
			return result, fail(phase+".restore", "RESTORE_FAILED")
		}
		if err := client.WaitDatabase(ctx, run, phase); err != nil {
			return result, fail(phase+".restore", "RESTORE_FAILED")
		}
		if err := restoreForgejoArchive(ctx, client, store, run, phase, config); err != nil {
			if phase == "baseline" {
				return result, fail("backup.inspect", "BACKUP_FAILED")
			}
			return result, fail(phase+".restore", "RESTORE_FAILED")
		}
		if phase == "baseline" {
			pass("backup.inspect")
		}
		pass(phase + ".restore")
		schema, err := readForgejoSchema(ctx, client, run, phase)
		if err != nil || !forgejo.SchemaMatches(schema, forgejo.SourceSchemaExpectation()) {
			check := phase + ".schema"
			if phase == "target" {
				check = "target.migration"
			}
			return result, fail(check, "SCHEMA_MISMATCH")
		}
		expectedSchema, expectedVersion := forgejo.SourceSchemaExpectation(), forgejo.SourceVersion
		if phase == "target" {
			if err := client.Start(ctx, run, phase, "migration"); err != nil {
				return result, fail("target.migration", "MIGRATION_FAILED")
			}
			if err := client.WaitMigration(ctx, run, phase); err != nil {
				return result, fail("target.migration", "MIGRATION_FAILED")
			}
			pass("target.migration")
			expectedSchema = forgejo.TargetSchemaExpectation()
			expectedVersion = forgejo.TargetVersion
		}
		if err := startForgejoApplication(ctx, client, run, phase, auth); err != nil {
			return result, fail(phase+".auth", "AUTH_FAILED")
		}
		// Inspect the schema again after app startup, which can itself run migrations.
		schema, err = readForgejoSchema(ctx, client, run, phase)
		if err != nil || !forgejo.SchemaMatches(schema, expectedSchema) {
			return result, fail(phase+".schema", "SCHEMA_MISMATCH")
		}
		pass(phase + ".schema")
		api, err := observeForgejoAPI(ctx, client, run, phase, auth, expectedVersion)
		if err != nil {
			return result, fail(phase+".auth", "AUTH_FAILED")
		}
		if phase != "baseline" && !sameForgejoAPI(baselineAPI, api) {
			return result, fail(phase+".auth", "DATA_CHANGED")
		}
		pass(phase + ".auth")
		if err := forgejoUnauthenticatedProbe(ctx, client, run, phase); err != nil {
			failure := "AUTH_FAILED"
			if err.Error() == "AUTH_BYPASS" {
				failure = "AUTH_BYPASS"
			}
			return result, fail(phase+".unauthenticated", failure)
		}
		pass(phase + ".unauthenticated")
		if err := client.VerifyPhase(ctx, run, phase); err != nil {
			return result, fail(phase+".network", "NETWORK_FAILED")
		}
		pass(phase + ".network")
		// Freeze application writers before comparing the restored database
		// and repository storage. The engine requires a clean, verified stop.
		if err := client.StopForgejoApp(ctx, run, phase); err != nil {
			return result, fail(phase+".data", "OPERATION_FAILED")
		}
		db, err := readForgejoDatabase(ctx, client, run, phase)
		if err != nil {
			return result, fail(phase+".data", "OPERATION_FAILED")
		}
		if db.Counts.Users == 0 || db.Counts.Repositories == 0 || db.Counts.Mirrors != 0 {
			return result, fail(phase+".data", "DATA_CHANGED")
		}
		result.Snapshots[phase] = report.Snapshot{SHA256: db.Projection.SHA256, Rows: db.Projection.Rows, Bytes: db.Projection.Bytes}
		if phase != "baseline" && db != baselineDB {
			return result, fail(phase+".data", "DATA_CHANGED")
		}
		pass(phase + ".data")
		files, err := readForgejoFiles(ctx, client, run, phase)
		if err != nil {
			return result, fail(phase+".files", "OPERATION_FAILED")
		}
		result.FileSnapshots[phase] = report.Snapshot{SHA256: files.SHA256, Rows: files.Files, Bytes: files.Bytes}
		if files.Repositories != db.Counts.Repositories || files.Files == 0 || files.Bytes == 0 || phase != "baseline" && files != baselineFiles {
			return result, fail(phase+".files", "DATA_CHANGED")
		}
		pass(phase + ".files")
		if phase == "baseline" {
			baselineDB = db
			baselineFiles = files
			baselineAPI = api
		}
	}
	return result, nil
}

func finalizeForgejoRun(ctx context.Context, store *state.Store, client forgejoRuntimeClient, runID, runDir string, lock *state.RunLock, r *report.Report, staged *state.Backup) (returnErr error, released bool) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	latest, err := store.Load(runID)
	cleanupErr := err
	if cleanupErr == nil {
		cleanupErr = client.Cleanup(cleanupCtx, latest)
	}
	if cleanupErr != nil {
		_ = r.Set("cleanup.ownership", "failed", "CLEANUP_HELD")
		returnErr = code("CLEANUP_HELD")
	} else {
		_ = r.Set("cleanup.ownership", "passed", "VALIDATED")
	}
	if staged != nil {
		unchanged, sourceErr := sourceUnchangedContext(ctx, *staged)
		if sourceErr == nil && unchanged {
			f, verifyErr := store.OpenVerifiedBackupContext(ctx, runID)
			if verifyErr != nil {
				sourceErr = verifyErr
			} else {
				sourceErr = f.Close()
			}
		}
		switch {
		case ctx.Err() != nil:
			_ = r.Set("source.unchanged", "failed", "CANCELED")
			returnErr = code("CANCELED")
		case sourceErr != nil:
			_ = r.Set("source.unchanged", "failed", "OPERATION_FAILED")
			returnErr = code("OPERATION_FAILED")
		case !unchanged:
			_ = r.Set("source.unchanged", "failed", "SOURCE_CHANGED")
			returnErr = code("SOURCE_CHANGED")
		default:
			_ = r.Set("source.unchanged", "passed", "VALIDATED")
		}
	}
	finished := time.Now().UTC()
	r.FinishedAt = &finished
	r.Result = r.Outcome()
	latest, err = store.Load(runID)
	if err == nil {
		switch {
		case latest.PendingBackup != nil:
			latest.Status = "staging"
		case cleanupErr != nil:
			latest.Status = "cleanup-held"
		case r.Result == "passed":
			latest.Status = "completed"
		default:
			latest.Status = "failed"
		}
		err, released = commitTerminalReport(runDir, r, latest, func(run state.Run) error {
			return store.Save(lock, run)
		}, lock.Release)
	} else {
		invalidateTerminalReport(runDir)
		if releaseErr := lock.Release(); releaseErr == nil {
			released = true
		}
	}
	if err != nil && returnErr == nil {
		returnErr = code("OPERATION_FAILED")
	}
	if returnErr == nil && r.Result != "passed" {
		returnErr = code("OPERATION_FAILED")
	}
	return returnErr, released
}

func restoreForgejoArchive(ctx context.Context, client forgejoRuntimeClient, store *state.Store, run state.Run, phase string, config []byte) error {
	f, err := store.OpenVerifiedBackupContext(ctx, run.ID)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	archive, err := forgejo.OpenArchive(ctx, f, info.Size())
	if err != nil {
		return err
	}
	consumeDump := func(args []string) error {
		member, err := archive.OpenMember(ctx, forgejo.DatabaseMember)
		if err != nil {
			return err
		}
		commandErr := client.Inside(ctx, run, phase, "db", args, member, io.Discard)
		// pg_restore --list may finish after the table of contents. Complete
		// the frozen member digest even when the subprocess needed fewer bytes.
		var verifyErr error
		if commandErr == nil {
			_, verifyErr = io.Copy(io.Discard, member)
		}
		closeErr := member.Close()
		if commandErr != nil {
			return commandErr
		}
		if verifyErr != nil {
			return verifyErr
		}
		return closeErr
	}
	if err := consumeDump([]string{"pg_restore", "--list"}); err != nil {
		return err
	}
	if err := consumeDump([]string{"pg_restore", "--username=rehearse", "--dbname=rehearse", "--no-owner", "--no-acl", "--exit-on-error"}); err != nil {
		return err
	}
	member, err := archive.OpenMember(ctx, forgejo.DataMember)
	if err != nil {
		return err
	}
	copyErr := client.CopyForgejoData(ctx, run, phase, member)
	closeErr := member.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return client.CopyForgejoData(ctx, run, phase, bytes.NewReader(config))
}

func readForgejoSchema(ctx context.Context, client runtimeClient, run state.Run, phase string) (forgejo.SchemaSnapshot, error) {
	data, err := queryOutput(ctx, client, run, phase, forgejo.SchemaSQL, int(forgejo.MaxSchemaBytes)+1)
	if err != nil {
		return forgejo.SchemaSnapshot{}, err
	}
	return forgejo.ParseSchemaProjection(bytes.NewReader(data))
}

func forgejoStream[T any](ctx context.Context, produce func(context.Context, io.Writer) error, parse func(context.Context, io.Reader) (T, error)) (T, error) {
	var zero T
	reader, writer := io.Pipe()
	streamCtx, cancel := context.WithCancel(ctx)
	stopClose := context.AfterFunc(streamCtx, func() { _ = reader.CloseWithError(streamCtx.Err()) })
	commandDone := make(chan error, 1)
	go func() { err := produce(streamCtx, writer); _ = writer.CloseWithError(err); commandDone <- err }()
	value, readErr := parse(streamCtx, reader)
	if readErr != nil {
		cancel()
		_ = reader.CloseWithError(readErr)
	}
	commandErr := <-commandDone
	stopClose()
	_ = reader.Close()
	cancel()
	if ctx.Err() != nil {
		return zero, code("CANCELED")
	}
	if readErr != nil || commandErr != nil {
		return zero, code("OPERATION_FAILED")
	}
	return value, nil
}

func readForgejoDatabase(ctx context.Context, client runtimeClient, run state.Run, phase string) (forgejoDatabaseObservation, error) {
	countsBytes, err := queryOutput(ctx, client, run, phase, forgejo.CountsSQL, int(forgejo.MaxDatabaseCountsBytes)+1)
	if err != nil {
		return forgejoDatabaseObservation{}, err
	}
	counts, err := forgejo.ParseDatabaseCounts(bytes.NewReader(countsBytes))
	if err != nil {
		return forgejoDatabaseObservation{}, code("OPERATION_FAILED")
	}
	projection, err := forgejoStream(ctx, func(streamCtx context.Context, output io.Writer) error {
		args := []string{"psql", "--username", "rehearse", "--dbname", "rehearse", "--no-psqlrc", "--quiet", "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1"}
		return client.Inside(streamCtx, run, phase, "db", args, strings.NewReader(forgejo.ProjectionSQL+"\n"), output)
	}, forgejo.ProjectDatabaseProjection)
	if err != nil || !projection.MatchesCounts(counts) {
		return forgejoDatabaseObservation{}, code("OPERATION_FAILED")
	}
	return forgejoDatabaseObservation{Counts: counts, Projection: projection}, nil
}
func readForgejoFiles(ctx context.Context, client forgejoRuntimeClient, run state.Run, phase string) (forgejo.RepositoryFileSummary, error) {
	return forgejoStream(ctx, func(streamCtx context.Context, output io.Writer) error {
		return client.ReadForgejoData(streamCtx, run, phase, output)
	}, forgejo.ProjectRepositoryFiles)
}

// No source configuration is executed. Secret-dependent subsystems (e.g.
// Actions secrets) are outside this identity/repository adapter contract.
func forgejoRuntimeConfigTar() ([]byte, error) {
	var secrets [64]byte
	if _, err := rand.Read(secrets[:]); err != nil {
		return nil, code("OPERATION_FAILED")
	}
	secretKey := base64.StdEncoding.EncodeToString(secrets[:32])
	internalToken := base64.StdEncoding.EncodeToString(secrets[32:])
	config := "APP_NAME = Rehearse isolated runtime\nRUN_USER = git\nRUN_MODE = prod\n[database]\nDB_TYPE = postgres\nHOST = db:5432\nNAME = rehearse\nUSER = rehearse\nPASSWD =\nSSL_MODE = disable\n[repository]\nROOT = /data/git/repositories\n[server]\nAPP_DATA_PATH = /data/gitea\nPROTOCOL = http\nDOMAIN = app\nROOT_URL = http://app:3000/\nHTTP_ADDR = 0.0.0.0\nHTTP_PORT = 3000\nDISABLE_SSH = true\nSTART_SSH_SERVER = false\nLFS_START_SERVER = false\nOFFLINE_MODE = true\n[security]\nINSTALL_LOCK = true\nSECRET_KEY = " + secretKey + "\nINTERNAL_TOKEN = " + internalToken + "\n[service]\nDISABLE_REGISTRATION = true\nENABLE_NOTIFY_MAIL = false\n[session]\nPROVIDER = memory\n[actions]\nENABLED = false\n[cron]\nENABLED = false\n[log]\nMODE = console\nLEVEL = warn\n"
	var output bytes.Buffer
	tw := tar.NewWriter(&output)
	if err := tw.WriteHeader(&tar.Header{Name: ".rehearse-runtime", Typeflag: tar.TypeDir, Mode: 0700, Uid: 1000, Gid: 1000}); err != nil {
		return nil, err
	}
	if err := tw.WriteHeader(&tar.Header{Name: ".rehearse-runtime/app.ini", Typeflag: tar.TypeReg, Mode: 0600, Uid: 1000, Gid: 1000, Size: int64(len(config))}); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(tw, config); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
