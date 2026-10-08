// Package fixture defines the deterministic, synthetic Miniflux database
// content used by a rehearsal. Its SQL is intended only for a fresh database
// that already contains the fixture administrator.
package fixture

const (
	AdminUsername = "rehearse-fixture"
	AdminPassword = "synthetic-fixture-password"

	FirstCategoryID  int64 = 91001
	SecondCategoryID int64 = 91002
	FirstFeedID      int64 = 92001
	SecondFeedID     int64 = 92002

	ReadEntryID            int64 = 930001
	UnreadEntryID          int64 = 930002
	EligibleRemovedEntryID int64 = 930003
	EmptyHashEntryID       int64 = 930004

	EligibleRemovedHash       = "fixture-removed-eligible"
	OrphanMigrationHash       = "rehearse-orphan-migration-127"
	OrphanMigrationFeed int64 = 9223372036854770000
)

// SeedSQL inserts the synthetic baseline into a fresh schema-125 database.
// It requires the administrator and default category created by Miniflux's
// environment-driven initialization path. It removes only that guarded,
// empty default category before inserting fixture categories. IDs are fixed
// for stable evidence and their sequences are advanced after insertion. The
// user ID is resolved by the fixed synthetic username.
const SeedSQL = `BEGIN;
SET LOCAL search_path = public;
SET LOCAL TIME ZONE 'UTC';
DO $fixture$
DECLARE
	fixture_user_id integer;
	fixture_user_is_admin boolean;
	fixture_default_category_id integer;
	deleted_categories integer;
BEGIN
	IF (SELECT count(*) FROM users) <> 1 THEN
		RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'FIXTURE_PRECONDITION_FAILED';
	END IF;

	SELECT id, is_admin INTO fixture_user_id, fixture_user_is_admin
	FROM users WHERE username = 'rehearse-fixture';
	IF NOT FOUND OR fixture_user_is_admin IS NOT TRUE THEN
		RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'FIXTURE_PRECONDITION_FAILED';
	END IF;

	IF EXISTS (SELECT 1 FROM feeds) OR EXISTS (SELECT 1 FROM entries) THEN
		RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'FIXTURE_PRECONDITION_FAILED';
	END IF;
	IF (SELECT count(*) FROM categories) <> 1 THEN
		RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'FIXTURE_PRECONDITION_FAILED';
	END IF;

	SELECT id INTO fixture_default_category_id
	FROM categories
	WHERE user_id = fixture_user_id AND title = 'All' AND hide_globally IS FALSE;
	IF NOT FOUND THEN
		RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'FIXTURE_PRECONDITION_FAILED';
	END IF;

	DELETE FROM categories
	WHERE id = fixture_default_category_id
	  AND user_id = fixture_user_id
	  AND title = 'All'
	  AND hide_globally IS FALSE;
	GET DIAGNOSTICS deleted_categories = ROW_COUNT;
	IF deleted_categories <> 1 THEN
		RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'FIXTURE_PRECONDITION_FAILED';
	END IF;

	INSERT INTO categories (id, user_id, title, hide_globally) VALUES
		(91001, fixture_user_id, 'Synthetic General — 第一', false),
		(91002, fixture_user_id, 'Synthetic Research & Café', false);

	INSERT INTO feeds (id, user_id, category_id, title, feed_url, site_url, checked_at, next_check_at) VALUES
		(92001, fixture_user_id, 91001, 'Rehearse Fixture Feed — Alpha', 'https://alpha.synthetic.invalid/rss.xml', 'https://alpha.synthetic.invalid/', '2024-05-06 07:08:09.123456+00', '2024-05-06 07:08:09.123456+00'),
		(92002, fixture_user_id, 91002, 'Rehearse Fixture Feed — β', 'https://beta.synthetic.invalid/rss.xml', 'https://beta.synthetic.invalid/', '2024-05-06 07:08:09.123456+00', '2024-05-06 07:08:09.123456+00');

	INSERT INTO entries (id, user_id, feed_id, hash, published_at, title, url, author, content, status, starred, tags, comments_url, changed_at, created_at) VALUES
		(930001, fixture_user_id, 92001, 'fixture-read-starred-tagged', '2024-05-06 07:08:09.123456+00', 'Synthetic read — café 東京', 'https://alpha.synthetic.invalid/read-1', 'Fixture Author', E'First line.\nSecond line — Καλημέρα.', 'read', true, ARRAY['rehearse', 'café']::text[], '', '2024-05-07 08:09:10.654321+00', '2024-05-06 07:08:09.123456+00'),
		(930002, fixture_user_id, 92002, 'fixture-unread-api-compatible', '2024-06-07 08:09:10.234567+00', 'Synthetic unread — α & β', 'https://beta.synthetic.invalid/unread-1', 'Second Fixture Author', E'Unread content with Unicode — 雪.\nA second line.', 'unread', false, ARRAY[]::text[], '', '2024-06-07 08:09:10.234567+00', '2024-06-07 08:09:10.234567+00'),
		(930003, fixture_user_id, 92001, 'fixture-removed-eligible', '2024-02-03 04:05:06.000007+00', 'Synthetic removed with key', 'https://alpha.synthetic.invalid/removed-key', NULL, NULL, 'removed', NULL, NULL, NULL, '2024-03-04 05:06:07.000008+00', '2024-02-03 04:05:06.000007+00'),
		(930004, fixture_user_id, 92002, '', '2024-01-02 03:04:05.000006+00', 'Synthetic removed with empty hash', 'https://beta.synthetic.invalid/removed-empty', '', '', 'removed', false, ARRAY[]::text[], '', '2024-01-03 04:05:06.000007+00', '2024-01-02 03:04:05.000006+00');

	PERFORM setval(pg_get_serial_sequence('categories', 'id'), 91002, true);
	PERFORM setval(pg_get_serial_sequence('feeds', 'id'), 92002, true);
	PERFORM setval(pg_get_serial_sequence('entries', 'id'), 930004, true);
END;
$fixture$;
COMMIT;
`

