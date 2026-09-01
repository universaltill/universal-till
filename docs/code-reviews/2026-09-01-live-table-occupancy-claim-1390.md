# 2026-09-01 — Live table occupancy claim (ut-docs#1390)

**Card:** universaltill/ut-docs#1390 — "Live table assignments do not
reserve or release table occupancy" (p1, bug, `source:user`,
`complexity:hard`).

**Model routing:** Dev at Fable (per complexity:hard), independent review
at Opus (deliberately not Fable — see `reviewer` skill).

## What shipped

Table occupancy for the LIVE (not-yet-held) basket used to be pure
in-memory state (`pos.Service.tableID`), invisible to
`POSRepo.IsTableFree`/`ListTablesWithState` (which only checked
`held_sales.table_id`) — so a second order could silently take a table
another order already had, and the floor plan didn't light the table up
until the order was explicitly held.

Fix: a new `table_claims` table (migration 077, one row per
currently-claimed table, `table_id TEXT PRIMARY KEY REFERENCES
tables(id)`, no owner/session column — there is exactly one live basket
per till process, so the HTTP handler layer short-circuits a same-table
re-pick before any DB call), with race-free `ClaimTable`/
`ReleaseTableClaim` repo methods (`INSERT OR IGNORE` on the PK — no
check-then-insert window). Wired into every point a live basket's table
assignment can change:

- `/api/pos/table` — claim on assign (rejected with an in-place error
  toast if occupied), release on clear, claim-new-before-release-old on a
  table-to-table move (never a window where the old table reads free
  before the new one is confirmed).
