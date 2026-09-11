# Code review — `fiscal.sign.reconcile.ask` (ADR-0077 Decision 3/4)

- **Card**: ut-docs#1520
- **Branch**: `feat/1520-fiscal-sign-reconcile`
- **Design**: ADR-0077 (`docs/adr/0077-fiscal-sign-two-phase-start-finish-reconcile.md`), Decisions 3 & 4
- **Dev**: Fable subagent (per `complexity:hard` model routing)
- **Review**: Opus subagent, independent — different model, fresh context, full worktree access
- **Verdict**: **No blocker.** Two should-fix findings fixed pre-merge; two more documented below as accepted/deferred, per this pipeline's rule that a second full review round is earned only by a blocker-class finding (none was found here).

## Scope implemented

- `unsigned_fiscal_signing` audit payload gains an `outcome` field
  (`backend`/`entry`/`offline`) distinguishing why a sale completed
  unsigned — the prerequisite ADR-0077 D3 names for reconcile eligibility.
- New `fiscal.sign.reconcile.ask` event + a periodic background sweep
  (`StartFiscalSignReconcileSweep`) that asks the exclusive signer to
  retrieve an already-produced TSE signature for a sale that failed at
  tender time with a **backend-level** failure only.
- Two-tier check (ADR-0077 D3): exact `tx_id` match when core holds one
  from the `fiscal.sign.start` round trip; a bounded time-window
  structural check on the degraded no-`tx_id` path.
- Confirmed evidence is recorded in a **new, separate table**
  (`fiscal_tse_reconciled_signatures`), never the receipt-facing
  `fiscal_tse_signatures` — making ADR-0077 D4 (a reconcile outcome never
  changes any receipt render, original or reprint) structural rather than
  a per-render check.
- Fiscal-signing exclusivity (ADR-0041 Decision B) extended from the
  single `fiscal.sign.ask` key to the 3-event group
  (`fiscal.sign.ask`/`fiscal.sign.start`/`fiscal.sign.reconcile.ask`) at
  both manifest-persist time and plugin-enable time.
- Contract doc (`ut-docs/reference/contracts/fiscal-sign-ask.md`)
  updated to 1.8.0 with a new sibling section, delivered as a separate
  patch (applied in the same session, see the `ut-docs` commit this PR's
  description links).

## Independent verification performed

Both the Dev subagent's own gate and the Reviewer subagent's gate were
re-run a third time, personally, after the review fixes below were
applied, in the actual worktree (not trusted from either subagent's
report alone):

- `gofmt -l .` — empty
- `go build ./...` — pass
- `go vet ./internal/...` — clean
- `go test ./internal/pages/... ./internal/plugins/... ./internal/data/... ./internal/db/... -count=1` — all packages `ok`, 0 failures
- `golangci-lint run ./internal/...` — 0 issues
- `scripts/ci/guard-data-access.sh` — pass

## Review findings and disposition

Ranked as the Opus review reported them (most severe first). Full
reasoning/evidence for each lives in the review agent's own transcript;
summarized here with what was actually done.

1. **Candidate starvation (should-fix) — FIXED.** The sweep's
   oldest-first `LIMIT` query had no cursor, so a permanently-not-found
   head (a legitimate, common non-write outcome under this design) meant
   every tick re-fetched the identical oldest slice forever — any sale
   past the batch limit could be starved for the entire 48h lookback.
   Fix: `ListFiscalSignReconcileCandidates` takes a rowid cursor;
   `fiscalSignReconcileTick` now pages through the full eligible set
   within one tick (bounded by a time/count safety ceiling so ticks never
   overlap), rather than only ever fetching page one. The cursor lives
   only for one tick's own loop — nothing persists across ticks, so
   ADR-0077 D3's "no per-sale state" still holds. Regression test:
   `TestFiscalSignReconcile_TickPagesPastAPermanentlyUnresolvedHead`
   (seeds `batchLimit+5` sales, all but the last answer not-found, asserts
   the last one is still reached and reconciled in the same tick).
2. **Poison-pill sale aborts every pass, forever (should-fix) — FIXED.**
   Any non-nil dispatch error previously ended the whole pass, so a
   guest-side error specific to one sale's payload (as opposed to a
   genuine backend-outage timeout) would wedge every sale behind it,
   indefinitely, since the same sale is always retried first next tick.
   Fix: `reconcileFiscalSignGap` now classifies a dispatch error the same
   way `askFiscalSign` already does for the finish path — a budget
   timeout (`context.DeadlineExceeded`) aborts the pass (likely to repeat
   for every remaining candidate too); any other handler/guest error is
   logged and the pass continues to the next candidate. Regression test:
   `TestFiscalSignReconcile_PoisonPillSaleDoesNotBlockOthers` (three
   sales, the middle one's handler errors, both neighbours still
   reconcile).
