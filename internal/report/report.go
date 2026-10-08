// Package report renders safe evidence rather than application records.
package report

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io"
	"regexp"
	"time"
)

const MaxReportBytes = 1 << 20

type code string

func (e code) Error() string { return string(e) }

type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code"`
}
type Snapshot struct {
	SHA256 string `json:"sha256"`
	Rows   uint64 `json:"rows"`
	Bytes  uint64 `json:"bytes"`
}
type Report struct {
	SchemaVersion          int                 `json:"schemaVersion"`
	RunID                  string              `json:"runId"`
	Adapter                string              `json:"adapter"`
	SourceVersion          string              `json:"sourceVersion"`
	TargetVersion          string              `json:"targetVersion"`
	Platform               string              `json:"platform"`
	StartedAt              time.Time           `json:"startedAt"`
	FinishedAt             *time.Time          `json:"finishedAt,omitempty"`
	Result                 string              `json:"outcome"`
	Checks                 []Check             `json:"checks"`
	Snapshots              map[string]Snapshot `json:"snapshots,omitempty"`
	BackupSHA256           string              `json:"backupSha256,omitempty"`
	BackupBytes            uint64              `json:"backupBytes,omitempty"`
	AdapterContractVersion int                 `json:"adapterContractVersion,omitempty"`
	FileSnapshots          map[string]Snapshot `json:"fileSnapshots,omitempty"`
}

var required = []string{"backup.inspect", "baseline.network", "baseline.restore", "baseline.schema", "baseline.data", "baseline.auth", "baseline.unauthenticated", "target.network", "target.restore", "target.migration", "target.schema", "target.data", "target.removed-transformation", "target.auth", "target.unauthenticated", "recovery.network", "recovery.restore", "recovery.schema", "recovery.data", "recovery.auth", "recovery.unauthenticated", "source.unchanged", "cleanup.ownership"}
var forgejoRequired = []string{
	"backup.inspect",
	"baseline.network", "baseline.restore", "baseline.schema", "baseline.data", "baseline.files", "baseline.auth", "baseline.unauthenticated",
	"target.network", "target.restore", "target.migration", "target.schema", "target.data", "target.files", "target.auth", "target.unauthenticated",
	"recovery.network", "recovery.restore", "recovery.schema", "recovery.data", "recovery.files", "recovery.auth", "recovery.unauthenticated",
	"source.unchanged", "cleanup.ownership",
}
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)
var failedCodes = map[string]bool{"DATA_CHANGED": true, "MIGRATION_FAILED": true, "BACKUP_FAILED": true, "NETWORK_FAILED": true, "RESTORE_FAILED": true, "SCHEMA_MISMATCH": true, "AUTH_FAILED": true, "AUTH_BYPASS": true, "RECOVERY_FAILED": true, "SOURCE_CHANGED": true, "CLEANUP_HELD": true, "CANCELED": true, "OPERATION_FAILED": true}

func validCheck(status, errorCode string) bool {
	switch status {
	case "passed":
		return errorCode == "VALIDATED"
	case "not-run":
		return errorCode == "NOT_RUN"
	case "failed":
		return failedCodes[errorCode]
	}
	return false
}

func New(id, platform string, start time.Time) *Report {
	r := &Report{SchemaVersion: 1, RunID: id, Adapter: "miniflux", SourceVersion: "2.2.19", TargetVersion: "2.3.3", Platform: platform, StartedAt: start.UTC(), Result: "not-run", Snapshots: map[string]Snapshot{}}
	for _, id := range required {
		r.Checks = append(r.Checks, Check{ID: id, Status: "not-run", Code: "NOT_RUN"})
	}
	return r
}

