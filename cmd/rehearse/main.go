package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Pastalikek65/rehearse/internal/app"
	"github.com/Pastalikek65/rehearse/internal/engine"
	"github.com/Pastalikek65/rehearse/internal/miniflux"
	"github.com/Pastalikek65/rehearse/internal/report"
	"github.com/Pastalikek65/rehearse/internal/spec"
	"github.com/Pastalikek65/rehearse/internal/state"
)

var version = "1.0.0-development"

const rootUsage = `Usage: rehearse <command> [options]

Commands:
  plan <config.json>                 Validate a local plan without Docker
  run [--wsl-distro NAME] <config>   Run an isolated rehearsal
  report [--format json|html] <id>   Print a saved report
  cleanup [--wsl-distro NAME] <id>   Clean owned rehearsal resources
  history [--limit N] [--json]       List bounded private run summaries
  recover <run-id>                   Recover one verified dead local process lock
  archive --database D --data T --output Z  Package an offline Forgejo snapshot

Requirements: Linux amd64 with Docker Engine 28+ and Compose, or Windows with
an explicitly named WSL2 distribution containing Docker Engine and Compose.
Flags must appear before positional arguments. Use ` + "`rehearse <command> --help`" + ` for command help.
`

type buildPlanFunc func(context.Context, spec.Config) (app.Plan, error)
type resolveAuthFunc func(spec.Config, func(string) string) (miniflux.Auth, error)
type runFunc func(context.Context, spec.Config, *state.Store, *engine.Engine, miniflux.Auth) (*report.Report, error)
type readReportFunc func(*state.Store, string) (*report.Report, error)
type cleanupFunc func(context.Context, *state.Store, *engine.Engine, string) error
type newEngineFunc func(string) (*engine.Engine, error)
type readInputFunc func(string) (io.ReadCloser, error)

// cliOptions carries only dependencies that need isolation in command tests.
// Production never accepts the state root from command-line or config input.
type cliOptions struct {
	stateRoot   string
	goos        string
	goarch      string
	getenv      func(string) string
	userConfig  func() (string, error)
	readInput   readInputFunc
	buildPlan   buildPlanFunc
	resolveAuth resolveAuthFunc
	run         runFunc
	readReport  readReportFunc
	cleanup     cleanupFunc
	newEngine   newEngineFunc
}

func main() {
	os.Exit(runCLI())
}

func runCLI() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return execute(ctx, os.Args[1:], os.Stdout, os.Stderr, defaultOptions())
}

func defaultOptions() cliOptions {
	return cliOptions{
		goos:        runtime.GOOS,
		goarch:      runtime.GOARCH,
		getenv:      os.Getenv,
		userConfig:  os.UserConfigDir,
		readInput:   func(path string) (io.ReadCloser, error) { return os.Open(path) },
		buildPlan:   app.BuildPlan,
		resolveAuth: app.ResolveAuth,
		run:         app.Run,
		readReport:  app.ReadReport,
		cleanup:     app.Cleanup,
		newEngine:   func(distro string) (*engine.Engine, error) { return newEngine(runtime.GOOS, distro) },
	}
}

func execute(ctx context.Context, args []string, stdout, stderr io.Writer, opts cliOptions) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	opts = completeOptions(opts)
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		return writeText(stdout, stderr, rootUsage)
	}
	if len(args) == 1 && args[0] == "--version" {
		return writeText(stdout, stderr, "rehearse "+version+"\n")
	}
	if ctx.Err() != nil {
		return writeFailure(stderr, ctx.Err(), "CANCELED")
	}
	switch args[0] {
	case "plan":
		return commandPlan(ctx, args[1:], stdout, stderr, opts)
	case "run":
		return commandRun(ctx, args[1:], stdout, stderr, opts)
	case "report":
		return commandReport(ctx, args[1:], stdout, stderr, opts)
	case "cleanup":
		return commandCleanup(ctx, args[1:], stdout, stderr, opts)
	case "history":
		return commandHistory(args[1:], stdout, stderr, opts)
	case "recover":
		return commandRecover(ctx, args[1:], stdout, stderr, opts)
	case "archive":
		return commandArchive(ctx, args[1:], stdout, stderr)
	case "help":
		if len(args) != 2 {
			return writeFailure(stderr, nil, "ARGUMENTS_INVALID")
		}
		if usage, ok := commandUsage(args[1]); ok {
			return writeText(stdout, stderr, usage)
		}
		return writeFailure(stderr, nil, "COMMAND_UNKNOWN")
	default:
		return writeFailure(stderr, nil, "COMMAND_UNKNOWN")
	}
}

