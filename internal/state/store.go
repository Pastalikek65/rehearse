// Package state persists private run intents before any Docker resources exist.
package state

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
)

const MaxBackupBytes int64 = 128 << 30
const MaxForgejoBackupBytes int64 = 2 << 30

func backupLimitForAdapter(adapter string) int64 {
	if adapter == "forgejo" {
		return MaxForgejoBackupBytes
	}
	return MaxBackupBytes
}

type code string

func (e code) Error() string { return string(e) }

type Resource struct {
	Kind   string            `json:"kind"`
	Role   string            `json:"role"`
	Phase  string            `json:"phase"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}
type Backup struct {
	Path       string `json:"-"`
	SourcePath string `json:"sourcePath"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
}
type Run struct {
	SchemaVersion          int        `json:"schemaVersion"`
	Adapter                string     `json:"adapter,omitempty"`
	AdapterContractVersion int        `json:"adapterContractVersion,omitempty"`
	ID                     string     `json:"id"`
	OwnerID                string     `json:"ownerId"`
	DaemonID               string     `json:"daemonId"`
	CreatedAt              time.Time  `json:"createdAt"`
	Status                 string     `json:"status"`
	Resources              []Resource `json:"resources"`
	Backup                 *Backup    `json:"backup,omitempty"`
	PendingBackup          *Backup    `json:"pendingBackup,omitempty"`
}
type Store struct {
	root  string
	owner string
}

// LockInfo is safe for the CLI to display when a prior process left a run
// locked. It is diagnostic metadata, not authority to remove the lock.
type LockInfo struct {
	SchemaVersion int       `json:"schemaVersion"`
	RunID         string    `json:"runId"`
	OwnerID       string    `json:"ownerId"`
	Hostname      string    `json:"hostname"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"startedAt"`
}

type lockRecord struct {
	LockInfo
	Token string `json:"token"`
}

// RunLock is an exclusive per-run lease. A lock is never considered stale
// automatically. Release must be called only by the holder that acquired it.
type RunLock struct {
	store           *Store
	info            LockInfo
	token           string
	mu              sync.RWMutex
	opMu            sync.Mutex
	active          bool
	metadataRemoved bool
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", code("RANDOM_FAILED")
	}
	return hex.EncodeToString(b[:]), nil
}
func validID(id string) bool {
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

// Open selects an account-owned state directory. It never follows a symlink
// for a managed directory or identity file. Windows privacy also depends on
// the directory's inherited account ACLs; Unix permissions are 0700/0600.
func Open(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, code("STATE_PATH_INVALID")
	}
	if err := mkdirAllDurable(abs); err != nil {
		return nil, err
	}
	if err := regularDir(abs); err != nil {
		return nil, err
	}
	ownerPath := filepath.Join(abs, "owner-id")
	owner, err := os.ReadFile(ownerPath)
	if errors.Is(err, os.ErrNotExist) {
		id, e := randomID()
		if e != nil {
			return nil, e
		}
		f, e := os.OpenFile(ownerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(e, os.ErrExist) {
			owner, err = os.ReadFile(ownerPath)
		} else if e != nil {
			return nil, code("STATE_IDENTITY_FAILED")
		} else {
			_, e = f.WriteString(id)
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e != nil || closeErr != nil {
				return nil, code("STATE_IDENTITY_FAILED")
			}
			owner = []byte(id)
			err = nil
			if e := syncDir(abs); e != nil {
				return nil, e
			}
		}
	}
	info, e := os.Lstat(ownerPath)
	if err != nil || e != nil || !info.Mode().IsRegular() || !validID(string(owner)) {
		return nil, code("STATE_IDENTITY_INVALID")
	}
	return &Store{root: abs, owner: string(owner)}, nil
}
func regularDir(path string) error {
	i, e := os.Lstat(path)
	if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return code("STATE_DIRECTORY_INVALID")
	}
	return nil
}
func (s *Store) RunDir(id string) (string, error) {
	if !validID(id) {
		return "", code("RUN_ID_INVALID")
	}
	p := filepath.Join(s.root, id)
	if err := regularDir(p); err != nil {
		return "", err
	}
	return p, nil
}

const lockDirectoryName = "run.lock"
const lockRecordName = "metadata.json"

