package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/forgejo"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
)

const (
	apiLimit         = miniflux.MaxAPIResponseBytes + 8
	queryOutputLimit = 1 << 20
	cleanupTimeout   = 2 * time.Minute
	apiReadyTimeout  = 90 * time.Second
)

type runtimeClient interface {
	Info(context.Context) (engine.Daemon, error)
	CreatePhase(context.Context, state.Run, string, string) error
	Start(context.Context, state.Run, string, string) error
	Inside(context.Context, state.Run, string, string, []string, io.Reader, io.Writer) error
	InsideBytes(context.Context, state.Run, string, string, []string, io.Reader, int) ([]byte, error)
	WaitDatabase(context.Context, state.Run, string) error
	WaitMigration(context.Context, state.Run, string) error
	VerifyPhase(context.Context, state.Run, string) error
	Cleanup(context.Context, state.Run) error
}

type apiObservation struct {
	UserID         int64
	Username       string
	Categories     int64
	Feeds          int64
	CategoryFeeds  int64
	CategoryUnread int64
	UnreadTotal    int64
	ReadTotal      int64
	StarredTotal   int64
}

type apiUser struct {
	ID       *int64  `json:"id"`
	Username *string `json:"username"`
}

type apiVersion struct {
	Version *string `json:"version"`
}

type apiCategory struct {
	ID           *int64  `json:"id"`
	UserID       *int64  `json:"user_id"`
	Title        *string `json:"title"`
	HideGlobally *bool   `json:"hide_globally"`
	FeedCount    *int64  `json:"feed_count"`
	TotalUnread  *int64  `json:"total_unread"`
}

type apiFeed struct {
	ID       *int64      `json:"id"`
	UserID   *int64      `json:"user_id"`
	Title    *string     `json:"title"`
	SiteURL  *string     `json:"site_url"`
	FeedURL  *string     `json:"feed_url"`
	Category *apiFeedCat `json:"category"`
}

type apiFeedCat struct {
	ID     *int64  `json:"id"`
	UserID *int64  `json:"user_id"`
	Title  *string `json:"title"`
}

type apiEntryResult struct {
	Total   *int64      `json:"total"`
	Entries []*apiEntry `json:"entries"`
}

type apiEntry struct {
	ID      *int64  `json:"id"`
	UserID  *int64  `json:"user_id"`
	FeedID  *int64  `json:"feed_id"`
	Hash    *string `json:"hash"`
	Title   *string `json:"title"`
	Status  *string `json:"status"`
	Starred *bool   `json:"starred"`
}

type dbCounts struct {
	Users          int64
	Categories     int64
	Feeds          int64
	EntriesUnread  int64
	EntriesRead    int64
	EntriesRemoved int64
	EntriesStarred int64
	EntriesTagged  int64
}

type dbObservation struct {
	Schema     int
	Counts     dbCounts
	Projection miniflux.FingerprintResult
	Removed    miniflux.FingerprintResult
	Tombstones miniflux.FingerprintResult
}

var apiEndpoints = []string{
	"/v1/me",
	"/v1/version",
	"/v1/categories?counts=true",
	"/v1/feeds",
	"/v1/entries?limit=100&status=unread",
	"/v1/entries?limit=100&status=read",
	"/v1/entries?limit=100&starred=true",
}

// Run executes a closed built-in adapter against a fresh set of owned
// resources. Configuration and authentication are validated before it asks
// Docker to create the persisted run intent.
func Run(ctx context.Context, cfg spec.Config, store *state.Store, client *engine.Engine, auth miniflux.Auth) (*report.Report, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, code("CANCELED")
	}
	if cfg.Adapter == "forgejo" {
		if client == nil {
			return nil, code("OPERATION_FAILED")
		}
		return runForgejoWithRuntime(ctx, cfg, store, client, forgejo.Auth{Token: auth.APIToken})
	}
	if err := preflight(ctx, cfg, auth); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, code("OPERATION_FAILED")
	}
	return runWithRuntime(ctx, cfg, store, client, auth)
}

func preflight(ctx context.Context, cfg spec.Config, auth miniflux.Auth) error {
	if ctx == nil || ctx.Err() != nil {
		return code("CANCELED")
	}
	if err := spec.Validate(cfg); err != nil {
		return code("CONFIG_INVALID")
	}
	if cfg.Adapter != "miniflux" {
		return code("ADAPTER_UNSUPPORTED")
	}
	if _, err := BuildPlan(ctx, cfg); err != nil {
		return err
	}
	if _, err := miniflux.CurlConfig("/v1/me", &auth); err != nil {
		return code("AUTH_INVALID")
	}
	return nil
}

