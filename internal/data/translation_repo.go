package data

import (
	"context"
	"database/sql"
)

// TranslationRepo persists shop translation overrides (docs repo:
// architecture/translation-editor.md). Overrides sit above base locale files
// and plugin overlays in the translator's lookup order.
type TranslationRepo struct {
	db *sql.DB
}

func NewTranslationRepo(db *sql.DB) *TranslationRepo {
	return &TranslationRepo{db: db}
}

// ListOverrides returns every override as locale -> key -> value, the shape
// config.I18n.SetShopOverrides consumes.
func (r *TranslationRepo) ListOverrides(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT locale, key, value FROM translation_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var locale, key, value string
		if err := rows.Scan(&locale, &key, &value); err != nil {
			return nil, err
		}
		if out[locale] == nil {
			out[locale] = map[string]string{}
		}
		out[locale][key] = value
	}
	return out, rows.Err()
}

// SetOverride creates or replaces one override.
func (r *TranslationRepo) SetOverride(ctx context.Context, locale, key, value, actorID, now string) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO translation_overrides (locale, key, value, updated_at, updated_by)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(locale, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by
`, locale, key, value, now, actorID)
	return err
}

// ClearOverride removes one override (the string falls back to its base or
// plugin value). Returns whether a row existed.
func (r *TranslationRepo) ClearOverride(ctx context.Context, locale, key string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM translation_overrides WHERE locale = ? AND key = ?`, locale, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
