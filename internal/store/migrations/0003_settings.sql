-- Store-held settings (design 4.7): JSON values by key. Branding is the
-- first one; the setup wizard (Milestone 2d) adds the rest.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      BLOB NOT NULL,
    updated_at TEXT NOT NULL
);