func runWithRuntime(ctx context.Context, cfg spec.Config, store *state.Store, client runtimeClient, auth miniflux.Auth) (result *report.Report, returnErr error) {
	if err := preflight(ctx, cfg, auth); err != nil {
		return nil, err
	}
	platform, err := hostPlatform()
	if err != nil {
		return nil, err
	}
	if store == nil || client == nil {
		return nil, code("OPERATION_FAILED")
	}
	daemon, err := client.Info(ctx)
	if err != nil || strings.TrimSpace(daemon.ID) == "" {
		return nil, code("OPERATION_FAILED")
	}
	intent, err := store.Create(daemon.ID)
	if err != nil {
		return nil, code("OPERATION_FAILED")
	}
	result = report.New(intent.ID, platform, time.Now().UTC())
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
	cleanupDone := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		latest, loadErr := store.Load(intent.ID)
		cleanupErr := loadErr
		if cleanupErr == nil {
			cleanupErr = client.Cleanup(cleanupCtx, latest)
		}
		cleanupDone = cleanupErr == nil
		if cleanupErr != nil {
			_ = result.Set("cleanup.ownership", "failed", "CLEANUP_HELD")
		} else {
			_ = result.Set("cleanup.ownership", "passed", "VALIDATED")
		}
		if staged != nil {
			unchanged, readErr := sourceUnchangedContext(ctx, *staged)
			if readErr != nil {
				failureCode := "OPERATION_FAILED"
				if ctx.Err() != nil {
					failureCode = "CANCELED"
				}
				_ = result.Set("source.unchanged", "failed", failureCode)
				if returnErr == nil {
					returnErr = code(failureCode)
				}
			} else if !unchanged {
				_ = result.Set("source.unchanged", "failed", "SOURCE_CHANGED")
				if returnErr == nil {
					returnErr = code("SOURCE_CHANGED")
				}
			} else {
				_ = result.Set("source.unchanged", "passed", "VALIDATED")
			}
		}
		if cleanupErr != nil && returnErr == nil {
			returnErr = code("CLEANUP_HELD")
		}
		finished := time.Now().UTC()
		result.FinishedAt = &finished
		result.Result = result.Outcome()
		latest, loadErr = store.Load(intent.ID)
		if loadErr != nil {
			invalidateTerminalReport(runDir)
			if returnErr == nil {
				returnErr = code("OPERATION_FAILED")
			}
			if releaseErr := lock.Release(); releaseErr == nil {
				locked = false
			}
		} else {
			switch {
			case latest.PendingBackup != nil:
				// Keep the staging state paired with its pending backup intent so
				// the store's explicit backup-recovery rules remain valid.
				latest.Status = "staging"
			case !cleanupDone:
				latest.Status = "cleanup-held"
			case result.Result == "passed":
				latest.Status = "completed"
			default:
				latest.Status = "failed"
			}
			finalErr, released := commitTerminalReport(runDir, result, latest, func(run state.Run) error {
				return store.Save(lock, run)
			}, lock.Release)
			if finalErr != nil && returnErr == nil {
				returnErr = finalErr
			}
			if released {
				locked = false
			}
		}
	}()

	fail := func(checkID, failureCode string) error {
		if ctx.Err() != nil {
			failureCode = "CANCELED"
		}
		_ = result.Set(checkID, "failed", failureCode)
		return code(failureCode)
	}
	pass := func(checkID string) {
		_ = result.Set(checkID, "passed", "VALIDATED")
	}

	backup, err := store.StageBackup(ctx, lock, cfg.BackupPath)
	if err != nil {
		return result, fail("backup.inspect", "BACKUP_FAILED")
	}
	staged = &backup
	result.BackupSHA256 = backup.SHA256
	result.BackupBytes = uint64(backup.Bytes)
	run, err := store.Load(intent.ID)
	if err != nil {
		return result, code("OPERATION_FAILED")
	}
	run.Status = "running"
	if err := store.Save(lock, run); err != nil {
		return result, code("OPERATION_FAILED")
	}

	if err := client.CreatePhase(ctx, run, runDir, "baseline"); err != nil {
		return result, fail("baseline.network", "NETWORK_FAILED")
	}
	if err := client.Start(ctx, run, "baseline", "db"); err != nil {
		return result, fail("baseline.restore", "OPERATION_FAILED")
	}
	if err := client.WaitDatabase(ctx, run, "baseline"); err != nil {
		return result, fail("baseline.restore", "OPERATION_FAILED")
	}
	if err := inspectArchive(ctx, client, store, run, "baseline"); err != nil {
		return result, fail("backup.inspect", "BACKUP_FAILED")
	}
	pass("backup.inspect")
	if err := restoreArchive(ctx, client, store, run, "baseline"); err != nil {
		return result, fail("baseline.restore", "RESTORE_FAILED")
	}
	pass("baseline.restore")
	baselineSchema, err := readSchema(ctx, client, run, "baseline")
	if err != nil {
		return result, fail("baseline.schema", "OPERATION_FAILED")
	}
	if baselineSchema != miniflux.BaselineSchema {
		return result, fail("baseline.schema", "SCHEMA_MISMATCH")
	}
	pass("baseline.schema")
	baselineData, err := readDatabase(ctx, client, run, "baseline", false)
	if err != nil {
		return result, fail("baseline.data", "OPERATION_FAILED")
	}
	setSnapshot(result, "baseline", baselineData.Projection)
	if err := startApplication(ctx, client, run, "baseline", auth); err != nil {
		return result, fail("baseline.auth", "AUTH_FAILED")
	}
	baselineAPI, err := authenticatedObservation(ctx, client, run, "baseline", auth, "2.2.19")
	if err != nil {
		return result, fail("baseline.auth", "AUTH_FAILED")
	}
	pass("baseline.auth")
	if err := unauthenticatedProbe(ctx, client, run, "baseline"); err != nil {
		failure := "AUTH_FAILED"
		if err.Error() == "AUTH_BYPASS" {
			failure = "AUTH_BYPASS"
		}
		return result, fail("baseline.unauthenticated", failure)
	}
	pass("baseline.unauthenticated")
	if err := client.VerifyPhase(ctx, run, "baseline"); err != nil {
		return result, fail("baseline.network", "NETWORK_FAILED")
	}
	pass("baseline.network")
	pass("baseline.data")

	if err := client.CreatePhase(ctx, run, runDir, "target"); err != nil {
		return result, fail("target.network", "NETWORK_FAILED")
	}
	if err := client.Start(ctx, run, "target", "db"); err != nil {
		return result, fail("target.restore", "RESTORE_FAILED")
	}
	if err := client.WaitDatabase(ctx, run, "target"); err != nil {
		return result, fail("target.restore", "RESTORE_FAILED")
	}
	if err := restoreArchive(ctx, client, store, run, "target"); err != nil {
		return result, fail("target.restore", "RESTORE_FAILED")
	}
	pass("target.restore")
	targetSourceSchema, err := readSchema(ctx, client, run, "target")
	if err != nil {
		return result, fail("target.migration", "OPERATION_FAILED")
	}
	if targetSourceSchema != miniflux.BaselineSchema {
		return result, fail("target.migration", "SCHEMA_MISMATCH")
	}
	if err := client.Start(ctx, run, "target", "migration"); err != nil {
		return result, fail("target.migration", "MIGRATION_FAILED")
	}
	if err := client.WaitMigration(ctx, run, "target"); err != nil {
		return result, fail("target.migration", "MIGRATION_FAILED")
	}
	pass("target.migration")
	targetSchema, err := readSchema(ctx, client, run, "target")
	if err != nil {
		return result, fail("target.schema", "OPERATION_FAILED")
	}
	if targetSchema != miniflux.TargetSchema {
		return result, fail("target.schema", "SCHEMA_MISMATCH")
	}
	pass("target.schema")
	targetData, err := readDatabase(ctx, client, run, "target", true)
	if err != nil {
		return result, fail("target.data", "OPERATION_FAILED")
	}
	setSnapshot(result, "target", targetData.Projection)
	if err := removedTransformation(baselineData, targetData); err != nil {
		return result, fail("target.removed-transformation", "DATA_CHANGED")
	}
	pass("target.removed-transformation")
	if err := startApplication(ctx, client, run, "target", auth); err != nil {
		return result, fail("target.auth", "AUTH_FAILED")
	}
	targetAPI, err := authenticatedObservation(ctx, client, run, "target", auth, "2.3.3")
	if err != nil {
		return result, fail("target.auth", "AUTH_FAILED")
	}
	pass("target.auth")
	if !databaseUnchanged(baselineData, targetData, false) || targetAPI != baselineAPI {
		return result, fail("target.data", "DATA_CHANGED")
	}
	if err := unauthenticatedProbe(ctx, client, run, "target"); err != nil {
		failure := "AUTH_FAILED"
		if err.Error() == "AUTH_BYPASS" {
			failure = "AUTH_BYPASS"
		}
		return result, fail("target.unauthenticated", failure)
	}
	pass("target.unauthenticated")
	if err := client.VerifyPhase(ctx, run, "target"); err != nil {
		return result, fail("target.network", "NETWORK_FAILED")
	}
	pass("target.network")
	pass("target.data")

	if err := client.CreatePhase(ctx, run, runDir, "recovery"); err != nil {
		return result, fail("recovery.network", "NETWORK_FAILED")
	}
	if err := client.Start(ctx, run, "recovery", "db"); err != nil {
		return result, fail("recovery.restore", "RECOVERY_FAILED")
	}
	if err := client.WaitDatabase(ctx, run, "recovery"); err != nil {
		return result, fail("recovery.restore", "RECOVERY_FAILED")
	}
	if err := restoreArchive(ctx, client, store, run, "recovery"); err != nil {
		return result, fail("recovery.restore", "RECOVERY_FAILED")
	}
	pass("recovery.restore")
	recoverySchema, err := readSchema(ctx, client, run, "recovery")
	if err != nil {
		return result, fail("recovery.schema", "OPERATION_FAILED")
	}
	if recoverySchema != miniflux.BaselineSchema {
		return result, fail("recovery.schema", "SCHEMA_MISMATCH")
	}
	pass("recovery.schema")
	recoveryData, err := readDatabase(ctx, client, run, "recovery", false)
	if err != nil {
		return result, fail("recovery.data", "OPERATION_FAILED")
	}
	setSnapshot(result, "recovery", recoveryData.Projection)
	if err := startApplication(ctx, client, run, "recovery", auth); err != nil {
		return result, fail("recovery.auth", "AUTH_FAILED")
	}
	recoveryAPI, err := authenticatedObservation(ctx, client, run, "recovery", auth, "2.2.19")
	if err != nil {
		return result, fail("recovery.auth", "AUTH_FAILED")
	}
	pass("recovery.auth")
	if !databaseUnchanged(baselineData, recoveryData, true) || recoveryAPI != baselineAPI {
		return result, fail("recovery.data", "DATA_CHANGED")
	}
	pass("recovery.data")
	if err := unauthenticatedProbe(ctx, client, run, "recovery"); err != nil {
		failure := "AUTH_FAILED"
		if err.Error() == "AUTH_BYPASS" {
			failure = "AUTH_BYPASS"
		}
		return result, fail("recovery.unauthenticated", failure)
	}
	pass("recovery.unauthenticated")
	if err := client.VerifyPhase(ctx, run, "recovery"); err != nil {
		return result, fail("recovery.network", "NETWORK_FAILED")
	}
	pass("recovery.network")
	return result, nil
}

