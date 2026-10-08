// Package forgejo contains the fixed, data-only Forgejo 15.0.9 to 16.0.5
// rehearsal contract. It intentionally does not manage containers or state.
package forgejo

import (
	"errors"
	"slices"
)

const (
	SourceVersion   = "15.0.9"
	TargetVersion   = "16.0.5"
	PostgresVersion = "17.11"

	// These immutable OCI references are the linux/amd64 child manifests of
	// the official Codeberg and Docker Hub image indexes.
	SourceImage   = "codeberg.org/forgejo/forgejo@sha256:46c82686a28aae45154325501ffa51aaee2cdf6cdfcf92137e3f7cce54987823"
	TargetImage   = "codeberg.org/forgejo/forgejo@sha256:523de0217475297d05786d7551c1c1d6b5c8b90d6fee7189e88a234260ec0e74"
	PostgresImage = "docker.io/library/postgres@sha256:3cec7eb015ba8adb28139fa5c83b8489cdf0e666e53dfdf20f598ae0cc8739e3"
	ProbeImage    = "docker.io/curlimages/curl@sha256:43366cd60f226c7655181a0f7e85c468a41d182fdd2dc2c1c3b872a2b9d05d7a"

	ArchiveFormat        = "rehearse-forgejo-archive"
	ArchiveFormatVersion = 1
	ManifestMember       = "manifest.json"
	DatabaseMember       = "database.pgdump"
	DataMember           = "forgejo-data.tar"

	MaxArchiveBytes       int64  = 2 << 30
	MaxManifestBytes      uint64 = 32 << 10
	MaxDatabaseBytes      uint64 = 1 << 30
	MaxDataTarBytes       uint64 = 1 << 30
	MaxDataExpandedBytes  uint64 = 1 << 30
	MaxDataFileBytes      int64  = 256 << 20
	MaxDataEntries        uint64 = 100_000
	MaxDataPathBytes             = 4096
	MaxDataPathDepth             = 128
	MaxDataPathBytesTotal uint64 = 64 << 20
	MaxDataAncestorRefs   uint64 = 1_000_000
	MaxSchemaBytes        uint64 = 128 << 10
	MaxSchemaLineBytes           = 4096
	MaxAPIResponseBytes          = 8 << 20
	MaxAPIListItems              = 50
	MaxFileContentBytes   int64  = 1 << 20
)

var (
	ErrArchiveInvalid      = errors.New("ARCHIVE_INVALID")
	ErrArchiveTooLarge     = errors.New("ARCHIVE_TOO_LARGE")
	ErrArchiveCanceled     = errors.New("ARCHIVE_CANCELED")
	ErrArchiveMember       = errors.New("ARCHIVE_MEMBER_INVALID")
	ErrArchiveSource       = errors.New("ARCHIVE_SOURCE_INVALID")
	ErrSourceChanged       = errors.New("SOURCE_CHANGED")
	ErrTarInvalid          = errors.New("DATA_TAR_INVALID")
	ErrTarUnsafePath       = errors.New("DATA_TAR_UNSAFE_PATH")
	ErrTarSpecialEntry     = errors.New("DATA_TAR_SPECIAL_ENTRY")
	ErrTarTooLarge         = errors.New("DATA_TAR_TOO_LARGE")
	ErrSchemaInvalid       = errors.New("SCHEMA_PROJECTION_INVALID")
	ErrAPICanceled         = errors.New("API_CANCELED")
	ErrAPIEndpoint         = errors.New("API_ENDPOINT_UNSUPPORTED")
	ErrAPIAuth             = errors.New("API_AUTH_INVALID")
	ErrAPIResponse         = errors.New("API_RESPONSE_INVALID")
	ErrAPIResponseTooLarge = errors.New("API_RESPONSE_TOO_LARGE")
)