// BrokenMigrationSQL modifies exactly one known retained fixture row in a
// separately restored, disposable database. session_replication_role is
// superuser-only; SET LOCAL confines the trigger suppression to this
// transaction, and the explicit reset re-enables triggers before commit.
const BrokenMigrationSQL = `BEGIN;
SET LOCAL search_path = public;
SET LOCAL session_replication_role = replica;
DO $fixture$
DECLARE
	changed_rows integer;
BEGIN
	UPDATE entries
	SET feed_id = 9223372036854770000,
	    hash = 'rehearse-orphan-migration-127',
	    status = 'removed'
	WHERE id = 930001
	  AND feed_id = 92001
	  AND hash = 'fixture-read-starred-tagged'
	  AND status = 'read';
	GET DIAGNOSTICS changed_rows = ROW_COUNT;
	IF changed_rows <> 1 THEN
		RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = 'FIXTURE_BROKEN_VARIANT_PRECONDITION_FAILED';
	END IF;
END;
$fixture$;
SET LOCAL session_replication_role = origin;
COMMIT;
`

type Counts struct {
	Users          int64 `json:"users"`
	Categories     int64 `json:"categories"`
	Feeds          int64 `json:"feeds"`
	EntriesUnread  int64 `json:"entries_unread"`
	EntriesRead    int64 `json:"entries_read"`
	EntriesRemoved int64 `json:"entries_removed"`
	EntriesStarred int64 `json:"entries_starred"`
	EntriesTagged  int64 `json:"entries_tagged"`
}

type StateExpectation struct {
	SchemaVersion int    `json:"schemaVersion"`
	Counts        Counts `json:"counts"`
}

type RemovedKey struct {
	FeedID int64  `json:"feed_id"`
	Hash   string `json:"hash"`
}

type Category struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type Feed struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	FeedURL  string `json:"feed_url"`
	SiteURL  string `json:"site_url"`
	Category int64  `json:"category_id"`
}

