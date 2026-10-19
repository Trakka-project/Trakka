package db

import (
	"context"
	"fmt"
	"testing"
)

// migration36FinalDDL is list_public_links as migration 36 defines it (one
// link per list), the shape migration 37 repairs drafts to and migration 38
// starts from.
var migration36FinalDDL = fmt.Sprintf(listPublicLinksDDL, "list_public_links")

// publicLinksDraftFixture is a database whose list_public_links was built by
// createOld (a draft of migration 36, or its final shape), holding one row
// per entry of rows (inserted with insertOld, the list id first), rewound to
// user_version 36 — the state the deployments that ran the feature branch
// were left in — then migrated again, which runs migrations 37 and 38.
func publicLinksDraftFixture(t *testing.T, createOld, insertOld string, rows [][]any) (*DB, []int64) {
	t.Helper()
	d := openTestDB(t)
	ctx := context.Background()
	hash := "x"
	user, err := d.CreateUser(ctx, "owner@example.com", &hash, nil, nil, "Owner")
	if err != nil {
		t.Fatal(err)
	}
	house, err := d.CreateHouseWithOwner(ctx, "Maison", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var listIDs []int64
	for range rows {
		list, err := d.CreateList(ctx, "Liste", "shopping", house.ID, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		listIDs = append(listIDs, list.ID)
	}
	for _, stmt := range []string{`DROP TABLE list_public_links`, createOld} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("recreating the migration 36 table: %v", err)
		}
	}
	for i, row := range rows {
		if _, err := d.conn.Exec(insertOld, append([]any{listIDs[i]}, row...)...); err != nil {
			t.Fatalf("inserting a migration 36 row: %v", err)
		}
	}
	if _, err := d.conn.Exec(`PRAGMA user_version = 36`); err != nil {
		t.Fatal(err)
	}
	if err := migrateSchema(d.conn, discardLogger(), func(int, int) error { return nil }); err != nil {
		t.Fatalf("migrateSchema: %v", err)
	}
	return d, listIDs
}

