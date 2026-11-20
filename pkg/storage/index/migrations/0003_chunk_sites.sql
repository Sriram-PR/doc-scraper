CREATE TABLE IF NOT EXISTS chunk_sites (
    site_key TEXT    PRIMARY KEY,
    format   INTEGER NOT NULL
);
INSERT OR IGNORE INTO chunk_sites (site_key, format)
SELECT DISTINCT site_key, 1 FROM chunks;
