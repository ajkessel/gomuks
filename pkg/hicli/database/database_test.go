// Copyright (c) 2026 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package database

import (
	"context"
	"testing"

	"go.mau.fi/util/dbutil"
)

func TestFreshSQLiteUpgradeUsesLatestRevision(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t, ctx)
	if err := db.Upgrade(ctx); err != nil {
		t.Fatalf("fresh database upgrade failed: %v", err)
	}

	var version, compat int
	err := db.QueryRow(ctx, "SELECT version, compat FROM version LIMIT 1").Scan(&version, &compat)
	if err != nil {
		t.Fatalf("failed to read schema version: %v", err)
	}
	if version != len(db.UpgradeTable) {
		t.Fatalf("schema version = %d, want latest %d", version, len(db.UpgradeTable))
	}
	if compat != 22 {
		t.Fatalf("schema compat = %d, want 22", compat)
	}

	_, err = db.Exec(ctx, `INSERT INTO room (room_id) VALUES ('!room:example.com')`)
	if err != nil {
		t.Fatalf("failed to insert test room: %v", err)
	}
	_, err = db.Exec(ctx, `INSERT INTO event (room_id, event_id, sender, type, timestamp, content, unsigned)
		VALUES ('!room:example.com', '$event', '@alice:example.com', 'm.room.message', 1, '{"body":"Cafe running"}', '{}')`)
	if err != nil {
		t.Fatalf("failed to insert test event: %v", err)
	}

	var matches int
	err = db.QueryRow(ctx, `SELECT count(*) FROM event_fts WHERE event_fts MATCH 'run'`).Scan(&matches)
	if err != nil {
		t.Fatalf("failed to query FTS index: %v", err)
	}
	if matches != 1 {
		t.Fatalf("FTS match count = %d, want 1", matches)
	}
}

func TestFTSIndexesRedactedEventsByDefault(t *testing.T) {
	ctx := context.Background()
	withIndexRedacted(t, true)
	db := openTestDatabase(t, ctx)
	if err := db.Upgrade(ctx); err != nil {
		t.Fatalf("fresh database upgrade failed: %v", err)
	}
	insertSearchTestRoom(t, ctx, db)

	_, err := db.Exec(ctx, `INSERT INTO event (room_id, event_id, sender, type, timestamp, content, unsigned, redacted_by)
		VALUES ('!room:example.com', '$redacted', '@alice:example.com', 'm.room.message', 1, '{"body":"secret message"}', '{}', '$redaction')`)
	if err != nil {
		t.Fatalf("failed to insert redacted test event: %v", err)
	}

	assertFTSMatchCount(t, ctx, db, "secret", 1)
	events, err := db.Event.Search(ctx, "secret", "", "!room:example.com", true, true, false, 0, 0, 10, 0)
	if err != nil {
		t.Fatalf("failed to search events: %v", err)
	}
	assertEventIDs(t, events, "$redacted")
	if events[0].RedactedBy != "$redaction" {
		t.Fatalf("redacted_by = %q, want $redaction", events[0].RedactedBy)
	}
}

func TestFTSCanExcludeRedactedEvents(t *testing.T) {
	ctx := context.Background()
	withIndexRedacted(t, false)
	db := openTestDatabase(t, ctx)
	if err := db.Upgrade(ctx); err != nil {
		t.Fatalf("fresh database upgrade failed: %v", err)
	}
	insertSearchTestRoom(t, ctx, db)

	_, err := db.Exec(ctx, `INSERT INTO event (room_id, event_id, sender, type, timestamp, content, unsigned, redacted_by)
		VALUES ('!room:example.com', '$redacted', '@alice:example.com', 'm.room.message', 1, '{"body":"secret message"}', '{}', '$redaction')`)
	if err != nil {
		t.Fatalf("failed to insert redacted test event: %v", err)
	}
	_, err = db.Exec(ctx, `INSERT INTO event (room_id, event_id, sender, type, timestamp, content, unsigned)
		VALUES ('!room:example.com', '$visible', '@alice:example.com', 'm.room.message', 2, '{"body":"visible message"}', '{}')`)
	if err != nil {
		t.Fatalf("failed to insert visible test event: %v", err)
	}

	assertFTSMatchCount(t, ctx, db, "secret", 0)
	assertFTSMatchCount(t, ctx, db, "visible", 1)
	events, err := db.Event.Search(ctx, "secret", "", "!room:example.com", true, true, false, 0, 0, 10, 0)
	if err != nil {
		t.Fatalf("failed to search events: %v", err)
	}
	assertEventIDs(t, events)
}

func TestApplyIndexRedactedRebuildsRedactedFTSRows(t *testing.T) {
	ctx := context.Background()
	withIndexRedacted(t, true)
	db := openTestDatabase(t, ctx)
	if err := db.Upgrade(ctx); err != nil {
		t.Fatalf("fresh database upgrade failed: %v", err)
	}
	insertSearchTestRoom(t, ctx, db)

	_, err := db.Exec(ctx, `INSERT INTO event (room_id, event_id, sender, type, timestamp, content, unsigned, redacted_by)
		VALUES ('!room:example.com', '$redacted', '@alice:example.com', 'm.room.message', 1, '{"body":"secret message"}', '{}', '$redaction')`)
	if err != nil {
		t.Fatalf("failed to insert redacted test event: %v", err)
	}
	assertFTSMatchCount(t, ctx, db, "secret", 1)

	if err = db.Event.ApplyIndexRedacted(ctx, false); err != nil {
		t.Fatalf("failed to disable redacted indexing: %v", err)
	}
	assertFTSMatchCount(t, ctx, db, "secret", 0)

	if err = db.Event.ApplyIndexRedacted(ctx, true); err != nil {
		t.Fatalf("failed to enable redacted indexing: %v", err)
	}
	assertFTSMatchCount(t, ctx, db, "secret", 1)
}