func completeOptions(opts cliOptions) cliOptions {
	defaults := defaultOptions()
	if opts.goos == "" {
		opts.goos = defaults.goos
	}
	if opts.goarch == "" {
		opts.goarch = defaults.goarch
	}
	if opts.getenv == nil {
		opts.getenv = defaults.getenv
	}
	if opts.userConfig == nil {
		opts.userConfig = defaults.userConfig
	}
	if opts.readInput == nil {
		opts.readInput = defaults.readInput
	}
	if opts.buildPlan == nil {
		opts.buildPlan = defaults.buildPlan
	}
	if opts.resolveAuth == nil {
		opts.resolveAuth = defaults.resolveAuth
	}
	if opts.run == nil {
		opts.run = defaults.run
	}
	if opts.readReport == nil {
		opts.readReport = defaults.readReport
	}
	if opts.cleanup == nil {
		opts.cleanup = defaults.cleanup
	}
	if opts.newEngine == nil {
		goos := opts.goos
		opts.newEngine = func(distro string) (*engine.Engine, error) { return newEngine(goos, distro) }
	}
	return opts
}

func commandUsage(command string) (string, bool) {
	switch command {
	case "plan":
		return "Usage: rehearse plan [--json] <config.json>\n\nBuild and print an offline plan. The backup path is relative to the config file.\n", true
	case "run":
		return "Usage: rehearse run [--wsl-distro NAME] <config.json>\n\nRun only against Linux Docker or the explicitly named Windows WSL2 distribution.\n", true
	case "report":
		return "Usage: rehearse report [--format json|html] <run-id>\n\nPrint a report already stored in the private Rehearse state directory.\n", true
	case "cleanup":
		return "Usage: rehearse cleanup [--wsl-distro NAME] <run-id>\n\nRemove only resources whose ownership is proven for this run.\n", true
	case "history":
		return "Usage: rehearse history [--limit N] [--json]\n\nPrint bounded run metadata only; invalid records are reported without their paths.\n", true
	case "recover":
		return "Usage: rehearse recover <run-id>\n\nRecover only a verified dead local process lock. Owned Docker resources are left for cleanup.\n", true
	case "archive":
		return "Usage: rehearse archive [--json] --database <database.pgdump> --data <forgejo-data.tar> --output <new.zip>\n\nPackage an offline Forgejo 15.0.9 / PostgreSQL 17.11 snapshot.\nProvide a consistent snapshot: stop Forgejo before capturing both inputs.\nThis command does not contact Docker or prove cross-file consistency.\nExisting outputs are never overwritten.\nThe resulting archive contains private instance data and must be kept private.\n", true
	default:
		return "", false
	}
}