func hostPlatform() (string, error) {
	if runtime.GOARCH != "amd64" {
		return "", code("DOCKER_PLATFORM_UNSUPPORTED")
	}
	switch runtime.GOOS {
	case "linux":
		return "linux/amd64", nil
	case "windows":
		return "windows/amd64", nil
	default:
		return "", code("DOCKER_PLATFORM_UNSUPPORTED")
	}
}

func decodeAPIObservation(responses map[string][]byte, expectedVersion string) (apiObservation, error) {
	if expectedVersion != "2.2.19" && expectedVersion != "2.3.3" {
		return apiObservation{}, code("API_OBSERVATION_INVALID")
	}
	for _, endpoint := range apiEndpoints {
		body, ok := responses[endpoint]
		if !ok || len(body) == 0 || len(body) > miniflux.MaxAPIResponseBytes || !json.Valid(body) {
			return apiObservation{}, code("API_OBSERVATION_INVALID")
		}
	}
	var user apiUser
	if json.Unmarshal(responses["/v1/me"], &user) != nil || user.ID == nil || *user.ID <= 0 || user.Username == nil || strings.TrimSpace(*user.Username) == "" {
		return apiObservation{}, code("API_OBSERVATION_INVALID")
	}
	var version apiVersion
	if json.Unmarshal(responses["/v1/version"], &version) != nil || version.Version == nil || *version.Version != expectedVersion {
		return apiObservation{}, code("API_VERSION_MISMATCH")
	}
	var categories []*apiCategory
	if json.Unmarshal(responses["/v1/categories?counts=true"], &categories) != nil || categories == nil {
		return apiObservation{}, code("API_OBSERVATION_INVALID")
	}
	var categoryFeeds, categoryUnread int64
	for _, category := range categories {
		if category == nil || category.ID == nil || *category.ID <= 0 || category.UserID == nil || *category.UserID != *user.ID ||
			category.Title == nil || strings.TrimSpace(*category.Title) == "" || category.HideGlobally == nil ||
			category.FeedCount == nil || *category.FeedCount < 0 || category.TotalUnread == nil || *category.TotalUnread < 0 {
			return apiObservation{}, code("API_OBSERVATION_INVALID")
		}
		categoryFeeds += *category.FeedCount
		categoryUnread += *category.TotalUnread
	}
	var feeds []*apiFeed
	if json.Unmarshal(responses["/v1/feeds"], &feeds) != nil || feeds == nil {
		return apiObservation{}, code("API_OBSERVATION_INVALID")
	}
	for _, feed := range feeds {
		if feed == nil || feed.ID == nil || *feed.ID <= 0 || feed.UserID == nil || *feed.UserID != *user.ID ||
			feed.Title == nil || feed.FeedURL == nil || strings.TrimSpace(*feed.FeedURL) == "" || feed.SiteURL == nil || feed.Category == nil ||
			feed.Category.ID == nil || *feed.Category.ID <= 0 || feed.Category.UserID == nil || *feed.Category.UserID != *user.ID || feed.Category.Title == nil {
			return apiObservation{}, code("API_OBSERVATION_INVALID")
		}
	}
	unread, err := decodeEntryResult(responses["/v1/entries?limit=100&status=unread"], *user.ID, "unread", false)
	if err != nil {
		return apiObservation{}, err
	}
	read, err := decodeEntryResult(responses["/v1/entries?limit=100&status=read"], *user.ID, "read", false)
	if err != nil {
		return apiObservation{}, err
	}
	starred, err := decodeEntryResult(responses["/v1/entries?limit=100&starred=true"], *user.ID, "", true)
	if err != nil {
		return apiObservation{}, err
	}
	if categoryFeeds != int64(len(feeds)) || categoryUnread != unread.total {
		return apiObservation{}, code("API_OBSERVATION_INVALID")
	}
	return apiObservation{
		UserID: *user.ID, Username: *user.Username,
		Categories: int64(len(categories)), Feeds: int64(len(feeds)),
		CategoryFeeds: categoryFeeds, CategoryUnread: categoryUnread,
		UnreadTotal: unread.total, ReadTotal: read.total, StarredTotal: starred.total,
	}, nil
}

