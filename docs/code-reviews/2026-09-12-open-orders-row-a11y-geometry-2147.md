# 2026-09-12 — Open-orders row accessible name + geometry regression coverage (ut-docs#2147)

## What changed

Split out of the ut-docs#2138 review (finding N3 + "process observations").
Two related gaps in the row-as-one-button pattern shared by `/open-orders`
(`web/ui/pages/open_orders.html`) and the sale-screen's parked-orders popup
(`web/ui/partials/parked_orders.html`):

1. **Accessible name never stated the action.** Both buttons' accessible
   name was just their visible spans concatenated (e.g. "Sarah Window 2 3
   £12.50 90 min") — a screen-reader user heard the order's details but
   never that activating the control resumes it. Fixed by prepending a
   `<span class="visually-hidden">{{ T "open_orders.resume_row" }}</span>`
   as the first child of each button — the same `.visually-hidden` idiom
   already used in `basket.html`, `bugreport_chip.html`, etc. This is a
   pure accessible-name change: the span is `position:absolute;
   width:1px; height:1px; clip-path:inset(50%)` (app.css's existing
   rule), verified via a live `boundingBox()`/computed-style probe to
   render zero visible pixels, and it does not replace or reorder any of
   the existing visible spans — every previously-announced detail (table,
   items, total, age) is still part of the concatenated name.
2. **No regression test for the row-as-button layout.** ut-docs#2138 fixed
   four real, measured CSS bugs on this exact pattern (header/value
   misalignment, ~25% of each row untappable, a 360px width regression,
   and a long label collapsing to zero width) and none of the four were
   ever pinned with an automated test — only found by eyeballing a real
   browser each time. New file `e2e/tests/open-orders-row-geometry-2147.spec.ts`
   pins all of it: real header/value pixel alignment at 360x740 /
   1024x600 / 1280x800, row-fills-list geometry, a long-cashier-label
   floor-width case, and an accessible-name assertion (via Playwright's
   `getByRole`, which resolves the browser's real accessible-name
   computation) on both surfaces.

New i18n key `open_orders.resume_row` ("Resume order" / "استئناف الطلب" /
"از سرگیری سفارش" / "Siparişi sürdür") added to all four core locales
(`web/locales/{en,ar,fa,tr}.json`). The external `ut-plugin-language-
{de,es}` packs get the matching translations ("Vorgang fortsetzen" /
"Reanudar cuenta") in separate follow-up PRs — see "Follow-up filed"
below for why those land after this PR, not alongside it. All
translations done directly in this session rather than via the NAS
Ollama endpoint (`reference/translation.md`), which is unreachable from
this cloud pipeline lane; each reuses vocabulary already established in
the same file (`hold.toast.resumed`'s root verb, `hold.action`'s
noun-phrase/imperative style per locale) for consistency.

## TDD

The two Go-level `open_orders_page_test.go` tests already covering this
markup are substring-`Contains` checks, unaffected either way, so the
real proof is the new e2e spec. Confirmed test-first by temporarily
stashing the two template edits and re-running the file: the three
geometry tests and the long-label test passed unchanged (they pin an
already-shipped fix from ut-docs#2138, not new behavior), and the
accessible-name test failed with `element(s) not found` against the
un-fixed templates, then passed after restoring the fix.

## Verification

- `gofmt -l .` — clean. `go build ./...` — clean. `go test ./...` — all
  packages pass (`internal/pages` 305s, full suite ~none skipped).
  `golangci-lint run ./...` — 0 issues.
- `scripts/ci/guard-i18n.sh` — passes (1659 keys resolve, all locales
  match `en.json`).
- `scripts/ci/guard-docs-shots.sh` — the two touched templates trip the
  surface-hash check by file-touch alone; confirmed genuinely zero
  rendered pixels (computed style + `boundingBox()` on the new span, plus
  a direct before/after screenshot comparison at 1024x600 and 360x740),
  so `scripts/ci/update-docs-shots-surface-hash.sh` was used instead of a
  full `make docs-shots` regeneration, per that script's own documented
  escape hatch. Commit below carries `Docs-Shots-Unchanged: true`.
- `guard-help-topics.sh`, `guard-help-drift.sh` (pre-existing baseline
  entries only, none new), `guard-compliance-claims.sh`,
  `guard-page-http-error.sh`, `guard-plugin-menu-read.sh`,
  `guard-e2e-fixtures-import.sh`, `guard-autofill-suppression.sh`,
  `guard-emoji-font.sh` — all green.
- `shellcheck` is not installed in this session's environment and no
  `.sh` file was touched by this change, so it was not run; flagged here
  rather than silently skipped.
- New e2e spec: 5/5 passing (after tightening per the independent
  review's nits — see below). Full existing `parked-orders-popup-2137.spec.ts`
  suite (the other surface this change touches): 5/5 passing, no
  regression.
- **Full `e2e/` default-project suite run for real** (487 tests, one
  worker, real Chromium against a real till) to confirm the two shared
  templates introduced no regression anywhere else in the suite —
  486 passed, 1 failed
  (`sale-screen-osk-scan-submit-1177.spec.ts:95`, an unrelated OSK
  scan-field test with no connection to this diff). Re-ran that one file
  alone once the machine was otherwise idle: 4/4 passed — confirms
  resource-contention flake (this session ran a full second Playwright
  run concurrently, in the independent review's isolated worktree, for
  part of this run), not a real regression.
- Driven, screenshotted, and looked at (not just asserted on): `/open-orders`
  at 1024x600 and 360x740 (English), and again at both sizes in `fa`
  (RTL) — header/value alignment correct, no visible artifact from the
  new span, RTL mirrors correctly with logical layout unaffected (no
  left/right literals touched). Dark/other theme plugins were **not**
  separately screenshotted — the change touches no CSS and the existing
  `.visually-hidden` rule is theme-independent, so this is stated as a
  visual-check gap rather than silently assumed clean.

## Independent review

`complexity:medium` per the pipeline's model-routing table → reviewed by
an **Opus** subagent, in a real isolated `git worktree` (never the
orchestrating session's own shared checkout), given full read/write/run
access and instructed to find real problems rather than confirm the work.
**Verdict: safe to merge.**

The reviewer independently re-ran the entire local gate (`gofmt`,
`go build`, `go vet`, `go test ./internal/pages/...`, `golangci-lint`,
`guard-i18n.sh`, `guard-docs-shots.sh`, plus several more guards) inside
its own worktree and confirmed every result above rather than trusting
this record's claims.

**TDD re-verification, done independently** (not just re-reading this
record's claim): the reviewer's first attempt actually caught a real
methodology trap first — `e2e/playwright.config.ts`'s
`reuseExistingServer` meant its first run silently reused a till server
already running on shared ports from a *different* checkout, so
reverting templates on disk in the worktree changed nothing served and
produced a false pass. It rebuilt from its own worktree onto isolated
ports and redid the check properly: reverting both template edits fails
test 5 (`element(s) not found` against `getByRole(... /Resume order.../)`),
restoring them passes all 5. It went one step further than this session
had — reverting `parked_orders.html` *alone* also independently fails
the popup half of the same test, confirming both surfaces genuinely need
their own fix (this session's own TDD pass had only exercised the
`/open-orders` half before the assertion aborted the test).

**"Zero rendered pixels" claim, independently re-verified**, not taken on
trust: screenshot hashes identical with/without the new spans at both
viewports (`cac1eb38c9248158` / `70c211db1cef1466`), cell boxes unchanged
to 2dp, confirmed the absolutely-positioned span is not a flex item so it
consumes no `gap`, and grepped for any `:first-child`/`:nth-child`
selector or JS `textContent`/`firstElementChild` read on these buttons
that the new first-child span could have broken — none found.

**Findings, triaged:**

1. **should-fix — this record understated the language-pack dependency**
   (fixed in this revision, see "Follow-up filed" below): `ut-plugin-
   language-{de,es}`'s own `check-key-drift.sh`, run against each pack's
   real `main`, reports **7** missing keys per pack — the 6 pre-existing
   `locations.*` ones plus `open_orders.resume_row` itself (only present
   in this session's local, not-yet-pushed sibling checkouts) — not just
   the 6 pre-existing ones this record originally implied. Both packs'
   `main` already fails `lang-pack-drift` today over the `locations.*`
   gap, so this PR doesn't newly redden anything, but the ordering
   matters and is now stated correctly below.
2. **nit — fixed**: three floor-width assertions in the long-label e2e
   test used a bare `toBeGreaterThan(0)`/`toBeGreaterThan(20)`, which a
   regression to half the real CSS floor would still pass. Tightened to
   per-column floor bounds (`label`/`lines` 40px, `total` 70px, `age`
   85px, each with headroom below the measured 51/51/85/102px) — see the
   spec's updated comment. Re-ran: still 5/5 green.
3. **nit — accepted, not changed**: `toHaveAccessibleName(/£\d/)`
   hardcodes GBP. Consistent with this suite's existing, established
   convention (the shared e2e demo catalog is fixed-currency; other specs
   in this same file and `parked-orders-popup-2137.spec.ts` already
   assert literal `£` amounts), so left as-is rather than introduced as a
   one-off deviation.
4. **observation, recorded here per the reviewer's suggestion**: the
   `reuseExistingServer` + `go:embed` interaction above can turn a
   revert-and-rerun TDD check into a silent false pass whenever another
   checkout already holds the default ports — worth knowing for anyone
   re-verifying this class of fix from a fresh worktree.
5. **N/A checks, explicitly confirmed rather than silently skipped**: no
   `.go` file in this diff at all, so the file-write-handler
   `os.MkdirAll` and cwd-relative-path-vs-`paths.Data(...)` checks this
   pipeline specifically watches for don't apply here.
6. **Manual (`web/help/`)**: confirmed not needed — `web/help/en/
   open-orders.md` already describes tapping the row; nothing visible or
   behavioural changed, and the screenshot is proven byte-identical.
7. **UX / RTL / theme**: clean on every applicable
   `reference/ux-guidelines.md` item (no new tokens, no CSS touched, no
   `left`/`right` literals, no layout change); longest-locale overflow
   ("Vorgang fortsetzen") is structurally impossible since the span is
   `width:1px; overflow:hidden`.

**Explicitly not verified by the reviewer** (stated rather than left
silent): `go test ./...` beyond `internal/pages`; real assistive-tech
announcement (NVDA/VoiceOver/TalkBack) — verified via Chromium's
accessible-name computation through Playwright only; the pack repos
themselves (worktree isolation has no access to the sibling checkouts —
confirmed via file grep plus the network-fetching drift check instead);
dark/other theme plugins; real touchscreen hardware.

## Follow-up filed

Running each language pack's own `check-key-drift.sh` against this
branch's `en.json` surfaces two DISTINCT gaps, correctly separated per
the independent review's should-fix finding above:

- **This card's own new key** (`open_orders.resume_row`): translated
  already, in `ut-plugin-language-{de,es}` commits on local branch
  `feat/2147-open-orders-resume-row-key` in each pack's sibling checkout
  — held back from pushing/opening a PR until *after* this core PR
  merges, per the documented new-key sequencing rule (a pack PR
  translating a key core doesn't have yet fails that pack's own
  orphan-key check, so pack-first isn't achievable for a brand-new key).
  `main` on both packs will show `lang-pack-drift` red for this key
  between this PR's merge and the pack PRs' merge, in the same cycle —
  expected, bounded, not a mistake.
- **A pre-existing, unrelated drift**: both packs are separately missing
  translations for 6 `locations.*` keys and carry 3 orphaned old
  `locations.*` keys, from an already-merged, unrelated core change
  (Locations admin's `record_dialog`/`list_header` port, ut-docs#2124).
  Already causing both packs' `main` to fail `lang-pack-drift` before
  this PR existed. Not caused by and out of scope for this card — filed
  as its own Backlog card (see close-out comment on ut-docs#2147 for the
  number) rather than fixed here.