func commandPlan(ctx context.Context, args []string, stdout, stderr io.Writer, opts cliOptions) int {
	fs := newFlagSet("plan")
	jsonOutput := fs.Bool("json", false, "print JSON")
	showHelp, showHelpShort := addHelpFlags(fs)
	if fs.Parse(args) != nil {
		return commandArgumentFailure(stderr, "plan")
	}
	if *showHelp || *showHelpShort {
		usage, _ := commandUsage("plan")
		return writeText(stdout, stderr, usage)
	}
	if len(fs.Args()) != 1 || strings.TrimSpace(fs.Args()[0]) == "" {
		return commandArgumentFailure(stderr, "plan")
	}
	cfg, err := readConfig(fs.Args()[0], opts)
	if err != nil {
		return writeFailure(stderr, err, "CONFIG_READ_FAILED")
	}
	plan, err := opts.buildPlan(ctx, cfg)
	if err != nil {
		return writeFailure(stderr, err, "OPERATION_FAILED")
	}
	if *jsonOutput {
		raw, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return writeFailure(stderr, err, "OPERATION_FAILED")
		}
		return writeBytes(stdout, stderr, append(raw, '\n'))
	}
	return printPlan(stdout, stderr, plan)
}

func commandRun(ctx context.Context, args []string, stdout, stderr io.Writer, opts cliOptions) int {
	fs := newFlagSet("run")
	distro := fs.String("wsl-distro", "", "explicit WSL2 distribution (Windows only)")
	showHelp, showHelpShort := addHelpFlags(fs)
	if fs.Parse(args) != nil {
		return commandArgumentFailure(stderr, "run")
	}
	if *showHelp || *showHelpShort {
		usage, _ := commandUsage("run")
		return writeText(stdout, stderr, usage)
	}
	if len(fs.Args()) != 1 || strings.TrimSpace(fs.Args()[0]) == "" {
		return commandArgumentFailure(stderr, "run")
	}
	if opts.goos == "windows" && *distro == "" {
		return writeFailure(stderr, nil, "WSL_DISTRIBUTION_REQUIRED")
	}
	if opts.goos != "windows" && *distro != "" {
		return writeFailure(stderr, nil, "WSL_PLATFORM_UNSUPPORTED")
	}
	if opts.goos == "linux" && opts.goarch != "amd64" {
		return writeFailure(stderr, nil, "DOCKER_PLATFORM_UNSUPPORTED")
	}
	cfg, err := readConfig(fs.Args()[0], opts)
	if err != nil {
		return writeFailure(stderr, err, "CONFIG_READ_FAILED")
	}
	auth, err := opts.resolveAuth(cfg, opts.getenv)
	if err != nil {
		return writeFailure(stderr, err, "OPERATION_FAILED")
	}
	engineClient, err := opts.newEngine(*distro)
	if err != nil {
		return writeFailure(stderr, err, "DOCKER_RUNTIME_UNAVAILABLE")
	}
	store, err := openState(opts)
	if err != nil {
		return writeFailure(stderr, err, "STATE_PATH_INVALID")
	}
	r, runErr := opts.run(ctx, cfg, store, engineClient, auth)
	if r == nil {
		return writeFailure(stderr, runErr, "OPERATION_FAILED")
	}
	if err := r.Validate(); err != nil {
		return writeFailure(stderr, err, "OPERATION_FAILED")
	}
	if runErr != nil && r.Result == "passed" {
		// The phase evidence can pass before report/state/lock finalization fails.
		// Do not advertise a completed pass or a report path in that case.
		if _, err := fmt.Fprintf(stdout, "Run: %s\nOutcome: failed\n", r.RunID); err != nil {
			return writeFailure(stderr, err, "OUTPUT_WRITE_FAILED")
		}
		return writeFailure(stderr, runErr, "OPERATION_FAILED")
	}
	if err := printRunSummary(stdout, store, r); err != nil {
		return writeFailure(stderr, err, "REPORT_PATH_UNAVAILABLE")
	}
	if runErr != nil {
		_ = writeFailure(stderr, runErr, "OPERATION_FAILED")
		return 1
	}
	if r.Result != "passed" {
		return 1
	}
	return 0
}

