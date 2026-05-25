// Copyright (c) 2026 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package database

import (
	"context"
	"sync/atomic"
)

const (
	dropEventFTSInsertTriggerQuery   = `DROP TRIGGER IF EXISTS event_fts_insert`
	dropEventFTSDecryptTriggerQuery  = `DROP TRIGGER IF EXISTS event_fts_decrypt`
	dropEventFTSRedactTriggerQuery   = `DROP TRIGGER IF EXISTS event_fts_redact`
	createEventFTSInsertTriggerQuery = `
		CREATE TRIGGER event_fts_insert AFTER INSERT ON event
		WHEN (index_redacted() OR NEW.redacted_by IS NULL)
		    AND json_extract(COALESCE(NEW.decrypted, NEW.content), '$.body') IS NOT NULL
		BEGIN
		    INSERT INTO event_fts(rowid, sender, body)
		    VALUES (NEW.rowid, NEW.sender, normalize_fts(json_extract(COALESCE(NEW.decrypted, NEW.content), '$.body')));
		END
	`
	createEventFTSDecryptTriggerQuery = `
		CREATE TRIGGER event_fts_decrypt AFTER UPDATE OF decrypted ON event
		WHEN NEW.decrypted IS NOT NULL AND OLD.decrypted IS NULL AND (index_redacted() OR NEW.redacted_by IS NULL)
		BEGIN
		    DELETE FROM event_fts WHERE rowid = NEW.rowid;
		    INSERT INTO event_fts(rowid, sender, body)
		    SELECT NEW.rowid, NEW.sender, normalize_fts(json_extract(NEW.decrypted, '$.body'))
		    WHERE json_extract(NEW.decrypted, '$.body') IS NOT NULL;
		END
	`
	createEventFTSRedactTriggerQuery = `
		CREATE TRIGGER event_fts_redact AFTER UPDATE OF redacted_by ON event
		WHEN NEW.redacted_by IS NOT NULL AND OLD.redacted_by IS NULL AND NOT index_redacted()
		BEGIN
		    DELETE FROM event_fts WHERE rowid = NEW.rowid;
		END
	`
	deleteRedactedFTSQuery = `
		DELETE FROM event_fts
		WHERE rowid IN (SELECT rowid FROM event WHERE redacted_by IS NOT NULL)
	`
	insertRedactedFTSQuery = `
		INSERT OR IGNORE INTO event_fts(rowid, sender, body)
		SELECT rowid, sender, normalize_fts(json_extract(COALESCE(decrypted, content), '$.body'))
		FROM event
		WHERE redacted_by IS NOT NULL
		  AND json_extract(COALESCE(decrypted, content), '$.body') IS NOT NULL
	`
)

var indexRedacted atomic.Bool

func init() {
	indexRedacted.Store(true)
}

func SetIndexRedacted(enabled bool) {
	indexRedacted.Store(enabled)
}

func IndexRedacted() bool {
	return indexRedacted.Load()
}

func (eq *EventQuery) ApplyIndexRedacted(ctx context.Context, enabled bool) error {
	SetIndexRedacted(enabled)
	return eq.GetDB().DoTxn(ctx, nil, func(ctx context.Context) error {
		err := eq.recreateFTSTriggers(ctx)
		if err != nil {
			return err
		}
		if enabled {
			return eq.Exec(ctx, insertRedactedFTSQuery)
		}
		return eq.Exec(ctx, deleteRedactedFTSQuery)
	})
}

func (eq *EventQuery) recreateFTSTriggers(ctx context.Context) error {
	for _, query := range []string{
		dropEventFTSInsertTriggerQuery,
		dropEventFTSDecryptTriggerQuery,
		dropEventFTSRedactTriggerQuery,
		createEventFTSInsertTriggerQuery,
		createEventFTSDecryptTriggerQuery,
		createEventFTSRedactTriggerQuery,
	} {
		err := eq.Exec(ctx, query)
		if err != nil {
			return err
		}
	}
	return nil
}