// AcquireRunLock atomically claims exclusive ownership of a run. An existing
// lock, including one with incomplete metadata after a crash, is never
// adopted automatically.
func (s *Store) AcquireRunLock(id string) (lock *RunLock, returnErr error) {
	runDir, err := s.RunDir(id)
	if err != nil {
		return nil, err
	}
	guard, err := acquireLockMutationGuard(runDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := guard.Release(); err != nil && returnErr == nil {
			lock = nil
			returnErr = err
		}
	}()
	return s.acquireRunLockUnderGuard(id)
}

// The caller holds the kernel mutation guard until all canonical lock
// directory and metadata changes are visible to other processes.
func (s *Store) acquireRunLockUnderGuard(id string) (*RunLock, error) {
	if _, err := s.Load(id); err != nil {
		return nil, err
	}
	runDir, err := s.RunDir(id)
	if err != nil {
		return nil, err
	}
	// Recovery's durable claim must be resolved explicitly before another
	// canonical holder can be created. The caller holds the mutation guard.
	if _, err := os.Lstat(filepath.Join(runDir, recoveryClaimDirectoryName)); err == nil {
		return nil, code("RUN_RECOVERY_CLAIM_PENDING")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, code("RUN_RECOVERY_CLAIM_PENDING")
	}
	lockDir := filepath.Join(runDir, lockDirectoryName)
	if err := os.Mkdir(lockDir, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, code("RUN_LOCK_HELD")
		}
		return nil, code("RUN_LOCK_CREATE_FAILED")
	}
	cleanup := func() {
		_ = os.Remove(filepath.Join(lockDir, lockRecordName))
		_ = os.Remove(lockDir)
		_ = syncDir(runDir)
	}
	if err := syncDir(runDir); err != nil {
		cleanup()
		return nil, err
	}
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" || len(hostname) > 255 {
		cleanup()
		return nil, code("RUN_LOCK_CREATE_FAILED")
	}
	token, err := randomID()
	if err != nil {
		cleanup()
		return nil, err
	}
	info := LockInfo{SchemaVersion: 1, RunID: id, OwnerID: s.owner, Hostname: hostname, PID: os.Getpid(), StartedAt: time.Now().UTC()}
	record := lockRecord{LockInfo: info, Token: token}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		cleanup()
		return nil, code("RUN_LOCK_CREATE_FAILED")
	}
	if err := atomicWrite(lockDir, lockRecordName, append(raw, '\n')); err != nil {
		cleanup()
		return nil, err
	}
	return &RunLock{store: s, info: info, token: token, active: true}, nil
}

func validateLockRecord(record lockRecord, owner, id string) error {
	if record.SchemaVersion != 1 || record.OwnerID != owner || record.RunID != id ||
		strings.TrimSpace(record.Hostname) == "" || len(record.Hostname) > 255 ||
		record.PID <= 0 || record.StartedAt.IsZero() || !validID(record.Token) {
		return code("RUN_LOCK_INFO_INVALID")
	}
	return nil
}

