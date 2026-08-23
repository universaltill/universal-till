package data

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Customer order tracking via QR (ut-docs#527), extending #526's order-status
// backbone (order_status_repo.go). A sale's tracking_token is an unguessable
// capability handed to the customer as a QR on the self-order confirmation
// screen; anyone holding it may read the order's STATUS and nothing else —
// TrackedOrder deliberately carries no total, no basket lines, no payment
// data, because /o/{token} is an anonymous, unauthenticated surface.

// TrackedOrder is the status-only view of a sale the anonymous tracking page
// may see. Timestamps are RFC3339 TEXT, same as the sales table itself.
type TrackedOrder struct {
	ReceiptNo       string
	Status          string
	StatusUpdatedAt string
	CreatedAt       string
}

// newTrackingToken mints 16 bytes of crypto/rand as lowercase hex — the same
// convention as the sync enrolment tokens (sync_api.go enrolTokens.issue):
// never uuid (v4 leaks its version/variant bits), never math/rand.
func newTrackingToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("tracking token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// EnsureOrderTrackingToken returns the sale's tracking token, minting and
// persisting one on first call. Idempotent: a re-rendered confirmation screen
// gets the SAME token back, never a second URL for the same order. The write
// is guarded (`AND tracking_token IS NULL`) and the result re-read, so a
// concurrent first call can never overwrite an already-issued token; a
// unique-index collision with ANOTHER sale's token (vanishingly unlikely with
// 128 random bits, but cheap to handle) regenerates once and retries.
//
// An unknown receipt is an error, unlike LookupOrderByTrackingToken's soft
// not-found: checkout calls this right after committing the sale, so a
// missing row means miswired code, not customer input.
func (r *POSRepo) EnsureOrderTrackingToken(ctx context.Context, receiptNo string) (string, error) {
	read := func() (string, bool, error) {
		var tok sql.NullString
		err := r.db.QueryRowContext(ctx, `SELECT tracking_token FROM sales WHERE receipt_no = ?`, receiptNo).Scan(&tok)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("ensure order tracking token: no sale with receipt %q", receiptNo)
		}
		if err != nil {
			return "", false, fmt.Errorf("ensure order tracking token: read: %w", err)
		}
		return tok.String, tok.Valid && tok.String != "", nil
	}

	if tok, ok, err := read(); err != nil || ok {
		return tok, err
	}
	for attempt := 0; ; attempt++ {
		tok, err := newTrackingToken()
		if err != nil {
			return "", err
		}
		_, err = r.db.ExecContext(ctx,
			`UPDATE sales SET tracking_token = ? WHERE receipt_no = ? AND tracking_token IS NULL`,
			tok, receiptNo)
		if err == nil {
			break
		}
		if attempt == 0 && strings.Contains(err.Error(), "UNIQUE") {
			continue // collided with another sale's token — regenerate once
		}
		return "", fmt.Errorf("ensure order tracking token: update: %w", err)
	}
	// Re-read rather than returning our candidate: if a concurrent call won
	// the guarded UPDATE, the stored token — not ours — is the sale's token.
	tok, ok, err := read()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("ensure order tracking token: token not persisted for receipt %q", receiptNo)
	}
	return tok, nil
}

// LookupOrderByTrackingToken resolves a token to its status-only order view.
// Unknown, malformed and empty tokens all return found=false with NO error —
// on an anonymous surface a guessed token must be indistinguishable from a
// mistyped one, never a different failure shape.
func (r *POSRepo) LookupOrderByTrackingToken(ctx context.Context, token string) (TrackedOrder, bool, error) {
	if token == "" {
		return TrackedOrder{}, false, nil
	}
	var o TrackedOrder
	err := r.db.QueryRowContext(ctx, `
SELECT receipt_no, order_status, COALESCE(order_status_updated_at, ''), created_at
FROM sales WHERE tracking_token = ?
`, token).Scan(&o.ReceiptNo, &o.Status, &o.StatusUpdatedAt, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackedOrder{}, false, nil
	}
	if err != nil {
		return TrackedOrder{}, false, fmt.Errorf("lookup order by tracking token: %w", err)
	}
	return o, true, nil
}