func TestApplyIndexRedactedRepairsOldRedactionTrigger(t *testing.T) {
	ctx := context.Background()
	withIndexRedacted(t, true)
	db := openTestDatabase(t, ctx)
	if err := db.Upgrade(ctx); err != nil {
		t.Fatalf("fresh database upgrade failed: %v", err)
	}
	insertSearchTestRoom(t, ctx, db)

	_, err := db.Exec(ctx, `DROP TRIGGER event_fts_redact`)
	if err != nil {
		t.Fatalf("failed to drop redaction trigger: %v", err)
	}
	_, err = db.Exec(ctx, `CREATE TRIGGER event_fts_redact AFTER UPDATE OF redacted_by ON event
		WHEN NEW.redacted_by IS NOT NULL AND OLD.redacted_by IS NULL
		BEGIN
			DELETE FROM event_fts WHERE rowid = NEW.rowid;
		END`)
	if err != nil {
		t.Fatalf("failed to create old redaction trigger: %v", err)
	}
	if err = db.Event.ApplyIndexRedacted(ctx, true); err != nil {
		t.Fatalf("failed to apply redacted indexing: %v", err)
	}

	_, err = db.Exec(ctx, `INSERT INTO event (room_id, event_id, sender, type, timestamp, content, unsigned)
		VALUES ('!room:example.com', '$redacted', '@alice:example.com', 'm.room.message', 1, '{"body":"secret message"}', '{}')`)
	if err != nil {
		t.Fatalf("failed to insert test event: %v", err)
	}
	assertFTSMatchCount(t, ctx, db, "secret", 1)

	_, err = db.Exec(ctx, `UPDATE event SET redacted_by = '$redaction' WHERE event_id = '$redacted'`)
	if err != nil {
		t.Fatalf("failed to redact test event: %v", err)
	}
	assertFTSMatchCount(t, ctx, db, "secret", 1)
}

func TestEventContextWorksWithoutTimelineRows(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t, ctx)
	if err := db.Upgrade(ctx); err != nil {
		t.Fatalf("fresh database upgrade failed: %v", err)
	}

	_, err := db.Exec(ctx, `INSERT INTO room (room_id) VALUES ('!room:example.com')`)
	if err != nil {
		t.Fatalf("failed to insert test room: %v", err)
	}
	for i, eventID := range []string{"$event1", "$event2", "$event3", "$event4", "$event5"} {
		_, err = db.Exec(ctx, `INSERT INTO event (room_id, event_id, sender, type, timestamp, content, unsigned)
			VALUES ('!room:example.com', $1, '@alice:example.com', 'm.room.message', $2, '{"body":"test"}', '{}')`, eventID, int64(i+1))
		if err != nil {
			t.Fatalf("failed to insert test event %s: %v", eventID, err)
		}
	}

	target, err := db.Event.GetByID(ctx, "!room:example.com", "$event3")
	if err != nil {
		t.Fatalf("failed to get target event: %v", err)
	} else if target == nil {
		t.Fatal("target event not found")
	}

	before, after, err := db.Event.GetContext(ctx, target, 2)
	if err != nil {
		t.Fatalf("failed to get event-table context: %v", err)
	}
	assertEventIDs(t, before, "$event2", "$event1")
	assertEventIDs(t, after, "$event4", "$event5")
}

func openTestDatabase(t *testing.T, ctx context.Context) *Database {
	t.Helper()
	rawDB, err := dbutil.NewWithDialect(":memory:", "sqlite3-fk-wal")
	if err != nil {
		t.Fatalf("failed to open in-memory SQLite database: %v", err)
	}
	t.Cleanup(func() {
		_ = rawDB.Close()
	})
	return New(rawDB)
}

func withIndexRedacted(t *testing.T, enabled bool) {
	t.Helper()
	previous := IndexRedacted()
	SetIndexRedacted(enabled)
	t.Cleanup(func() {
		SetIndexRedacted(previous)
	})
}

func insertSearchTestRoom(t *testing.T, ctx context.Context, db *Database) {
	t.Helper()
	_, err := db.Exec(ctx, `INSERT INTO room (room_id) VALUES ('!room:example.com')`)
	if err != nil {
		t.Fatalf("failed to insert test room: %v", err)
	}
}

func assertFTSMatchCount(t *testing.T, ctx context.Context, db *Database, query string, expected int) {
	t.Helper()
	var matches int
	err := db.QueryRow(ctx, `SELECT count(*) FROM event_fts WHERE event_fts MATCH $1`, query).Scan(&matches)
	if err != nil {
		t.Fatalf("failed to query FTS index: %v", err)
	}
	if matches != expected {
		t.Fatalf("FTS match count for %q = %d, want %d", query, matches, expected)
	}
}

func assertEventIDs(t *testing.T, events []*Event, expected ...string) {
	t.Helper()
	if len(events) != len(expected) {
		t.Fatalf("event count = %d, want %d", len(events), len(expected))
	}
	for i, evt := range events {
		if evt.ID.String() != expected[i] {
			t.Fatalf("event #%d = %s, want %s", i, evt.ID, expected[i])
		}
	}
}