func (s *Store) readLockRecord(id string) (lockRecord, error) {
	runDir, err := s.RunDir(id)
	if err != nil {
		return lockRecord{}, err
	}
	lockDir := filepath.Join(runDir, lockDirectoryName)
	lockDirInfo, err := os.Lstat(lockDir)
	if err != nil || !lockDirInfo.IsDir() || lockDirInfo.Mode()&os.ModeSymlink != 0 {
		if errors.Is(err, os.ErrNotExist) {
			return lockRecord{}, code("RUN_LOCK_NOT_FOUND")
		}
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	path := filepath.Join(lockDir, lockRecordName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4096 {
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	f, err := os.Open(path)
	if err != nil {
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 4097))
	dec.DisallowUnknownFields()
	var record lockRecord
	if err := dec.Decode(&record); err != nil {
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return lockRecord{}, code("RUN_LOCK_INFO_INVALID")
	}
	if err := validateLockRecord(record, s.owner, id); err != nil {
		return lockRecord{}, err
	}
	return record, nil
}

// InspectRunLock returns process metadata for operator diagnosis. It never
// clears or adopts the lock.
func (s *Store) InspectRunLock(id string) (LockInfo, error) {
	record, err := s.readLockRecord(id)
	if err != nil {
		return LockInfo{}, err
	}
	return record.LockInfo, nil
}

// Info returns this holder's process metadata.
func (l *RunLock) Info() LockInfo { return l.info }

func (l *RunLock) hold(store *Store, id string) (func(), error) {
	if l == nil || store == nil {
		return nil, code("RUN_LOCK_REQUIRED")
	}
	l.mu.RLock()
	if !l.active || l.store != store || l.info.RunID != id || l.info.OwnerID != store.owner {
		l.mu.RUnlock()
		return nil, code("RUN_LOCK_REQUIRED")
	}
	record, err := store.readLockRecord(id)
	if err != nil || record.Token != l.token || !reflect.DeepEqual(record.LockInfo, l.info) {
		l.mu.RUnlock()
		return nil, code("RUN_LOCK_OWNERSHIP_CHANGED")
	}
	return l.mu.RUnlock, nil
}

// Release removes only the lock record whose run, installation and unguessable
// in-memory token still match this holder. It is idempotent after success.
func (l *RunLock) Release() (returnErr error) {
	if l == nil || l.store == nil {
		return code("RUN_LOCK_REQUIRED")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		return nil
	}
	runDir, err := l.store.RunDir(l.info.RunID)
	if err != nil {
		return err
	}
	guard, err := acquireLockMutationGuard(runDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := guard.Release(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	lockDir := filepath.Join(runDir, lockDirectoryName)
	lockInfo, err := os.Lstat(lockDir)
	if err != nil || !lockInfo.IsDir() || lockInfo.Mode()&os.ModeSymlink != 0 {
		return code("RUN_LOCK_OWNERSHIP_CHANGED")
	}
	metadata := filepath.Join(lockDir, lockRecordName)
	if !l.metadataRemoved {
		record, err := l.store.readLockRecord(l.info.RunID)
		if err != nil || record.Token != l.token || !reflect.DeepEqual(record.LockInfo, l.info) {
			return code("RUN_LOCK_OWNERSHIP_CHANGED")
		}
		if err := os.Remove(metadata); err != nil {
			return code("RUN_LOCK_RELEASE_FAILED")
		}
		l.metadataRemoved = true
	} else if _, err := os.Lstat(metadata); !errors.Is(err, os.ErrNotExist) {
		return code("RUN_LOCK_OWNERSHIP_CHANGED")
	}
	entries, err := os.ReadDir(lockDir)
	if err != nil || len(entries) != 0 {
		return code("RUN_LOCK_RELEASE_FAILED")
	}
	if err := os.Remove(lockDir); err != nil {
		return code("RUN_LOCK_RELEASE_FAILED")
	}
	l.active = false
	if err := syncDir(runDir); err != nil {
		return err
	}
	return nil
}

// IntendedResources is fixed by the runner, not by user JSON. Each phase has
// one fresh volume and network plus database, migration, app and probe services.
func IntendedResources(owner, id string) []Resource {
	var out []Resource
	for _, phase := range []string{"baseline", "target", "recovery"} {
		for _, role := range []string{"network", "volume", "db", "migration", "app", "probe"} {
			kind := "container"
			if role == "network" || role == "volume" {
				kind = role
			}
			out = append(out, Resource{Kind: kind, Role: role, Phase: phase, Name: "rehearse-" + id + "-" + phase + "-" + role, Labels: map[string]string{"io.rehearse.owner": owner, "io.rehearse.run": id, "io.rehearse.kind": role}})
		}
	}
	return out
}
func (s *Store) Create(daemonID string) (Run, error) {
	if strings.TrimSpace(daemonID) == "" || len(daemonID) > 128 {
		return Run{}, code("DAEMON_ID_INVALID")
	}
	id, err := randomID()
	if err != nil {
		return Run{}, err
	}
	if err := os.Mkdir(filepath.Join(s.root, id), 0700); err != nil {
		return Run{}, code("RUN_CREATE_FAILED")
	}
	if err := syncDir(s.root); err != nil {
		return Run{}, err
	}
	run := Run{SchemaVersion: 1, ID: id, OwnerID: s.owner, DaemonID: daemonID, CreatedAt: time.Now().UTC(), Status: "planned", Resources: IntendedResources(s.owner, id)}
	if err := s.save(run); err != nil {
		return Run{}, err
	}
	return run, nil
}
func (s *Store) validate(run Run) error {
	if !validID(run.ID) {
		return code("RUN_ID_INVALID")
	}
	if run.OwnerID != s.owner || run.CreatedAt.IsZero() || strings.TrimSpace(run.DaemonID) == "" || len(run.DaemonID) > 128 {
		return code("RUN_INTENT_INVALID")
	}
	switch run.Status {
	case "planned", "staging", "staged", "running", "completed", "failed", "interrupted", "cleaned", "cleanup-held":
	default:
		return code("RUN_STATUS_INVALID")
	}
	if err := ValidateRunResources(run); err != nil {
		return err
	}
	if run.Backup != nil && run.PendingBackup != nil {
		return code("RUN_BACKUP_INVALID")
	}
	if run.Backup != nil {
		if err := validateBackup(*run.Backup); err != nil {
			return err
		}
		if run.Backup.Bytes > backupLimitForAdapter(run.AdapterID()) {
			return code("RUN_BACKUP_INVALID")
		}
	}
	if run.PendingBackup != nil {
		if run.Status != "staging" {
			return code("RUN_BACKUP_INVALID")
		}
		if err := validateBackup(*run.PendingBackup); err != nil {
			return err
		}
		if run.PendingBackup.Bytes > backupLimitForAdapter(run.AdapterID()) {
			return code("RUN_BACKUP_INVALID")
		}
	}
	return nil
}

func validateBackup(backup Backup) error {
	if len(backup.SHA256) != 64 || backup.Bytes < 5 || backup.Bytes > MaxBackupBytes || backup.SourcePath == "" {
		return code("RUN_BACKUP_INVALID")
	}
	for _, c := range backup.SHA256 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return code("RUN_BACKUP_INVALID")
		}
	}
	return nil
}

// Save updates a run only while the same Store instance holds its exclusive
// run lock. Create writes the initial planned intent before a lock exists.
func (s *Store) Save(lock *RunLock, run Run) error {
	release, err := lock.hold(s, run.ID)
	if err != nil {
		return err
	}
	defer release()
	lock.opMu.Lock()
	defer lock.opMu.Unlock()
	return s.save(run)
}

func (s *Store) save(run Run) error {
	if err := s.validate(run); err != nil {
		return err
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return code("STATE_ENCODE_FAILED")
	}
	return atomicWrite(dir, "run.json", append(raw, '\n'))
}

func syncDir(dir string) error {
	// Windows does not expose a portable directory fsync through the Go stdlib.
	// We guarantee process-interruption recovery there, not power-loss durability.
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return code("STATE_SYNC_FAILED")
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil || closeErr != nil {
		return code("STATE_SYNC_FAILED")
	}
	return nil
}

func mkdirAllDurable(path string) error {
	var missing []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		_, err := os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return code("STATE_CREATE_FAILED")
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return code("STATE_CREATE_FAILED")
	}
	// Flush every parent directory entry created by MkdirAll, from the
	// shallowest missing ancestor to the new leaf.
	for i := len(missing) - 1; i >= 0; i-- {
		if err := syncDir(filepath.Dir(missing[i])); err != nil {
			return err
		}
	}
	return nil
}

func atomicWrite(dir, name string, raw []byte) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".write-"+id)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return code("STATE_WRITE_FAILED")
	}
	defer os.Remove(tmp)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil {
		return code("STATE_WRITE_FAILED")
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return code("STATE_WRITE_FAILED")
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	return nil
}
func (s *Store) Load(id string) (Run, error) {
	dir, err := s.RunDir(id)
	if err != nil {
		return Run{}, err
	}
	p := filepath.Join(dir, "run.json")
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return Run{}, code("STATE_READ_FAILED")
	}
	f, err := os.Open(p)
	if err != nil {
		return Run{}, code("STATE_READ_FAILED")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	dec.DisallowUnknownFields()
	var run Run
	if err := dec.Decode(&run); err != nil {
		return Run{}, code("STATE_FORMAT_INVALID")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return Run{}, code("STATE_FORMAT_INVALID")
	}
	if run.ID != id {
		return Run{}, code("RUN_INTENT_INVALID")
	}
	if err := s.validate(run); err != nil {
		return Run{}, err
	}
	if run.Backup != nil {
		run.Backup.Path = filepath.Join(dir, "backup.dump")
	}
	if run.PendingBackup != nil {
		run.PendingBackup.Path = filepath.Join(dir, "backup.dump")
	}
	return run, nil
}

