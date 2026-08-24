# Code review — split-tender panel status copy i18n (ut-docs#925)

- **Date:** 2026-08-24
- **Card:** universaltill/ut-docs#925 (`p3`, `complexity:medium`)
- **Branch:** `fix/925-split-tender-panel-i18n`
- **Reviewer:** independent fresh-context subagent at **Opus** (per the
  `scrum-master` skill's model routing: Sonnet builds, Opus reviews), given a
  clean git worktree and a read-only brief.
- **Rounds:** one. A second round was not earned — nothing the review found
  was blocker-class (no money/tax, data-loss or security finding).

## What changed and why

`web/public/app.js`'s `initSplitTender()` rendered the split-tender panel's own
status and validation copy from hardcoded English string literals — `'Sale
completed.'`, `'No pending payments yet.'`, `'Added ' + method + ' payment for
' + amount + '.'`, and a dozen more. A Persian-locale sale therefore showed
English text amid otherwise fully-Persian UI, which is how ut-docs#925 was
discovered (screenshotting the panel in fa while verifying ut-docs#921).

`scripts/ci/guard-i18n.sh` does not catch this: it scans `web/ui/**/*.html`
only, not shipped JS under `web/public/`. That gap is already documented in
this repo's `CLAUDE.md`.

The fix routes every one of those strings through `data-msg-*` attributes
rendered on `#split-tender-card` in `web/ui/pages/index.html` via `{{ T "…" }}`,
read in app.js from `card.dataset`. **This is the pattern the repo already
uses for this exact situation** — `#barcode-scan-overlay`, in the same
template, does the same thing for the camera scanner's messages. `CLAUDE.md`'s
`var T = { … }` pattern was not used because it is for *inline* `<script>`
blocks in templates; `app.js` is an external static file with no template
rendering, so `data-msg-*` is the applicable variant, and it keeps `app.js`
locale-free.

15 new `tender.status.*` keys were added to all four locale files. The empty
state deliberately **reuses** the existing `tender.no_pending` key that the
server-rendered markup already uses, rather than introducing a duplicate.

`web/help/img/manifest.json` is regenerated because the change touches
`web/ui/**` and `web/public/**`, which `guard-docs-shots.sh` requires. See
"Screenshot regeneration" below.

## Verification performed

- `gofmt -l .` clean, `go build ./...`, `go vet ./internal/pages/...` clean.
- **Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job** run
  locally — all pass, including `guard-i18n.sh` and both `guard-docs-shots`
  scripts.
- `go test ./internal/pages/... -race` — **pass** (847s; note Go's default
  10-minute per-package timeout kills this package under `-race` in this
  sandbox, so it needs an explicit `-timeout`; not a defect in this change).
- E2E: the new `split-tender-i18n-925.spec.ts` plus the pre-existing
  `split-tender-underpayment-921.spec.ts` — 4/4 pass, confirming no regression
  in the panel's rejection handling.
- **Driven for real.** The panel was exercised in a live browser at `?lang=fa`:
  empty state, fill-remaining, add-with-change, and a rejected tender all
  render Persian, with correct RTL layout. Screenshot inspected directly. The
  only non-Persian text on screen is the payment-method name (`cash`), which is
  shop-configured *data*, not UI copy, and is correctly out of scope.
- **TDD honesty check.** The fix was reverted and both Go tests re-run: they
  fail with the real symptom (`no msg.msgXxx dataset reads found in
  initSplitTender()` / `no data-msg-* attributes on #split-tender-card`), then
  pass again when restored.

## Review findings and dispositions

The review confirmed clean: build/vet/gofmt, all guards, exact attribute↔read
parity (16 rendered, 16 read, no orphans either way), no `msg` shadowing after
the catch-block's old `var msg = err.message` was removed, `%s` placeholder
counts matching across all four locales, genuine (not copy-pasted-English)
translations, no remaining user-facing English literal in `initSplitTender()`,
and that the change strictly *improved* HTML-escaping posture (the baseline
wrote the change note and the empty-state `<p>` to `innerHTML` unescaped; both
are now `escapeHtml`-wrapped).

**No BLOCKER findings.** One MAJOR and four MINOR/NIT, all addressed:

### MAJOR — the regression test did not catch the regression *(fixed)*

The reviewer demonstrated this empirically rather than asserting it: it pasted
`setStatus('Sale completed.', 'success')` back into app.js **and** deleted the
matching attribute, and the suite stayed green.

Two independent causes, both real:

1. The test only checked that the template's attribute set and app.js's read
   set *agreed with each other*. Deleting both halves keeps them consistent.
   The only floor was `len(read) == 0`, which 15 survivors clear easily.
2. The fa spot-check was vacuous on a missing key —
   `strings.Contains(fa["sale-completed"], "Sale completed")` is `false` when
   the map lookup misses and yields `""`.

Worse than useless: the test's own "attributes app.js never reads" arm would
have *guided* a future refactorer into deleting the attribute to get green.

**Fixed** by pinning the expected attribute set in an explicit hardcoded
`wantSplitTenderMsgAttrs` list (asserted for presence in both the template and
app.js, so deleting either half fails), and by asserting each rendered value
equals that locale's actual value from the locale file — a positive assertion
that cannot pass vacuously. **Re-verified personally by reproducing the
reviewer's exact mutation**, which now fails with five distinct errors naming
`data-msg-sale-completed`.

### MINOR — app.js scan not bounded to `initSplitTender()` *(fixed)*

The comment claimed to stop at the next same-indentation function; the code
actually anchored on the `ready(initSplitTender)` call site ~250 lines further
down, sweeping in every sibling function. Demonstrated with an injected decoy.
Fails loud rather than silently, but it meant an unrelated panel added to the
same IIFE would produce a confusing split-tender failure. Now bounded on the
function's own closing brace.

### MINOR — nothing guarded `%s` count agreement across locales *(fixed)*

Counts were correct, but unguarded: the reviewer cut fa's
`tender.status.added` to one `%s` and the suite stayed green, which at runtime
silently drops the **amount** from a payment confirmation. Added
`TestLocalePlaceholderCountsMatchEnglish`, checking `%s` and `%d` counts for
**every** key repo-wide, not just these 16. Verified by mutation.

### MINOR — `fmt()` supports only `%s`, but the comment claimed Sprintf parity *(fixed)*

Six existing `en.json` keys already use `%d`, and Go-side keys may legally use
indexed `%[1]s` — neither of which the `/%s/g` helper handles; `%[2]s` would
pass through *literally* onto an operator's screen. That is the normal thing a
de/es pack translator would reach for when reordering a sentence. No live bug
(all 16 keys use plain `%s`). Narrowed the comment to state the limitation
explicitly, and added `TestSplitTenderKeysUseOnlyPlainStringVerb` to pin these
keys to the supported subset. Verified by mutation with a `%[2]s` rewrite.

### MINOR — e2e spec was near-all negative assertions, several vacuous *(fixed)*

`not.toContainText('(change ')` was **unreachable** (the test added a payment
with change `0`, so no change note was ever emitted), and despite its title the
spec never completed a sale — leaving `tender.status.sale_completed`, the exact
string the card was raised for, unexercised at runtime. Rewritten to assert the
**expected Persian strings positively**, to add a payment *with* change so the
change-note path is real, and to drive all the way through a successful sale
(net 1.20 covering a 1.20 total) so the success branch is genuinely hit.

### NIT — `>`-scan could truncate on a future attribute *(fixed)*

`tagEnd` took the first `>` after `<div`, safe only because no static attribute
on that div contains a literal `>`. `x-show="tab === 'split'"` is one character
from breaking it. Now walks the tag tracking quote state.

### NIT — "en must differ from fa for every message" *(resolved by redesign)*

Fair today, but would false-fail on a legitimately identical value. Moot now:
the test asserts against the locale file directly rather than comparing locales.

### Pre-existing, not introduced *(noted, not changed)*

`app.js:210`'s `formatMoney(net)` goes into `innerHTML` unescaped while the
adjacent change note is escaped. Risk negligible — the currency symbol comes
from a fixed table in `internal/httpx/currency.go` — and out of scope here.

## Screenshot regeneration

`guard-docs-shots.sh` mechanically requires `make docs-shots` after any
`web/ui/**` or `web/public/**` change. Running it revealed that the target is
**nondeterministic**: run twice from an identical clean tree with no source
change between, it rewrites `{en,fa,ar,tr}/alerts.png` and
`{en,fa,ar,tr}/designer.png` every time, and `tr/invoices.png` intermittently.

The sale-screen topics this change actually touches came back **byte-identical**
both times — the expected result, and confirmation that the edit is visually
invisible in en.

This PR therefore commits **only the regenerated `manifest.json`** and reverts
the flaky PNGs, keeping the diff reviewable. The guard passes that way: it
recomputes surface and topic hashes from source and does not hash the PNG
bytes. Filed as **ut-docs#930** so the nondeterminism gets fixed properly
rather than being worked around silently on every future UI PR.

## Follow-ups filed

- **ut-docs#930** — `make docs-shots` nondeterminism (above).
- The underlying `guard-i18n.sh` gap (no coverage of `web/public/**`) remains
  open and is already recorded in `CLAUDE.md`. Closing it would have prevented
  this defect class outright; the reviewer flagged it as the durable fix, and
  it is deliberately out of scope for this card, which is scoped to one panel.

## Verdict

Ship. The production diff was sound on first review; all of the review's
substance was about test quality, which is now materially stronger than what
was originally written — verified by re-running the reviewer's own mutations
against the fixed tests rather than by re-reading them.