// NewForgejo creates the closed Forgejo 15.0.9 to 16.0.5 schema-2 report.
// Its required checks and file fingerprints are intentionally distinct from
// the legacy Miniflux report contract.
func NewForgejo(id, platform string, start time.Time) *Report {
	r := &Report{
		SchemaVersion: 2, Adapter: "forgejo", AdapterContractVersion: 1,
		RunID: id, SourceVersion: "15.0.9", TargetVersion: "16.0.5", Platform: platform,
		StartedAt: start.UTC(), Result: "not-run", Snapshots: map[string]Snapshot{}, FileSnapshots: map[string]Snapshot{},
	}
	for _, id := range forgejoRequired {
		r.Checks = append(r.Checks, Check{ID: id, Status: "not-run", Code: "NOT_RUN"})
	}
	return r
}
func (r *Report) Outcome() string {
	if !r.outcomeShapeValid() {
		return "not-run"
	}
	for _, c := range r.Checks {
		if c.Status == "failed" {
			return "failed"
		}
	}
	for _, c := range r.Checks {
		if c.Status != "passed" {
			return "not-run"
		}
	}
	if r.FinishedAt == nil || r.FinishedAt.Before(r.StartedAt) || !hashPattern.MatchString(r.BackupSHA256) || r.BackupBytes < 5 {
		return "not-run"
	}
	baseline, ok := r.Snapshots["baseline"]
	if !ok || !hashPattern.MatchString(baseline.SHA256) || baseline.Rows == 0 || baseline.Bytes == 0 {
		return "not-run"
	}
	for _, phase := range []string{"target", "recovery"} {
		s, ok := r.Snapshots[phase]
		if !ok || s != baseline {
			return "not-run"
		}
	}
	if r.SchemaVersion == 2 {
		baselineFiles, ok := r.FileSnapshots["baseline"]
		if !ok || !snapshotNonempty(baselineFiles) {
			return "not-run"
		}
		for _, phase := range []string{"target", "recovery"} {
			if snapshot, ok := r.FileSnapshots[phase]; !ok || snapshot != baselineFiles {
				return "not-run"
			}
		}
	}
	return "passed"
}

func (r *Report) outcomeShapeValid() bool {
	if r == nil || !idPattern.MatchString(r.RunID) || r.StartedAt.IsZero() ||
		(r.Platform != "linux/amd64" && r.Platform != "windows/amd64") {
		return false
	}
	var checks []string
	switch r.SchemaVersion {
	case 1:
		if r.Adapter != "miniflux" || r.SourceVersion != "2.2.19" || r.TargetVersion != "2.3.3" ||
			r.AdapterContractVersion != 0 || r.FileSnapshots != nil {
			return false
		}
		checks = required
	case 2:
		if r.Adapter != "forgejo" || r.AdapterContractVersion != 1 || r.SourceVersion != "15.0.9" || r.TargetVersion != "16.0.5" {
			return false
		}
		checks = forgejoRequired
	default:
		return false
	}
	if r.FinishedAt != nil && r.FinishedAt.Before(r.StartedAt) {
		return false
	}
	if len(r.Checks) != len(checks) {
		return false
	}
	for i, c := range r.Checks {
		if c.ID != checks[i] || !validCheck(c.Status, c.Code) {
			return false
		}
	}
	if r.BackupSHA256 != "" && !hashPattern.MatchString(r.BackupSHA256) {
		return false
	}
	if !validSnapshots(r.Snapshots) || !validSnapshots(r.FileSnapshots) {
		return false
	}
	return true
}

func snapshotNonempty(s Snapshot) bool {
	return hashPattern.MatchString(s.SHA256) && s.Rows > 0 && s.Bytes > 0
}

