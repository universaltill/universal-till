# Code review: `fiscal.sign.ask` drops the background re-sign retry (ut-docs#839)

**Date:** 2026-08-19
**Author:** Universal Till autonomous SDLC pipeline (Fable build, Opus independent review)
**Card:** universaltill/ut-docs#839
**Repos touched:** `universal-till` (code + tests + locales + user manual),
`ut-docs` (ADR-0056, ADR-0044 amendment note, ADR README index, contract doc → 1.4.0),
`ut-plugin-language-de`, `ut-plugin-language-es` (receipt wording)

## What shipped

fiskaly's own SIGN DE vendor documentation states that belated ("nachträgliche")
signing of a transaction is not permitted. `fiscal.sign.ask`'s background retry —
a 2-minute ticker that re-asked a signer for an already-completed unsigned sale
until it succeeded, then wrote a `fiscal_signing_resolved` marker and rendered the
sale as cleanly signed thereafter — was exactly that. `ADR-0056` (amending ADR-0044
Decision 1) withdraws it outright:

- The retry queue, ticker, goroutine, and `retry` payload field are deleted from
  `internal/pages/fiscal_sign_hook.go` — not throttled, not deprecated, removed.
- A sale that fails to sign at tender time now stays **permanently** unsigned:
  journal marker + receipt notice + operator alert, no later recovery.
- A one-time boot migration (`dropStaleFiscalSignRetryQueue`) clears any queue a
  pre-1.4.0 build left under `fiscal.pending_sign_retries`.
- Receipt/journal wording (`receipt.fiscal.unsigned_signing`,
  `receipt.fiscal.unsigned_cannot_sign`) rewritten in en/ar/fa/tr, and in the
  external `ut-plugin-language-{de,es}` packs, to state the gap as a permanent
  fact rather than promise later signing — German aligned to fiskaly's own
  suggested phrasing ("TSE ausgefallen").
- User manual (`web/help/{en,ar,fa,tr}/sell.md`) rewritten to match; screenshots
  regenerated.
- Contract doc bumped to 1.4.0 (`ut-docs/reference/contracts/fiscal-sign-ask.md`).
- `ut-plugin-tax-de` needs **no code change** — confirmed by reading its source:
  its own prior private retry queue was already removed in ut-docs#818, so it has
  no retry logic left to disable.
- `ut-docs#819` (a bug in a retry-only code path) closes as superseded: with no
  retry path left anywhere in the ecosystem, its bug has nothing left to occur
  through.

## Independent review (Opus, isolated worktree)

Briefed with the full diff, the ADR, the 1.4.0 contract, and this repo's
CLAUDE.md; told explicitly to find problems, not confirm the work, and to check
the two recurring bug classes this pipeline's reviews watch for (missing
`os.MkdirAll` on a file write, a cwd-relative path instead of `paths.Data(...)`).

**First-pass verdict: not safe to merge as written.** One blocker, one should-fix,
several nits — all resolved before this record:

1. **Blocker** — the German and Spanish language-pack receipt strings
   (`ut-plugin-language-{de,es}`) still promised automatic retry
   ("…die Signierung wird automatisch erneut versucht." /
   "…la firma se reintenta automáticamente."), even though the en/ar/fa/tr
   strings in core had already been fixed. `check-lang-pack-drift.sh` couldn't
   catch this — it only compares key *sets*, not values. On a German pilot till
   this would have printed a factually false statement on a fiscal receipt,
   worse than before the fix because the code no longer backs the promise up at
   all. **Fixed**: both packs' `unsigned_signing`/`unsigned_cannot_sign` values
   rewritten to the same permanent-fact wording as core, German using fiskaly's
   own suggested phrasing ("TSE ausgefallen"); both packs' `validate.sh` and
   `check-key-drift.sh` re-run clean; pushed directly to each pack's `main`.
