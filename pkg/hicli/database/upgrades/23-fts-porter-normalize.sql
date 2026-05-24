-- v23 (compatible with v22+): Rebuild FTS index with porter stemming and diacritic normalization
DROP TRIGGER IF EXISTS event_fts_insert;
DROP TRIGGER IF EXISTS event_fts_decrypt;
DROP TRIGGER IF EXISTS event_fts_redact;
DROP TABLE IF EXISTS event_fts;

CREATE VIRTUAL TABLE event_fts USING fts4(body, tokenize=porter);

INSERT INTO event_fts(rowid, body)
SELECT rowid, normalize_fts(json_extract(COALESCE(decrypted, content), '$.body'))
FROM event
WHERE redacted_by IS NULL
  AND json_extract(COALESCE(decrypted, content), '$.body') IS NOT NULL;

CREATE TRIGGER event_fts_insert AFTER INSERT ON event
WHEN NEW.redacted_by IS NULL
    AND json_extract(COALESCE(NEW.decrypted, NEW.content), '$.body') IS NOT NULL
BEGIN
    INSERT INTO event_fts(rowid, body)
    VALUES (NEW.rowid, normalize_fts(json_extract(COALESCE(NEW.decrypted, NEW.content), '$.body')));
END;

CREATE TRIGGER event_fts_decrypt AFTER UPDATE OF decrypted ON event
WHEN NEW.decrypted IS NOT NULL AND OLD.decrypted IS NULL AND NEW.redacted_by IS NULL
BEGIN
    DELETE FROM event_fts WHERE rowid = NEW.rowid;
    INSERT INTO event_fts(rowid, body)
    SELECT NEW.rowid, normalize_fts(json_extract(NEW.decrypted, '$.body'))
    WHERE json_extract(NEW.decrypted, '$.body') IS NOT NULL;
END;

CREATE TRIGGER event_fts_redact AFTER UPDATE OF redacted_by ON event
WHEN NEW.redacted_by IS NOT NULL AND OLD.redacted_by IS NULL
BEGIN
    DELETE FROM event_fts WHERE rowid = NEW.rowid;
END;