func commandReport(ctx context.Context, args []string, stdout, stderr io.Writer, opts cliOptions) int {
	fs := newFlagSet("report")
	format := fs.String("format", "json", "json or html")
	showHelp, showHelpShort := addHelpFlags(fs)
	if fs.Parse(args) != nil {
		return commandArgumentFailure(stderr, "report")
	}
	if *showHelp || *showHelpShort {
		usage, _ := commandUsage("report")
		return writeText(stdout, stderr, usage)
	}
	if len(fs.Args()) != 1 || strings.TrimSpace(fs.Args()[0]) == "" {
		return commandArgumentFailure(stderr, "report")
	}
	if !validRunID(fs.Args()[0]) {
		return writeFailure(stderr, nil, "RUN_ID_INVALID")
	}
	if *format != "json" && *format != "html" {
		return writeFailure(stderr, nil, "REPORT_FORMAT_UNSUPPORTED")
	}
	store, err := openState(opts)
	if err != nil {
		return writeFailure(stderr, err, "STATE_PATH_INVALID")
	}
	r, err := opts.readReport(store, fs.Args()[0])
	if err != nil {
		return writeFailure(stderr, err, "REPORT_READ_FAILED")
	}
	if ctx.Err() != nil {
		return writeFailure(stderr, ctx.Err(), "CANCELED")
	}
	if *format == "json" {
		raw, err := r.JSON()
		if err != nil {
			return writeFailure(stderr, err, "REPORT_FORMAT_INVALID")
		}
		return writeBytes(stdout, stderr, raw)
	}
	if err := r.HTML(stdout); err != nil {
		return writeFailure(stderr, err, "REPORT_FORMAT_INVALID")
	}
	return 0
}

func commandCleanup(ctx context.Context, args []string, stdout, stderr io.Writer, opts cliOptions) int {
	fs := newFlagSet("cleanup")
	distro := fs.String("wsl-distro", "", "explicit WSL2 distribution (Windows only)")
	showHelp, showHelpShort := addHelpFlags(fs)
	if fs.Parse(args) != nil {
		return commandArgumentFailure(stderr, "cleanup")
	}
	if *showHelp || *showHelpShort {
		usage, _ := commandUsage("cleanup")
		return writeText(stdout, stderr, usage)
	}
	if len(fs.Args()) != 1 || strings.TrimSpace(fs.Args()[0]) == "" {
		return commandArgumentFailure(stderr, "cleanup")
	}
	if !validRunID(fs.Args()[0]) {
		return writeFailure(stderr, nil, "RUN_ID_INVALID")
	}
	if opts.goos == "windows" && *distro == "" {
		return writeFailure(stderr, nil, "WSL_DISTRIBUTION_REQUIRED")
	}
	if opts.goos != "windows" && *distro != "" {
		return writeFailure(stderr, nil, "WSL_PLATFORM_UNSUPPORTED")
	}
	if opts.goos == "linux" && opts.goarch != "amd64" {
		return writeFailure(stderr, nil, "DOCKER_PLATFORM_UNSUPPORTED")
	}
	engineClient, err := opts.newEngine(*distro)
	if err != nil {
		return writeFailure(stderr, err, "DOCKER_RUNTIME_UNAVAILABLE")
	}
	store, err := openState(opts)
	if err != nil {
		return writeFailure(stderr, err, "STATE_PATH_INVALID")
	}
	if err := opts.cleanup(ctx, store, engineClient, fs.Args()[0]); err != nil {
		return writeFailure(stderr, err, "OPERATION_FAILED")
	}
	return writeText(stdout, stderr, "Cleanup completed for run "+fs.Args()[0]+".\n")
}

