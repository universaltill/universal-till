package ui

import (
	"database/sql"
	"errors"
	"strings"

	_ "modernc.org/sqlite"
)

type SQLiteButtonStore struct { db *sql.DB }

func NewSQLiteButtonStore(path string) (*SQLiteButtonStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil { return nil, err }
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS buttons(
	  code TEXT PRIMARY KEY,
	  label TEXT NOT NULL,
	  price_cents INTEGER NOT NULL,
	  image_url TEXT
	);`); err != nil { return nil, err }
	return &SQLiteButtonStore{db: db}, nil
}

func (s *SQLiteButtonStore) Load() ([]Button, error) {
	rows, err := s.db.Query(`SELECT label, code, price_cents, image_url FROM buttons ORDER BY label`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Button
	for rows.Next() {
		var b Button
		var img sql.NullString
		if err := rows.Scan(&b.Label, &b.Code, &b.PriceCents, &img); err != nil { return nil, err }
		if img.Valid { b.ImageURL = img.String }
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *SQLiteButtonStore) Save(list []Button) error {
	// naive full replace
	tx, err := s.db.Begin()
	if err != nil { return err }
	if _, err := tx.Exec(`DELETE FROM buttons`); err != nil { tx.Rollback(); return err }
	stmt, err := tx.Prepare(`INSERT INTO buttons(code,label,price_cents,image_url) VALUES(?,?,?,?)`)
	if err != nil { tx.Rollback(); return err }
	defer stmt.Close()
	for _, b := range list {
		if _, err := stmt.Exec(b.Code, b.Label, b.PriceCents, nullIfEmpty(b.ImageURL)); err != nil { tx.Rollback(); return err }
	}
	return tx.Commit()
}

func (s *SQLiteButtonStore) Add(btn Button) error {
	btn.Label = strings.TrimSpace(btn.Label)
	btn.Code = strings.TrimSpace(btn.Code)
	if btn.Label == "" || btn.Code == "" { return errors.New("label and code are required") }
	_, err := s.db.Exec(`INSERT INTO buttons(code,label,price_cents,image_url) VALUES(?,?,?,?)
	ON CONFLICT(code) DO UPDATE SET label=excluded.label, price_cents=excluded.price_cents, image_url=excluded.image_url`,
		btn.Code, btn.Label, btn.PriceCents, nullIfEmpty(btn.ImageURL))
	return err
}

func (s *SQLiteButtonStore) Remove(code string) error {
	_, err := s.db.Exec(`DELETE FROM buttons WHERE code=?`, strings.TrimSpace(code))
	return err
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" { return nil }
	return s
}