type cancellableReader struct {
	ctx context.Context
	r   io.Reader
}

func (r cancellableReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func (s *Store) StageBackup(ctx context.Context, lock *RunLock, source string) (Backup, error) {
	if lock == nil {
		return Backup{}, code("RUN_LOCK_REQUIRED")
	}
	release, err := lock.hold(s, lock.info.RunID)
	if err != nil {
		return Backup{}, err
	}
	defer release()
	lock.opMu.Lock()
	defer lock.opMu.Unlock()
	if ctx.Err() != nil {
		return Backup{}, code("CANCELED")
	}
	run, err := s.Load(lock.info.RunID)
	if err != nil {
		return Backup{}, err
	}
	if run.Backup != nil {
		return Backup{}, code("BACKUP_ALREADY_STAGED")
	}
	if run.PendingBackup != nil {
		return Backup{}, code("BACKUP_RECOVERY_REQUIRED")
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		return Backup{}, err
	}
	partialExists, err := regularOrMissing(filepath.Join(dir, "backup.partial"))
	if err != nil {
		return Backup{}, err
	}
	destExists, err := regularOrMissing(filepath.Join(dir, "backup.dump"))
	if err != nil {
		return Backup{}, err
	}
	if partialExists || destExists {
		return Backup{}, code("BACKUP_RECOVERY_REQUIRED")
	}
	sourceAbs, err := absoluteSource(source)
	if err != nil {
		return Backup{}, err
	}
	return s.stageBackupLocked(ctx, run, sourceAbs)
}

func absoluteSource(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", code("BACKUP_UNREADABLE")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", code("BACKUP_UNREADABLE")
	}
	return abs, nil
}

