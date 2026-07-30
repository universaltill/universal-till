# Code review — coverage batch 4: `internal/ui` (26.7% → 96.4%)

- **Date**: 2026-07-30
- **Branch/PR**: `test/coverage-internal-ui`
- **Author**: SDLC pipeline (fable), cycle 10
- **Independent reviewer**: different model (opus), full re-verification
- **Verdict**: **SAFE TO MERGE — no MUST-FIX or SHOULD-FIX findings**

## Scope

Fourth batch of the [QUEUE.md] test-coverage push. `internal/ui`
(basket/journal view constructors, the shortcut-button store + HTTP
handlers, the price-resolver adapter — all on the live sale screen and
Designer paths) from 26.7% to **96.4%** of statements. All new tests
hermetic: in-memory sqlite, embedded templates, `httptest` recorders;
suite passes with `HTTP(S)_PROXY` poisoned to a dead port; `-race`
clean; all 5 CI guards green.

**Batch-pick honesty note**: the other named target,
`internal/plugins/storage` (39.3%), was inspected first and found to be
**dead code** — `CacheStore` has zero importers (the live download path
is `internal/plugins/download_manager.go` + `installer_store.go`, with
their own `.part` handling). Driving dead code to a high number would
be coverage theater, so it was skipped and logged in QUEUE.md as its
own decide-remove-vs-wire item (pairs with the plugin_api
ghost-install decision).

## Real bugs found TDD-first (both medium)

1. **Designer add-button broke for any item name containing a double
   quote.** `web/ui/partials/buttons_admin.html` interpolated raw
   name/barcode/image fields into a JSON literal inside the `hx-vals`
   attribute. `html/template` escapes `"` as `&#34;`; the browser
   decodes that back to a literal `"` inside the JSON string before
   htmx calls `JSON.parse` — invalid JSON, so tapping the search
   result silently posted no fields and the add failed with 400.
   Proven red first (`internal/pages/buttons_search_vals_test.go`
   decodes entities then JSON-parses, exactly what browser+htmx do;
   observed: `invalid character 'B' after object key:value pair` on
   `{"label":"5" Blade",…}`). Fixed by marshaling server-side
   (`ui.SearchResult.AddVals()`) and templating `hx-vals='{{ .AddVals }}'`.
2. **Designer search 500'd whenever any matching item had no barcode**
   (loose produce, services). `POSRepo.SearchItemsForShortcuts`
   scanned a nullable barcode subquery into a plain `string` —
   observed red: `Scan error on column index 2, name "barcode":
   converting NULL to string is unsupported`, which failed the WHOLE
   result set, surfacing as a 500 from `/api/buttons/search`. Fixed
   with `COALESCE(…, '')` in `internal/data/pos_repo.go` (repository
   layer — the only place SQL may change, per repo rules).

## Also shipped

- Removed dead unexported helpers `currentPrice`/`deref`/`nullIfEmpty`
  from `internal/ui/buttons.go` (zero callers, compiler-verified;
  `data`'s own `nullIfEmpty` is a separate copy, untouched).
- `ButtonStore.Save` pinned by test and noted as having **no
  production caller** today (the Designer adds/removes/reorders one
  button at a time).

## Tester false-pass hardening (mutation probes)

Author probes, all caught: modifier-flag map keyed wrong (`b.Code`
instead of `b.ItemID`) → caught; image-normalization dropped → caught;
`AddVals` marshaling the wrong field → caught at BOTH the unit and the
endpoint layer; `COALESCE` reverted → caught. (First modifier-flag
probe variant hit a compile error, not a test — re-run with a
compiling mutation before counting it.)

## Independent review (different model, opus) — what it verified

- **Re-proved both TDD arcs itself** by isolated reverts: exact
  claimed failure modes reproduced, then green on restore.
- **3 fresh mutation probes, 3/3 caught**: `AddVals` label→barcode
  swap; `ORDER BY ib.is_primary DESC→ASC` (primary-barcode preference);
  resolver `Qty: 1→2`.
- **Validated the honesty disclosure**: collapsed the resolver's rungs
  1–3 entirely — all tests still pass, confirming the 4-rung wrapper
  chain in `PriceResolverAdapter.Resolve` is behaviorally redundant
  over `POSRepo.ResolveShortcutLine`'s own internal fallthrough (an
  accurate author disclosure, not a hidden coverage gap).
