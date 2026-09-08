# Code review: atomic cross-till voucher redemption (ut-docs#1716, ADR-0084)

- **Date**: 2026-09-08
- **Branch**: `fix/1716-atomic-cross-till-voucher-redemption`
- **Card**: ut-docs#1716 (split from ut-docs#1668)
- **Design**: `ut-docs/adr/0084-atomic-cross-till-voucher-redemption-via-idempotent-primary-reservation.md`

## What shipped

Closes the fully-simultaneous two-till voucher redemption race #1668 left
open, without reintroducing either blocker-class bug the original
write-through draft was reverted for (double-debit via journal replay,
orphaned debit on a later payment's refusal in the same sale).

- Two new mutating, idempotent primary-side endpoints:
  `POST /api/sync/vouchers/{id}/redeem` and `.../release`. `/redeem` reuses
  the existing, already-correct predicate-guarded `DebitVoucherForRedemption`
  UPDATE as the one shared serialization point — two concurrent reservations
  against the same voucher genuinely serialize at the primary's
  `BEGIN IMMEDIATE`, and the loser gets a clean `ErrVoucherInsufficientBalance`
  refusal.
- Idempotency key `(voucher_id, sale_id)`, enforced at TWO independent
  layers: an app-level pre-check inside `ReserveVoucherRedemption`, and a
  new partial unique index (`ux_voucher_tx_redemption_once`, migration 012)
  that makes a duplicate a hard DB constraint. `pos.CompleteSale`'s
  redemption path checks `VoucherRedemptionRecorded` before its own debit,
  so a journal replay of an already-reserved sale skips re-debiting.
- `voucherRedeemWriteThrough` (replica side) becomes the reservation call;
  `completeTender` tracks every voucher reserved in a tender attempt and
  releases all of them (`releaseReservedVouchers`) if a later payment is
  refused or the local sale fails — synchronously, before any local sale
  row exists, so no journal entry for a released attempt can ever be
  produced.
- A same-sale duplicate-voucher guard (`pos.ErrDuplicateVoucherPayment`)
  fires before any reservation is attempted. A new quarantine allowlist
  entry covers a pre-1.8.0 replica journaling that same shape.
- `internal/auth/middleware.go` exempts the two new paths (bounded to one
  id segment + a known suffix); `TestSyncPullPathsAreExempt` pins both the
  positive and negative cases.
- Manual (`web/help/en/multitill.md`) and the LAN-sync journal contract
  (`ut-docs/reference/contracts/pos-lan-sync-journal.md`, 1.8.0) updated in
  the same branch.

## Independent review

**Dev**: a Fable subagent, briefed with a detailed design derived from the
ADR (two passes — an initial brief, then a corrections addendum after a
first attempt was blocked by an unrelated harness issue before writing any
code and used its investigation time productively). TDD throughout; full
gate green; the end-to-end race test (acceptance criterion #2, literally)
proven via revert-then-restore against the pre-fix code.

**Review**: an Opus subagent, fresh context, isolated worktree, no prior
knowledge of the implementation reasoning. Independently:
- Read every changed file in full, not just the diff.
- Re-verified the core correctness claims by reading the code (real
  serialization via `_txlock=immediate`; the partial unique index is real
  and rejects a literal double-insert; the app-level skip can't misfire on
  a genuinely new sale id; `/release`'s call sites are strictly before any
  successful `CompleteSale`; a 404 correctly falls back to local rather
  than aborting; `EnsureVoucherLocalRow` receives the pre-debit snapshot,
  not post-debit).
- Ran the full gate itself (`gofmt`, `go build`, `go vet`,
  `golangci-lint run ./...`, full `go test ./...`, every relevant CI
  guard) and re-ran the two most load-bearing race tests 10× each with
  `-race` — zero flakes.
- Did its own revert-then-restore TDD verification on the unique index, the
  `VoucherRedemptionRecorded` skip, and (in isolation) each of the two
  guards inside `ReserveVoucherRedemption`'s race protection — confirmed
  each guard is independently sufficient (deliberate defence in depth, not
  redundant dead code).
- Checked the manual and the LAN-sync contract doc against the actual code
  behaviour, not just their presence.
- Checked for real client/shop names, secret-shaped literals, and the two
  recurring bug classes (missing `os.MkdirAll`, a cwd-relative path instead
  of `paths.Data(...)`) — none applicable.

**Verdict: SAFE TO MERGE.** Zero blocking findings. Seven non-blocking
findings, all addressed in a follow-up commit on the same branch before
merge (not a second review round — targeted fixes re-verified against the
existing gate):

1. **(should-fix, addressed)** `reserveVoucherOnPrimary` only tracked
   *confirmed* reservations for release; a transport failure or a 200 with
   an undecodable body — both cases where the primary may have already
   committed — went untracked, so a subsequent tender failure could leave
   an orphaned primary-side debit with no release ever sent. Fixed: the
   function now returns a third `maybeReserved` signal for exactly these
   two ambiguous cases, and `completeTender` releases those vouchers
   exactly like confirmed ones (release is a safe idempotent no-op on a
   voucher never actually reserved, so over-releasing costs nothing).
2. **(addressed, docs-only)** The LAN-sync journal contract doc's version
   header still said 1.7.0/2026-09-07 despite its own 1.8.0 changelog row;
   and its skew note wrongly said an old primary answers `/redeem` with
   404 (it's 401, no exempt-list entry on the old primary). Both corrected.
3. **(addressed)** The concurrent-reserve test's comment overstated which
   single guard closes the race; corrected to name both independently
   verified guards (the UPDATE's own predicate, and the same-tx pre-read)
   plus a pointer to the DB-level third layer.
4. **(addressed)** `ReserveVoucherRedemption`'s idempotent-retry branch
   didn't compare the retried amount to the one already recorded — not
   reachable from this repo's own client, but `/redeem` is an externally
   reachable endpoint and CLAUDE.md requires validating all external
   input. New sentinel `ErrVoucherRedemptionAmountMismatch`, wired to a
   409 with a stable reason, treated as a definitive refusal like the
   other two. New repo-level and handler-level tests.
5. **(addressed, documented)** `/release` doesn't verify the caller owns
   the reservation — deliberate, consistent with the existing bearer-auth
   trust model (a bearer-authed till can already journal an arbitrary sale
   via `/api/sync/sales`); now stated explicitly in the handler's comment
   rather than left implicit.
6. **(addressed)** `ErrDuplicateVoucherPayment` and the new amount-mismatch
   sentinel now map to the existing `pos.toast.voucher_invalid` key in
   `classifyTenderError` instead of falling through to the generic
   `tender_failed` toast — no new locale key needed (neither sentinel is
   reachable through any shipped voucher-tender UI today).
7. **(addressed, docs-only)** ADR-0084 §2 described `sale_id` as always
   pre-minted by `dispatchFiscalSignStart`; the implementation found this
   only happens when a plugin subscribes to `fiscal.sign.start`, so
   `completeTender` mints it explicitly when still empty. ADR text
   corrected to match the implementation, which was already right.

The design question the review was explicitly asked to weigh in on — should
an unrecognised 409 reason from `/redeem` fail closed instead of falling
back to local-only? — the reviewer agreed the current choice is correct:
falling back never fails open (a voucher this till has no local row for
still fails closed via `ErrVoucherNotFound`), while failing closed would let
any future primary-side refusal reason brick voucher tenders on every
not-yet-upgraded replica, contradicting the primary-upgrades-first skew
rule. Documented in `reserveVoucherOnPrimary`'s own comment.

## What was verified beyond automated tests

- Full gate re-run clean after the review-fix commit: `gofmt -l .`,
  `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues),
  full `go test ./...` (every package `ok`), `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-docs-shots.sh` (regenerated after the review-fix
  commit touched `internal/pages/**.go` again — only the same
  run-to-run PNG-encoding noise already documented in
  `2026-08-24-docs-shots-determinism-930.md`, not a real visual
  regression), `guard-help-topics.sh`, `guard-migration-version-collision.sh`.
- The end-to-end race test (`TestCrossTillVoucherRace_
  OnlyOneReservationWinsAndReplayDoesNotDoubleDebit`) and the repo-level
  concurrent test (`TestVoucherRepo_ConcurrentReserveOnlyOneWins`) both
  re-run 10× with `-race` by the reviewer with zero flakes.
- No new locale keys reach the shipped UI (the one new key,
  `sync.quarantine_reason.duplicate_voucher_redemption`, is a back-office
  quarantine-list label, not a cashier-facing string) — the external
  `ut-plugin-language-{de,es}` packs get their follow-up PR in the same
  pipeline cycle, after this merges, mirroring how ut-docs#1759's
  `lang-pack-drift` follow-up was handled earlier in this same cycle.

## Deferred / explicitly out of scope

- **Reservation-expiry/reconciliation for the crash window** (ADR-0084
  Decision 4): a crash on the replica between a successful `/redeem` and
  the local sale committing leaves a permanent primary-side debit with no
  local sale and no release ever sent. Accepted residual risk, not solved
  here — real follow-up work if ever prioritized, not a silent gap.
- Voucher issuance — untouched, same as #1668.
- Whether `vouchers` joins the periodic admin-bundle sync — stays ruled
  out for the same staleness reason ut-docs#1554 already gave.

---
_Generated by [Claude Code](https://claude.ai/code)_
