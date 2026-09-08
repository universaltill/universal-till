package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till voucher lookup + redemption reservation, replica side.
//
// ut-docs#1668: a voucher issued at one till was, before that card, only
// ever queryable/redeemable at a till whose OWN local vouchers row already
// knew about it — GetVoucherBalance and DebitVoucherForRedemption both read
// purely local SQLite. On a REPLICA (sync.primary_url set), voucher_api.go's
// balance query tries fetchVoucherFromPrimary first, and pos_api.go's
// completeTender runs voucherRedeemWriteThrough for every tracked voucher
// payment. On ANY failure reaching the primary (not a replica, network
// error, timeout, 404, malformed body) both fall back — silently — to the
// existing local-only path, same fallback shape as fetchOrdersFromPrimary/
// claimTableWriteThrough. This till NOT being a replica, or being the
// primary itself, takes the exact same fallback branch — no change in
// behaviour for a single-till shop or the primary's own tender flow, and
// checkout is never blocked by the network (offline-first, ADR-0003).
//
// ADR-0084 (ut-docs#1716): the redemption side is now a real RESERVATION on
// the primary, not a read-only validation. voucherRedeemWriteThrough POSTs
// /api/sync/vouchers/{id}/redeem, which runs DebitVoucherForRedemption's
// predicate-guarded UPDATE on the PRIMARY's database under this sale's id —
// so two tills racing for one balance serialize there and the second loses
// cleanly. History matters here and is still true: #1668's FIRST draft did
// exactly this write-through and was reverted (round-2 review, 2026-09-07)
// because it double-debited every online redemption — the replica's own
// completed sale journaled the SAME debit up again via the unconditional
// sales-journal sync, which had no way to know the write-through already
// applied it (applyJournal forces the debit through via
// AllowVoucherOverdraft) — and orphaned a committed primary debit whenever a
// later payment in the same sale was refused. #1668 therefore shipped
// read-only, closing the "can't redeem at all" case but leaving the
// fully-simultaneous two-till race open. What changed is not the
// write-through but the two things it lacked (see the ADR for the full
// argument rather than re-deriving it here): (1) an idempotency key — the
// reservation is recorded on the primary as a 'redemption'
// voucher_transactions row for (voucher_id, sale_id), the SAME sale id this
// till's local sale and its journal will carry (completeTender mints it
// before reserving), and pos.CompleteSale skips its own debit when that row
// already exists, so the journal replay recognizes the reservation; (2) a
// release — completeTender unwinds every reservation it made
// (releaseReservedVouchers → POST .../release) when a later payment is
// refused or its local pos.CompleteSale fails, synchronously, before any
// local sale row exists, so there is never a journal entry for a released
// attempt. A crash between a successful reservation and the local sale
// committing is the accepted residual risk (ADR-0084 Decision 4) — a failed
// release is logged as a Problem for that reason, never swallowed.

// voucherProxyClient budget: this sits on the checkout/tender submit path
// (voucherRedeemWriteThrough) and the balance-lookup API (fetchVoucherFromPrimary)
// — both user-initiated taps, not a per-keystroke or per-render hot path
// like tables_sync_proxy.go's basket-picker span, but still something a
// cashier is standing at the till watching. 2s: longer than
// tablesProxyClient's 800ms (that one fires on every basket edit), shorter
// than orderProxyClient's 3s (a background 15s poll, not a blocking tap) —
// a blackholed primary must not make a voucher tender feel frozen for long,
// but a slow-not-dead LAN link should still get its answer.
var voucherProxyClient = &http.Client{Timeout: 2 * time.Second}

// voucherFromSyncRow converts the wire form back to data.Voucher.
func voucherFromSyncRow(row syncVoucherRow) data.Voucher {
	return data.Voucher{
		ID:                  row.ID,
		HolderLabel:         row.HolderLabel,
		OriginalAmountMinor: row.OriginalAmount,
		BalanceMinor:        row.Balance,
		Currency:            row.Currency,
		VoucherType:         row.VoucherType,
		Status:              row.Status,
		IssuedSaleID:        row.IssuedSaleID,
		CreatedAt:           row.CreatedAt,
	}
}