- Re-ran build/vet/tests/`-race`/poisoned-proxy/guards independently;
  confirmed template change has no other consumers and no e2e spec
  covers the designer search flow; money-type usage in tests correct.

### Reviewer findings (both NITPICK, non-blocking, logged to QUEUE.md)

1. **Same latent hx-vals pattern in sibling templates** —
   `catalog_variants.html`, `suggestions.html`, `self_order_grid.html`,
   `basket.html` still interpolate raw fields into `hx-vals` JSON
   literals (low real risk: codes/SKUs, not free-text names). Backlog:
   apply the same server-side-marshal pattern.
2. **Redundant resolver rung chain** — refactor candidate, deliberately
   not restructured inside a coverage batch.

## Honestly-untestable remainder (documented, not faked)

- `Resolve` rungs 3–4 success returns: structurally unreachable — every
  repo hit carries a non-empty `ItemID` (all queries `JOIN items`), so
  rungs 1–2 always claim a hit first. No fake fixture with an `''`
  item id was written to force them.
- `Load`'s swallowed `ItemIDsWithModifiers` error (`hasMods, _ =`): a
  DB failure silently renders tiles without the customize flag —
  noted; behavior choice predates this batch.

## Addendum — third real bug, found by the PR's own CI gate (medium)

After the main review, the PR's playwright job failed **twice,
deterministically**, on `settings-osk.spec.ts` + `ui-scale-basket.spec.ts`
while main stayed green on identical runner images and the suite passed
locally AND in a matching Linux container. Root-caused, not waved off as
flake:

- **The product bug**: the sale screen's scan input has `autofocus`; on
  slow machines (busy CI runners — and the real field Pi) it gains focus
  BEFORE the deferred `osk.js` attaches its `focusin` listener, and a
  click on an already-focused input fires no focus events — so with OSK
  forced on, the keyboard silently never appeared for the very first
  field until focus left and came back. Empirically proven both ways:
  on fast machines `#osk` is already open right after load (the
  autofocus event itself lands after listener attach), which is the only
  reason the old spec ever passed. Fixed in `web/public/osk.js`: at
  init, catch up with `document.activeElement` (guarded by the same
  `enabled`/`wantsOSK` conditions as the focusin handler).
- **The cascade**: the failed OSK spec never reached its inline
  `osk=auto` restore, so `ui-scale-basket` ran with the keyboard
  covering its submit button and its scan click hung. Fixed by moving
  the restore to `test.afterEach` (runs on failure too).
- **Deterministic regression test**: new spec delays `osk.js` by 700ms
  via `page.route` so autofocus always wins the race, then asserts the
  keyboard appears with NO click. Proven red against unfixed osk.js
  (exact CI failure mode), green with the fix.

Independent addendum review (opus): **APPROVE ADDENDUM**, no MUST/
SHOULD-FIX — re-proved the red/green arc itself with retries off, ran
the full 20/20 suite, verified `show()` idempotence (double-show safe,
`inputmode` save guarded), auto/non-touch short-circuit, kiosk/Android
IME-suppression path unchanged, and no leftover servers. One cosmetic
nitpick (unroute placement) accepted as-is.

## Adjacent findings logged to QUEUE.md (not chased here)

- `internal/plugins/storage` dead package (above).
- `/api/buttons/search` writes a hardcoded English hint string
  (`"Type 3+ characters"`) from Go — invisible to `guard-i18n.sh`,
  which only scans templates/locales; Go-side string audit is a
  guard-gap follow-up.
- Barcode-less items are now searchable but still un-addable as tiles
  (`shortcut_buttons` is barcode-keyed; Add requires a code) — UX gap.