// linksOf returns listID's links, failing the test on error.
func linksOf(t *testing.T, d *DB, listID int64) []*ListPublicLink {
	t.Helper()
	links, err := d.ListListPublicLinks(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	return links
}

// assertCurrentPublicLinksSchema checks the migrated table takes all four
// modes, several links per list and a label, and refuses anything else.
func assertCurrentPublicLinksSchema(t *testing.T, d *DB, listID int64) {
	t.Helper()
	ctx := context.Background()
	before := len(linksOf(t, d, listID))
	label := "Famille"
	for _, mode := range []string{"read", "add_only", "deposit_only", "full_edit"} {
		if _, err := d.CreateListPublicLink(ctx, listID, "probe-"+mode, &label, mode, nil, 1); err != nil {
			t.Errorf("mode %s refused after migrations 37 and 38: %v", mode, err)
		}
	}
	if got := len(linksOf(t, d, listID)); got != before+4 {
		t.Errorf("the list must hold several links: %d, want %d", got, before+4)
	}
	if _, err := d.CreateListPublicLink(ctx, listID, "probe-write", nil, "write", nil, 1); err == nil {
		t.Error("'write' must be refused")
	}
	tooLong := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxy" // 51 characters
	if _, err := d.CreateListPublicLink(ctx, listID, "probe-label", &tooLong, "read", nil, 1); err == nil {
		t.Error("a label over 50 characters must be refused by the schema too")
	}
	var version int
	if err := d.conn.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version < 38 {
		t.Errorf("user_version = %d (%v), want at least 38", version, err)
	}
}

func TestMigration37RepairsTheReadWriteDraft(t *testing.T) {
	d, lists := publicLinksDraftFixture(t, `
		CREATE TABLE list_public_links (
		    list_id      INTEGER PRIMARY KEY REFERENCES lists(id) ON DELETE CASCADE,
		    share_token  TEXT    NOT NULL UNIQUE,
		    access_mode  TEXT    NOT NULL CHECK (access_mode IN ('read', 'write')),
		    is_enabled   INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0, 1)),
		    created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
		    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		    expires_at   TEXT,
		    last_used_at TEXT
		)`,
		`INSERT INTO list_public_links (list_id, share_token, access_mode, is_enabled, expires_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`,
		[][]any{
			{"tok-write", "write", 1, "2099-01-01T00:00:00Z", "2026-10-19T10:00:00.000Z"},
			{"tok-read", "read", 0, nil, nil},
		})
	ctx := context.Background()

	write, err := d.UseListPublicLink(ctx, "tok-write")
	if err != nil {
		t.Fatalf("the 'write' link must survive: %v", err)
	}
	if write.ListID != lists[0] || write.AccessMode != "full_edit" || !write.Enabled || write.ExpiresAt != "2099-01-01T00:00:00Z" || write.Label != "" {
		t.Fatalf("'write' must become 'full_edit' with its settings kept, got %+v", write)
	}
	read := linksOf(t, d, lists[1])
	if len(read) != 1 || read[0].Token != "tok-read" || read[0].AccessMode != "read" || read[0].Enabled {
		t.Fatalf("the 'read' link must be kept as is, got %+v", read)
	}
	assertCurrentPublicLinksSchema(t, d, lists[0])

	// The rebuilt table still goes with its list.
	if err := d.DeleteList(ctx, lists[1]); err != nil {
		t.Fatal(err)
	}
	if links := linksOf(t, d, lists[1]); len(links) != 0 {
		t.Fatal("ON DELETE CASCADE must survive the rebuilds")
	}
}

func TestMigration37DropsHashedDraftLinks(t *testing.T) {
	d, lists := publicLinksDraftFixture(t, `
		CREATE TABLE list_public_links (
		    list_id      INTEGER PRIMARY KEY REFERENCES lists(id) ON DELETE CASCADE,
		    token_hash   TEXT    NOT NULL UNIQUE,
		    access_mode  TEXT    NOT NULL CHECK (access_mode IN ('read', 'write')),
		    is_enabled   INTEGER NOT NULL DEFAULT 1 CHECK (is_enabled IN (0, 1)),
		    created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
		    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		    expires_at   TEXT,
		    last_used_at TEXT
		)`,
		`INSERT INTO list_public_links (list_id, token_hash, access_mode) VALUES (?, ?, ?)`,
		[][]any{{"0123abcd", "write"}})

	if links := linksOf(t, d, lists[0]); len(links) != 0 {
		t.Fatalf("a hashed link can't be carried over and must be dropped, got %+v", links)
	}
	assertCurrentPublicLinksSchema(t, d, lists[0])
}

// Migration 37 alone, on a table already in migration 36's final shape:
// nothing to repair, so the table is left byte for byte as it was.
func TestMigration37LeavesACurrentTableAlone(t *testing.T) {
	d := openTestDB(t)
	for _, stmt := range []string{`DROP TABLE list_public_links`, migration36FinalDDL} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	readSQL := func() string {
		var s string
		if err := d.conn.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'list_public_links'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := readSQL()
	if err := applyPublicLinkModesMigration(d.conn); err != nil {
		t.Fatalf("migration 37: %v", err)
	}
	if after := readSQL(); after != before {
		t.Fatalf("an up-to-date table must not be rebuilt:\nbefore %s\nafter  %s", before, after)
	}
}

// Migration 38 carries every one-per-list link over — token, mode and
// settings — with no label, and lists can then hold several.
func TestMigration38KeepsExistingLinks(t *testing.T) {
	d, lists := publicLinksDraftFixture(t, migration36FinalDDL,
		`INSERT INTO list_public_links (list_id, share_token, access_mode, is_enabled, expires_at) VALUES (?, ?, ?, ?, ?)`,
		[][]any{
			{"tok-a", "deposit_only", 1, "2099-01-01T00:00:00Z"},
			{"tok-b", "add_only", 0, nil},
		})

	a := linksOf(t, d, lists[0])
	if len(a) != 1 || a[0].Token != "tok-a" || a[0].AccessMode != "deposit_only" || !a[0].Enabled || a[0].ExpiresAt != "2099-01-01T00:00:00Z" || a[0].Label != "" || a[0].ID == 0 {
		t.Fatalf("list A's link must be carried over as is, got %+v", a)
	}
	b := linksOf(t, d, lists[1])
	if len(b) != 1 || b[0].Token != "tok-b" || b[0].AccessMode != "add_only" || b[0].Enabled {
		t.Fatalf("list B's link must be carried over as is, got %+v", b)
	}
	assertCurrentPublicLinksSchema(t, d, lists[0])
	// New links don't reuse a carried-over id.
	if links := linksOf(t, d, lists[0]); links[len(links)-1].ID <= b[0].ID {
		t.Fatalf("ids must keep growing past the carried-over links: %+v", links)
	}
}
