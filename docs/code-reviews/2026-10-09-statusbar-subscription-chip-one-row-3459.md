# Review: status-bar subscription chip stays on one row (ut-docs#3459)

**Scope:** `web/public/app.css` (`.sb-subscription`), a comment in
`web/ui/partials/subscription_chip.html`, new e2e spec
`e2e/tests/statusbar-subscription-chip-one-row-3459.spec.ts`. CSS only: no
Go, no migration, no `en.json` key (no language-pack PRs), no help topic.

**Built by:** Sonnet 5.5 (easy card). **Reviewed by:** Opus 5.5 subagent
(read-only), plus the orchestrator's own red/green runs.

## What shipped
- Chip cap 16rem -> 14rem (border-box: the cap is the chip's whole width;
  272 px -> 238 px at the kiosk's 17 px root).
- The 2.25rem touch target moved from `min-block-size` to a `::before` hit
  area centred on the chip. A 2.25rem chip made the whole bar ~15 px taller
  the moment it appeared.
- The bar keeps wrapping when genuinely crowded. A `nowrap` at >=1024 px was
  tried and rejected: with update + register + marketplace chips showing, the
  version label overflowed the bar by ~22 px (#3050).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | `::before` was a fixed `.4rem` outset: hit area ~34.5 px, not 36+; the spec passed by 0.3 px | Fixed: `inset-block: calc(50% - 1.125rem)` (exactly 2.25rem); spec hit-tests at +-(1.125rem - 1px) with the live rem |
| 2 | minor | Calibration read rem on about:blank (16 px) and double-counted padding (border-box); two errors cancelled | Fixed: rem read after `goto`, gap from computed style, no +16 |
| 3 | minor | `test.skip(filler < 0)` could silently skip the key scenario | Fixed: optional chips are hidden before measuring; a hard `expect(filler).toBeGreaterThan(0)` |
| 4 | minor | Hit area reaches into the next row's gap where the bar wraps | Accepted, stated in the CSS comment |
| 5 | nit | `flex: 0 1 auto` on the mount is the default | Removed |
| 6 | nit | Stale "never wraps" comment in the chip template; "36px" in the CSS comment | Fixed |
| 7 | nit | "today's real label" used a German string | Fixed: the shipped English strings |

## Verified beyond the review
- Red before / green after, same spec, old CSS restored: 2 failures
  (`Received: 272` = the card's number; and the 14rem-vs-16rem scenario).
  New CSS: 5/5 (6 with the extra label) green.
- 82 related specs (status bar, nav rail, showmodal, phone drawer,
  diagnostic mode, bug-report sheet, cloud-reachability, basket
  no-horizontal-scroll) green on the final CSS.
- `go test ./internal/pages`, `guard-i18n.sh`, `guard-compliance-claims.sh`
  green.
- Driven in Chromium at 1024x600 and 600x900.

## Verdict
Safe to merge.

## Deferred
- Where the bar genuinely wraps (many chips + a long label) the chip still
  ellipsizes at 14rem but the bar is two rows. Intended (#3050); a smarter
  priority layout would be its own card.
