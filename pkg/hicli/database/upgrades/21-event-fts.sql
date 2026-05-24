-- v21 (compatible with v10+): Add FTS4 index for full-text message search
CREATE VIRTUAL TABLE event_fts USING fts4(body);

INSERT INTO event_fts(rowid, body)
SELECT rowid, json_extract(COALESCE(decrypted, content), '$.body')
FROM event
WHERE type IN ('m.room.message', 'm.sticker')
  AND json_extract(COALESCE(decrypted, content), '$.body') IS NOT NULL;

CREATE TRIGGER event_fts_insert AFTER INSERT ON event
WHEN NEW.type IN ('m.room.message', 'm.sticker')
BEGIN
    INSERT INTO event_fts(rowid, body)
    SELECT NEW.rowid, json_extract(COALESCE(NEW.decrypted, NEW.content), '$.body')
    WHERE json_extract(COALESCE(NEW.decrypted, NEW.content), '$.body') IS NOT NULL;
END;

CREATE TRIGGER event_fts_decrypt AFTER UPDATE OF decrypted ON event
WHEN NEW.decrypted IS NOT NULL AND OLD.decrypted IS NULL
BEGIN
    DELETE FROM event_fts WHERE rowid = NEW.rowid;
    INSERT INTO event_fts(rowid, body)
    SELECT NEW.rowid, json_extract(NEW.decrypted, '$.body')
    WHERE json_extract(NEW.decrypted, '$.body') IS NOT NULL;
END;

CREATE TRIGGER event_fts_redact AFTER UPDATE OF redacted_by ON event
WHEN NEW.redacted_by IS NOT NULL AND OLD.redacted_by IS NULL
BEGIN
    DELETE FROM event_fts WHERE rowid = NEW.rowid;
END;
