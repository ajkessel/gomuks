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