3. **No kill-switch for the degraded window tier (should-fix) — ACCEPTED,
   deferred to the ADR-0077 D5 go-live review, not fixed in this PR.**
   ADR-0077 D5 itself is explicit that the go-live sign-off, not the
   implementation card, decides whether the window tier ships enabled or
   is restricted to the exact-match tier only. Adding a settings-backed
   toggle now would be building ahead of a decision that isn't this
   pipeline's to make (`ut-plugin-tax-de` doesn't exist yet either —
   ut-docs#1521 — so nothing can exercise this tier live regardless).
   Recorded here explicitly, as the review requested, so it reaches the
   D5 agenda rather than being silently assumed resolved.
4. **Silent check refusals (should-fix) — FIXED.** Four distinct "signer
   said confirmed, core refused it" outcomes (unparseable answer,
   unrecognised status, missing signature, failed tier check) wrote no
   log line at all, indistinguishable from the signer's own honest
   not-found. Fix: each now logs an `Info` line naming the sale and the
   specific reason. A genuine `not-found` answer is deliberately left
   unlogged — it's the common, expected non-write outcome, and logging it
   would drown the genuinely exceptional refusals in noise.
5. **D4 receipt test's HTML half reimplements input derivation rather
   than driving `pos_api.go` (test-quality nit) — ACCEPTED, not fixed.**
   The ESC/POS half of `TestFiscalSignReconcile_ReceiptsAreByteIdenticalAfterReconcile`
   is genuinely end-to-end; the HTML half proves `renderReceipt` is
   faithful given hand-derived inputs, so it would not by itself catch a
   *future* edit to `pos_api.go` that added a read from the reconciled
   table. The property itself was independently verified structurally by
   the reviewer (both render paths read only `fiscal_tse_signatures`,
   `saleFiscalSigningGapKind` is byte-identical to `origin/main`) — this
   is a coverage gap in one test's shape, not a live defect. Left as a
   known gap rather than reworked, to avoid widening this PR further
   after a clean review; a natural pickup if `pos_api.go`'s fiscal
   rendering is next touched.
6. **Group exclusivity isn't retroactive (nit) — accepted, no code
   change.** A till that installed a second plugin declaring only
   `fiscal.sign.start` during the #1519→#1520 window (when that event was
   explicitly documented as unenforced) keeps both active after this
   lands; nothing re-checks at boot. Narrow — and moot for
   `fiscal.sign.reconcile.ask` itself, a brand-new key with no prior
   window where it could have been declared unchecked.
7. **Reconciled-evidence table has no reader yet (nit) — accepted.**
   `GetFiscalTSEReconciledSignature` has no production caller today; its
   value is realized once a DSFinV-K export exists, per ADR-0077's own
   "a future DSFinV-K export" framing. The `fiscal_signing_reconciled`
   audit marker is reachable through existing audit surfaces now.
8. **`audit_log` growth from repeated dispatch rows (nit) — accepted,
   materially reduced by fix #1.** `EventBus.Ask` writes one
   `event_dispatch` audit row per call; finding #1's fix (walking the
   full eligible set instead of resweeping the same stuck 50 forever)
   removes the main driver of this growth.
9. **Misc (nits) — accepted, no code change**: `FiscalSignExclusiveEvents`
   is an exported mutable `[]string` (tamper-proof only by convention, not
   by the type system); the evidence-then-marker write order in
   `reconcileFiscalSignGap` could in principle log a stable tier
   inconsistency on a retried partial write, but is harmless given
   `heldTxID`'s own stability; the contract-doc patch's new 1.8.0
   changelog row is out of order relative to 1.6.0/1.7.0, matching the
   table's own pre-existing ordering rather than introducing a new defect.

## What the review verified clean (independent of the findings above)

Traced structurally, not just via the supplied tests — see the review
transcript for full file:line evidence:

- D3 eligibility (only `fiscalSignFailedBackend` gaps are ever candidates)
- The two-tier check's edge cases (mismatched tx_id never rescued by the
  window; unparseable/absent timestamps refuse; timezone handling correct)
- D4 receipt immutability, structurally (separate table; both render paths
  read only the original table; `saleFiscalSigningGapKind` untouched)
- `fiscal_signing_resolved` (pre-ADR-0056) is written by no new code path
- Exclusivity-group symmetry across all 3×2 directional pairs, fail-closed
  on a DB error at both persist and enable time, self-update exempted
- No double-dispatch / no duplicate `fiscal_signing_reconciled` rows
  possible (ticker never overlaps itself; a sale can never carry two
  `unsigned_fiscal_signing` rows)
- Migration 027's `IF NOT EXISTS` matches the real post-009 convention;
  correctly classified as a per-till (non-admin-synced) table
- Zero-plugin-cost claim, read from the code (`HasSubscribers` gates
  before any DB access)
- Money/tax isolation — the new payload carries no monetary data, and no
  code path here can construct a signing instruction, only confirm/deny

## Follow-ups filed

None new — findings #3, #5, #6, #7, #8, #9 are recorded above rather than
filed as separate cards; #3 already has a home (ADR-0077 D5's go-live
agenda), and the rest are narrow enough not to warrant their own tracking
card at this scope.
