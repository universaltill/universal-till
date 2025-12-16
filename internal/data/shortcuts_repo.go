package data

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type ShortcutsRepo struct {
	db *sql.DB
}

func NewShortcutsRepo(db *sql.DB) *ShortcutsRepo {
	return &ShortcutsRepo{db: db}
}

var shortcutsObs = newRepoObservability("shortcuts")

type ShortcutButton struct {
	Label    string
	Barcode  string
	ItemID   string
	ImageURL string
}

func (r *ShortcutsRepo) LoadButtons(ctx context.Context) ([]ShortcutButton, error) {
	var err error
	done := shortcutsObs.trace("load_buttons")
	defer func() { done(err) }()
	rows, err := r.db.QueryContext(ctx, `SELECT label, barcode, item_id, image_path FROM shortcut_buttons ORDER BY label`)
	if err != nil {
		return nil, shortcutsObs.wrap("load_buttons", err)
	}
	defer rows.Close()
	var out []ShortcutButton
	for rows.Next() {
		var b ShortcutButton
		var img sql.NullString
		if err := rows.Scan(&b.Label, &b.Barcode, &b.ItemID, &img); err != nil {
			err = shortcutsObs.wrap("load_buttons", err)
			return nil, err
		}
		if img.Valid {
			b.ImageURL = img.String
		}
		out = append(out, b)
	}
	err = rows.Err()
	if err != nil {
		err = shortcutsObs.wrap("load_buttons", err)
	}
	return out, err
}

func (r *ShortcutsRepo) SaveButtons(ctx context.Context, list []ShortcutButton) error {
	var err error
	done := shortcutsObs.trace("save_buttons")
	defer func() { done(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		err = shortcutsObs.wrap("save_buttons", err)
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shortcut_buttons`); err != nil {
		tx.Rollback()
		err = shortcutsObs.wrap("save_buttons", err)
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path) VALUES(?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		err = shortcutsObs.wrap("save_buttons", err)
		return err
	}
	defer stmt.Close()
	for _, b := range list {
		if _, err := stmt.ExecContext(ctx, b.Barcode, b.Label, b.ItemID, nullIfEmptyButton(b.ImageURL)); err != nil {
			tx.Rollback()
			err = shortcutsObs.wrap("save_buttons", err)
			return err
		}
	}
	err = tx.Commit()
	if err != nil {
		err = shortcutsObs.wrap("save_buttons", err)
	}
	return err
}

func (r *ShortcutsRepo) AddButton(ctx context.Context, b ShortcutButton) error {
	var err error
	done := shortcutsObs.trace("add_button")
	defer func() { done(err) }()
	b.Label = strings.TrimSpace(b.Label)
	b.Barcode = strings.TrimSpace(b.Barcode)
	b.ItemID = strings.TrimSpace(b.ItemID)
	if b.Label == "" || b.Barcode == "" || b.ItemID == "" {
		err = errors.New("label, barcode, and itemId are required")
		return err
	}
	var exists int
	if err = r.db.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ? AND is_active = 1`, b.ItemID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.New("item not found or inactive")
			return err
		}
		err = shortcutsObs.wrap("add_button", err)
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path) VALUES(?,?,?,?)
ON CONFLICT(barcode) DO UPDATE SET label=excluded.label, item_id=excluded.item_id, image_path=excluded.image_path`,
		b.Barcode, b.Label, b.ItemID, nullIfEmptyButton(b.ImageURL))
	err = shortcutsObs.wrap("add_button", err)
	return err
}

func (r *ShortcutsRepo) RemoveButton(ctx context.Context, code string) error {
	var err error
	done := shortcutsObs.trace("remove_button")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `DELETE FROM shortcut_buttons WHERE barcode=?`, strings.TrimSpace(code))
	err = shortcutsObs.wrap("remove_button", err)
	return err
}

func nullIfEmptyButton(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