// stageBackupLocked writes bytes to backup.partial, persists their digest as
// pending intent, renames the verified file, then commits the final manifest.
// The caller holds the run lock for the whole sequence.
func (s *Store) stageBackupLocked(ctx context.Context, run Run, sourceAbs string) (Backup, error) {
	dir, err := s.RunDir(run.ID)
	if err != nil {
		return Backup{}, err
	}
	tmp := filepath.Join(dir, "backup.partial")
	dest := filepath.Join(dir, "backup.dump")
	backup, err := copyBackupToPartialForAdapter(ctx, sourceAbs, tmp, run.AdapterID())
	if err != nil {
		return Backup{}, err
	}
	keepPartial := false
	defer func() {
		if !keepPartial {
			_ = os.Remove(tmp)
		}
	}()
	if ctx.Err() != nil {
		return Backup{}, code("CANCELED")
	}
	run.PendingBackup = &backup
	run.Status = "staging"
	if err := s.save(run); err != nil {
		return Backup{}, err
	}
	keepPartial = true
	if ctx.Err() != nil {
		return Backup{}, code("CANCELED")
	}
	if err := os.Rename(tmp, dest); err != nil {
		return Backup{}, code("BACKUP_STAGE_FAILED")
	}
	if err := syncDir(dir); err != nil {
		return Backup{}, err
	}
	run.Backup = &backup
	run.Backup.Path = dest
	run.PendingBackup = nil
	run.Status = "staged"
	if err := s.save(run); err != nil {
		return Backup{}, err
	}
	return *run.Backup, nil
}

func copyBackupToPartial(ctx context.Context, sourceAbs, tmp string) (Backup, error) {
	return copyBackupToPartialForAdapter(ctx, sourceAbs, tmp, "miniflux")
}

func copyBackupToPartialForAdapter(ctx context.Context, sourceAbs, tmp, adapter string) (Backup, error) {
	return copyBackupToPartialWithLimit(ctx, sourceAbs, tmp, adapter, backupLimitForAdapter(adapter))
}