type decodedEntryResult struct {
	total   int64
	entries []*apiEntry
}

func decodeEntryResult(raw []byte, userID int64, expectedStatus string, starred bool) (decodedEntryResult, error) {
	var result apiEntryResult
	if json.Unmarshal(raw, &result) != nil || result.Total == nil || *result.Total < 0 || result.Entries == nil || len(result.Entries) > 100 || *result.Total < int64(len(result.Entries)) {
		return decodedEntryResult{}, code("API_OBSERVATION_INVALID")
	}
	if *result.Total <= 100 && *result.Total != int64(len(result.Entries)) {
		return decodedEntryResult{}, code("API_OBSERVATION_INVALID")
	}
	if *result.Total > 100 && len(result.Entries) != 100 {
		return decodedEntryResult{}, code("API_OBSERVATION_INVALID")
	}
	for _, entry := range result.Entries {
		if entry == nil || entry.ID == nil || *entry.ID <= 0 || entry.UserID == nil || *entry.UserID != userID ||
			entry.FeedID == nil || *entry.FeedID <= 0 || entry.Hash == nil || entry.Title == nil || entry.Status == nil || entry.Starred == nil {
			return decodedEntryResult{}, code("API_OBSERVATION_INVALID")
		}
		if expectedStatus != "" && *entry.Status != expectedStatus {
			return decodedEntryResult{}, code("API_OBSERVATION_INVALID")
		}
		if starred && !*entry.Starred {
			return decodedEntryResult{}, code("API_OBSERVATION_INVALID")
		}
		if *entry.Status != "read" && *entry.Status != "unread" {
			return decodedEntryResult{}, code("API_OBSERVATION_INVALID")
		}
	}
	return decodedEntryResult{total: *result.Total, entries: result.Entries}, nil
}