// Member records one payload's exact uncompressed size and SHA-256 digest.
type Member struct {
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest binds an offline archive to this exact source/target contract.
// Payload members are sorted by name and exclude the manifest itself.
type Manifest struct {
	Format          string   `json:"format"`
	FormatVersion   int      `json:"format_version"`
	SourceVersion   string   `json:"source_version"`
	TargetVersion   string   `json:"target_version"`
	PostgresVersion string   `json:"postgres_version"`
	SourceImage     string   `json:"source_image"`
	TargetImage     string   `json:"target_image"`
	PostgresImage   string   `json:"postgres_image"`
	Members         []Member `json:"members"`
}

// SchemaExpectation is the exact schema/migration state recorded by a tagged
// Forgejo source tree. MigrationIDs are returned in bytewise order.
type SchemaExpectation struct {
	GiteaVersion   int64
	ForgejoVersion int64
	MigrationIDs   []string
}

// SchemaSnapshot is the small database schema projection parsed from SchemaSQL.
type SchemaSnapshot struct {
	GiteaVersion   int64
	ForgejoVersion int64
	MigrationIDs   []string
}

var sourceMigrationIDs = []string{
	"v14a_actions-approval-and-trust",
	"v14a_add-action_task-index",
	"v14a_add-foreign-keys-collaboration",
	"v14a_add-foreign-keys-forgejo_auth_token",
	"v14a_add-foreign-keys-pull_request-1",
	"v14a_add-forgejo-migrations-table",
	"v14a_ap-change-fedi-handle-structure",
	"v14a_migrate_task_secrets",
	"v14a_migrate_webhook_authorization",
	"v14a_remove-is-deleted-column-from-activity-action-table",
	"v14a_rework-notification",
	"v14a_rm-repository-issue-stat-fields",
	"v14a_set_remote_user_prohibit_login",
	"v14b_action-reindexing",
	"v14b_action-run-add-workflow-directory",
	"v14b_add-action_run-preexecutionerrorcode",
	"v14b_rm-repository-actionrun-stat-fields",
	"v15a_remove-softdelete-action_runner_token",
	"v15b_add-access_token-owned-repos",
	"v15b_add-access_token_resource",
	"v15b_add-ephemeral_runner",
	"v15b_add-foreign-keys-action_runner_token",
	"v15b_add-runner_request_key",
	"v15c_add_job_handle",
	"v15c_add_mirror_remoteaddressauth",
	"v15c_add_schedule_spec_time_zones",
	"v15c_fix-project-sorting-unique-constraints",
	"v17a_add-action-run-workflow-source-commit",
}

var targetMigrationIDs = []string{
	"v14a_actions-approval-and-trust",
	"v14a_add-action_task-index",
	"v14a_add-foreign-keys-collaboration",
	"v14a_add-foreign-keys-forgejo_auth_token",
	"v14a_add-foreign-keys-pull_request-1",
	"v14a_add-forgejo-migrations-table",
	"v14a_ap-change-fedi-handle-structure",
	"v14a_migrate_task_secrets",
	"v14a_migrate_webhook_authorization",
	"v14a_remove-is-deleted-column-from-activity-action-table",
	"v14a_rework-notification",
	"v14a_rm-repository-issue-stat-fields",
	"v14a_set_remote_user_prohibit_login",
	"v14b_action-reindexing",
	"v14b_action-run-add-workflow-directory",
	"v14b_add-action_run-preexecutionerrorcode",
	"v14b_rm-repository-actionrun-stat-fields",
	"v15a_remove-softdelete-action_runner_token",
	"v15b_add-access_token-owned-repos",
	"v15b_add-access_token_resource",
	"v15b_add-ephemeral_runner",
	"v15b_add-foreign-keys-action_runner_token",
	"v15b_add-runner_request_key",
	"v15c_add_job_handle",
	"v15c_add_mirror_remoteaddressauth",
	"v15c_add_schedule_spec_time_zones",
	"v15c_fix-project-sorting-unique-constraints",
	"v16a_add_authorized_integration",
	"v16a_add_oidcsubjectformat_actions_unit_config",
	"v16b_add-login-source-id-to-forgejo-auth-token",
	"v16b_add_comment_line_count",
	"v16b_authorized_integration_name_description",
	"v16c_action_run_priority",
	"v16c_add_team_invite_invited_id",
	"v16c_authorized_integration_ui",
	"v16c_cleanup_package_blob_indexes",
	"v16d_action_run_warnings",
	"v16e_add-granular-watch",
	"v17a_add-action-run-workflow-source-commit",
}

// SourceSchemaExpectation is derived from the v15.0.9 migration declarations:
// Gitea DB version 305, Forgejo legacy version 44 and the listed registered IDs.
func SourceSchemaExpectation() SchemaExpectation {
	return SchemaExpectation{GiteaVersion: 305, ForgejoVersion: 44, MigrationIDs: slices.Clone(sourceMigrationIDs)}
}

// TargetSchemaExpectation is derived from the v16.0.5 migration declarations.
func TargetSchemaExpectation() SchemaExpectation {
	return SchemaExpectation{GiteaVersion: 305, ForgejoVersion: 44, MigrationIDs: slices.Clone(targetMigrationIDs)}
}

// SchemaMatches compares all three migration trackers, including exact modern
// migration IDs rather than only a count or maximum version.
func SchemaMatches(actual SchemaSnapshot, expected SchemaExpectation) bool {
	return actual.GiteaVersion == expected.GiteaVersion &&
		actual.ForgejoVersion == expected.ForgejoVersion &&
		slices.Equal(actual.MigrationIDs, expected.MigrationIDs)
}

// ProjectionSQL emits one JSON array per line, ordered by kind and primary key.
// Fields are common to the v15.0.9 and v16.0.5 user/repository models. Password
// material, private email, timestamps, counters and repository size caches are
// intentionally excluded; no joins can hide orphaned rows.
const ProjectionSQL = `SELECT line::text
FROM (
    SELECT 'user'::text AS kind, id::bigint AS sort_id,
           jsonb_build_array('user', id, lower_name, name, is_active, is_admin) AS line
    FROM "user"
    UNION ALL
    SELECT 'repository', id::bigint,
           jsonb_build_array('repository', id, owner_id, lower_name, name,
                             default_branch, is_private, is_empty, is_archived,
                             is_mirror, is_fork, fork_id, is_template, template_id,
                             object_format_name)
    FROM repository
) AS projection
ORDER BY kind COLLATE "C", sort_id`

// CountsSQL exposes only bounded aggregate counts used to explain the selected
// SQL projection; it never selects profile names or file/repository contents.
const CountsSQL = `SELECT jsonb_build_object(
    'users', (SELECT count(*) FROM "user"),
    'repositories', (SELECT count(*) FROM repository),
    'private_repositories', (SELECT count(*) FROM repository WHERE is_private IS TRUE),
    'empty_repositories', (SELECT count(*) FROM repository WHERE is_empty IS TRUE),
    'archived_repositories', (SELECT count(*) FROM repository WHERE is_archived IS TRUE),
    'mirrors', (SELECT count(*) FROM repository WHERE is_mirror IS TRUE),
    'forks', (SELECT count(*) FROM repository WHERE is_fork IS TRUE)
)::text`

// SchemaSQL emits version tracker rows followed by the exact registered modern
// Forgejo migration IDs. Creation timestamps are deliberately excluded.
const SchemaSQL = `SELECT line
FROM (
    SELECT 1 AS ord, id::text AS sort_key,
           jsonb_build_array('version', id, version)::text AS line
    FROM version
    UNION ALL
    SELECT 2, id::text,
           jsonb_build_array('forgejo_version', id, version)::text
    FROM forgejo_version
    UNION ALL
    SELECT 3, id,
           jsonb_build_array('forgejo_migration', id)::text
    FROM forgejo_migration
) AS records
ORDER BY ord, sort_key COLLATE "C"`