// The copy algorithm takes its bound explicitly so every read, including a
// source that grows after Stat, uses the same closed adapter budget.
func copyBackupToPartialWithLimit(ctx context.Context, sourceAbs, tmp, adapter string, maxBytes int64) (Backup, error) {
	if ctx == nil || ctx.Err() != nil {
		return Backup{}, code("CANCELED")
	}
	if adapter != "miniflux" && adapter != "forgejo" {
		return Backup{}, code("ADAPTER_UNSUPPORTED")
	}
	if maxBytes < 5 || maxBytes > backupLimitForAdapter(adapter) {
		return Backup{}, code("BACKUP_TOO_LARGE")
	}
	info, err := os.Lstat(sourceAbs)
	if err != nil || !info.Mode().IsRegular() {
		return Backup{}, code("BACKUP_NOT_REGULAR")
	}
	f, err := os.Open(sourceAbs)
	if err != nil {
		return Backup{}, code("BACKUP_UNREADABLE")
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(info, before) {
		return Backup{}, code("BACKUP_NOT_REGULAR")
	}
	if before.Size() < 5 {
		return Backup{}, code("BACKUP_FORMAT_UNSUPPORTED")
	}
	if before.Size() > maxBytes {
		return Backup{}, code("BACKUP_TOO_LARGE")
	}
	var prefix [5]byte
	if _, err := io.ReadFull(f, prefix[:]); err != nil {
		return Backup{}, code("BACKUP_FORMAT_UNSUPPORTED")
	}
	if adapter == "miniflux" && string(prefix[:]) != "PGDMP" || adapter == "forgejo" && string(prefix[:4]) != "PK\x03\x04" {
		return Backup{}, code("BACKUP_FORMAT_UNSUPPORTED")
	}
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Backup{}, code("BACKUP_STAGE_FAILED")
	}
	removePartial := true
	defer func() {
		if removePartial {
			_ = os.Remove(tmp)
		}
	}()
	hash := sha256.New()
	writer := io.MultiWriter(out, hash)
	_, err = writer.Write(prefix[:])
	var n int64
	if err == nil {
		n, err = io.CopyBuffer(writer, io.LimitReader(cancellableReader{ctx: ctx, r: f}, maxBytes-4), make([]byte, 64<<10))
	}
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if ctx.Err() != nil {
		return Backup{}, code("CANCELED")
	}
	if err != nil || closeErr != nil {
		return Backup{}, code("BACKUP_STAGE_FAILED")
	}
	if n+5 > maxBytes {
		return Backup{}, code("BACKUP_TOO_LARGE")
	}
	after, err := f.Stat()
	if err != nil || n+5 != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return Backup{}, code("BACKUP_CHANGED_DURING_STAGING")
	}
	removePartial = false
	return Backup{Path: "", SourcePath: sourceAbs, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: n + 5}, nil
}

func regularOrMissing(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, code("BACKUP_ORPHAN_UNSAFE")
	}
	return true, nil
}

func removeRegularOrphan(path string) error {
	exists, err := regularOrMissing(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return code("BACKUP_ORPHAN_UNSAFE")
	}
	return nil
}

func sameSourcePath(given, recorded string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(given), filepath.Clean(recorded))
	}
	return filepath.Clean(given) == filepath.Clean(recorded)
}

func fileFingerprint(ctx context.Context, path string, expected Backup) error {
	exists, err := regularOrMissing(path)
	if err != nil || !exists {
		return code("BACKUP_INTEGRITY_FAILED")
	}
	f, err := os.Open(path)
	if err != nil {
		return code("BACKUP_INTEGRITY_FAILED")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expected.Bytes {
		return code("BACKUP_INTEGRITY_FAILED")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(cancellableReader{ctx: ctx, r: f}, expected.Bytes+1))
	if err != nil || n != expected.Bytes || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
		return code("BACKUP_INTEGRITY_FAILED")
	}
	return nil
}

