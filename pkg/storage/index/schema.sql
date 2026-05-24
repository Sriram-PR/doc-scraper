CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS crawls (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    site_key         TEXT    NOT NULL,
    crawl_started_at TEXT    NOT NULL,
    crawl_ended_at   TEXT    NOT NULL,
    total_pages      INTEGER NOT NULL,
    mode             TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS crawls_site_ended ON crawls(site_key, crawl_ended_at DESC);

CREATE TABLE IF NOT EXISTS page_history (
    crawl_id     INTEGER NOT NULL,
    url          TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    content_hash TEXT    NOT NULL,
    depth        INTEGER NOT NULL,
    PRIMARY KEY (crawl_id, url),
    FOREIGN KEY (crawl_id) REFERENCES crawls(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS page_history_url ON page_history(url);