func inspectArchive(ctx context.Context, client runtimeClient, store *state.Store, run state.Run, phase string) error {
	backup, err := store.OpenVerifiedBackupContext(ctx, run.ID)
	if err != nil {
		return code("BACKUP_FAILED")
	}
	defer backup.Close()
	if err := client.Inside(ctx, run, phase, "db", []string{"pg_restore", "--list"}, backup, io.Discard); err != nil {
		return code("BACKUP_FAILED")
	}
	return nil
}

func restoreArchive(ctx context.Context, client runtimeClient, store *state.Store, run state.Run, phase string) error {
	backup, err := store.OpenVerifiedBackupContext(ctx, run.ID)
	if err != nil {
		return code("RESTORE_FAILED")
	}
	defer backup.Close()
	args := []string{"pg_restore", "--username=rehearse", "--dbname=rehearse", "--no-owner", "--no-acl", "--exit-on-error"}
	if err := client.Inside(ctx, run, phase, "db", args, backup, io.Discard); err != nil {
		return code("RESTORE_FAILED")
	}
	return nil
}

func startApplication(ctx context.Context, client runtimeClient, run state.Run, phase string, auth miniflux.Auth) error {
	if err := client.Start(ctx, run, phase, "app"); err != nil {
		return code("AUTH_FAILED")
	}
	if err := client.Start(ctx, run, phase, "probe"); err != nil {
		return code("AUTH_FAILED")
	}
	return waitAPIReady(ctx, client, run, phase, auth)
}

func waitAPIReady(ctx context.Context, client runtimeClient, run state.Run, phase string, auth miniflux.Auth) error {
	readyCtx, cancel := context.WithTimeout(ctx, apiReadyTimeout)
	defer cancel()
	for {
		status, body, err := requestAPI(readyCtx, client, run, phase, "/v1/me", &auth)
		if err == nil && status == 200 && validCurrentUser(body) {
			return nil
		}
		if err == nil && status >= 400 && status < 500 {
			return code("AUTH_FAILED")
		}
		select {
		case <-readyCtx.Done():
			return code("AUTH_FAILED")
		case <-time.After(time.Second):
		}
	}
}