func commandHistory(args []string, stdout, stderr io.Writer, opts cliOptions) int {
	fs := newFlagSet("history")
	limit := fs.Int("limit", 0, "maximum rows (default 20, maximum 100)")
	jsonOutput := fs.Bool("json", false, "print JSON")
	showHelp, showHelpShort := addHelpFlags(fs)
	if fs.Parse(args) != nil {
		return commandArgumentFailure(stderr, "history")
	}
	if *showHelp || *showHelpShort {
		usage, _ := commandUsage("history")
		return writeText(stdout, stderr, usage)
	}
	if len(fs.Args()) != 0 {
		return commandArgumentFailure(stderr, "history")
	}
	if *limit < 0 || *limit > state.MaxHistoryEntries {
		return writeFailure(stderr, fixedError("HISTORY_LIMIT_INVALID"), "HISTORY_LIMIT_INVALID")
	}
	displayLimit := *limit
	if displayLimit == 0 {
		displayLimit = state.DefaultHistoryLimit
	}
	store, err := openState(opts)
	if err != nil {
		return writeFailure(stderr, err, "STATE_PATH_INVALID")
	}
	snapshot, err := store.History(*limit)
	if err != nil {
		return writeFailure(stderr, err, "HISTORY_READ_FAILED")
	}
	if *jsonOutput {
		raw, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return writeFailure(stderr, err, "OPERATION_FAILED")
		}
		if code := writeBytes(stdout, stderr, append(raw, '\n')); code != 0 {
			return code
		}
	} else if code := printHistory(stdout, stderr, snapshot, displayLimit); code != 0 {
		return code
	}
	if snapshot.InvalidRecordsCount > 0 {
		return writeFailure(stderr, nil, "HISTORY_INVALID_RECORDS")
	}
	if snapshot.ScanLimitReached {
		return writeFailure(stderr, nil, "HISTORY_SCAN_LIMIT_REACHED")
	}
	return 0
}

func commandRecover(ctx context.Context, args []string, stdout, stderr io.Writer, opts cliOptions) int {
	fs := newFlagSet("recover")
	showHelp, showHelpShort := addHelpFlags(fs)
	if fs.Parse(args) != nil {
		return commandArgumentFailure(stderr, "recover")
	}
	if *showHelp || *showHelpShort {
		usage, _ := commandUsage("recover")
		return writeText(stdout, stderr, usage)
	}
	if len(fs.Args()) != 1 || strings.TrimSpace(fs.Args()[0]) == "" {
		return commandArgumentFailure(stderr, "recover")
	}
	if !validRunID(fs.Args()[0]) {
		return writeFailure(stderr, nil, "RUN_ID_INVALID")
	}
	if ctx == nil || ctx.Err() != nil {
		return writeFailure(stderr, fixedError("CANCELED"), "CANCELED")
	}
	store, err := openState(opts)
	if err != nil {
		return writeFailure(stderr, err, "STATE_PATH_INVALID")
	}
	result, err := store.RecoverDeadLock(ctx, fs.Args()[0])
	if err != nil {
		if result.LockRecovered {
			if _, writeErr := fmt.Fprintf(stdout, "Dead process lock recovered for run %s; persisted status is %s.\n", result.RunID, result.Status); writeErr != nil {
				return writeFailure(stderr, writeErr, "OUTPUT_WRITE_FAILED")
			}
		}
		return writeFailure(stderr, err, "RUN_RECOVERY_STATUS_FAILED")
	}
	if _, err := fmt.Fprintf(stdout, "Dead process lock recovered for run %s.\nPersisted status: %s\n", result.RunID, result.Status); err != nil {
		return writeFailure(stderr, err, "OUTPUT_WRITE_FAILED")
	}
	return writeText(stdout, stderr, "Owned Docker resources were not removed; inspect the history and use cleanup when appropriate.\n")
}

func printHistory(out, stderr io.Writer, snapshot state.HistorySnapshot, limit int) int {
	var b strings.Builder
	fmt.Fprintf(&b, "Rehearse history (%d run records shown)\n", len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if entry.Status == "invalid" {
			fmt.Fprintf(&b, "%s  invalid  %s\n", entry.RunID, entry.Code)
			continue
		}
		fmt.Fprintf(&b, "%s  %s  %s  resources=%d  backup=%t  pending-backup=%t",
			entry.RunID, entry.CreatedAt.UTC().Format(time.RFC3339), entry.Status,
			entry.ResourceCount, entry.HasBackup, entry.HasPendingBackup)
		if entry.ReportFinalizationPending {
			b.WriteString("  report-finalization=pending")
		}
		b.WriteByte('\n')
	}
	if snapshot.InvalidRecordsCount > 0 {
		fmt.Fprintf(&b, "Invalid records observed: %d\n", snapshot.InvalidRecordsCount)
	}
	if snapshot.Truncated && !snapshot.ScanLimitReached {
		fmt.Fprintf(&b, "History truncated at the requested limit of %d rows.\n", limit)
	}
	if snapshot.ScanLimitReached {
		b.WriteString("The state-directory scan limit was reached; additional records may remain unseen.\n")
	}
	return writeText(out, stderr, b.String())
}