// fetchVoucherFromPrimary tries GET /api/sync/vouchers/{id} on the primary.
// ok=false on ANY failure — not a replica, network error, timeout, non-200
// (404 included: "primary doesn't have it either" collapses into the same
// "no answer" fallback as unreachable, since the caller's own local 404 is
// already the correct final answer in that case), malformed body — and the
// caller falls through to its own local read. Read-only, no side effect:
// voucher_api.go's plain balance lookup must never reserve anything.
func fetchVoucherFromPrimary(ctx context.Context, d *common.Deps, client *http.Client, id string) (data.Voucher, bool) {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return data.Voucher{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/sync/vouchers/"+url.PathEscape(id), nil)
	if err != nil {
		return data.Voucher{}, false
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("voucher proxy: primary unreachable on lookup %s (%v) — using local", id, err)
		return data.Voucher{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("voucher proxy: primary answered %s on lookup %s — using local", resp.Status, id)
		return data.Voucher{}, false
	}
	var out struct {
		Data *syncVoucherRow `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data == nil {
		logging.L().Debugf("voucher proxy: malformed primary response on lookup %s — using local", id)
		return data.Voucher{}, false
	}
	return voucherFromSyncRow(*out.Data), true
}

// postVoucherActionOnPrimary is the one HTTP shape both reserve and release
// share: POST base/api/sync/vouchers/{id}/{action} with a JSON body, bearer
// from replicaSyncTarget. ok=false with a nil resp when this till is not a
// replica (nothing to talk to); otherwise the caller owns resp.Body.
func postVoucherActionOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, voucherID, action string, body any) (*http.Response, bool, error) {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return nil, false, nil
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, true, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sync/vouchers/"+url.PathEscape(voucherID)+"/"+action, bytes.NewReader(payload))
	if err != nil {
		return nil, true, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, true, err
	}
	return resp, true, nil
}

// reserveVoucherOnPrimary POSTs /api/sync/vouchers/{id}/redeem with
// {sale_id, amount_minor} (ADR-0084 Decision 1).
//
//   - 200: reserved=true, v = the PRE-debit snapshot the primary answered
//     with (brief addendum, correction 5) — the caller seeds its local
//     mirror from it before its own forced local debit.
//   - 409 with a known reason (sync_vouchers.go's stable strings): a
//     DEFINITIVE refusal, returned as the matching data sentinel
//     (ErrVoucherNotActive / ErrVoucherInsufficientBalance); the caller must
//     abort the tender with it, never fall back to a local attempt against a
//     voucher this till may not even have a row for. Nothing was debited.
//   - EVERYTHING else — not a replica, unreachable, timeout, 404, any other
//     status, an unrecognised 409 reason, malformed body — reserved=false,
//     err=nil: the same total fallback shape as fetchVoucherFromPrimary, and
//     the caller proceeds exactly as it did before #1668 (local-only
//     validation; offline-first unchanged). 404 in particular is NOT a
//     refusal (brief addendum, correction 4): a voucher issued at THIS till
//     and not yet journaled to the primary 404s on /redeem, and hard-failing
//     that would make a till unable to redeem its own freshly-issued voucher
//     the moment it comes online — the local row is the correct answer.
func reserveVoucherOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, voucherID, saleID string, amountMinor int64) (v data.Voucher, reserved bool, err error) {
	resp, isReplica, err := postVoucherActionOnPrimary(ctx, d, client, voucherID, "redeem", voucherRedeemRequest{SaleID: saleID, AmountMinor: amountMinor})
	if !isReplica {
		return data.Voucher{}, false, nil
	}
	if err != nil {
		logging.L().Debugf("voucher proxy: primary unreachable on reserve %s for sale %s (%v) — using local", voucherID, saleID, err)
		return data.Voucher{}, false, nil
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Data *syncVoucherRow `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data == nil {
			// The primary may well have committed the reservation and only
			// the body was lost; a retry would be idempotent on the same
			// sale id, but this path has no retry — it falls back to local
			// and the reservation, if any, is released with the rest on
			// any later failure. Logged at Debug like every other fallback.
			logging.L().Debugf("voucher proxy: malformed primary response on reserve %s for sale %s — using local", voucherID, saleID)
			return data.Voucher{}, false, nil
		}
		return voucherFromSyncRow(*out.Data), true, nil
	case http.StatusConflict:
		var out struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		switch out.Error {
		case syncVoucherErrNotActive:
			return data.Voucher{}, false, fmt.Errorf("voucher %q refused by primary: %w", voucherID, data.ErrVoucherNotActive)
		case syncVoucherErrInsufficientBalance:
			return data.Voucher{}, false, fmt.Errorf("voucher %q refused by primary: %w", voucherID, data.ErrVoucherInsufficientBalance)
		}
		logging.L().Debugf("voucher proxy: primary refused reserve %s for sale %s with unrecognised reason %q — using local", voucherID, saleID, out.Error)
		return data.Voucher{}, false, nil
	default:
		logging.L().Debugf("voucher proxy: primary answered %s on reserve %s for sale %s — using local", resp.Status, voucherID, saleID)
		return data.Voucher{}, false, nil
	}
}

// releaseVoucherOnPrimary POSTs /api/sync/vouchers/{id}/release with
// {sale_id} (ADR-0084 Decision 3) — best-effort: it never returns an error
// the caller must handle, same "never unwinds further" shape as
// voucherRedeemWriteThrough's own mirror-failure handling. Any failure is
// logged at Warn (so it lands on the Problems panel, same plumbing as
// warnIfVoucherOverdrawnReason), because a reservation that could not be
// released is exactly the accepted residual-risk shape ADR-0084 Decision 4
// names: a primary-side debit with no sale behind it, which needs a human
// to notice. The release itself is idempotent on the primary (no matching
// reservation is a 200 no-op), so it is safe to fire for a voucher whose
// reservation was refused or never attempted.
//
// Deliberately detached from the request's cancellation
// (context.WithoutCancel) but still bounded by the client's own timeout: a
// cashier navigating away from a failing tender must not also strand the
// primary's reservation.
func releaseVoucherOnPrimary(ctx context.Context, d *common.Deps, client *http.Client, voucherID, saleID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), client.Timeout)
	defer cancel()
	resp, isReplica, err := postVoucherActionOnPrimary(ctx, d, client, voucherID, "release", voucherReleaseRequest{SaleID: saleID})
	if !isReplica {
		return
	}
	if err != nil {
		logging.L().Warnf("voucher reservation release failed: %q for sale %s could not reach the primary (%v) — the primary may hold a debit with no sale behind it (ADR-0084 Decision 4)", voucherID, saleID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Warnf("voucher reservation release failed: %q for sale %s — primary answered %s — the primary may hold a debit with no sale behind it (ADR-0084 Decision 4)", voucherID, saleID, resp.Status)
	}
}

// releaseReservedVouchers releases every voucher completeTender reserved
// during one tender attempt, once that attempt has failed — best-effort,
// same as the underlying call, so a failure on one never stops the rest.
func releaseReservedVouchers(ctx context.Context, d *common.Deps, voucherIDs []string, saleID string) {
	for _, id := range voucherIDs {
		releaseVoucherOnPrimary(ctx, d, voucherProxyClient, id, saleID)
	}
}

// voucherRedeemWriteThrough is THE redemption pre-step completeTender runs
// for every tracked voucher payment, right before pos.CompleteSale, under
// the sale id that sale will be persisted with.
//
//   - Primary reachable and knows the voucher: RESERVED there
//     (reserveVoucherOnPrimary) — the primary's guarded debit is the one
//     shared serialization point, so a definitive refusal
//     (data.ErrVoucherNotActive / data.ErrVoucherInsufficientBalance) is
//     returned as-is and the caller must abort the tender with it, never
//     fall back to a local attempt against a voucher this till may not even
//     have a row for. On success, the PRE-debit snapshot is mirrored into
//     this till's own local vouchers row via EnsureVoucherLocalRow (a no-op
//     if a local row already exists — never clobbers this till's own
//     more-recent view), and preauthorized=true tells the caller to set this
//     payment's VoucherPreauthorized so its LOCAL debit (inside
//     pos.CompleteSale) is forced through against that starting balance
//     rather than failing closed on ErrVoucherNotFound for a voucher issued
//     elsewhere. The caller is responsible for releasing this reservation
//     (releaseReservedVouchers) if the tender fails after this point.
//   - Primary reachable but doesn't know the voucher (404), not a replica,
//     or any failure reaching it: preauthorized=false, err=nil — the caller
//     proceeds exactly as it did before #1668 (today's local-only
//     validation, offline-first unchanged).
func voucherRedeemWriteThrough(ctx context.Context, d *common.Deps, repo *data.POSRepo, voucherID string, amountMinor int64, saleID string) (preauthorized bool, err error) {
	primaryV, reserved, err := reserveVoucherOnPrimary(ctx, d, voucherProxyClient, voucherID, saleID, amountMinor)
	if err != nil {
		return false, err
	}
	if !reserved {
		return false, nil
	}
	if mirrorErr := repo.EnsureVoucherLocalRow(ctx, nil, primaryV); mirrorErr != nil {
		// Best-effort: if the local mirror insert somehow fails, the
		// imminent local DebitVoucherForRedemption(force=true) call will
		// simply fail closed with ErrVoucherNotFound (no local row exists),
		// aborting the WHOLE sale — and completeTender then releases this
		// reservation on the primary along with any other, so nothing is
		// left orphaned. A local DB unable to take one INSERT OR IGNORE
		// right now is a till that cannot complete ANY sale, voucher or
		// not. Logged, never silently treated as if the mirror had
		// succeeded.
		logging.L().Debugf("voucher proxy: local mirror of primary-reserved %s failed: %v", voucherID, mirrorErr)
	}
	return true, nil
}