func validCurrentUser(body []byte) bool {
	var user apiUser
	return json.Unmarshal(body, &user) == nil && user.ID != nil && *user.ID > 0 && user.Username != nil && strings.TrimSpace(*user.Username) != ""
}

func authenticatedObservation(ctx context.Context, client runtimeClient, run state.Run, phase string, auth miniflux.Auth, expectedVersion string) (apiObservation, error) {
	responses := make(map[string][]byte, len(apiEndpoints))
	for _, endpoint := range apiEndpoints {
		status, body, err := requestAPI(ctx, client, run, phase, endpoint, &auth)
		if err != nil || status != 200 {
			return apiObservation{}, code("AUTH_FAILED")
		}
		responses[endpoint] = body
	}
	observation, err := decodeAPIObservation(responses, expectedVersion)
	if err != nil {
		return apiObservation{}, code("AUTH_FAILED")
	}
	return observation, nil
}

func requestAPI(ctx context.Context, client runtimeClient, run state.Run, phase, endpoint string, auth *miniflux.Auth) (int, []byte, error) {
	config, err := miniflux.CurlConfig(endpoint, auth)
	if err != nil {
		return 0, nil, code("AUTH_FAILED")
	}
	output, err := client.InsideBytes(ctx, run, phase, "probe", []string{"curl", "--disable", "--config", "-"}, bytes.NewReader(config), apiLimit)
	if err != nil {
		return 0, nil, code("OPERATION_FAILED")
	}
	status, body, err := miniflux.ParseResponse(output)
	if err != nil {
		return 0, nil, code("AUTH_FAILED")
	}
	return status, body, nil
}

func unauthenticatedProbe(ctx context.Context, client runtimeClient, run state.Run, phase string) error {
	status, _, err := requestAPI(ctx, client, run, phase, "/v1/me", nil)
	if err != nil {
		return code("AUTH_FAILED")
	}
	if status != 401 {
		return code("AUTH_BYPASS")
	}
	return nil
}

func readSchema(ctx context.Context, client runtimeClient, run state.Run, phase string) (int, error) {
	raw, err := queryOutput(ctx, client, run, phase, miniflux.SchemaSQL, 64)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return 0, code("OPERATION_FAILED")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, code("OPERATION_FAILED")
		}
	}
	schema, err := strconv.Atoi(value)
	if err != nil {
		return 0, code("OPERATION_FAILED")
	}
	return schema, nil
}

