-- v22 (compatible with v21+): Fix FTS index to include already-decrypted encrypted messages
INSERT OR IGNORE INTO event_fts(rowid, body)
SELECT rowid, json_extract(decrypted, '$.body')
FROM event
WHERE type = 'm.room.encrypted'
  AND decrypted IS NOT NULL
  AND json_extract(decrypted, '$.body') IS NOT NULL;

DROP TRIGGER event_fts_insert;
CREATE TRIGGER event_fts_insert AFTER INSERT ON event
WHEN json_extract(COALESCE(NEW.decrypted, NEW.content), '$.body') IS NOT NULL
BEGIN
    INSERT INTO event_fts(rowid, body)
    VALUES (NEW.rowid, json_extract(COALESCE(NEW.decrypted, NEW.content), '$.body'));
END;
