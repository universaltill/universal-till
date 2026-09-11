# Code review: sale/refund/import/sync draw stock from the register's assigned location (ut-docs#2067)

**Card:** universaltill/ut-docs#2067 — "Multi-location: every sale/refund/
import hardcodes the 'Main' stock location — no per-till or per-sale
location choice"
**Complexity:** hard (Dev: Fable subagent; Review: Opus subagent, fresh
context, independent from the Dev pass)

## What the card asked for vs. what was actually needed

BA/Architect passes (this cycle, recorded on the issue) found the
apparent scope — "design a till/register-to-location mapping" — was
already half-built and undocumented as such: `registers.location_id` has
existed as a real column (FK to `stock_locations`) since the schema's
first migration, Settings → Registers has let a manager assign it since
ut-docs#651/#895/#897/#903, and `pos.ResolveTillRegisterID`
(ut-docs#268) already reliably resolves "this till's own register
identity." Nothing anywhere actually *read* `location_id` on a write
path — every stock-mutating call site called
`data.POSRepo.EnsureStockLocation(ctx)` unconditionally, which always
resolves the hardcoded "Main" location. So the real, narrower fix was:
read the mapping that already exists, at each of the five call sites the
card named.

## What shipped

- `internal/data/pos_repo.go`: new `RegisterLocationID(ctx, registerID)`
  (a register's assigned, still-active location, if any); additive
  `SaleDetail.RegisterID` field (`json:"register_id,omitempty"`, read via
  `COALESCE`) for the sync path below.
- `internal/pos/stock_location.go` (new): `ResolveStockLocationID(ctx,
  sqlDB, registerID)` — the register's location if set and active, else
  `EnsureStockLocation` (today's Main fallback, unchanged for every shop
  that never touches Settings → Registers).
- `internal/pages/register_stock_location.go` (new):
  `tillRegisterIDBestEffort` — calls `pos.ResolveTillRegisterID`,
  tolerates `ErrRegisterIdentityAmbiguous` by falling through to Main
  (matches `settings_page.go`/`shifts_page.go`'s existing tolerant
  pattern on these same read/display paths, not `shifts_api.go`'s
  stricter 409-refusal on a shift-open write) rather than refusing the
  sale outright.
- Wired into `pos_api.go` (tender), `self_order_shop.go` (kiosk),
  `refund_page.go`, `import_page.go`, `sync_sales.go` (applies a remote
  till's synced sale using *that* till's `register_id`, never copied onto
  the replayed sale's own `register_id` column — keeps the existing
  1.7.0 FK-quarantine mechanism intact for an unknown remote register).
  `sync_admin.go`'s own separate `EnsureStockLocation` call (replica
  pulling aggregate stock-level corrections) is untouched — a different
  mechanism, out of scope.
- `web/help/en/inventory.md`: corrected the "sales always draw from
  Main" line ut-docs#330 added, now that a register with an assigned
  location changes this.
- `web/help/img/**` + `manifest.json`: regenerated via `make
  docs-shots` (pre-installed Chromium at `/opt/pw-browsers`,
  ut-docs#622's cloud-session fallback) — a handful of PNGs whose
  render drifted since the last capture (catalog/sell/reports/invoices/
  till-designer, across topics unrelated to this change) plus the new
  surface hash.
- `ut-docs/reference/contracts/pos-lan-sync-journal.md` bumped to 1.9.0,
  documenting the new optional `sale.register_id` journal field and its
  degradation table (committed separately in the ut-docs repo, same
  logical change).

Tests: `internal/data/pos_repo_register_test.go`,
`internal/pos/stock_location_test.go` (new),
`internal/pages/register_stock_location_test.go` (new), plus additions
to `pos_api_test.go`, `self_order_shop_test.go`, `refund_page_test.go`,
`import_page_test.go`, `sync_sales_test.go` — a regression case (no
location ever assigned → still Main, byte-identical) and a positive case
(register pinned to a second location → that location, `inventory` and
`stock_movements` both asserted, Main asserted untouched) for every one
of the five call sites.

## Independent review (Opus, fresh context, read-only pass)

Re-ran `gofmt`, `go build`, `go vet`, and the full `internal/pos`/
`internal/data`/`internal/pages` suites personally rather than trusting
the Dev report (3502 pages subtests, 0 failures). Read every changed/new
file, not a summary. Specifically checked, and confirmed:

- No dangling reference or panic on a retired/deactivated location —
  `RegisterLocationID` degrades cleanly to `("", false, nil)`.
- No transaction hazard: none of the five call sites call the new
  resolver from inside an open `Tx` (`import_page.go`'s own `BeginTx`
  starts well after its resolution call).
- No concurrency hazard on `ResolveTillRegisterID`'s first-boot
  self-heal: a losing concurrent `INSERT` hits the existing UNIQUE
  constraint, falls back to Main for that one resolution, and the sale
  still completes (each call site's own pre-existing `EnsureRegister`
  fallback finds the now-existing row).
- The FK-quarantine safety claim is true by direct reading: `SaleInput`
  has no `RegisterID` field, and `sync_sales.go`'s `applyJournal` never
  sets one from `j.Sale.RegisterID` — only feeds it to the new resolver.
  `pos-lan-sync-journal.md` 1.9.0 matches the code exactly.
- `sync_admin.go` is correctly out of scope — `ApplyStockLevels` reads
  `DumpStock`'s shop-wide `SUM(quantity) GROUP BY item` on the primary
  side, so a per-location split on the write paths cannot desync its
  arithmetic.
- Zero test deletions/weakenings across the diff (`git diff --numstat`:
  every test file is pure addition).
- No i18n gap (no new user-facing string — the existing Settings →
  Registers picker is reused), no repository-pattern violation
  (`guard-data-access.sh` green), no money arithmetic touched.

**One real defect found, not caught by the Dev pass's own verification:**
`guard-docs-shots.sh` failed on this branch (passed on a clean `main`
worktree used to confirm it wasn't pre-existing) — the rewritten
`inventory.md` and the new no-route `register_stock_location.go` both
correctly changed the guard's tracked surface hash, and the screenshots
had gone stale. Fixed in this same session: ran the real `make
docs-shots` (this cloud session has a pre-installed, smoke-launchable
Chromium via ut-docs#622's fallback, unlike the review subagent's own
sandbox, which had none) and committed the regenerated output. Guard now
green.

**One deliberate, reviewed scope deviation — approved, and actually
fixes a latent bug**: the original brief assumed a live tender request
already carries `RegisterID`; it doesn't (no UI template sends
`registerId`). Instead of leaving that dead, `pos_api.go`/
`self_order_shop.go` now resolve `registerID` via
`tillRegisterIDBestEffort` and use *that same id* for both the stock
location **and** the `sales.register_id` column stamped on the sale
row — previously the latter came from `EnsureRegister`'s
`ORDER BY id LIMIT 1`, which disagrees with `shifts.register_id`
(already resolved via `ResolveTillRegisterID`) on any shop with 2+
registers and a persisted till identity whose register doesn't happen to
sort first. Since `ComputeExpectedCash` tallies sales
`WHERE register_id = <the shift's register>`, this mismatch was silently
under-counting expected cash on a Z-report for exactly that shop shape.
Traced and confirmed by direct reading, not assumed; the 0/1-register
and ambiguous-identity cases are byte-identical to before.

**Two non-blocking findings, deliberately not fixed here — filed as
follow-ups rather than silently expanding this diff:**

1. **Replica per-location drift**: a replica pinned to a non-Main
   location decrements that location locally, but `sync_admin.go`'s
   corrective `adjust` movements from the primary all still land at
   Main (the primary is authoritative and the shop-wide total stays
   correct, so this is not a money bug — just a per-location view that
   can drift on the replica). Fixing it needs a per-location stock sync
   bundle, a bigger contract change than this card scoped.
2. **A refund restocks at the *refunding* till's location, not the
   *selling* till's** (sell at a Warehouse-pinned kiosk, refund at a
   Main-pinned counter → Warehouse permanently short over time). Both
   the selling and refunding register are in-scope data on the refund
   path, so "restock where it was sold" was technically available; the
   chosen "restock where the refund is being processed" is defensible
   (the goods are physically back at the counter) but is a product
   decision, not an implementation detail, and should be confirmed by
   the product owner rather than inherited silently. Documented in the
   manual as shipped; flagged here for a follow-up card if the product
   owner wants the other semantics.

## Verified beyond automated tests

- `gofmt -l .` clean; `go build ./...`, `go vet ./...` clean.
- `go test ./internal/pos/... ./internal/data/... ./internal/pages/...`
  — all green, re-run personally on the final tree (after the
  docs-shots fix) as well as by the Dev and review passes independently.
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-help-topics.sh`,
  `bash scripts/ci/guard-help-drift.sh`,
  `bash scripts/ci/guard-docs-shots.sh`,
  `bash scripts/ci/guard-compliance-claims.sh`,
  `bash scripts/ci/guard-page-http-error.sh` — all green on the final
  tree.
- `guard-deadcode-baseline.sh` could not run in this sandbox (needs
  `-tags=desktop` cgo packages, gtk+-3.0/webkit2gtk, not installed here)
  — every new function has a real caller in the diff; left for CI's own
  run to confirm.

---
_Generated by [Claude Code](https://claude.ai/code)_
