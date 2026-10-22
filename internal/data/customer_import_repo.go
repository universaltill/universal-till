package data

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/phonenumber"
)

// CustomerImportEntity is the import_source_links.entity of a customer link.
const CustomerImportEntity = "customer"

// CustomerFields are the customer columns an import may write (ADR-0168 §5).
// All are personal data: nothing here is ever logged or audited by value.
type CustomerFields struct {
	Name, Email, Phone, Address, Postcode string
}

// CustomerImportFieldNames is the fixed order the import reports fields in.
var CustomerImportFieldNames = []string{"name", "email", "phone", "address", "postcode"}

// Get returns the field by its import name ("" for an unknown name).
func (f CustomerFields) Get(name string) string {
	switch name {
	case "name":
		return f.Name
	case "email":
		return f.Email
	case "phone":
		return f.Phone
	case "address":
		return f.Address
	case "postcode":
		return f.Postcode
	}
	return ""
}

// CustomerLink is one (source, customer, external_id) link row.
type CustomerLink struct {
	LocalID  string
	Baseline string
	Erased   bool
}

// CustomerImportRepo is the customer write side of the smart import
// (ADR-0168 §5, ut-docs#4748): create and update, the folded lookups the
// match order needs, the source links with their hashed baseline, and the
// erasure suppression list. Stateless over a *sql.DB; every method names its
// executor so a whole chunk runs in one transaction.
type CustomerImportRepo struct{}

// NewCustomerImportRepo returns the repository.
func NewCustomerImportRepo() *CustomerImportRepo { return &CustomerImportRepo{} }

// FoldEmail is the match key of an email: trimmed, ASCII lower-cased (the
// same fold SQLite's lower() applies, so the lower(email) index is usable).
func FoldEmail(s string) string { return asciiLower(strings.TrimSpace(s)) }

// FoldPostcode is the match key of a postcode: spaces removed, ASCII
// upper-cased (the expression idx_customers_postcode_fold indexes).
func FoldPostcode(s string) string {
	return asciiUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
}

// FoldText is the comparison key of a name or an address line: the search
// fold with runs of whitespace collapsed.
func FoldText(s string) string { return strings.Join(strings.Fields(SearchFold(s)), " ") }

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// Region is the shop's country, the default region for phone numbers, read
// through q so a transaction never waits on a second connection.
func (*CustomerImportRepo) Region(ctx context.Context, q SyncQuerier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, StoreCountrySettingsKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("customer import region: %w", err)
	}
	return strings.ToUpper(strings.TrimSpace(v)), nil
}

// PhoneKey is what customers.phone_e164 holds for phone: E.164 when it
// normalises with region, else its digits ("" when there are none).
func PhoneKey(phone, region string) string { return phoneE164Value(phone, region) }

// Salt returns this till's import salt, creating it on first use. It never
// leaves the till.
func (r *CustomerImportRepo) Salt(ctx context.Context, q SyncQuerier) ([]byte, error) {
	var salt []byte
	err := q.QueryRowContext(ctx, `SELECT salt FROM customer_import_salt WHERE id = 1`).Scan(&salt)
	if err == nil {
		return salt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("customer import salt: %w", err)
	}
	salt = make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("customer import salt: %w", err)
	}
	if _, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO customer_import_salt (id, salt) VALUES (1, ?)`, salt); err != nil {
		return nil, fmt.Errorf("customer import salt: %w", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT salt FROM customer_import_salt WHERE id = 1`).Scan(&salt); err != nil {
		return nil, fmt.Errorf("customer import salt: %w", err)
	}
	return salt, nil
}

// Hash is the salted, keyed hash used for baselines and the erasure list:
// hex HMAC-SHA256 of kind + ":" + value.
func Hash(salt []byte, kind, value string) string {
	m := hmac.New(sha256.New, salt)
	m.Write([]byte(kind + ":" + value))
	return hex.EncodeToString(m.Sum(nil))
}

