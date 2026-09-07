package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till voucher lookup + redemption validation, replica side
// (ut-docs#1668): a voucher issued at one till was, before this, only ever
// queryable/redeemable at a till whose OWN local vouchers row already knew
// about it — GetVoucherBalance and DebitVoucherForRedemption both read
// purely local SQLite. On a REPLICA (sync.primary_url set), voucher_api.go's
// balance query and pos_api.go's completeTender now try fetchVoucherFromPrimary
// first; on ANY failure reaching the primary (not a replica, network error,
// timeout, non-200, malformed body) they fall back — silently — to the
// existing local-only path, same fallback shape as fetchOrdersFromPrimary/
// claimTableWriteThrough. This till NOT being a replica, or being the
// primary itself, takes the exact same fallback branch — no change in
// behaviour for a single-till shop or the primary's own tender flow.
//
// READ-ONLY on the primary (round-2 review, 2026-09-07): the redemption side
// (voucherRedeemWriteThrough) does NOT call a mutating endpoint on the
// primary — see sync_vouchers.go's file-level comment for why the first
// draft's write-through debit double-applied every online cross-till
// redemption (the replica's own completed sale journals the SAME debit up
// moments later via the existing, unconditional sales-journal sync,
// unconditionally, since that mechanism has no idea the write-through
// already applied it) and orphaned a debit whenever a later payment in the
// same sale was refused. Instead: fetch the primary's CURRENT balance,
// validate locally against the SAME rules DebitVoucherForRedemption applies,
// then let the local debit (inside pos.CompleteSale, force=true) be the
// ONLY thing that ever writes a real debit — reaching the primary exactly
// once, via the ordinary journal, exactly as it always has. This closes the
// common case (a voucher unknown locally can now be validated and redeemed
// at all, against a balance that is at most a couple of seconds stale) but
// does NOT eliminate the fully-simultaneous two-till race — same residual
// risk class this codebase already accepts for the single-till-offline
// case (AllowVoucherOverdraft, ut-docs#1053). A genuinely atomic, race-free
// serialization is real follow-up work (the journal replay would need to
// recognize and skip a redemption already reflected via some shared
// idempotency key) — deliberately not this card, same shape as ut-docs#1703
// splitting write-through claim enforcement out of ut-docs#1392's read-only
// slice.

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
// caller falls through to its own local read.
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

// voucherRedeemWriteThrough is THE redemption pre-check completeTender runs
// for every tracked voucher payment, right before pos.CompleteSale.
//
//   - Primary reachable and knows the voucher: validated HERE against the
//     SAME rules DebitVoucherForRedemption applies (status must be
//     'active', balance must cover amountMinor) — a definitive refusal
//     (data.ErrVoucherNotActive / data.ErrVoucherInsufficientBalance) is
//     returned as-is; the caller must abort the tender with it, never fall
//     back to a local attempt against a voucher this till may not even
//     have a row for. On success, the fetched snapshot is mirrored into
//     this till's own local vouchers row via EnsureVoucherLocalRow (a
//     no-op if a local row already exists — never clobbers this till's own
//     more-recent view), and preauthorized=true tells the caller to set
//     this payment's VoucherPreauthorized so its imminent LOCAL debit
//     (inside pos.CompleteSale) is forced through against that starting
//     balance rather than failing closed on ErrVoucherNotFound for a
//     voucher issued elsewhere. This is the ONLY debit that ever actually
//     happens — see this file's own top comment for why a second,
//     primary-side debit here would double-apply once the sale journals up.
//   - Primary reachable but doesn't know the voucher either (404), not a
//     replica, or any failure reaching it: preauthorized=false, err=nil —
//     the caller proceeds exactly as it did before this card (today's
//     local-only validation, offline-first unchanged).
func voucherRedeemWriteThrough(ctx context.Context, d *common.Deps, repo *data.POSRepo, voucherID string, amountMinor int64) (preauthorized bool, err error) {
	primaryV, ok := fetchVoucherFromPrimary(ctx, d, voucherProxyClient, voucherID)
	if !ok {
		return false, nil
	}
	// Same predicate order as DebitVoucherForRedemption(force=false):
	// status first (a void voucher is refused regardless of balance), then
	// balance. No "redeemed" relaxation here — this is a live tender, not a
	// replay tolerating a genuine offline double-spend.
	if primaryV.Status != "active" {
		return false, data.ErrVoucherNotActive
	}
	if primaryV.BalanceMinor < amountMinor {
		return false, data.ErrVoucherInsufficientBalance
	}
	if mirrorErr := repo.EnsureVoucherLocalRow(ctx, nil, primaryV); mirrorErr != nil {
		// Best-effort: if the local mirror insert somehow fails, the
		// imminent local DebitVoucherForRedemption(force=true) call will
		// simply fail closed with ErrVoucherNotFound (no local row exists),
		// aborting the WHOLE sale (nothing was ever debited anywhere,
		// unlike the first draft's write-through, which had already
		// committed a real debit on the primary at this point — this
		// design has nothing left to orphan). A local DB unable to take
		// one INSERT OR IGNORE right now is a till that cannot complete
		// ANY sale, voucher or not. Logged, never silently treated as if
		// the mirror had succeeded.
		logging.L().Debugf("voucher proxy: local mirror of primary-validated %s failed: %v", voucherID, mirrorErr)
	}
	return true, nil
}
