CREATE TABLE IF NOT EXISTS chunks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    site_key     TEXT    NOT NULL,
    url          TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    heading_path TEXT    NOT NULL,
    anchor       TEXT    NOT NULL,
    seq          INTEGER NOT NULL,
    content_hash TEXT    NOT NULL,
    text         TEXT    NOT NULL,
    identifiers  TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS chunks_site_url ON chunks(site_key, url);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    title,
    heading_path,
    text,
    identifiers,
    content='chunks',
    content_rowid='id',
    tokenize='porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts(rowid, title, heading_path, text, identifiers)
    VALUES (new.id, new.title, new.heading_path, new.text, new.identifiers);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, title, heading_path, text, identifiers)
    VALUES ('delete', old.id, old.title, old.heading_path, old.text, old.identifiers);
END;

CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
    INSERT INTO chunks_fts(chunks_fts, rowid, title, heading_path, text, identifiers)
    VALUES ('delete', old.id, old.title, old.heading_path, old.text, old.identifiers);
    INSERT INTO chunks_fts(rowid, title, heading_path, text, identifiers)
    VALUES (new.id, new.title, new.heading_path, new.text, new.identifiers);
END;
