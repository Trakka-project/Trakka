package db

import (
	"context"
	"testing"

	"trakka/internal/recurrence"
)

// TestMigrationRewritesLegacyRecurrenceRules runs migration 22 over rows in
// every legacy recurrence form and checks each comes out exactly as the
// write path's normalizer (internal/recurrence) would spell it, and that a
// rule already in canonical form is left alone.
func TestMigrationRewritesLegacyRecurrenceRules(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	owner := mustCreateUser(t, ctx, d)
	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", owner)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Tâches", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}

	rules := []string{
		"DAILY", "WEEKLY", "MONTHLY", "YEARLY",
		"EVERY_X_DAYS:1", "EVERY_X_DAYS:3", "EVERY_X_DAYS:7", "EVERY_X_DAYS:10", "EVERY_X_DAYS:14", "EVERY_X_DAYS:21",
		"EVERY_X_MONTHS:1", "EVERY_X_MONTHS:6",
		"FREQ=WEEKLY;BYDAY=MO,WE,FR",
	}
	ids := make([]int64, len(rules))
	for i, rule := range rules {
		// Written with raw SQL: CreateItem's callers always validate, and
		// these rows stand for values stored before migration 22.
		res, err := d.conn.ExecContext(ctx,
			`INSERT INTO items (list_id, title, is_recurring, recurrence_rule) VALUES (?, ?, 1, ?)`, list.ID, rule, rule)
		if err != nil {
			t.Fatalf("inserting %q: %v", rule, err)
		}
		if ids[i], err = res.LastInsertId(); err != nil {
			t.Fatalf("reading id: %v", err)
		}
	}

	// Rewinding re-runs every later migration too: 23 is idempotent, but
	// 24's ADD COLUMN and 25's CREATE TABLE have to be undone first.
	if _, err := d.conn.Exec(`ALTER TABLE users DROP COLUMN vibrate_on_notification`); err != nil {
		t.Fatalf("undoing migration 24: %v", err)
	}
	if _, err := d.conn.Exec(`DROP TABLE calendar_feed_tokens`); err != nil {
		t.Fatalf("undoing migration 25: %v", err)
	}
	if _, err := d.conn.Exec(`PRAGMA user_version = 21`); err != nil {
		t.Fatalf("rewinding user_version: %v", err)
	}
	if err := migrateSchema(d.conn, discardLogger(), func(int, int) error { return nil }); err != nil {
		t.Fatalf("migrateSchema: %v", err)
	}

	for i, rule := range rules {
		item, err := d.GetItem(ctx, ids[i])
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		parsed, err := recurrence.Parse(rule)
		if err != nil {
			t.Fatalf("recurrence.Parse(%q): %v", rule, err)
		}
		if item.RecurrenceRule == nil || *item.RecurrenceRule != parsed.String() {
			t.Errorf("%q migrated to %v, want %q", rule, item.RecurrenceRule, parsed.String())
		}
	}
}
