// Copyright (c) 2026 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package hicli

import (
	"context"
	"testing"

	"go.mau.fi/util/dbutil"

	"go.mau.fi/gomuks/pkg/hicli/database"
)

func TestParseSearchQueryDateAliases(t *testing.T) {
	expectedStart, expectedEnd, err := parseDateSpec("4/1/26-4/3/26")
	if err != nil {
		t.Fatalf("failed to parse expected date range: %v", err)
	}

	ftsQuery, senderName, startTime, endTime, err := parseSearchQuery(`hello received:4/1/26-4/3/26 from:"Alice Smith"`)
	if err != nil {
		t.Fatalf("parseSearchQuery failed: %v", err)
	}
	if ftsQuery != "hello" {
		t.Fatalf("ftsQuery = %q, want %q", ftsQuery, "hello")
	}
	if senderName != "Alice Smith" {
		t.Fatalf("senderName = %q, want %q", senderName, "Alice Smith")
	}
	if startTime != expectedStart {
		t.Fatalf("startTime = %d, want %d", startTime, expectedStart)
	}
	if endTime != expectedEnd {
		t.Fatalf("endTime = %d, want %d", endTime, expectedEnd)
	}
}

func TestParseSearchQueryOpenEndedDateOperators(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		equivalent string
	}{{
		name:       "greater than is start-only range",
		query:      "date:>4/1/26 hello",
		equivalent: "date:4/1/26- hello",
	}, {
		name:       "less than is end-only range",
		query:      "date:<4/1/26 hello",
		equivalent: "date:-4/1/26 hello",
	}, {
		name:       "received greater than is start-only range",
		query:      "received:>4/1/26 hello",
		equivalent: "date:4/1/26- hello",
	}, {
		name:       "received less than is end-only range",
		query:      "received:<4/1/26 hello",
		equivalent: "date:-4/1/26 hello",
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ftsQuery, senderName, startTime, endTime, err := parseSearchQuery(tt.query)
			if err != nil {
				t.Fatalf("parseSearchQuery(%q) failed: %v", tt.query, err)
			}
			expectedFTS, expectedSender, expectedStart, expectedEnd, err := parseSearchQuery(tt.equivalent)
			if err != nil {
				t.Fatalf("parseSearchQuery(%q) failed: %v", tt.equivalent, err)
			}
			if ftsQuery != expectedFTS {
				t.Fatalf("ftsQuery = %q, want %q", ftsQuery, expectedFTS)
			}
			if senderName != expectedSender {
				t.Fatalf("senderName = %q, want %q", senderName, expectedSender)
			}
			if startTime != expectedStart {
				t.Fatalf("startTime = %d, want %d", startTime, expectedStart)
			}
			if endTime != expectedEnd {
				t.Fatalf("endTime = %d, want %d", endTime, expectedEnd)
			}
		})
	}
}

func TestGetLocalEventContextFallsBackToEventTable(t *testing.T) {
	ctx := context.Background()
	rawDB, err := dbutil.NewWithDialect(":memory:", "sqlite3-fk-wal")
	if err != nil {
		t.Fatalf("failed to open in-memory SQLite database: %v", err)
	}
	t.Cleanup(func() {
		_ = rawDB.Close()
	})
	db := database.New(rawDB)
	if err = db.Upgrade(ctx); err != nil {
		t.Fatalf("fresh database upgrade failed: %v", err)
	}

	const roomID = "!room:example.com"
	_, err = db.Exec(ctx, `INSERT INTO room (room_id) VALUES ($1)`, roomID)
	if err != nil {
		t.Fatalf("failed to insert test room: %v", err)
	}
	for i, eventID := range []string{"$event1", "$event2", "$event3", "$event4", "$event5"} {
		_, err = db.Exec(ctx, `INSERT INTO event (room_id, event_id, sender, type, timestamp, content, unsigned)
			VALUES ($1, $2, '@alice:example.com', 'm.room.message', $3, '{"body":"test"}', '{}')`, roomID, eventID, int64(i+1))
		if err != nil {
			t.Fatalf("failed to insert test event %s: %v", eventID, err)
		}
	}

	client := &HiClient{DB: db}
	resp, err := client.getLocalEventContext(ctx, roomID, "$event3", 1)
	if err != nil {
		t.Fatalf("failed to get local event context: %v", err)
	}
	if resp.Event.ID.String() != "$event3" {
		t.Fatalf("target event = %s, want $event3", resp.Event.ID)
	}
	assertHicliEventIDs(t, resp.Before, "$event2")
	assertHicliEventIDs(t, resp.After, "$event4")
}

func assertHicliEventIDs(t *testing.T, events []*database.Event, expected ...string) {
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
