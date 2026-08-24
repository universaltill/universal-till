# Code review: wire internal/barcode into scan-path lookup and AddBarcode (ut-docs#934)

**Date:** 2026-08-24
**Author:** autonomous pipeline (Dev: Fable, subagent; Review: Opus, worktree-isolated subagent — `complexity:hard` per the scrum-master skill's model routing)
**Issue:** universaltill/ut-docs#934
**Design of record:** `ut-docs/adr/0059-barcode-symbology-registry.md` (Decision §2, §3)

## What shipped

ADR-0059 §1's `internal/barcode` registry/parser (ut-docs#933) is now wired
into the two places that decide what a scanned/entered code means:

- **Shop-enabled-symbology storage**: reused the existing `settings`
  key/value table (`SettingsRepo`) — key `barcode_enabled_symbologies`,
  JSON array of registry ids — instead of a new migration. `GetOrCreate`
  seeds the ADR §2 default set (every non-embedded symbology) on first
  read, so an untouched shop's settings row appears exactly when it's
  first needed and never blocks a scan on a settings-read failure
  (offline-first: the accessor returns the default set alongside any
  error).
- `internal/data/catalog_repo.go` — `AddBarcode`'s untyped-inference path
  now calls `barcode.Default().Match` against the shop's enabled set
  instead of the old EAN13-or-CODE128 rule; stores the canonicalised
  symbology id and the decoded `LookupKey` (a no-op for plain symbologies,
  the zeroed template for the two embedded ones). New
  `ErrBarcodeNoSymbologyMatch` sentinel for the named-rejection case. The
  explicit `EAN13` path is unchanged.
- `internal/data/pos_repo.go` — new `ResolveScanLine`/
  `ResolveShortcutLineDecoded`: the variant/item tiers now match through
  the registry and look up the decoded `LookupKey`; shortcut/SKU/name
  tiers stay on the raw code (ADR-0059 Non-goals — `shortcut_buttons` is
  out of scope).
- `internal/ui/buttons.go` — `PriceResolverAdapter` maps a weight decode
  onto `BasketLine.Qty` and a price decode onto `PriceCents`/`Qty=1`, via
  two new `BasketLine` flags (`QtyFromCode`, `NoMerge`) rather than
  changing the `PriceResolver` interface — the `/api/pos/scan` handler and
  both engines (cashier, self-order kiosk) needed no changes.
- `internal/pos/service.go` — `mergeResolved`/`scanQty` respect
  `QtyFromCode`/`NoMerge`: a price-embedded scan always appends a new
  line instead of merging into an existing same-SKU line (the money bug
  ADR-0059 §3's "Revised after review" note flags — `mergeResolved`
  overwrites `PriceCents` on merge, which would silently drop one
  label's price).
- `internal/pos/hold.go` — `QtyFromCode`/`NoMerge` round-trip through
  hold/resume snapshots.

Out of scope, untouched: the settings checklist UI (#935), `internal/catimport`
wiring (#936), and confirming which EAN-13 `20`-`29` convention real German
scale hardware uses (ADR-0059 flags this as unconfirmed and explicitly not
this card's job — implemented against the ADR's stated default, weight-
embedded). A new Backlog card tracks the hardware confirmation separately.

## Independent review (Opus, worktree-isolated subagent)

**Initial verdict: PASS WITH FIXES NEEDED.** Ran the full gate itself
(`gofmt`, `go build`, `go test ./...`, both CI-blocking guards) — all green
independently. **Independently re-verified two of the highest-value TDD
claims** by reverting the fix in the worktree and re-running the tests:

- Removed the `NoMerge` handling from `mergeResolved` — all four related
  tests failed, reproducing the exact money bug: two price-embedded scans
  (€3.50 then €7.20) merged into `Qty:2, PriceCents:7.20, LineTotal:14.40`
  instead of two separate lines totalling €10.70.
- Disabled the weight `ParseFloat` decode wiring in `buttons.go` — both the
  service-level and the `internal/pages` end-to-end test failed as claimed.

Restored both; suite green again. Tests confirmed real, not tautological —
`internal/pages/pos_scan_barcode_test.go` in particular exercises the real
migrated DB, real resolver, and real HTTP handler, with a deliberate decoy
item proving specificity-order matching (a weight-embedded label's full code
also happens to be a different item's plain barcode; the decoy must NOT win).

### Findings and disposition

**F1 (HIGH, fixed as a documented guard, not a behaviour change)** — once a
shop enables a `20`-`29`/`02`-prefixed embedded symbology, `AddBarcode`'s
untyped-inference path will classify *any* check-digit-valid EAN-13 in that
prefix range as embedded-data first (correct, specificity-order behaviour
per ADR-0059 §3) — including a genuine plain retail product that happens to
share the prefix — and no production UI caller passes an explicit
`BarcodeType` to escape it. **Fix applied**: a detailed in-code comment at
the exact decision point (`catalog_repo.go`) spelling out the risk and
requiring #935 (or a fast-follow) to give the operator an escape hatch
before a shop can actually reach this state. **Not exploitable today**:
`SetEnabledBarcodeSymbologies` has no non-test caller yet (verified), so
nothing in production can enable the affected symbologies until #935 ships
— the fix's job is to make sure #935 can't ship without addressing this,
not to solve a catalog-form UX question inside this backend-wiring card.

**F2 (MEDIUM, fixed)** — `ResolveScanLine` tried only the decoded
`LookupKey` and returned false on a miss, which would make a shop's
pre-existing full-digit `2…`/`02…` EAN-13 barcodes unscannable the moment it
enabled the corresponding embedded symbology. Fixed: added a raw-code
fallback, tried only after the zeroed-key tiers miss — so it never shadows
a genuine scale-label row (which already matches at the LookupKey tier and
returns before the fallback runs; the decoy test is unaffected, verified by
re-running it).

**F3 (MEDIUM, fixed)** — ADR-0059 §3's "named rejection" (say what was
scanned and what's enabled) reached the log but not the operator's screen —
`FriendlyBarcodeConflict` collapsed `ErrBarcodeNoSymbologyMatch` into the
generic `catalog.error.barcode_attach_failed`. Fixed: new locale key
`catalog.error.barcode_no_symbology_match` in all four locale files, and a
dedicated branch in `FriendlyBarcodeConflict` (shared by both `AddBarcode`
call sites in `internal/pages/catalog/handlers.go`) ahead of the generic
conflict-error path.

**F4 (LOW, fixed)** — a comment claimed the default-set untyped-inference
reproduces old behaviour "exactly," which is false for 8/12/14-digit codes
(now typed `EAN8`/`UPCA`/`GTIN14` instead of falling through to `CODE128`).
No functional impact (`barcode_type` is write-only, `LookupKey == code` for
all plain symbologies), but misleading. Fixed the comment and added an
EAN-8 case to `TestAddBarcode_NoSettingsRowSeedsDefaultAndInfersAsBefore`
pinning the drift as intended.

**F5 (LOW, informational)** — the card title says "barcode_type becomes
read"; it stays write-only by design (re-deriving the symbology at scan
time is more correct than trusting a stored value that could go stale if a
shop changes its enabled set). No change needed.

**F6 (LOW, deferred)** — `BarcodeExists`/`DeleteBarcode` pre-checks in
`cloudsync_wire.go`/`import_page.go` still key on the raw code, which can
disagree with what `AddBarcode` actually stores for an embedded-data match.
Not corrupting (the transactional guard in `addBarcodeInTx` still holds —
this only affects a pre-check's accuracy and error messaging), and reachable
only once #935 lets a shop enable an embedded symbology, same as F1. Noted
as a real but non-blocking follow-up rather than expanding this diff into
`cloudsync`/import territory; a Backlog card is filed alongside F1's note.

**F7 (LOW, fixed)** — `mergeResolved`'s combine branch didn't propagate
`QtyFromCode` onto the surviving line. Currently inert (nothing reads it
off `s.lines`), but fixed for correctness/clarity — a merged weight-embedded
line is still code-derived, not operator-typed.

**F8 (LOW, fixed)** — an unreachable-in-practice `ParseFloat` error branch
in `buttons.go` silently fell back to the caller's qty with no trace; added
a log line. A redundant qty-then-overwrite pattern in
`AddLineWithModifiers` simplified to match the `if !QtyFromCode` idiom
already used in `scanQty`.

**F9 (LOW, deferred)** — two test-coverage gaps noted by the reviewer (a
missing-`settings`-table offline-first case, verified correct by manual
probe but unpinned; an exact-float-equality assertion in
`service_embedded_test.go` that should use an epsilon like the
`pos_scan_barcode_test.go` sibling does). Left as documented gaps rather
than expanding this already-large diff further — real-but-accepted per the
`tester` skill's own guidance on coverage gaps.

## Verified beyond automated tests

- Full gate re-run by the orchestrator after every fix batch (not just once
  at the end): `gofmt -l .` clean, `go build ./...` clean, `go test ./...`
  full suite green (41 packages), `guard-data-access.sh` and
  `guard-i18n.sh` both pass.
- Every other CI-blocking guard from `universal-till/CLAUDE.md`'s list run
  explicitly: `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
  `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-makefile-version.sh`,
  `check-brand-assets.sh` — all pass.
- `guard-docs-shots.sh` initially failed (this diff touches
  `internal/pages/common/barcode_conflict.go`, inside the guard's tracked
  surface even though the change is backend-only, no template/markup
  touched). Ran `make docs-shots` for real (92 Playwright screenshots
  across 23 topics × 4 locales, pre-installed Chromium) — all passed, one
  incidental pixel diff (`tr/invoices.png`, unrelated to this change) —
  and `web/help/img/manifest.json` refreshed; guard now passes.
- No feature is operator-reachable yet (settings UI is #935), so no manual
  screenshot/behaviour change was needed beyond the docs-shots freshness
  regen above; `web/help/` prose is unaffected.
- No real client/shop name used as test data (test fixtures use "Scale
  Test Shop" and generic product names); no secret-shaped literal in the
  diff.

## Safe-to-merge verdict

**Safe to merge.** All ACCEPTANCE CRITERIA (1-6 from the issue) independently
confirmed, plus the reviewer's own from-scratch TDD re-verification on the
two highest-risk money paths. All findings from the independent review are
either fixed in this diff (F2, F3, F4, F7, F8) or explicitly, narrowly
deferred with a documented reason and a Backlog follow-up (F1's UX escape
hatch and F6, both genuinely blocked on #935's settings UI existing at all;
F9's two test-coverage gaps).

## Explicitly deferred / follow-up work

- New Backlog card: give the catalog barcode-entry UI a way to force the
  plain interpretation of a code that would otherwise match an enabled
  embedded symbology (ties F1 and F6 together) — must land before or
  alongside #935.
- #935 (settings checklist UI) and #936 (`internal/catimport` wiring) —
  unchanged, as originally scoped.
- Confirming the real German scale-hardware `20`-`29` prefix convention
  (weight vs. price) — new Backlog card, `blocked:env` (needs real
  hardware/a human at a machine), tracked separately from this card per
  ADR-0059 §1.
