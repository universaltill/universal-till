# Review — on-screen quick-button reorder refuses a shared tile code (ut-docs#6708)

Date: 2026-11-23 · Lane: lane:cloud-41 · Branch: `fix/6708-shared-code-reorder`
Author: Sonnet 5.5 (Dev subagent) · Reviewer: Opus 5.5 (independent subagent) · Tester: Opus 5.5 (separate subagent)

## What shipped
`shortcut_buttons` rows are keyed by code, so two tiles can share one row. The till's own
reorder (`POST /api/buttons/reorder`: Designer and the sale screen's jiggle Done, full list
and `scope=subset`) wrote `sort_order` by code and moved the twin tile to the end of the grid.
- `ButtonStore.updateOrderWith` (`internal/ui/buttons.go`) refuses before any write when a
  code in the written range (0..last changed position) is held by two tiles in the current
  grid. It returns `sharedLayoutCodeError` (`errors.Is(err, ErrSharedTileCode)`), the same
  rule `ApplyReportedLayout` (cloud path) already applies.
- `ui.SharedTileCode(err)` exposes the code; the route answers **409** with the localized
  `designer.error.shared_code` message naming it (shown by app.js `postButtons` via
  `textContent`, then the grid reloads). Logged at Info.
- New key `designer.error.shared_code` (English + `en.meta.json`, one `%s` placeholder).
- Help: `web/help/en/till-designer.md` and `web/help/en/sell.md` (Rearranging the quick buttons).

## Findings
- M1 (medium, FIXED): the first message said only the shared tiles can't be reordered and
  didn't name the code, while any reorder whose written range reaches the pair is refused.
  Now the message names the code and help/meta describe the real scope.
- L1 (low, FIXED): `sell.md` jiggle section not updated.
- L2 (low, FIXED): handler test asserted only "SKU" in the body; now the exact string.
- L3 (low, ACCEPTED): `ApplyReportedLayout` keeps its own (now redundant) check; harmless.
- Usability note (ACCEPTED, Tester): one shared pair blocks every reorder at or after its
  slot, including other categories. Not a false refusal — without the guard the twin tile
  is really moved (`DUP,DUP,B,C,D` → `DUP,C,B,DUP,D`). The message names the culprit.

## Verification
- TDD red first: ui `err = <nil>, want ErrSharedTileCode naming DUP` (full + subset);
  pages `code = 204, want 409`; L2 test red on the old message.
- Re-verified independently by both Reviewer (`git archive` copy, fix hunk reverted) and
  Tester (guard disabled → both tests fail; restored → pass). Tester edge runs: shared pair
  before the moved tiles refused; shared pair after them allowed and correct; no-op 204.
- `go build ./...`, `go vet`, gofmt, golangci-lint (ui, pages: 0 issues), `go test
  ./internal/ui/ ./internal/data/`, `go test ./internal/pages/ -run 'Button|Reorder|Designer|Quick'`,
  guard-i18n, guard-i18n-meta, guard-help-topics, compliance, competitor naming, data-access.
- No browser run: no new surface; the alert path is unchanged existing code.
- CI round 1: `build` red on `guard-docs-shots_test.sh` (help prose changed, manifest topic hashes stale). Fixed by regenerating `web/help/img/manifest.json` with `node e2e/tests-docs/write-manifest.js` (only the en/sell and en/till-designer hashes moved; no screenshot changes, prose only); `guard-docs-shots.sh` and its test pass locally.

Pre-flight: till user-visible behaviour → `web/help/` topics updated (till-designer, sell),
docs-shots n/a (no visual change); `reference/` n/a (no reference page covers reorder);
README n/a; new `en.json` key → `en.meta.json` updated; review record: this file.

Verdict: APPROVE.