func readConfig(path string, opts cliOptions) (spec.Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return spec.Config{}, fixedError("CONFIG_READ_FAILED")
	}
	f, err := opts.readInput(abs)
	if err != nil {
		return spec.Config{}, fixedError("CONFIG_READ_FAILED")
	}
	cfg, parseErr := spec.Parse(f)
	closeErr := f.Close()
	if parseErr != nil {
		return spec.Config{}, parseErr
	}
	if closeErr != nil {
		return spec.Config{}, fixedError("CONFIG_READ_FAILED")
	}
	if !filepath.IsAbs(cfg.BackupPath) {
		cfg.BackupPath = filepath.Join(filepath.Dir(abs), cfg.BackupPath)
	} else {
		cfg.BackupPath = filepath.Clean(cfg.BackupPath)
	}
	return cfg, nil
}

func openState(opts cliOptions) (*state.Store, error) {
	root := opts.stateRoot
	if root == "" {
		base, err := opts.userConfig()
		if err != nil || strings.TrimSpace(base) == "" {
			return nil, fixedError("STATE_PATH_INVALID")
		}
		root = filepath.Join(base, "rehearse")
	}
	return state.Open(root)
}

func newEngine(goos, distro string) (*engine.Engine, error) {
	switch goos {
	case "windows":
		if distro == "" {
			return nil, fixedError("WSL_DISTRIBUTION_REQUIRED")
		}
		return engine.NewWSL(distro)
	case "linux":
		if distro != "" {
			return nil, fixedError("WSL_PLATFORM_UNSUPPORTED")
		}
		return engine.New()
	default:
		return nil, fixedError("DOCKER_PLATFORM_UNSUPPORTED")
	}
}

func printPlan(out, stderr io.Writer, p app.Plan) int {
	var b strings.Builder
	fmt.Fprintln(&b, "Rehearse plan")
	fmt.Fprintf(&b, "Adapter: %s\n", title(p.Adapter))
	fmt.Fprintf(&b, "Versions: %s %s → %s; PostgreSQL %s\n", title(p.Adapter), p.SourceVersion, p.TargetVersion, p.PostgresVersion)
	if p.Adapter == "forgejo" {
		fmt.Fprintf(&b, "Backup: %d bytes\nValidation: %s\n", p.BackupBytes, p.ValidationNote)
	} else {
		fmt.Fprintf(&b, "Backup: %d bytes; custom-format header recognized; archive validity is not established\n", p.BackupBytes)
	}
	fmt.Fprintf(&b, "Phases: %s\n", strings.Join(p.Phases, ", "))
	fmt.Fprintf(&b, "Planned resources: %d\n", p.ResourceCount)
	fmt.Fprintf(&b, "Memory floor: %d MiB\n", p.MemoryFloorMiB)
	fmt.Fprintf(&b, "Runtime: %s\n", p.RuntimeNote)
	fmt.Fprintf(&b, "Disk: %s\n", p.DiskPlanningNote)
	fmt.Fprintln(&b, "Pinned images:")
	for _, image := range p.Images {
		fmt.Fprintf(&b, "  - %s\n", image)
	}
	return writeText(out, stderr, b.String())
}