// Entry is an expectation for the fixed database projection. Nil pointers
// represent SQL NULL, which is distinct from false and empty arrays.
type Entry struct {
	ID       int64    `json:"id"`
	FeedID   int64    `json:"feed_id"`
	Hash     string   `json:"hash"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Author   *string  `json:"author"`
	Content  *string  `json:"content"`
	Starred  *bool    `json:"starred"`
	Tags     []string `json:"tags"`
	Comments *string  `json:"comments_url"`
}

type Expectations struct {
	AdminUsername  string           `json:"adminUsername"`
	Categories     []Category       `json:"categories"`
	Feeds          []Feed           `json:"feeds"`
	ActiveEntries  []Entry          `json:"activeEntries"`
	RemovedEntries []Entry          `json:"removedEntries"`
	RemovedKeys    []RemovedKey     `json:"removedKeys"`
	TombstoneKeys  []RemovedKey     `json:"tombstoneKeys"`
	Baseline       StateExpectation `json:"baseline"`
	Target         StateExpectation `json:"target"`
	Recovery       StateExpectation `json:"recovery"`
	BrokenVariant  StateExpectation `json:"brokenVariantBeforeMigration"`
}

var (
	readAuthor          = "Fixture Author"
	readContent         = "First line.\nSecond line — Καλημέρα."
	readStarred         = true
	readTags            = []string{"rehearse", "café"}
	readComment         = ""
	removedStarred      = false
	emptyRemovedComment = ""
	emptyRemovedAuthor  = ""
	emptyRemovedContent = ""
	unreadAuthor        = "Second Fixture Author"
	unreadContent       = "Unread content with Unicode — 雪.\nA second line."
	unreadStarred       = false
	unreadTags          = []string{}
	unreadComment       = ""
)

// Expected is deliberately declared separately from SeedSQL so the runner's
// checks are a reviewed contract rather than values extracted from the SQL.
var Expected = Expectations{
	AdminUsername: AdminUsername,
	Categories: []Category{
		{ID: FirstCategoryID, Title: "Synthetic General — 第一"},
		{ID: SecondCategoryID, Title: "Synthetic Research & Café"},
	},
	Feeds: []Feed{
		{ID: FirstFeedID, Title: "Rehearse Fixture Feed — Alpha", FeedURL: "https://alpha.synthetic.invalid/rss.xml", SiteURL: "https://alpha.synthetic.invalid/", Category: FirstCategoryID},
		{ID: SecondFeedID, Title: "Rehearse Fixture Feed — β", FeedURL: "https://beta.synthetic.invalid/rss.xml", SiteURL: "https://beta.synthetic.invalid/", Category: SecondCategoryID},
	},
	ActiveEntries: []Entry{
		{ID: ReadEntryID, FeedID: FirstFeedID, Hash: "fixture-read-starred-tagged", Title: "Synthetic read — café 東京", Status: "read", Author: &readAuthor, Content: &readContent, Starred: &readStarred, Tags: readTags, Comments: &readComment},
		{ID: UnreadEntryID, FeedID: SecondFeedID, Hash: "fixture-unread-api-compatible", Title: "Synthetic unread — α & β", Status: "unread", Author: &unreadAuthor, Content: &unreadContent, Starred: &unreadStarred, Tags: unreadTags, Comments: &unreadComment},
	},
	RemovedEntries: []Entry{
		{ID: EligibleRemovedEntryID, FeedID: FirstFeedID, Hash: EligibleRemovedHash, Title: "Synthetic removed with key", Status: "removed", Author: nil, Content: nil, Starred: nil, Tags: nil, Comments: nil},
		{ID: EmptyHashEntryID, FeedID: SecondFeedID, Hash: "", Title: "Synthetic removed with empty hash", Status: "removed", Author: &emptyRemovedAuthor, Content: &emptyRemovedContent, Starred: &removedStarred, Tags: []string{}, Comments: &emptyRemovedComment},
	},
	RemovedKeys:   []RemovedKey{{FeedID: FirstFeedID, Hash: EligibleRemovedHash}},
	TombstoneKeys: []RemovedKey{{FeedID: FirstFeedID, Hash: EligibleRemovedHash}},
	Baseline:      StateExpectation{SchemaVersion: 125, Counts: Counts{Users: 1, Categories: 2, Feeds: 2, EntriesUnread: 1, EntriesRead: 1, EntriesRemoved: 2, EntriesStarred: 1, EntriesTagged: 1}},
	Target:        StateExpectation{SchemaVersion: 132, Counts: Counts{Users: 1, Categories: 2, Feeds: 2, EntriesUnread: 1, EntriesRead: 1, EntriesRemoved: 0, EntriesStarred: 1, EntriesTagged: 1}},
	Recovery:      StateExpectation{SchemaVersion: 125, Counts: Counts{Users: 1, Categories: 2, Feeds: 2, EntriesUnread: 1, EntriesRead: 1, EntriesRemoved: 2, EntriesStarred: 1, EntriesTagged: 1}},
	BrokenVariant: StateExpectation{SchemaVersion: 125, Counts: Counts{Users: 1, Categories: 2, Feeds: 2, EntriesUnread: 1, EntriesRead: 0, EntriesRemoved: 3, EntriesStarred: 0, EntriesTagged: 0}},
}
