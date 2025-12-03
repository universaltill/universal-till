package data

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// POSRepo centralizes DB access for POS handlers.
type POSRepo struct {
	db *sql.DB
}

func NewPOSRepo(db *sql.DB) *POSRepo {
	return &POSRepo{db: db}
}

// LookupCustomer resolves a customer by id/loyalty/phone.
func (r *POSRepo) LookupCustomer(ctx context.Context, code string) (string, string, bool) {
	c := strings.TrimSpace(code)
	if c == "" {
		return "", "", false
	}
	row := r.db.QueryRowContext(ctx, `
SELECT id, name FROM customers
WHERE id = ? OR loyalty_no = ? OR phone = ?
LIMIT 1
`, c, c, c)
	var id, name string
	if err := row.Scan(&id, &name); err != nil {
		return "", "", false
	}
	return id, name, true
}

// FindActivePromo returns promo type/value if active and optionally targeted to the given customer.
func (r *POSRepo) FindActivePromo(ctx context.Context, customerID string, code string) (string, int64, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT type, value
FROM promotions
WHERE code = ?
  AND is_active = 1
  AND (customer_id IS NULL OR customer_id = ?)
  AND (starts_at IS NULL OR datetime(starts_at) <= CURRENT_TIMESTAMP)
  AND (ends_at IS NULL OR datetime(ends_at) >= CURRENT_TIMESTAMP)
LIMIT 1
`, strings.TrimSpace(code), nullIfEmpty(customerID))
	var pType string
	var value int64
	if err := row.Scan(&pType, &value); err != nil {
		return "", 0, false
	}
	if value <= 0 {
		return "", 0, false
	}
	if pType == "" {
		pType = "amount"
	}
	return pType, value, true
}

// EnsureStockLocation returns an existing location id or creates a default one.
func (r *POSRepo) EnsureStockLocation(ctx context.Context) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM stock_locations WHERE name = 'Main' OR id = 'loc_main' ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = "loc_main"
	if _, err := r.db.ExecContext(ctx, `INSERT INTO stock_locations(id, name) VALUES(?,?)`, id, "Main"); err != nil {
		return "", err
	}
	return id, nil
}

// EnsurePaymentMethod upserts a minimal payment method to satisfy FK.
func (r *POSRepo) EnsurePaymentMethod(ctx context.Context, id string) error {
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM payment_methods WHERE id = ? AND is_active = 1`, id).Scan(&exists); err == nil {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES (?, ?, 'cash', 1)`, id, id)
	return err
}

// EnsureRegister returns an existing register or creates a default one.
func (r *POSRepo) EnsureRegister(ctx context.Context) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM registers WHERE is_active = 1 ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = "reg-default"
	if _, err := r.db.ExecContext(ctx, `INSERT INTO registers (id, name, is_active) VALUES (?, ?, 1)`, id, "Default Register"); err != nil {
		return "", err
	}
	return id, nil
}

// EnsureUser returns a default cashier user if none exists.
func (r *POSRepo) EnsureUser(ctx context.Context) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM users WHERE is_active = 1 ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = "cashier-default"
	if _, err := r.db.ExecContext(ctx, `INSERT INTO users (id, username, display_name, role, is_active) VALUES (?, ?, ?, 'cashier', 1)`, id, "cashier", "Default Cashier"); err != nil {
		return "", err
	}
	return id, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