func readCounts(ctx context.Context, client runtimeClient, run state.Run, phase string) (dbCounts, error) {
	raw, err := queryOutput(ctx, client, run, phase, miniflux.CountsSQL, queryOutputLimit)
	if err != nil {
		return dbCounts{}, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(bytesTrimSpace(raw), &fields) != nil || len(fields) != 8 {
		return dbCounts{}, code("OPERATION_FAILED")
	}
	values := map[string]*int64{
		"users": nil, "categories": nil, "feeds": nil, "entries_unread": nil,
		"entries_read": nil, "entries_removed": nil, "entries_starred": nil, "entries_tagged": nil,
	}
	for key := range values {
		field, ok := fields[key]
		if !ok {
			return dbCounts{}, code("OPERATION_FAILED")
		}
		text := string(field)
		if text == "" || strings.Trim(text, "0123456789") != "" {
			return dbCounts{}, code("OPERATION_FAILED")
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil || value < 0 {
			return dbCounts{}, code("OPERATION_FAILED")
		}
		v := value
		values[key] = &v
	}
	return dbCounts{
		Users: *values["users"], Categories: *values["categories"], Feeds: *values["feeds"],
		EntriesUnread: *values["entries_unread"], EntriesRead: *values["entries_read"], EntriesRemoved: *values["entries_removed"],
		EntriesStarred: *values["entries_starred"], EntriesTagged: *values["entries_tagged"],
	}, nil
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func queryOutput(ctx context.Context, client runtimeClient, run state.Run, phase, query string, limit int) ([]byte, error) {
	args := []string{"psql", "--username", "rehearse", "--dbname", "rehearse", "--no-psqlrc", "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1"}
	output, err := client.InsideBytes(ctx, run, phase, "db", args, strings.NewReader(query+"\n"), limit)
	if err != nil {
		return nil, code("OPERATION_FAILED")
	}
	return output, nil
}

func streamFingerprint(ctx context.Context, client runtimeClient, run state.Run, phase, query string) (miniflux.FingerprintResult, error) {
	reader, writer := io.Pipe()
	streamCtx, cancel := context.WithCancel(ctx)
	stopClose := context.AfterFunc(streamCtx, func() { _ = reader.CloseWithError(streamCtx.Err()) })
	commandDone := make(chan error, 1)
	go func() {
		args := []string{"psql", "--username", "rehearse", "--dbname", "rehearse", "--no-psqlrc", "--quiet", "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1"}
		err := client.Inside(streamCtx, run, phase, "db", args, strings.NewReader(query+"\n"), writer)
		if err != nil {
			_ = writer.CloseWithError(err)
		} else {
			_ = writer.Close()
		}
		commandDone <- err
	}()
	fingerprint, readErr := miniflux.Fingerprint(reader)
	if readErr != nil {
		cancel()
		_ = reader.CloseWithError(readErr)
	}
	commandErr := <-commandDone
	stopClose()
	_ = reader.Close()
	cancel()
	if ctx.Err() != nil {
		return miniflux.FingerprintResult{}, code("CANCELED")
	}
	if readErr != nil || commandErr != nil {
		return miniflux.FingerprintResult{}, code("OPERATION_FAILED")
	}
	return fingerprint, nil
}

func readDatabase(ctx context.Context, client runtimeClient, run state.Run, phase string, target bool) (dbObservation, error) {
	counts, err := readCounts(ctx, client, run, phase)
	if err != nil {
		return dbObservation{}, err
	}
	projection, err := streamFingerprint(ctx, client, run, phase, miniflux.ProjectionSQL)
	if err != nil {
		return dbObservation{}, err
	}
	removedQuery := miniflux.RemovedKeysSQL
	if target {
		removedQuery = miniflux.TombstoneKeysSQL
	}
	removed, err := streamFingerprint(ctx, client, run, phase, removedQuery)
	if err != nil {
		return dbObservation{}, err
	}
	data := dbObservation{Counts: counts, Projection: projection}
	if target {
		data.Tombstones = removed
	} else {
		data.Removed = removed
	}
	return data, nil
}

func sameCoreCounts(a, b dbCounts) bool {
	return a.Users == b.Users && a.Categories == b.Categories && a.Feeds == b.Feeds &&
		a.EntriesUnread == b.EntriesUnread && a.EntriesRead == b.EntriesRead &&
		a.EntriesStarred == b.EntriesStarred && a.EntriesTagged == b.EntriesTagged
}

func databaseUnchanged(baseline, current dbObservation, includeRemoved bool) bool {
	if baseline.Projection != current.Projection || !sameCoreCounts(baseline.Counts, current.Counts) {
		return false
	}
	if includeRemoved {
		return baseline.Counts.EntriesRemoved == current.Counts.EntriesRemoved && baseline.Removed == current.Removed
	}
	return true
}

func removedTransformation(baseline, target dbObservation) error {
	if target.Counts.EntriesRemoved != 0 || baseline.Removed != target.Tombstones {
		return code("DATA_CHANGED")
	}
	return nil
}

func setSnapshot(r *report.Report, phase string, value miniflux.FingerprintResult) {
	r.Snapshots[phase] = report.Snapshot{SHA256: value.SHA256, Rows: value.Rows, Bytes: value.Bytes}
}

type backupContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r backupContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func sourceUnchangedContext(ctx context.Context, backup state.Backup) (bool, error) {
	if ctx == nil || ctx.Err() != nil {
		return false, code("CANCELED")
	}
	info, err := os.Lstat(backup.SourcePath)
	if err != nil {
		return false, code("OPERATION_FAILED")
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > state.MaxBackupBytes {
		return false, nil
	}
	f, err := os.Open(backup.SourcePath)
	if err != nil {
		return false, code("OPERATION_FAILED")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return false, code("OPERATION_FAILED")
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return false, nil
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(backupContextReader{ctx: ctx, reader: f}, state.MaxBackupBytes+1))
	if ctx.Err() != nil {
		return false, code("CANCELED")
	}
	if err != nil {
		return false, code("OPERATION_FAILED")
	}
	if n > state.MaxBackupBytes {
		return false, nil
	}
	if n != backup.Bytes || hex.EncodeToString(hash.Sum(nil)) != backup.SHA256 {
		return false, nil
	}
	return true, nil
}

func writeReportFiles(dir string, r *report.Report) error {
	jsonBytes, err := r.JSON()
	if err != nil {
		return code("REPORT_WRITE_FAILED")
	}
	var html strings.Builder
	if err := r.HTML(&html); err != nil {
		return code("REPORT_WRITE_FAILED")
	}
	if err := atomicReportWrite(dir, "report.json", jsonBytes); err != nil {
		return code("REPORT_WRITE_FAILED")
	}
	if err := atomicReportWrite(dir, "report.html", []byte(html.String())); err != nil {
		return code("REPORT_WRITE_FAILED")
	}
	return nil
}

func atomicReportWrite(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".report-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return nil
}

// ReadReport loads only a validated report under the exact run directory
// recorded by the private state store.
func ReadReport(store *state.Store, runID string) (*report.Report, error) {
	if !validRunID(runID) {
		return nil, code("RUN_ID_INVALID")
	}
	if store == nil {
		return nil, code("REPORT_READ_FAILED")
	}
	run, err := store.Load(runID)
	if err != nil {
		return nil, code("REPORT_READ_FAILED")
	}
	switch run.Status {
	case "completed", "failed", "cleaned", "cleanup-held":
	default:
		return nil, code("REPORT_READ_FAILED")
	}
	if _, err := store.InspectRunLock(runID); err == nil || err.Error() != "RUN_LOCK_NOT_FOUND" {
		return nil, code("REPORT_READ_FAILED")
	}
	dir, err := store.RunDir(runID)
	if err != nil {
		return nil, code("REPORT_READ_FAILED")
	}
	if !terminalPublicationAbsent(dir) {
		return nil, code("REPORT_READ_FAILED")
	}
	path := filepath.Join(dir, "report.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > report.MaxReportBytes {
		return nil, code("REPORT_READ_FAILED")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, code("REPORT_READ_FAILED")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, code("REPORT_READ_FAILED")
	}
	value, err := report.Parse(f)
	if err != nil || value.RunID != runID {
		return nil, code("REPORT_FORMAT_INVALID")
	}
	if value.Adapter != run.AdapterID() || value.AdapterContractVersion != run.AdapterContractVersion || value.SchemaVersion != run.SchemaVersion {
		return nil, code("REPORT_FORMAT_INVALID")
	}
	if run.Status == "completed" && value.Result != "passed" || (run.Status == "failed" || run.Status == "cleanup-held") && value.Result == "passed" {
		return nil, code("REPORT_FORMAT_INVALID")
	}
	htmlPath := filepath.Join(dir, "report.html")
	htmlInfo, err := os.Lstat(htmlPath)
	if err != nil || !htmlInfo.Mode().IsRegular() || htmlInfo.Mode()&os.ModeSymlink != 0 || htmlInfo.Size() <= 0 || htmlInfo.Size() > report.MaxReportBytes {
		return nil, code("REPORT_READ_FAILED")
	}
	htmlFile, err := os.Open(htmlPath)
	if err != nil {
		return nil, code("REPORT_READ_FAILED")
	}
	defer htmlFile.Close()
	openedHTML, err := htmlFile.Stat()
	if err != nil || !openedHTML.Mode().IsRegular() || !os.SameFile(htmlInfo, openedHTML) {
		return nil, code("REPORT_READ_FAILED")
	}
	htmlBytes, err := io.ReadAll(io.LimitReader(htmlFile, report.MaxReportBytes+1))
	if err != nil || len(htmlBytes) == 0 || len(htmlBytes) > report.MaxReportBytes {
		return nil, code("REPORT_READ_FAILED")
	}
	var expectedHTML bytes.Buffer
	if err := value.HTML(&expectedHTML); err != nil || !bytes.Equal(htmlBytes, expectedHTML.Bytes()) {
		return nil, code("REPORT_FORMAT_INVALID")
	}
	return value, nil
}

// Cleanup serializes cleanup with any run/report operation and changes only
// the exact run's persisted status after verified Docker cleanup.
func Cleanup(ctx context.Context, store *state.Store, client *engine.Engine, runID string) error {
	if ctx == nil || ctx.Err() != nil {
		return code("CANCELED")
	}
	if !validRunID(runID) {
		return code("RUN_ID_INVALID")
	}
	if client == nil {
		return code("OPERATION_FAILED")
	}
	return cleanupWithRuntime(ctx, store, client, runID)
}

func cleanupWithRuntime(ctx context.Context, store *state.Store, client runtimeClient, runID string) (returnErr error) {
	if ctx == nil || ctx.Err() != nil {
		return code("CANCELED")
	}
	if !validRunID(runID) {
		return code("RUN_ID_INVALID")
	}
	if store == nil || client == nil {
		return code("OPERATION_FAILED")
	}
	lock, err := store.AcquireRunLock(runID)
	if err != nil {
		return code("OPERATION_FAILED")
	}
	defer func() {
		if err := lock.Release(); err != nil && returnErr == nil {
			returnErr = code("OPERATION_FAILED")
		}
	}()
	run, err := store.Load(runID)
	if err != nil {
		return code("OPERATION_FAILED")
	}
	cleanupErr := client.Cleanup(ctx, run)
	if cleanupErr != nil {
		if run.PendingBackup != nil {
			run.Status = "staging"
		} else {
			run.Status = "cleanup-held"
		}
		if saveErr := store.Save(lock, run); saveErr != nil {
			return code("OPERATION_FAILED")
		}
		if ctx.Err() != nil {
			return code("CANCELED")
		}
		return code("CLEANUP_HELD")
	}
	if run.PendingBackup != nil {
		run.Status = "staging"
	} else {
		run.Status = "cleaned"
	}
	if err := store.Save(lock, run); err != nil {
		return code("OPERATION_FAILED")
	}
	if ctx.Err() != nil {
		return code("CANCELED")
	}
	return nil
}

func validRunID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