func validSnapshots(snapshots map[string]Snapshot) bool {
	for phase, snapshot := range snapshots {
		if (phase != "baseline" && phase != "target" && phase != "recovery") || !hashPattern.MatchString(snapshot.SHA256) {
			return false
		}
	}
	return true
}
func (r *Report) Set(id, status, errorCode string) error {
	if status != "passed" && status != "failed" && status != "not-run" {
		return code("REPORT_STATUS_INVALID")
	}
	if !validCheck(status, errorCode) {
		return code("REPORT_CODE_INVALID")
	}
	for i := range r.Checks {
		if r.Checks[i].ID == id {
			r.Checks[i].Status = status
			r.Checks[i].Code = errorCode
			r.Result = r.Outcome()
			return nil
		}
	}
	return code("REPORT_CHECK_UNKNOWN")
}
func (r *Report) Validate() error {
	if r == nil {
		return code("REPORT_METADATA_INVALID")
	}
	if r.SchemaVersion != 1 && r.SchemaVersion != 2 {
		return code("REPORT_VERSION_UNSUPPORTED")
	}
	checks := required
	if r.SchemaVersion == 1 {
		if !idPattern.MatchString(r.RunID) || r.Adapter != "miniflux" || r.SourceVersion != "2.2.19" || r.TargetVersion != "2.3.3" || r.StartedAt.IsZero() ||
			r.AdapterContractVersion != 0 || r.FileSnapshots != nil {
			return code("REPORT_METADATA_INVALID")
		}
	} else {
		checks = forgejoRequired
		if !idPattern.MatchString(r.RunID) || r.Adapter != "forgejo" || r.AdapterContractVersion != 1 || r.SourceVersion != "15.0.9" || r.TargetVersion != "16.0.5" || r.StartedAt.IsZero() {
			return code("REPORT_METADATA_INVALID")
		}
	}
	if r.Platform != "linux/amd64" && r.Platform != "windows/amd64" {
		return code("REPORT_PLATFORM_INVALID")
	}
	if r.FinishedAt != nil && r.FinishedAt.Before(r.StartedAt) {
		return code("REPORT_TIME_INVALID")
	}
	if len(r.Checks) != len(checks) {
		return code("REPORT_CHECKS_INVALID")
	}
	for i, c := range r.Checks {
		if c.ID != checks[i] || !validCheck(c.Status, c.Code) {
			return code("REPORT_CHECKS_INVALID")
		}
	}
	if r.Result != r.Outcome() {
		return code("REPORT_OUTCOME_INVALID")
	}
	if r.BackupSHA256 != "" && !hashPattern.MatchString(r.BackupSHA256) {
		return code("REPORT_HASH_INVALID")
	}
	if !validSnapshots(r.Snapshots) || !validSnapshots(r.FileSnapshots) {
		return code("REPORT_SNAPSHOT_INVALID")
	}
	return nil
}
func (r *Report) JSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, code("REPORT_ENCODE_FAILED")
	}
	return append(raw, '\n'), nil
}
func Parse(input io.Reader) (*Report, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxReportBytes+1))
	if err != nil {
		return nil, code("REPORT_READ_FAILED")
	}
	if len(raw) > MaxReportBytes {
		return nil, code("REPORT_TOO_LARGE")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r Report
	if err := dec.Decode(&r); err != nil {
		return nil, code("REPORT_FORMAT_INVALID")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, code("REPORT_FORMAT_INVALID")
	}
	if r.SchemaVersion == 1 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, code("REPORT_FORMAT_INVALID")
		}
		if _, present := fields["adapterContractVersion"]; present {
			return nil, code("REPORT_METADATA_INVALID")
		}
		if _, present := fields["fileSnapshots"]; present {
			return nil, code("REPORT_METADATA_INVALID")
		}
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