func printRunSummary(out io.Writer, store *state.Store, r *report.Report) error {
	dir, err := store.RunDir(r.RunID)
	if err != nil {
		return fixedError("REPORT_PATH_UNAVAILABLE")
	}
	htmlPath := filepath.Join(dir, "report.html")
	if info, err := os.Lstat(htmlPath); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fixedError("REPORT_PATH_UNAVAILABLE")
	}
	_, err = fmt.Fprintf(out, "Run: %s\nOutcome: %s\nReport: %s\n", r.RunID, r.Result, htmlPath)
	if err != nil {
		return fixedError("OUTPUT_WRITE_FAILED")
	}
	return nil
}

func title(value string) string {
	if value == "" {
		return "Unknown"
	}
	return strings.ToUpper(value[:1]) + value[1:]
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

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func addHelpFlags(fs *flag.FlagSet) (*bool, *bool) {
	long := fs.Bool("help", false, "show command help")
	short := fs.Bool("h", false, "show command help")
	return long, short
}

func commandArgumentFailure(stderr io.Writer, command string) int {
	usage, _ := commandUsage(command)
	_, _ = io.WriteString(stderr, usage)
	return writeFailure(stderr, nil, "ARGUMENTS_INVALID")
}

func writeFailure(stderr io.Writer, err error, fallback string) int {
	code := safeCode(err, fallback)
	_, _ = fmt.Fprintf(stderr, "rehearse: %s\n", code)
	return 1
}

func writeText(out, stderr io.Writer, value string) int {
	return writeBytes(out, stderr, []byte(value))
}

func writeBytes(out, stderr io.Writer, value []byte) int {
	if _, err := out.Write(value); err != nil {
		return writeFailure(stderr, err, "OUTPUT_WRITE_FAILED")
	}
	return 0
}

func safeCode(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	message := err.Error()
	if safeCodeNames[message] {
		return message
	}
	return fallback
}

type fixedError string

func (e fixedError) Error() string { return string(e) }

var safeCodeNames = func() map[string]bool {
	codes := strings.Fields(`
ARGUMENTS_INVALID AUTH_INVALID AUTH_ENV_REFS_INVALID AUTH_FAILED AUTH_BYPASS ADAPTER_UNSUPPORTED
ARCHIVE_OUTPUT_EXISTS ARCHIVE_OUTPUT_INVALID ARCHIVE_WRITE_FAILED
API_OBSERVATION_INVALID API_VERSION_MISMATCH
	BACKUP_FAILED DATA_CHANGED NETWORK_FAILED RESTORE_FAILED SCHEMA_MISMATCH RECOVERY_FAILED
BACKUP_ALREADY_STAGED BACKUP_CHANGED_DURING_STAGING BACKUP_FORMAT_UNSUPPORTED
BACKUP_INTEGRITY_FAILED BACKUP_NOT_REGULAR BACKUP_NOT_STAGED BACKUP_ORPHAN_UNSAFE
BACKUP_PATH_REQUIRED BACKUP_RECOVERY_NOT_NEEDED BACKUP_RECOVERY_REQUIRED
BACKUP_RECOVERY_SOURCE_CHANGED BACKUP_RECOVERY_SOURCE_MISMATCH BACKUP_STAGE_FAILED
BACKUP_TOO_LARGE BACKUP_UNREADABLE CANCELED
CLEANUP_OWNERSHIP_HELD COMMAND_UNKNOWN
COMPOSE_ENCODE_FAILED COMPOSE_WRITE_FAILED
CONFIG_INVALID CONFIG_READ_FAILED CONFIG_TOO_LARGE
CONTAINER_BOUNDARY_FAILED CONTAINER_INSPECT_INVALID
DAEMON_ID_INVALID DAEMON_ID_MISMATCH DATABASE_NOT_READY
DOCKER_COMMAND_FAILED DOCKER_INFO_INVALID DOCKER_NOT_FOUND DOCKER_OUTPUT_LIMIT_INVALID
DOCKER_OUTPUT_TOO_LARGE DOCKER_PLATFORM_UNSUPPORTED DOCKER_TIMEOUT DOCKER_VERSION_UNSUPPORTED
DUPLICATE_FIELD INVALID_FIELD_TYPE INVALID_JSON JSON_TOO_DEEP
MIGRATION_FAILED NETWORK_BOUNDARY_FAILED NETWORK_FOREIGN_ATTACHMENT NETWORK_INSPECT_INVALID
NULL_NOT_ALLOWED OPERATION_FAILED OUTPUT_WRITE_FAILED PHASE_INVALID POSTGRES_VERSION_UNSUPPORTED
RANDOM_FAILED
REPORT_CHECK_UNKNOWN REPORT_CHECKS_INVALID REPORT_CODE_INVALID REPORT_ENCODE_FAILED
REPORT_FORMAT_INVALID REPORT_FORMAT_UNSUPPORTED REPORT_HASH_INVALID REPORT_METADATA_INVALID
REPORT_OUTCOME_INVALID REPORT_PATH_UNAVAILABLE REPORT_PLATFORM_INVALID REPORT_READ_FAILED
REPORT_SNAPSHOT_INVALID REPORT_STATUS_INVALID REPORT_TIME_INVALID REPORT_TOO_LARGE
REPORT_VERSION_UNSUPPORTED REPORT_WRITE_FAILED
RESOURCE_ALREADY_EXISTS RESOURCE_INSPECT_INVALID RESOURCE_OWNERSHIP_MISMATCH
RESOURCE_TYPE_INVALID RESOURCE_UNKNOWN
HISTORY_INVALID_RECORDS HISTORY_LIMIT_INVALID HISTORY_READ_FAILED HISTORY_SCAN_LIMIT_REACHED
RUN_BACKUP_INVALID RUN_CREATE_FAILED RUN_ID_INVALID RUN_INTENT_INVALID
RUN_DIRECTORY_INVALID RUN_METADATA_INVALID
RUN_LOCK_CREATE_FAILED RUN_LOCK_HELD RUN_LOCK_INFO_INVALID RUN_LOCK_NOT_FOUND
RUN_LOCK_GUARD_CREATE_FAILED RUN_LOCK_GUARD_HELD RUN_LOCK_GUARD_INVALID
RUN_LOCK_GUARD_LOCK_FAILED RUN_LOCK_GUARD_RELEASE_FAILED
RUN_LOCK_OWNERSHIP_CHANGED RUN_LOCK_RELEASE_FAILED RUN_LOCK_REQUIRED
	RUN_RECOVERY_CLAIM_FAILED RUN_RECOVERY_CLAIM_PENDING RUN_RECOVERY_FOREIGN_HOST RUN_RECOVERY_PROCESS_LIVE RUN_RECOVERY_PROCESS_UNKNOWN
	RUN_RECOVERY_STATUS_FAILED RUN_RECOVERY_STATUS_HELD
	RUN_RESOURCES_INVALID RUN_STATUS_INVALID SOURCE_CHANGED CLEANUP_HELD
SCHEMA_VERSION_UNSUPPORTED STATE_CREATE_FAILED STATE_DIRECTORY_INVALID STATE_ENCODE_FAILED
STATE_FORMAT_INVALID STATE_IDENTITY_FAILED STATE_IDENTITY_INVALID STATE_PATH_INVALID
STATE_READ_FAILED STATE_SYNC_FAILED STATE_WRITE_FAILED
TRAILING_DATA UNKNOWN_FIELD VERSION_INVALID VERSION_PAIR_UNSUPPORTED
VOLUME_BOUNDARY_FAILED VOLUME_INSPECT_INVALID
WSL_DISTRIBUTION_INVALID WSL_DISTRIBUTION_REQUIRED WSL_NOT_FOUND
WSL_PATH_TRANSLATION_FAILED WSL_PLATFORM_UNSUPPORTED
`)
	out := make(map[string]bool, len(codes))
	for _, code := range codes {
		out[code] = true
	}
	return out
}()
