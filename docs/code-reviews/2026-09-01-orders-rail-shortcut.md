# 2026-09-01 — Orders rail shortcut (ut-docs#1349)

## What shipped

The product owner reported twice, live on v0.8.2, that Orders (the
kitchen-progress board, `GET /orders`) had no shortcut on the sale
screen's left icon rail — only reachable via ☰ Menu → Orders.

- `web/ui/partials/nav.html`: new `.nav-toggle` rail button in
  `.nav-primary`, after Inventory — same markup pattern as every
  existing rail item, `nav-rail-only` (hidden at ≤480px, same tradeoff
  as Inventory: the Menu tile already covers phone width, and
  ut-docs#413's phone-width budget has no room to spare). Icon 🛎️
  matches the existing Menu tile's icon (`menu_page.go`'s
  `iconFor["/orders"]`, added by ut-docs#1371). i18n key `nav.orders`
  already existed in every locale (previously unused) — no new
  translation needed.
- `e2e/tests/sale-rail-orders-1349.spec.ts`: 3 tests — rail button
  visible/styled like its siblings, click navigates to `/orders`,
  hidden at phone width with the Menu-tile fallback still present.
- `web/help/{en,fa,ar,tr}/order-status.md`: step 1 now mentions the rail
  shortcut alongside the existing Menu path (all 4 locales).
- `web/help/img/**` + `manifest.json`: regenerated via `make
  docs-shots` — `nav.html` is a shared partial rendered on every page,
  so `guard-docs-shots.sh`'s global surface hash requires a full
  screenshot refresh for this change (88 PNGs across 23 topics × 4
  locales).

No Go code touched — confirmed via `git diff --stat -- '*.go'` (empty).

## Independent review

Fresh-context Sonnet subagent (complexity:easy → Sonnet review per the
`scrum-master` skill's model-routing table), run in an isolated git
worktree. **Verdict: SAFE TO MERGE, no blocking findings.**

Verified independently by the reviewer (not taken on the implementer's
word):
- `gofmt -l .`, `go build ./...` — pass.
- Guards: `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
  `guard-compliance-claims.sh`, `guard-e2e-fixtures-import.sh` — all
  pass, run directly by the reviewer.
- `nav.orders` carries a real, non-placeholder translation in all 4
  locale files (en/ar/fa/tr).
- e2e: the new spec + the two specs most likely to interact with the
  same shared partial (`nav-rail-icon-consistency-1348.spec.ts`,
  `phone-width-layout-413.spec.ts`) — 18/18 passed, no regression.
- **TDD claim independently re-verified**: reverted just the new
  `<a>` block in `nav.html`, re-ran the new spec — 2 of 3 tests failed
  red exactly as expected (the third, phone-width-hidden, passed
  vacuously since the element was simply absent); restored the fix,
  re-ran — 3/3 green, working tree clean.
- Visual check: viewed `sell.png`/`order-status.png` in both `en` and
  `ar` (RTL) — rail renders correctly, no clipping/overlap, RTL mirrors
  correctly, no state where the (longer) fa/ar label is both visible
  and space-constrained (confirmed from the CSS: the label is
  visually-hidden by default and only un-hides at the same ≤480px
  breakpoint where `nav-rail-only` hides the whole item).
- No client/shop demo names or secret-shaped literals in the diff.

**Non-blocking watch-item raised by the reviewer**: this is a genuine
(if small, ~44px) net addition to the rail's vertical budget, on top of
the headroom risk ut-docs#1346 already tracks separately (no headroom
at 1024×600 with a full manager session + all nav-right chips present).
It degrades gracefully today (`.nav` has `overflow-y: auto`, so a tight
rail scrolls rather than clips/breaks), and #1346 is explicitly the
card scoped to that cumulative question — noted here and on the PR so
it isn't silently made worse without acknowledgment, not fixed in this
change.

## Verified beyond automated tests

- `make docs-shots` re-run end-to-end (92 screenshots captured, 88
  changed) against the pre-installed sandbox Chromium
  (`scripts/resolve-chromium.sh`); `guard-docs-shots.sh`'s recomputed
  surface hash (`eab501bdc15d…`) matches the freshly written manifest.
- Full `go test ./...` run (unaffected by this diff, included as the
  standing full-gate pass) — all packages pass.
- Full e2e default-project suite run once (262/263 passed; the one
  failure, `settings-pos-notice-918.spec.ts`'s customer-search-progress
  test, is an unrelated pre-existing flake — reproduced clean in
  isolation, confirmed unrelated to `nav.html`/this diff before
  discounting it).

## Safe to merge

Yes — reviewer's verdict, `reviewer` skill's TDD re-verification done,
full gate green, only a non-blocking watch-item (already tracked on a
separate card).

## Explicitly deferred

- ut-docs#1346 (nav rail vertical headroom at 1024×600) — this change
  is a small contributor to that risk, noted above; not addressed here,
  stays its own card.
