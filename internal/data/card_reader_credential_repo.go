package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ut-docs#6538 R1a (ADR-0198 §6.2/6.3): the identity of a paired phone card
// reader. All SQL on card_reader_credentials lives here (the guard test
// TestCardReaderCredentialsOnlyInRepo fails if any other production file
// names the table). Till-local: no journal, snapshot, bundle or roster.

// ReaderBearerGrace is how long a reader's previous bearer keeps working
// after a rotation, so a lost reply does not lock the phone out.
const ReaderBearerGrace = 10 * time.Minute

// NewReaderCredential is what enrolment stores: the SHA-256 of the bearer
// (never the plaintext) and the LAN TLS pin this main served to the phone.
type NewReaderCredential struct {
	BearerHash string
	PinSPKI    string
}

// ReaderCredential is an authenticated reader: its credential joined to its
// active (not removed) registry row.
type ReaderCredential struct {
	ReaderID   string
	Name       string
	MethodKey  string
	PinSPKI    string
	NextPin    string
	LastSeenAt string
	LastIP     string
	CreatedAt  string
}

// CardReaderCredentialRepo owns card_reader_credentials.
type CardReaderCredentialRepo struct{ db *sql.DB }

// NewCardReaderCredentialRepo returns the repository.
func NewCardReaderCredentialRepo(db *sql.DB) *CardReaderCredentialRepo {
	return &CardReaderCredentialRepo{db: db}
}

var cardReaderCredObs = newRepoObservability("card_reader_credential")

// insertReaderCredentialTx writes the credential inside the caller's
// transaction (the registry row was inserted by the same one).
func insertReaderCredentialTx(ctx context.Context, tx *sql.Tx, readerID string, c NewReaderCredential) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO card_reader_credentials (reader_id, bearer_hash, pin_spki) VALUES (?, ?, ?)
ON CONFLICT(reader_id) DO UPDATE SET bearer_hash = excluded.bearer_hash, pin_spki = excluded.pin_spki,
  bearer_prev_hash = NULL, bearer_prev_expires_at = NULL, next_pin = '', revoked_at = NULL,
  last_seen_at = NULL, last_ip = '', created_at = datetime('now')`,
		readerID, c.BearerHash, c.PinSPKI)
	return err
}

// revokeReaderCredentialTx revokes a reader's credential inside the caller's
// transaction (a removed reader row must never authenticate).
func revokeReaderCredentialTx(ctx context.Context, tx *sql.Tx, readerID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE card_reader_credentials SET revoked_at = datetime('now') WHERE reader_id = ? AND revoked_at IS NULL`, readerID)
	return err
}

const readerCredTS = time.RFC3339

// ByBearerHashAt authenticates a reader by the SHA-256 of its bearer. The
// previous bearer works while its grace (ReaderBearerGrace after a rotation)
// lasts. A revoked credential or a removed reader row never matches.
func (r *CardReaderCredentialRepo) ByBearerHashAt(ctx context.Context, hash string, now time.Time) (ReaderCredential, bool, error) {
	if hash == "" {
		return ReaderCredential{}, false, nil
	}
	var c ReaderCredential
	err := r.db.QueryRowContext(ctx, `
SELECT k.reader_id, c.name, c.method_key, k.pin_spki, k.next_pin, COALESCE(k.last_seen_at,''), k.last_ip, k.created_at
FROM card_reader_credentials k JOIN card_readers c ON c.id = k.reader_id
WHERE k.revoked_at IS NULL AND c.removed_at IS NULL
  AND (k.bearer_hash = ? OR (k.bearer_prev_hash = ? AND k.bearer_prev_expires_at > ?))`,
		hash, hash, now.UTC().Format(readerCredTS)).
		Scan(&c.ReaderID, &c.Name, &c.MethodKey, &c.PinSPKI, &c.NextPin, &c.LastSeenAt, &c.LastIP, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ReaderCredential{}, false, nil
	}
	if err != nil {
		return ReaderCredential{}, false, cardReaderCredObs.wrap("by_bearer", err)
	}
	return c, true, nil
}

// Revoke ends a reader's credential at once. Idempotent.
func (r *CardReaderCredentialRepo) Revoke(ctx context.Context, readerID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return cardReaderCredObs.wrap("revoke", err)
	}
	defer tx.Rollback()
	if err := revokeReaderCredentialTx(ctx, tx, readerID); err != nil {
		return cardReaderCredObs.wrap("revoke", err)
	}
	return tx.Commit()
}

// RotateBearer swaps the reader's bearer for newHash if presentedHash is its
// CURRENT bearer (the previous one cannot rotate again). The old hash keeps
// working for ReaderBearerGrace. false means the presenter lost a race, was
// not current, or the reader is revoked.
func (r *CardReaderCredentialRepo) RotateBearer(ctx context.Context, readerID, presentedHash, newHash string, now time.Time) (bool, error) {
	if newHash == "" || newHash == presentedHash {
		return false, fmt.Errorf("rotate reader bearer: invalid new bearer")
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE card_reader_credentials
SET bearer_prev_hash = bearer_hash, bearer_prev_expires_at = ?, bearer_hash = ?
WHERE reader_id = ? AND bearer_hash = ? AND revoked_at IS NULL`,
		now.UTC().Add(ReaderBearerGrace).Format(readerCredTS), newHash, readerID, presentedHash)
	if err != nil {
		return false, cardReaderCredObs.wrap("rotate", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Touch records the reader's last contact (best effort, bounded: two columns
// of one row).
func (r *CardReaderCredentialRepo) Touch(ctx context.Context, readerID, ip string) {
	_, _ = r.db.ExecContext(ctx, `UPDATE card_reader_credentials SET last_seen_at = datetime('now'), last_ip = ? WHERE reader_id = ?`, ip, readerID)
}

// CreatePaired adds a phone reader and its credential in ONE transaction
// (ADR-0198 §6.2: approval is one step). The registry rules of Create apply
// (name 1-40 unique, 32 active cap, reader id format); only the phone kind
// may be paired, and the bearer hash must not be empty.
func (r *CardReaderRepo) CreatePaired(ctx context.Context, in CardReader, cred NewReaderCredential) (CardReader, error) {
	if in.Kind != CardReaderKindPhone {
		return CardReader{}, ErrCardReaderKind
	}
	if cred.BearerHash == "" {
		return CardReader{}, fmt.Errorf("create paired reader: empty bearer hash")
	}
	in.Source = CardReaderSourcePaired
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CardReader{}, cardReaderObs.wrap("create_paired", err)
	}
	defer tx.Rollback()
	id, err := r.createTx(ctx, tx, in)
	if err != nil {
		return CardReader{}, err
	}
	if err := insertReaderCredentialTx(ctx, tx, id, cred); err != nil {
		return CardReader{}, cardReaderObs.wrap("create_paired", err)
	}
	if err := tx.Commit(); err != nil {
		return CardReader{}, cardReaderObs.wrap("create_paired", err)
	}
	return r.Get(ctx, id)
}

// Active reports whether readerID has a credential that is not revoked.
func (r *CardReaderCredentialRepo) Active(ctx context.Context, readerID string) bool {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_reader_credentials WHERE reader_id = ? AND revoked_at IS NULL`, readerID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}