// RecoverBackup resolves only interrupted state for this run while its lock is
// held. A partial file without a pending manifest is discarded and restaged
// from the supplied source. A file with a pending manifest is accepted only
// when its exact length and digest match that persisted intent.
func (s *Store) RecoverBackup(ctx context.Context, lock *RunLock, source string) (Backup, error) {
	if lock == nil {
		return Backup{}, code("RUN_LOCK_REQUIRED")
	}
	release, err := lock.hold(s, lock.info.RunID)
	if err != nil {
		return Backup{}, err
	}
	defer release()
	lock.opMu.Lock()
	defer lock.opMu.Unlock()
	if ctx.Err() != nil {
		return Backup{}, code("CANCELED")
	}
	run, err := s.Load(lock.info.RunID)
	if err != nil {
		return Backup{}, err
	}
	if run.Backup != nil {
		return Backup{}, code("BACKUP_ALREADY_STAGED")
	}
	dir, err := s.RunDir(run.ID)
	if err != nil {
		return Backup{}, err
	}
	tmp := filepath.Join(dir, "backup.partial")
	dest := filepath.Join(dir, "backup.dump")
	partialExists, err := regularOrMissing(tmp)
	if err != nil {
		return Backup{}, err
	}
	destExists, err := regularOrMissing(dest)
	if err != nil {
		return Backup{}, err
	}
	if run.PendingBackup == nil {
		if destExists {
			return Backup{}, code("BACKUP_ORPHAN_UNSAFE")
		}
		if !partialExists {
			return Backup{}, code("BACKUP_RECOVERY_NOT_NEEDED")
		}
		if err := removeRegularOrphan(tmp); err != nil {
			return Backup{}, err
		}
		if err := syncDir(dir); err != nil {
			return Backup{}, err
		}
		sourceAbs, err := absoluteSource(source)
		if err != nil {
			return Backup{}, err
		}
		return s.stageBackupLocked(ctx, run, sourceAbs)
	}
	if partialExists && destExists {
		return Backup{}, code("BACKUP_ORPHAN_UNSAFE")
	}
	pending := *run.PendingBackup
	if destExists {
		if err := fileFingerprint(ctx, dest, pending); err != nil {
			if ctx.Err() != nil {
				return Backup{}, code("CANCELED")
			}
			return Backup{}, code("BACKUP_INTEGRITY_FAILED")
		}
	} else if partialExists {
		if err := fileFingerprint(ctx, tmp, pending); err != nil {
			if ctx.Err() != nil {
				return Backup{}, code("CANCELED")
			}
			return Backup{}, code("BACKUP_INTEGRITY_FAILED")
		}
		if ctx.Err() != nil {
			return Backup{}, code("CANCELED")
		}
		if err := os.Rename(tmp, dest); err != nil {
			return Backup{}, code("BACKUP_STAGE_FAILED")
		}
		if err := syncDir(dir); err != nil {
			return Backup{}, err
		}
	} else {
		sourceAbs, err := absoluteSource(source)
		if err != nil || !sameSourcePath(sourceAbs, pending.SourcePath) {
			return Backup{}, code("BACKUP_RECOVERY_SOURCE_MISMATCH")
		}
		copied, err := copyBackupToPartialForAdapter(ctx, sourceAbs, tmp, run.AdapterID())
		if err != nil {
			return Backup{}, err
		}
		if copied.Bytes != pending.Bytes || copied.SHA256 != pending.SHA256 {
			_ = os.Remove(tmp)
			return Backup{}, code("BACKUP_RECOVERY_SOURCE_CHANGED")
		}
		if ctx.Err() != nil {
			_ = os.Remove(tmp)
			return Backup{}, code("CANCELED")
		}
		if err := os.Rename(tmp, dest); err != nil {
			return Backup{}, code("BACKUP_STAGE_FAILED")
		}
		if err := syncDir(dir); err != nil {
			return Backup{}, err
		}
	}
	if ctx.Err() != nil {
		return Backup{}, code("CANCELED")
	}
	run.Backup = &pending
	run.Backup.Path = dest
	run.PendingBackup = nil
	run.Status = "staged"
	if err := s.save(run); err != nil {
		return Backup{}, err
	}
	return *run.Backup, nil
}

// OpenVerifiedBackup opens the persisted backup, hashes and sizes that open
// descriptor, then rewinds that same descriptor for the restore stream.
func (s *Store) OpenVerifiedBackup(id string) (*os.File, error) {
	return s.OpenVerifiedBackupContext(context.Background(), id)
}

// OpenVerifiedBackupContext checks cancellation between bounded reads while
// retaining and rewinding the same descriptor. An in-flight filesystem read
// still depends on the host filesystem returning.
func (s *Store) OpenVerifiedBackupContext(ctx context.Context, id string) (*os.File, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, code("CANCELED")
	}
	run, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	if run.Backup == nil {
		return nil, code("BACKUP_NOT_STAGED")
	}
	path := run.Backup.Path
	exists, err := regularOrMissing(path)
	if err != nil || !exists {
		return nil, code("BACKUP_INTEGRITY_FAILED")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, code("BACKUP_INTEGRITY_FAILED")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != run.Backup.Bytes {
		f.Close()
		return nil, code("BACKUP_INTEGRITY_FAILED")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(cancellableReader{ctx: ctx, r: f}, run.Backup.Bytes+1))
	if ctx.Err() != nil {
		f.Close()
		return nil, code("CANCELED")
	}
	if err != nil || n != run.Backup.Bytes || hex.EncodeToString(h.Sum(nil)) != run.Backup.SHA256 {
		f.Close()
		return nil, code("BACKUP_INTEGRITY_FAILED")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, code("BACKUP_INTEGRITY_FAILED")
	}
	return f, nil
}