var htmlPage = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Rehearse result</title>
<style>body{font:16px system-ui,sans-serif;max-width:1050px;margin:2rem auto;padding:0 1rem;color:#162536;background:#f7f9fc}h1{margin-bottom:.4rem}table{width:100%;border-collapse:collapse;background:white;margin:1rem 0}th,td{text-align:left;border-bottom:1px solid #dce2eb;padding:.7rem}code{font-size:.9em;overflow-wrap:anywhere}.passed{color:#11623d}.failed{color:#a72129}.not-run{color:#605917}caption{text-align:left;font-weight:600;padding:.6rem 0}dl{display:grid;grid-template-columns:10rem 1fr;gap:.4rem}dd{margin:0}.note{padding:1rem;background:#e8eef6}a{color:#245da8}</style></head>
<body><h1>Rehearse: {{.Result}}</h1><p>Miniflux {{.SourceVersion}} → {{.TargetVersion}}</p><dl><dt>Run</dt><dd><code>{{.RunID}}</code></dd><dt>Runner</dt><dd>{{.Platform}}</dd><dt>Started</dt><dd>{{.StartedAt}}</dd>{{if .FinishedAt}}<dt>Finished</dt><dd>{{.FinishedAt}}</dd>{{end}}</dl>
<table><caption>Checks</caption><thead><tr><th scope="col">Check</th><th scope="col">Result</th><th scope="col">Evidence code</th></tr></thead><tbody>{{range .Checks}}<tr><th scope="row"><code>{{.ID}}</code></th><td class="{{.Status}}">{{.Status}}</td><td><code>{{.Code}}</code></td></tr>{{end}}</tbody></table>
{{if .Snapshots}}<table><caption>Core data fingerprints</caption><thead><tr><th scope="col">Phase</th><th scope="col">Rows</th><th scope="col">Bytes</th><th scope="col">SHA-256</th></tr></thead><tbody>{{range $phase,$s:=.Snapshots}}<tr><th scope="row">{{$phase}}</th><td>{{$s.Rows}}</td><td>{{$s.Bytes}}</td><td><code>{{$s.SHA256}}</code></td></tr>{{end}}</tbody></table>{{end}}
{{if .BackupSHA256}}<p>Staged backup: {{.BackupBytes}} bytes; SHA-256 <code>{{.BackupSHA256}}</code>.</p>{{end}}
<p class="note">This report describes this rehearsal only. Not-run checks provide no evidence of success. Core fingerprints cover the adapter's documented projection; review migration-specific changes separately. The host runner and Docker engine are trusted. No production service was tested.</p></body></html>`))

var forgejoHTMLPage = template.Must(template.New("forgejo-report").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Rehearse Forgejo result</title>
<style>body{font:16px system-ui,sans-serif;max-width:1050px;margin:2rem auto;padding:0 1rem;color:#162536;background:#f7f9fc}h1{margin-bottom:.4rem}table{width:100%;border-collapse:collapse;background:white;margin:1rem 0}th,td{text-align:left;border-bottom:1px solid #dce2eb;padding:.7rem}code{font-size:.9em;overflow-wrap:anywhere}.passed{color:#11623d}.failed{color:#a72129}.not-run{color:#605917}caption{text-align:left;font-weight:600;padding:.6rem 0}dl{display:grid;grid-template-columns:10rem 1fr;gap:.4rem}dd{margin:0}.note{padding:1rem;background:#e8eef6}</style></head>
<body><h1>Rehearse: {{.Result}}</h1><p>Forgejo {{.SourceVersion}} → {{.TargetVersion}}</p><dl><dt>Run</dt><dd><code>{{.RunID}}</code></dd><dt>Runner</dt><dd>{{.Platform}}</dd><dt>Started</dt><dd>{{.StartedAt}}</dd>{{if .FinishedAt}}<dt>Finished</dt><dd>{{.FinishedAt}}</dd>{{end}}</dl>
<table><caption>Checks</caption><thead><tr><th scope="col">Check</th><th scope="col">Result</th><th scope="col">Evidence code</th></tr></thead><tbody>{{range .Checks}}<tr><th scope="row"><code>{{.ID}}</code></th><td class="{{.Status}}">{{.Status}}</td><td><code>{{.Code}}</code></td></tr>{{end}}</tbody></table>
{{if .Snapshots}}<table><caption>Database identity projection</caption><thead><tr><th scope="col">Phase</th><th scope="col">Rows</th><th scope="col">Bytes</th><th scope="col">SHA-256</th></tr></thead><tbody>{{range $phase,$s:=.Snapshots}}<tr><th scope="row">{{$phase}}</th><td>{{$s.Rows}}</td><td>{{$s.Bytes}}</td><td><code>{{$s.SHA256}}</code></td></tr>{{end}}</tbody></table>{{end}}
{{if .FileSnapshots}}<table><caption>Repository file fingerprints</caption><thead><tr><th scope="col">Phase</th><th scope="col">Files</th><th scope="col">Bytes</th><th scope="col">SHA-256</th></tr></thead><tbody>{{range $phase,$s:=.FileSnapshots}}<tr><th scope="row">{{$phase}}</th><td>{{$s.Rows}}</td><td>{{$s.Bytes}}</td><td><code>{{$s.SHA256}}</code></td></tr>{{end}}</tbody></table>{{end}}
{{if .BackupSHA256}}<p>Staged backup: {{.BackupBytes}} bytes; SHA-256 <code>{{.BackupSHA256}}</code>.</p>{{end}}
<p class="note">This report describes this rehearsal only. Database fingerprints cover the fixed Forgejo identity projection. File fingerprints cover the adapter's bounded repository-file projection; they report counts and a digest, not repository contents. Not-run checks provide no evidence of success. The host runner and Docker engine are trusted. No production service was tested.</p></body></html>`))

func (r *Report) HTML(out io.Writer) error {
	if err := r.Validate(); err != nil {
		return err
	}
	page := htmlPage
	if r.SchemaVersion == 2 {
		page = forgejoHTMLPage
	}
	if err := page.Execute(out, r); err != nil {
		return code("REPORT_WRITE_FAILED")
	}
	return nil
}

// SafeCode withholds arbitrary errors, including uppercase strings that could
// be a private token. Operation-specific codes are assigned by the caller's
// fixed check branches; formatting alone cannot prove an error is safe.
func SafeCode(err error) string {
	if err == nil {
		return "VALIDATED"
	}
	return "OPERATION_FAILED"
}
