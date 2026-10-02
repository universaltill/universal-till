# Review: UK age-restricted sales support (ut-docs#3340)

Author: Dev subagent (Opus 5.5). Independent review: Fable, in an isolated
worktree (`/home/user/universal-till-review-wt`, detached at the pre-review
WIP commit). Fix pass: Opus 5.5 (same author model — review-only separation
applies to the review step, not the fix step). Orchestrator/Tester: this
session (Sonnet 5).

## What shipped

- A catalog item can be flagged `age_restricted` (new boolean column,
  migration `055_items_age_restricted.sql`).
- The till gates tendering a sale on the cashier recording an ID-check
  outcome (`accepted`/`refused`) for any unverified restricted line, via a
  new non-modal sheet (`.show()`, never `.showModal()`) and a new
  `POST /api/pos/age-check` endpoint. The outcome is written to a new
  append-only `age_verifications` table (migration `056`) in the same
  transaction as the sale-completion insert, using the real `sale_id`.
- The self-order kiosk fails closed (409, basket preserved, no sale row) on
  any `age_restricted` item; staff take over via the existing kiosk
  PIN-login path into till mode.
- `pos.DOBBeforeCutoff(dob, cutoff time.Time) bool` — a pure, exported,
  unit-tested helper (dates either side of a 2009-01-01-style fixture)
  expressing that a restriction can be a permanent birth-date cutoff, not a
  rolling age. Deliberately unwired from any UI this slice (AC3 only
  requires the mechanism to exist and be tested); listed in
  `scripts/ci/deadcode-baseline.txt` as a result.
- Help topic `age-restricted-sales` in en/ar/fa/tr/de, reachable both from
  the catalog item editor checkbox and from the till's age-check sheet; a
  short addition to the `sell` topic (every locale) describing the badge.
- i18n keys in en/ar/fa/tr (real translations, not English copies).

No jurisdiction-specific legal value (an age, a date, a country name) is
hardcoded anywhere in production code — only in test fixtures — per
ADR-0050's core-vs-plugin boundary test and the 2026-09-25 owner rule that
jurisdiction-specific behaviour belongs in a plugin, not core. A future
plugin can supply real jurisdiction defaults later (not this slice); the
mechanism is fully usable by a merchant without one.

## Review findings and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | `age_verifications.sale_id` has no `ON DELETE` action; `ResetTransactionHistory`'s `DELETE FROM sales` hit the FK and failed outright once any verification existed — the exact `sale_charges`/ADR-0062 trap. | **Fixed.** New migration `057_age_verifications_archive.sql` adds the archive twin, following the established `*_archive` + `reset_batch_id` pattern exactly (child archived/cleared before `sales`, restored after). Reset→restore round-trip test added (`internal/data/age_verification_reset_test.go`). |
| 2 | Should-fix | Obsolete-item / demo-item cleanup predicates didn't check `age_verifications.item_id`, so removing an item that was only ever "refused" (never sold) hit the same FK and rolled back the whole purge. | **Fixed.** `obsoleteItemsPredicate` (`pos_repo.go`), `KeptReasonHistory` (`demo_seed_repo.go`), and both `remove_demo*.sql` predicates now exclude items with a verification row. `shrinkage_events` has the same latent (pre-existing, unrelated) gap — **not fixed here**, filed as its own Backlog card (see below) rather than widening this PR. |
| 3 | Should-fix | The till's own age-check sheet had no `helpLink`, and the `sell` help topic never mentioned the feature — reachable only from the catalog editor. | **Fixed.** `helpLink "age-restricted-sales"` added to the sheet; a short paragraph added to `sell.md` in every locale; `make docs-shots` re-run, `guard-docs-shots`/`guard-help-topics`/`guard-help-drift` all green. |
| 4 | Should-fix | `TestAgeChecks_ClearedOnResetAndResume` reset the basket to empty *before* recording a check, so its core assertion was vacuous (no-op on an unknown key). | **Fixed.** Rewritten to record a real outcome, hold the sale, reset, restore, and assert the restored line actually needs re-verification. Behaviour itself was already correct (independently confirmed by the reviewer); only the test was wrong. |
| 5 | Nit | `DOBBeforeCutoff` ships as deliberately-unreachable code (baseline entry). | Accepted as a judgment call for AC3 — see "What shipped" above. |
| 6 | Nit | The flag doesn't round-trip through catalog CSV export/import or the cloud catalog snapshot contract (my. can't see/set it; a re-import can't clear it). | **Deferred — filed as Backlog.** Real gap, not this card's scope. |
| 7 | Nit | Kiosk only blocks after a payment method is picked, not at the payment-picker's first render. | **Deferred — filed as Backlog** (UX polish, not a correctness or compliance issue — the sale still never completes). |
| 8 | Nit | No CHANGELOG entry; `ut-plugin-language-{de,es}` need follow-up PRs for the new keys (lang-pack-drift is advisory); satellite-till verifications don't reach the main till via sync (documented limitation in `sales.go`, not fixed). | Housekeeping — CHANGELOG entry added in this commit; language-pack and sync-scope items **filed as Backlog**, not blocking. |

## Verified beyond automated tests

- Real driven run (not just httptest assertions): booted a throwaway till
  (`e2e/run-till.sh`) with this session's pre-installed Chromium, flagged a
  real item via the catalog editor UI, added it to a live basket, opened
  the sheet, accepted, watched the badge flip states — screenshots looked
  at, not just asserted to exist.
- RTL (`ar`): re-ran the same screens with `?lang=ar` — nav mirrors
  correctly, Arabic labels render correctly in the checkbox and the sheet,
  no clipping/overlap/truncation at the 1024×600 kiosk floor.
- Full gate re-run after the fix pass: `go build`/`go vet`/`gofmt -l .`
  clean; `go test ./... -count=1` 76 packages ok, 0 FAIL (confirmed twice,
  including after this session's container restarted mid-review — the
  `internal/pages` package briefly looked hung at a 90s timeout post-restart
  but turned out to be a cold-build-cache slowdown affecting even unmodified
  `main` identically in a disposable worktree, not a regression; it passes
  at 300s); `golangci-lint run ./...` 0 issues; `guard-data-access`,
  `guard-i18n`, `guard-help-topics`, `guard-help-drift`,
  `guard-migration-version-collision`, `guard-core-neutral`,
  `guard-no-showmodal`, `guard-docs-shots` all green.
- Transaction/mutation testing: the reviewer temporarily disabled the
  tender gate, the kiosk block, and the in-transaction verification insert
  and confirmed the relevant new tests fail each time, then restored them.

Not checked: real touch hardware (none available in this sandbox).

## Safe to merge

Yes, with the fixes above applied. No open blockers.
