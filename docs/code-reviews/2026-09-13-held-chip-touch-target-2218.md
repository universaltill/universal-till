# Review: held-chip touch target (ut-docs#2218)

## What shipped

`.held-chip` (`web/public/app.css`) raised from `min-height: 0` to
`min-height: 44px`, matching this codebase's established touch-target
floor (`.modifier-option`, `.catalog-detail-title .thumb`). Padding and
font-size are unchanged, so the chip keeps its compact visual footprint —
only the touch target grows.

Unlike the superficially similar ut-docs#1340 (basket qty/discount
inputs, where raising the height was rejected because of a hard ">=4
rows visible" acceptance criterion), `.held-chip` lives in `.held-strip`
(a `flex-wrap` row) inside `.tender-scroll` (a free-scrolling container
with no fixed row-count constraint), so nothing depends on this chip
staying short.

New regression test: `e2e/tests/held-chip-touch-target-2218.spec.ts`,
modeled on the existing `held-strip-scroll-affordance-2128.spec.ts` (same
hold-a-sale flow). Asserts the chip's real `getBoundingClientRect().height`
is `>= 44` at both 1280x800 (pilot device) and 1024x600 (kiosk floor).

`web/help/img/manifest.json`'s `surface_sha256` was refreshed via
`scripts/ci/update-docs-shots-surface-hash.sh` (the documented escape
hatch), not a full `make docs-shots` regeneration — confirmed (by both
Dev and the independent review, separately) that no held sale is ever
created before any of the 31 documented topics' screenshots are taken, so
`.held-strip`/`.held-chip` cannot appear in any shipped screenshot and no
rendered pixel actually changed.

## What the independent review found

Fresh-context Sonnet subagent, isolated worktree, ran the full change
independently (not a rubber stamp — verdict PASS):

- Read `CLAUDE.md`'s non-negotiables (repository pattern / money / i18n /
  offline-first / plugin signing) — confirmed none apply to a pure-CSS +
  test-only change.
- `go build ./...` / `go vet ./...` — clean.
- `guard-docs-shots.sh`, `guard-i18n.sh`, `guard-help-topics.sh` — all
  pass.
- Independently re-verified the escape-hatch claim by reading
  `e2e/tests-docs/docs-shots.spec.ts` directly and confirming no topic's
  fixture ever holds a sale.
- **Independently re-verified TDD**, in the isolated worktree (mutating
  `.held-chip` back to `min-height: 0`): the new spec failed for real,
  with measured heights **31.625px** at 1280x800 and **30.1875px** at
  1024x600 (both `< 44`, a genuine assertion failure, not a crash/error).
  Restored the fix, both re-passed.
- Ran `tender-panel-reachable.spec.ts` (10 tests covering held-chip
  scroll-reachability/footer-clipping with 1-3 held sales) as a bonus
  regression check — all 10 pass with the taller chips; no clipping or
  reachability regression.
- Grepped for any Go/JS/TS code reading `.held-chip`'s height —
  none found; the server only emits the class.
- No secrets, no real client/shop names in the diff.
- Skimmed `web/help/en/sell.md` — describes held-sale *behavior*, never
  chip pixel dimensions; no manual update needed.

Two non-blocking notes, both accepted as-is (no action needed in this
PR):
- The new spec's `afterEach` uses the same bare (non-`try/finally`)
  cleanup shape as the `held-strip-scroll-affordance-2128.spec.ts` file
  it was deliberately modeled on — a different, unrelated spec in this
  suite (`tender-panel-reachable.spec.ts`) uses `try/finally` instead.
  Inconsistent across the suite, but not a defect introduced here, and
  the cleanup steps here can't plausibly throw before completing.
- `tender-panel-reachable.spec.ts`'s own existing comment already
  documents that `.held-chip` is not a real hit-test target once
  `.tender-scroll` overflows — a pre-existing scroll-reachability gap,
  unrelated to this touch-target-*height* fix, already tracked under
  ut-docs#2128's own follow-up scope.

## Verified beyond automated tests

- Live screenshot QA at 1280x800 and 1024x600 with up to 3 held sales:
  chips wrap correctly inside `.held-strip`, no clipping/overlap, still
  reads as visually compact.
- Real TDD red→green, independently re-verified by a second, fresh-context
  model instance in an isolated worktree (not just the implementer's own
  claim).

## Safe to merge

Yes. No blocking findings.