2. **Should-fix** — `fiscalSignResult.Payload` (the request payload echoed back
   on every result) had become write-only dead state: nothing in the repo read
   it once the retry queue was deleted, and its doc comment invented a
   justification ("echoed back for callers/tests that inspect what was
   dispatched") for a consumer that doesn't exist. ADR-0056 itself is explicit
   that this class of leftover machinery is "worse than no code, since it
   invites a future edit to 'helpfully' re-wire it" — and the reviewer's own
   TDD re-verification (below) used exactly this field to reconstruct the old
   enqueue behavior, which is the concrete version of that risk. **Fixed**:
   the field and its ten assignment sites deleted; `fiscalSignResult`'s doc
   comment now explains why no payload copy is kept.
3. **Nits, all fixed** — three stale comments elsewhere in the repo still
   described the background retry as a live mechanism
   (`internal/data/fiscal_repo.go`'s `RecordFiscalTSESignature` doc,
   `internal/pages/print_api.go`'s TSE-evidence comment, the
   `fiscalsign_unreachable_guest` test guest's doc comment) — reworded to drop
   the retry references / mark as historical. `internal/db/migrations/
   048_fiscal_tse_signatures.sql`'s comment was **left as-is** on purpose:
   migrations are append-only after release, and the comment is historically
   accurate for what the migration described at the time it landed.

**Noted, not fixed — filed as a follow-up, not a blocker**: `fiscal.pending_sign_retries`
isn't covered by `data.PerTillSettingPrefixes`, so during a mixed-version rollout
a pre-1.4.0 primary could theoretically re-seed the (now-inert) key onto a 1.4.0
replica after its one-time boot migration already ran. The reviewer confirmed
this has **no compliance or functional impact on the 1.4.0 build** — nothing
reads the key anymore, in either direction — it's a housekeeping gap, not a
correctness one. Fixing it properly means understanding this repo's sync
replication semantics well enough not to introduce a real bug for a cosmetic
one; deferred to a new Backlog card rather than rushed here.

## TDD re-verification (performed by the reviewer, independently)

Temporarily restored the pre-fix enqueue behavior (re-added a faithful
reimplementation of the deleted `enqueueFiscalSignRetry`, called it from
`declareUnsignedFiscalSale`'s old call site) with every test left untouched, in
an isolated worktree:

- **Pre-fix** (old behavior restored): `TestFiscalSignAsk_NeverReDispatchesAfterTenderCompletes`
  and three other tests **FAIL** with real assertion failures — the retry-queue
  assertion sees a genuine queued JSON entry, not a compile error or a
  vacuously-true check.
- Reverted the temporary change; rebuilt.
- **Post-fix**: all four tests **PASS**, plus the new migration test
  (`TestDropStaleFiscalSignRetryQueue_ClearsPre140Queue`) and the full
  `print_api` receipt-doc suite.

## Verification performed personally (beyond the reviewer's pass)

- `go build ./...` and `go vet ./...` — clean, after the should-fix/nit round of
  fixes (not just the first pass).
- Full `go test ./...` — all 38 packages green.
- `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` — all green on the final diff (`make docs-shots` re-run
  after the should-fix edits touched `internal/pages` again; the only pixel
  changes were the two screenshots — `alerts`, `designer` — that carry a live
  timestamp and drift on every run regardless of app changes, same as every
  prior cycle touching this area; the `sell` screenshots themselves, whose
  prose changed, did **not** need new pixels since nothing on-screen changed).
- Confirmed via `grep` across the whole repo (not just the diff) that no
  remaining reference — admin page, cloud-heartbeat report, export, reconciliation
  job — still assumes a sale can become `fiscal_signing_resolved` after tender
  time; the read-side `saleFiscalSigningGapKind` check is correctly retained
  and documented as historical-only.
- Confirmed the two locale-pack repos' `validate.sh` and `check-key-drift.sh`
  pass after the blocker fix, and that both were pushed to their own `main`
  branches directly (asset-only plugin repos, same convention this pipeline
  used for the ut-docs#835 lang-pack-drift fix earlier the same cycle).

## Not done, and why

- Real start/finish-lifecycle-aware reconciliation (fetching a signature
  fiskaly already produced for a transaction that genuinely was started before
  the connection dropped — not "belated signing" under the vendor's own
  statement) is deliberately not built here. It needs new contract state a
  signer can use to prove (not merely claim) a transaction was started, and
  deserves its own tax-advisor-reviewed ADR given how easily "reconcile" and
  "sign late" can look identical in code. Tracked as a new Backlog card.
- `fiscal.pending_sign_retries`'s sync-replication housekeeping gap (see above)
  — new Backlog card, not a blocker on this one.