- `/api/pos/order-type` — releases when switching to Takeaway clears the
  table (ut-docs#1355's existing clear rule).
- `/api/pos/reset` and `completeTender` — release on explicit reset and
  on sale completion (captured before `engine.Reset()` clears it).
- `hold_api.go` Hold — releases the live claim once `held_sales.table_id`
  takes over as the occupancy signal (one source per lifecycle stage).
- `hold_api.go` Resume — re-claims before the `held_sales` row is
  deleted, so the table never reads falsely free in the gap; reads the
  claim from the engine post-`Restore`, not `held.TableID`, because
  `Restore` itself re-enforces takeaway-clears-table.
- **Boot sweep** (`internal/pages.Init` → `POSRepo.ClearAllTableClaims`) —
  added after independent review (see below): a table_claims row still
  present at process start belongs to a process that crashed/was killed
  without releasing it, since `pos.Service` always starts empty. Without
  this, a table claimed right before an unclean shutdown had no
  in-product recovery at all.

**Deliberately out of scope**, split into follow-up cards at BA/Architect
time (mirrors ADR-0054's own #814/#820 split): cross-till live-occupancy
proxying (ut-docs#1392 — neither `tables` nor `held_sales` syncs across
tills today, pre-existing and unrelated to this change) and an explicit
manual "Free table / Guests left" recovery action (ut-docs#1393).

## Independent review (Opus, isolated worktree)

**Verdict: safe to merge, no blockers.** Full findings:

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Should-fix | Crash-restart left a table permanently unbookable with no in-product recovery (picker filters it out, both assignment handlers reject a pick on it) | **Fixed** — boot sweep (`ClearAllTableClaims`), added post-review, own TDD cycle (see below) |
| 2 | Should-fix | Turkish manual prose ungrammatical (`"...bekleyen bir **restoranda** siparişini..."`) and didn't use the product's own Turkish for dine-in | **Fixed** — `web/help/tr/tables.md`, `restoranda` → `yerinde tüketim` (matches `tr.json`'s `import.status.tax_takeaway_only`) |
| 3 | Nitpick | Residual TOCTOU: `ClaimTable` doesn't re-check `held_sales` between the `IsTableFree` read and the insert | Accepted as-is — requires two concurrent requests on one till (unreachable in the single-cashier flow); noted for a future tightening (`INSERT … SELECT … WHERE NOT EXISTS`) |
| 4 | Nitpick | "Occupied since" resets to the claim time on Resume (held→resumed loses the original seating time) | Accepted — traced both interleavings, sub-millisecond windows, no user-visible wrong value in practice |
| 5 | Should-fix | Stale doc comment in `table_picker_api.go` directly falsified by this change | **Fixed** — rewritten to record that the self-exclusion is now load-bearing |
| 6 | Nitpick | Pre-existing stale comments in `tables_page.go` (predate #820, unrelated to this diff) | Left alone — filed as a note for a future Backlog card, not fixed here (keeps the review delta minimal) |

Reviewer independently re-derived, not just trusted: the `SetOrderType`
before/after comparison (necessary, not just defensive — `service.go`
clears the table inside the lock, so the handler can't infer it from the
order type alone), the Resume re-claim-from-engine reasoning (`Restore`
re-enforces takeaway-clears-table, so `held.TableID` can go stale),
exhaustive grep for every `Reset()`/`SetTable`/`ClearTable`/
`SetOrderType`/`Restore` call site (confirmed no leaked or lost claim
anywhere, including the kiosk engine, which never touches tables at all),
migration `IF NOT EXISTS` precedent and the replay-test justification for
it, and the two timestamp formats feeding `ListTablesWithState`'s `MIN()`.

## TDD — re-verified independently, twice

1. **Dev's own revert-then-restore** (isolated worktree, per the
   `reviewer` skill's mandatory re-verification): reverting the
   `/api/pos/table` occupancy check reproduced the reported bug verbatim
   — `order 2 must not be assigned the parked order's table, got TableID
   "…"` — then reverting `IsTableFree`'s live-claim check failed
   `TestIsTableFree_LiveClaimOccupiesTable`. Both restored clean, both
   pass again.
2. **The boot-sweep addition** (post-review, orchestrator's own fix): a
   new `TestInit_ClearsStaleTableClaimLeftByUncleanShutdown` (drives the
   real `pages.Init` boot path — not just the repo method in isolation —
   against a DB pre-seeded with an orphaned claim, simulating a crashed
   process) failed with a real assertion (`after restart, T1 must be free
   again... got ok=false`) with the sweep temporarily removed, then
   passed with it restored (file diff-clean after restore, confirmed by
   `git diff --stat`). A repo-level unit test
   (`TestClearAllTableClaims_WipesExistingRowsAndIsSafeOnEmpty`) covers
   the method directly, including the empty-table no-op case.

## Verified beyond automated tests

- Full `go test ./...` (every package, not just the touched ones): all
  green, both before and after the boot-sweep addition.
- `gofmt -l .` empty, `go build ./...` / `go vet ./...` clean.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  directly (not assumed from local tests passing): all pass, including
  `guard-data-access.sh` (repository pattern — all new SQL confined to
  `internal/data`/`internal/db/migrations`), `guard-i18n.sh` (+ its toast
  variant), `guard-docs-shots.sh` (re-run after the boot-sweep addition
  touched `internal/pages/init.go` again post-Dev — the guard correctly
  flagged the surface as stale until `make docs-shots` regenerated the
  manifest a second time), `guard-help-topics.sh`, `guard-compliance-claims.sh`,
  and the rest.
- `make docs-shots`: run twice (once by Dev, once more after the
  post-review boot-sweep fix touched `internal/pages/**.go` again). Both
  runs: only byte-level render jitter on `sell.png` across locales (±20
  bytes) — no new UI element, confirming the occupied-table rejection
  reuses the existing basket toast surface rather than adding a new one.
- i18n: exactly one new key (`basket.table.occupied`) across all four
  locale files, valid JSON, identical key counts. **Disclosure**: the
  self-hosted Ollama NAS (`reference/translation.md`) is unreachable from
  this cloud pipeline session (`192.168.1.231:11434` times out) — the
  ar/fa/tr translations for this key and the manual-topic prose edits
  were written directly, then independently sanity-checked by the Opus
  reviewer for accuracy (one real error found and fixed — see finding #2
  above).
- Repository pattern, money type (n/a — no amounts involved), offline-first
  (every release path logs-and-continues rather than failing a sale;
  nothing here depends on network reachability): all consistent with
  `CLAUDE.md`.
- No real client/shop name, no secret-shaped literal.

## Explicitly deferred (tracked, not silently dropped)

- **ut-docs#1392** — cross-till live-occupancy proxying. Verified while
  designing this card: neither `tables` nor `held_sales` is in the
  admin-managed sync bundle (`internal/data/sync_admin_repo.go`'s
  `adminTables`) — a satellite till's floor plan and held orders are
  already entirely till-local today, pre-existing and unrelated to this
  change. The fix is the same replica→primary live-proxy pattern
  `order_status.go`'s `fetchOrdersFromPrimary` already established for
  the Orders board.
- **ut-docs#1393** — explicit manual "Free table / Guests left" recovery
  action. Now a smaller card than originally scoped, since the boot sweep
  above already closes the one concrete "no recovery path" gap
  (crash-restart); #1393 is the remaining general-purpose manual override
  for a stuck claim outside that case.

## Safe-to-merge verdict

**Yes.** No blockers from independent review; both should-fix findings
were fixed and re-verified; both nitpicks are genuinely low-risk and
documented rather than silently accepted. Full gate green.
