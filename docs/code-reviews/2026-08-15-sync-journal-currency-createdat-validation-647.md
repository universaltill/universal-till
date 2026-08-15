# Review: LAN sync journal — validate currency + created_at (ut-docs#647)

**Date**: 2026-08-15
**Card**: universaltill/ut-docs#647 — "LAN sync journal: validate remaining
SaleDetail fields beyond id/receipt_no/sale_type"
**Complexity**: medium
**Reviewer model**: Opus subagent, fresh context, worktree-isolated (per
this card's `complexity:medium` tier — see `scrum-master` skill's model
routing)

## What shipped

`POST /api/sync/sales` (`internal/pages/sync_sales.go`) previously
validated only `id`/`receipt_no`/`sale_type` on an incoming journal entry
(the ut-docs#262 version-skew guard). Everything else — `currency`,
`created_at`, `cashier_id`, and all line fields — was documented as
"optional; absence degrades gracefully."

Verifying that against the real code (BA step) found two of those claims
were wrong in ways that mattered:

- **`currency`**: the live checkout path (`completeTender`,
  `internal/pages/pos_api.go`) always derives `Currency` server-side from
  `d.CurrentState().Currency` — a client never supplies it. The sync
  journal path is different: it trusted a replica's claimed
  `sale.currency` verbatim, with nothing cross-checking it against the
  shop's actual configured currency. A wrong-currency journal entry was
  silently applied and booked real revenue under the wrong currency.
- **`created_at`**: not "degrades gracefully" at all. `applyJournal`'s own
  `repo.SetSaleProvenance(ctx, j.Sale.ID, tillID, j.Sale.CreatedAt)` call
  writes it **verbatim** over the `sales.created_at` column that
  `pos.CompleteSale` had just stamped with the real completion time — an
  empty or malformed value silently corrupted the sale's actual creation
  timestamp, with no downstream default catching it.
- **Not real gaps** (verified, no code change): line-level `item_id`/
  `variant_id` presence, `unit_price >= 0`, `qty > 0`, and discount bounds
  are already rejected loudly by `pos.CompleteSale`'s own `validateLine`/
  `computeSaleTotals`/`netPayments`, before any DB transaction opens.
  `cashier_id` is intentionally untrusted-but-accepted per the contract's
  own Security section (any enrolled till may already attribute a sale to
  any `cashier_id` it claims — ADR-0011 mutual trust); tightening it would
  be a trust-model change, not a bug fix, and is explicitly out of scope.

The fix adds `invalidJournalFields` alongside the existing
`missingJournalFields`, called right after it in `applyJournal`:

- `currency`: **still accepted when empty** (unchanged graceful default —
  `pos.CompleteSale` defaults an empty `Currency` to `"GBP"`); rejected
  (`422`) when **non-empty and not equal** to `d.CurrentState().Currency`.
- `created_at`: now **required**, must parse as `time.RFC3339`.

TDD-first: `TestApplyJournal_RejectsInvalidCurrency`,
`TestApplyJournal_AcceptsEmptyCurrency`,
`TestApplyJournal_RejectsMissingOrMalformedCreatedAt` (3 subcases), and
`TestApplyJournal_AcceptsRealCurrencyWhenPrimaryUnconfigured` (added from
the review finding below). Confirmed failing against the pre-fix code
(exact claimed rejection), then passing, by both the implementer and,
independently, the reviewer.

`reference/contracts/pos-lan-sync-journal.md` (in `ut-docs`) bumped
1.0.0 → 1.1.0 with a changelog entry: what changed, why, and that the only
known consumer (`buildJournal`, which always populates `created_at` from
the sale's own DB row) is unaffected — this can only reject a malformed or
adversarial payload, never a well-behaved replica.

## Independent review (Opus, fresh context, worktree-isolated)

**Verdict: safe to merge**, with one real finding fixed before merge.

### Findings triaged

- **F1 (fixed)** — `internal/pages/sync_sales.go`: if the primary's own
  `d.CurrentState().Currency` is blank, the original condition
  (`s.Currency != "" && s.Currency != configuredCurrency`) rejected
  **every** non-empty currency — i.e. every well-behaved replica push —
  until the setting was fixed or the till restarted. Reachable:
  `POST /api/settings/upsert` (unlike `/api/settings/save`) doesn't
  validate `store.currency` before assigning it, so an operator/admin
  action could blank it. The reviewer demonstrated it live: with
  `Currency=""`, a genuine `"GBP"` journal entry from a shop that's
  actually `"GBP"` still got `422 invalid currency ("GBP", shop is "")`.
  Fixed by additionally requiring `configuredCurrency != ""` before the
  mismatch check fires — "not yet configured" now stays permissive, same
  as the behaviour this card started from, rather than becoming a new
  fail-closed mode on the sync path. New regression test:
  `TestApplyJournal_AcceptsRealCurrencyWhenPrimaryUnconfigured` —
  confirmed failing (rejected with the exact reported message) against
  the pre-fix logic, passing after.
- **F2 (accepted, informational, no code change)** —
  `scripts/smoke_quickstart/main.go` writes `created_at` in a non-RFC3339
  layout (`"2006-01-02 15:04:05"`). Test/dev tooling only, explicitly
  out of `guard-data-access.sh`'s domain-code scope per
  `universal-till/CLAUDE.md`; not a production insert path, and not a
  replica-side journal source. Noted, not fixed — the "every sale insert
  is RFC3339" invariant this card leans on isn't mechanically enforced
  for seed/smoke scripts, but nothing production-facing depends on that
  script's output being replica-journaled.
- **F3, F4 (accepted, both nits)** — Go's `time.Parse(time.RFC3339, …)`
  is marginally stricter than RFC 3339 §5.6 itself (rejects lowercase
  `t`/`z`); irrelevant for the only real consumer (`buildJournal` always
  formats with Go's own `time.RFC3339`). The rejection error names the
  offending currency but not the offending `created_at` value — a small
  diagnosability gap, not a correctness issue. Both accepted as-is per
  process-depth guidance (fix what matters, not every nit).

### Explicitly checked and confirmed fine (from the review's own report)

- **The `created_at` corruption claim, proven empirically, not just by
  reading**: reviewer temporarily removed the guard, ran a probe through
  `applyJournal` with `created_at=""` and `created_at="not-a-timestamp"`,
  and confirmed both persisted verbatim into `sales.created_at`
  (`applied=true`, no error) while `completed_at` retained the real time.
  `created_at TEXT NOT NULL` does not catch this — `""` is not `NULL`.
- **Blast radius of tightening `created_at` to required**: traced
  `CompleteSale`'s `time.Now().UTC().Format(time.RFC3339)` stamp through
  to the only production insert path (`InsertSale`); the schema's
  `datetime('now')` column default is never hit by a real insert;
  `LocalSalesSince` excludes journaled-in sales (`till_id != ''`), so a
  provenance-stamped row can never be re-pushed. No well-behaved replica
  can send a non-RFC3339 `created_at`.
- **Scope-out of `cashier_id` and line-level fields** — read
  `validateLine`/`computeSaleTotals`/`netPayments` directly and confirmed
  all reached, and all executed, before `CompleteSale`'s transaction
  opens; `cashier_id` trust matches the contract's documented Security
  section.
- **Test quality / no false-passes**: `TestApplyJournal_AcceptsEmptyCurrency`
  mutation-tested by dropping its guarding clause — fails with the
  expected rejection, confirming it isn't a tautology.
- **Two recurring pipeline bug classes**: N/A — no file I/O in this diff.
- **Repository pattern / money / secrets / demo data**: no SQL outside
  `internal/data`; no money-type misuse; rejection text is an operator/API
  diagnostic (`http.Error`/`fmt.Errorf`), not user-facing UI, so no i18n
  requirement; no secrets; test literals are generic (`Apple`, `till-1`,
  `T2-R903`).
- **Contract doc accuracy**: version bump, changelog entry, `422` status
  code, and per-field notes all verified to match the code.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on both changed files —
  clean (implementer and reviewer, independently, both before and after
  the F1 fix).
- `go test ./internal/pages/ -run 'TestApplyJournal|TestSyncSalesAPI|
  TestBuildJournal|TestJournalSale' -v` — all pass, including the 4 new/
  updated cases.
- Whole-repo `go test ./...` — green (implementer, after the F1 fix).
- `bash scripts/ci/guard-data-access.sh` /
  `guard-kiosk-engine.sh` / `guard-plugin-menu-read.sh` — all pass (no
  self-order route, no plugin-menu read touched).
- TDD claim re-verified independently by the reviewer for the original
  two guards (revert → fail with the exact message → restore → pass), and
  by the implementer for the F1 fix specifically (same pattern).

## N/A for this diff

No i18n strings (backend-only sync boundary, not UI), no `web/help/`
manual topic (no page/route changed), no money-type conversion beyond
what already existed, no plugin manifest change, no self-order/kiosk
route.

## Safe to merge

Yes, after the F1 fix above. F2–F4 accepted as noted, not blockers.