// idsQuery runs a query returning customer ids (at most 3: callers only need
// to tell none, one and several apart).
func idsQuery(ctx context.Context, q SyncQuerier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("customer lookup: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("customer lookup: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FindByEmail returns the live customers whose email folds to email
// (erased shells, with an empty name, never match).
func (*CustomerImportRepo) FindByEmail(ctx context.Context, q SyncQuerier, email string) ([]string, error) {
	key := FoldEmail(email)
	if key == "" {
		return nil, nil
	}
	return idsQuery(ctx, q, `SELECT id FROM customers WHERE lower(email) = ? AND name <> '' ORDER BY id LIMIT 3`, key)
}

// PendingPhoneIndex maps the PhoneKey of every customer whose phone_e164 the
// back-fill has not computed yet to their ids. It parses each such row, so a
// chunk builds it once.
func (*CustomerImportRepo) PendingPhoneIndex(ctx context.Context, q SyncQuerier, region string) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, phone FROM customers WHERE phone_e164 IS NULL AND phone IS NOT NULL AND phone <> '' AND name <> ''`)
	if err != nil {
		return nil, fmt.Errorf("customer lookup: %w", err)
	}
	defer rows.Close()
	idx := map[string][]string{}
	for rows.Next() {
		var id, phone string
		if err := rows.Scan(&id, &phone); err != nil {
			return nil, fmt.Errorf("customer lookup: %w", err)
		}
		if k := phoneE164Value(phone, region); k != "" {
			idx[k] = append(idx[k], id)
		}
	}
	return idx, rows.Err()
}

// FindByPhone returns the live customers whose phone has the given PhoneKey:
// the indexed phone_e164 column, plus the rows the back-fill has not reached
// (pending, from PendingPhoneIndex).
func (r *CustomerImportRepo) FindByPhone(ctx context.Context, q SyncQuerier, key string, pending map[string][]string) ([]string, error) {
	if key == "" {
		return nil, nil
	}
	ids, err := idsQuery(ctx, q, `SELECT id FROM customers WHERE phone_e164 = ? AND name <> '' ORDER BY id LIMIT 3`, key)
	if err != nil {
		return nil, err
	}
	return append(ids, pending[key]...), nil
}

// PhoneKeyForms are the keys a phone is remembered and recognised by on the
// erasure list: its PhoneKey, and its bare digits when that differs (the
// shop's country may be set between an erasure and an import).
func PhoneKeyForms(phone, phoneKey string) []string {
	var out []string
	if phoneKey != "" {
		out = append(out, phoneKey)
	}
	if d := phonenumber.Digits(phone); d != "" && d != phoneKey {
		out = append(out, d)
	}
	return out
}

// FindByNamePostcode returns the live customers with this name and postcode,
// both folded.
func (*CustomerImportRepo) FindByNamePostcode(ctx context.Context, q SyncQuerier, name, postcode string) ([]string, error) {
	pc := FoldPostcode(postcode)
	if pc == "" || strings.TrimSpace(name) == "" {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM customers WHERE replace(upper(postcode), ' ', '') = ? AND name <> ''`, pc)
	if err != nil {
		return nil, fmt.Errorf("customer lookup: %w", err)
	}
	defer rows.Close()
	want := FoldText(name)
	var ids []string
	for rows.Next() {
		var id, n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("customer lookup: %w", err)
		}
		if FoldText(n) == want {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// Get reads a customer's importable fields; false when the row is gone or is
// an erased shell.
func (*CustomerImportRepo) Get(ctx context.Context, q SyncQuerier, id string) (CustomerFields, bool, error) {
	var f CustomerFields
	var email, phone, address, postcode sql.NullString
	err := q.QueryRowContext(ctx, `SELECT name, email, phone, address, postcode FROM customers WHERE id = ? AND name <> ''`, id).
		Scan(&f.Name, &email, &phone, &address, &postcode)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, fmt.Errorf("customer read: %w", err)
	}
	f.Email, f.Phone, f.Address, f.Postcode = email.String, phone.String, address.String, postcode.String
	return f, true, nil
}

// Create inserts a customer and returns its id. phone_e164 is set with the
// row so a lookup never waits for the back-fill.
func (r *CustomerImportRepo) Create(ctx context.Context, q SyncQuerier, f CustomerFields, region string) (string, error) {
	if strings.TrimSpace(f.Name) == "" {
		return "", errors.New("customer name required")
	}
	id := uuid.NewString()
	_, err := q.ExecContext(ctx, `
INSERT INTO customers (id, name, phone, phone_e164, email, address, postcode)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, f.Name, nullIfEmpty(f.Phone), phoneE164Value(f.Phone, region), nullIfEmpty(f.Email),
		nullIfEmpty(f.Address), nullIfEmpty(f.Postcode))
	if err != nil {
		return "", fmt.Errorf("customer create: %w", err)
	}
	return id, nil
}

// SetFields writes the named fields (keys of CustomerImportFieldNames) of a
// customer. A phone write sets phone_e164 with it.
func (r *CustomerImportRepo) SetFields(ctx context.Context, q SyncQuerier, id string, set map[string]string, region string) error {
	for _, name := range CustomerImportFieldNames {
		v, ok := set[name]
		if !ok {
			continue
		}
		var query string
		args := []any{nullIfEmpty(v), id}
		switch name {
		case "name":
			if strings.TrimSpace(v) == "" {
				return errors.New("customer name required")
			}
			query, args = `UPDATE customers SET name = ? WHERE id = ?`, []any{v, id}
		case "email":
			query = `UPDATE customers SET email = ? WHERE id = ?`
		case "phone":
			query, args = `UPDATE customers SET phone = ?, phone_e164 = ? WHERE id = ?`, []any{nullIfEmpty(v), phoneE164Value(v, region), id}
		case "address":
			query = `UPDATE customers SET address = ? WHERE id = ?`
		case "postcode":
			query = `UPDATE customers SET postcode = ? WHERE id = ?`
		default:
			return fmt.Errorf("customer field %q is not importable", name)
		}
		if _, err := q.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("customer update: %w", err)
		}
	}
	return nil
}

// LinkGet reads a customer link.
func (*CustomerImportRepo) LinkGet(ctx context.Context, q SyncQuerier, source, externalID string) (CustomerLink, bool, error) {
	var l CustomerLink
	var baseline, erased sql.NullString
	err := q.QueryRowContext(ctx, `
SELECT local_id, baseline_hash, erased_at FROM import_source_links
WHERE source = ? AND entity = ? AND external_id = ?`, source, CustomerImportEntity, externalID).Scan(&l.LocalID, &baseline, &erased)
	if errors.Is(err, sql.ErrNoRows) {
		return l, false, nil
	}
	if err != nil {
		return l, false, fmt.Errorf("customer link: %w", err)
	}
	l.Baseline, l.Erased = baseline.String, erased.Valid
	return l, true, nil
}

// LinkPut records (or re-points) a live link with the baseline of the fields
// last imported.
func (*CustomerImportRepo) LinkPut(ctx context.Context, q SyncQuerier, source, externalID, localID, baseline string) error {
	if source == "" || externalID == "" || localID == "" {
		return errors.New("customer link: every field is required")
	}
	_, err := q.ExecContext(ctx, `
INSERT INTO import_source_links (source, entity, external_id, local_id, updated_at, baseline_hash, erased_at)
VALUES (?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(source, entity, external_id) DO UPDATE SET
  local_id = excluded.local_id, updated_at = excluded.updated_at,
  baseline_hash = excluded.baseline_hash, erased_at = NULL`,
		source, CustomerImportEntity, externalID, localID, time.Now().UTC().Format(time.RFC3339), baseline)
	if err != nil {
		return fmt.Errorf("customer link put: %w", err)
	}
	return nil
}

// LinkTombstone turns a link into an erasure tombstone: the pointer to the
// customer is blanked, the link stays. Creates the tombstone when the link
// did not exist (a link to a customer the sync pruned).
func (*CustomerImportRepo) LinkTombstone(ctx context.Context, q SyncQuerier, source, externalID string) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO import_source_links (source, entity, external_id, local_id, updated_at, baseline_hash, erased_at)
VALUES (?, ?, ?, '', ?, NULL, ?)
ON CONFLICT(source, entity, external_id) DO UPDATE SET
  local_id = '', baseline_hash = NULL, erased_at = excluded.erased_at, updated_at = excluded.updated_at`,
		source, CustomerImportEntity, externalID, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("customer link tombstone: %w", err)
	}
	return nil
}

// Suppressed reports whether the salted hash of an erased customer's email
// or phone is on the suppression list.
func (*CustomerImportRepo) Suppressed(ctx context.Context, q SyncQuerier, hash string) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM customer_erasure_hashes WHERE hash = ?`, hash).Scan(&n); err != nil {
		return false, fmt.Errorf("customer suppression: %w", err)
	}
	return n > 0, nil
}

// Suppress adds a hash to the list.
func (*CustomerImportRepo) Suppress(ctx context.Context, q SyncQuerier, kind, hash string) error {
	_, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO customer_erasure_hashes (hash, kind, erased_at) VALUES (?, ?, ?)`,
		hash, kind, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("customer suppression: %w", err)
	}
	return nil
}

