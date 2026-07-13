-- Shop translation overrides (docs: architecture/translation-editor.md).
-- Managers edit any visible string per locale; overrides win over base
-- locale files and plugin overlays.
CREATE TABLE IF NOT EXISTS translation_overrides (
    locale     TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (locale, key)
);