// recordCustomerErasure runs inside EraseCustomer's transaction, before the
// row is deleted: the customer's email and phone go to the suppression list
// as salted hashes, and every import link to them becomes a tombstone, so a
// later import skips this person instead of recreating them (ADR-0168 §5).
func recordCustomerErasure(ctx context.Context, r *CustomerImportRepo, tx *sql.Tx, id string) error {
	var email, phone sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT email, phone FROM customers WHERE id = ?`, id).Scan(&email, &phone)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("erase customer: read contact: %w", err)
	}
	salt, err := r.Salt(ctx, tx)
	if err != nil {
		return err
	}
	if k := FoldEmail(email.String); k != "" {
		if err := r.Suppress(ctx, tx, "email", Hash(salt, "email", k)); err != nil {
			return err
		}
	}
	if phone.String != "" {
		region, err := r.Region(ctx, tx)
		if err != nil {
			return err
		}
		for _, k := range PhoneKeyForms(phone.String, phoneE164Value(phone.String, region)) {
			if err := r.Suppress(ctx, tx, "phone", Hash(salt, "phone", k)); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
UPDATE import_source_links SET local_id = '', baseline_hash = NULL, erased_at = ?, updated_at = ?
WHERE entity = ? AND local_id = ?`, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), CustomerImportEntity, id)
	if err != nil {
		return fmt.Errorf("erase customer: tombstone links: %w", err)
	}
	return nil
}
