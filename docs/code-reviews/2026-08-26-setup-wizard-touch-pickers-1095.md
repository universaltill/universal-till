# Setup wizard touch-tile pickers (ut-docs#1095)

**Card:** universaltill/ut-docs#1095 — "Setup wizard's dropdowns are unusable
on a touchscreen — selecting a country took real effort on the target
hardware." Reported by the product owner on a real Pi 5, real touchscreen,
v0.6.2.

**Complexity:** medium. Dev at Sonnet (inline), review at Opus
(fresh-context subagent, isolated worktree), per `scrum-master`'s model
routing table.

## What shipped

`web/ui/pages/setup.html`'s two native `<select>` elements — `country`
(step 2) and `shop_type` (step 5) — are replaced with touch-first tappable
tile pickers (new `.picker-grid`/`.picker-tile` CSS in `web/public/app.css`,
sized for a single line of text, distinct from the existing 8.5rem
image+price product `.btn-tile`).

- **Country step:** the OS-detected country (if any) shows alone at first,
  pre-selected, with an explicit **"Show all countries"** toggle revealing
  the rest (new locale key `setup.country.show_all`, added to
  en/ar/fa/tr). If nothing was detected, the picker opens already expanded
  — never an empty screen. Every tile still carries the exact
  `data-currency`/`data-tax`/`data-taxinc`/`value` attributes the old
  `<option>`s did, so the client-side currency/tax prefill logic and the
  server-side Go tests (`setup_page_test.go`) needed zero changes to their
  assertions.
- **Shop-type step:** all six ADR-0026 tiles shown directly — short enough
  a "show all" toggle would add a tap for no benefit.
- No Go/server-side behaviour changed. `wizardCountries()`, `detectCountry()`,
  `setupShopTypes`, the `/api/setup` POST handler, and `currency_touched`'s
  ut-docs#970 semantics are all untouched — this is a rendering/interaction
  change only.
- Two now-unused locale keys (`setup.country.choose`, `setup.shop_type.choose`
  — the old `<option value="">` placeholders) removed from all four locale
  files (found by review, see below).
- The manual (`web/help/{en,ar,fa,tr}/users.md`, item 6) updated to
  describe the new collapsed-then-"Show all" country flow instead of the
  old one-tap dropdown — the CLAUDE.md "manual ships with the feature"
  rule. `make docs-shots` re-run; only `manifest.json` changed (no PNG,
  since no help topic's `routes:` actually screenshots `/setup` itself).
- Fixed three e2e/test helpers that drove the old `<select>`:
  `e2e/tests/helpers.ts`'s `ensureOperator`, `e2e/tests/login.spec.ts`
  (three call sites), `e2e/tests-docs/docs-shots.spec.ts`'s own
  `ensureOperator`. `helpers.ts` also carried a genuine **pre-existing**
  off-by-one bug (step numbers never updated after ADR-0053's TSE-step
  insertion) — invisible until now because that function's `/setup`
  branch is only reachable from a genuinely fresh install, and the one
  spec that imports it (`tables-keyboard-reposition-826.spec.ts`) runs on
  the auth-off "default" project, where `/` never redirects to `/setup`.
  Fixed in the same edit since the country-tile swap already required
  touching this function.
- `internal/pages/demo_seed_opt_in_test.go`'s
  `TestSetupWizardRendersShopTypeStep` updated: shop-type tiles carry the
  type in `data-value=`, not an `<option>`'s `value=` (country tiles kept
  `value=` deliberately, so the existing `setup_page_test.go` country
  assertions needed no changes).

## Independent review (Opus, fresh context, isolated worktree)

Full findings and verification detail: see the review agent's transcript
(spawned via `Agent` with `isolation: "worktree"` per the `reviewer`
skill). Summary:

### 🔴 Blocker — fixed
**`isVisible()` does not auto-wait**, so every e2e/test helper's
`if (await showAllBtn.isVisible()) …` guard raced Alpine's `x-show` and
read `false` unconditionally, immediately after the click that reveals
step 2. On any host where a country IS detected — reproduced deterministically
against a `LANG=de_DE.UTF-8 TZ=Europe/Berlin` till (5/5 failures) — this
silently skipped "Show all countries" and left the GB tile (or any other)
permanently hidden, breaking `make docs-shots` on a real developer machine
and leaving the card's actual headline behaviour (collapsed-then-expand)
with **zero real coverage** — the sandbox this pipeline runs in never
detects a country, so every green run before this fix only ever exercised
the "nothing detected, everything already showing" branch.

**Fix:** `await <step2>.locator('h1').waitFor()` before every `isVisible()`
check, at all five call sites. Independently re-verified against a real
DE-detecting till, both directions: `isVisible()` immediately after the
click reads `false` (the bug, reproduced live), `isVisible()` after the
`waitFor()` reads `true` (the fix). A new dedicated e2e test — **"a
detected country shows alone at first, with an explicit toggle to see the
rest"** — closes the coverage gap for good: it forces the exact same
`detectedCountry`-set initial state a real detection would produce (via
the PIN-mismatch server re-render path, the same mechanism
`TestSetupWizardPINErrorRerenderKeepsOperatorCountryNotDetected` already
covers server-side — real OS locale detection can't be forced from this
sandbox), then asserts only one tile is visible, "Show all countries" is
present, expanding reveals the rest, and the pinned tile keeps its
selection.

### 🟠 Should-fix — fixed
1. **Focus dropped to `<body>` on expand.** The "Show all countries"
   button hides itself (`x-show="!showAllCountries"`) the instant it's
   tapped, with nothing moving focus into the 13 newly-shown tiles — a
   keyboard/switch-access operator had to tab from the top of the
   document. Fixed: `$nextTick` moves focus to the selected tile (or the
   first tile) once the DOM has actually updated; `aria-expanded` added to
   the toggle button.
2. **Two dead locale keys** (`setup.country.choose`, `setup.shop_type.choose`
   — the old `<option value="">` placeholders, now unreferenced anywhere)
   removed from all four locale files.
3. **Manual (`users.md`) left describing the old one-tap-dropdown flow.**
   Updated in all four locales (see "What shipped" above).

### 🟠 Should-fix — deferred, tracked
**Single-select modelled as `role="group"` + `aria-pressed` toggle
buttons, not `role="radiogroup"`/`role="radio"`.** Functionally fine —
real `<button>` elements, Enter/Space work natively, no dead ends — but a
genuine semantic regression from a native `<select>`'s built-in "1 of N,
mutually exclusive" announcement and roving tabindex (measured: 17
tabbable buttons in the expanded country grid vs. 1 for the old select).
A full ARIA radiogroup conversion (roving tabindex, arrow-key navigation)
is real, separate interaction-model work beyond "replace select with
tiles" — **filed as a new Backlog card** rather than widening this branch
further.

### ⚪ Nits — accepted, no action
- No way to return to "nothing picked" once a tile is tapped (both fields
  are optional; "Other" is shop-type's escape hatch; low impact).
- Country tiles use `value=`, shop-type tiles use `data-value=` — kept
  deliberately different so the pre-existing `setup_page_test.go` country
  assertions (`value="GB"`, the `value="ZZ"[^>]*>ZZ<` label regex) needed
  no changes.
- `TestSetupWizardRendersShopTypeStep` is a markup-shape check, not a
  semantics check (the browser-level `login.spec.ts` covers `aria-pressed`
  and keyboard behaviour).

## Verified beyond automated tests

- **Real driven run**, not just rendered-HTML assertions: booted a
  throwaway till, drove the wizard through a real Chromium (touch-context),
  at the 1024×600 kiosk floor, in English and Farsi (RTL).
- **Screenshots actually looked at** (not just asserted-on): collapsed/
  pinned state (forced via the PIN-mismatch re-render, since this sandbox
  never detects a country for real), expanded state, keyboard-focus state,
  shop-type step, all in en and fa/RTL. No overlap, no clipped labels, no
  broken alignment; RTL grid mirrors correctly with zero RTL-specific code
  (CSS Grid's own direction-awareness); selected-tile styling (accent
  border + inset shadow) reads clearly in both directions.
- **Touch-target size measured live**, not assumed: GB tile 177×51px at
  1024×600 — well above the ~44px bar.
- **Keyboard operability proven, not just claimed**: real `Tab`+`Enter` in
  the e2e suite actually switches the selected country and re-fires the
  currency/tax prefill.
- **TDD**: the original `.picker-tile`/`data-value` markup was red against
  the pre-fix `<select>` markup (confirmed via `git stash`, both for the
  new e2e test and for `TestSetupWizardRendersShopTypeStep`), green after.
  The Blocker's own fix was independently re-verified live against a real
  country-detecting till in both directions (see above) — not just
  "e2e suite is green," since the suite's own environment can't detect a
  country at all.
- **Full gate, twice** (once pre-review, once post-fix): `gofmt -l .`
  (clean), `go build ./...`, `go vet ./...`, `go test ./...` (full suite,
  all packages), the e2e suite in full (188/189 specs — the one failure,
  `catalog-image-to-till.spec.ts`, is pre-existing on a clean, completely
  unmodified checkout too, confirmed by reverting this branch's entire
  diff and re-running it in isolation; unrelated to anything this diff
  touches), and every CI-blocking guard: `guard-i18n.sh`,
  `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`
  (regenerated via `make docs-shots`; two incidentally-regenerated PNGs —
  `ar/translations.png`, `tr/sell.png` — were AA-noise-only per this
  harness's own documented convention and reverted, not committed),
  `guard-htmx-loaded.sh`, `guard-autofill-suppression.sh`.
- Recurring bug classes this pipeline watches for: N/A — no file writes,
  no Go handler logic, no SQL in this diff.
- No real client/shop name used as demo data (`Demo Shop`,
  `E2E Test Shop`, `Tile Picker Test Shop`, `Rerender Test Shop` — all
  generic/test).

## Deferred / follow-up

- **New Backlog card**: convert the country/shop-type tile pickers to a
  proper `role="radiogroup"`/`role="radio"` pattern with roving tabindex —
  a real accessibility improvement, scoped separately from this fix.
- Real Pi 5 touchscreen verification (this card's own AC4) is out of
  reach for a cold cloud pipeline session — no physical hardware. The
  driven browser check above (real Chromium, touch-context, 1024×600,
  measured hit-test geometry) is the closest available substitute; noted
  as a residual, accepted gap on the issue, same convention this
  pipeline already uses for other hardware-only verification steps
  (e.g. ut-docs#1078).

## Verdict

**Safe to merge.** The one real blocker the independent review found is
fixed and independently re-verified against the exact failure scenario
(a country-detecting host) in both directions; the two cheap should-fix
items are fixed in-branch; the one should-fix that would genuinely widen
scope (ARIA radiogroup conversion) is tracked as its own Backlog card
rather than folded in here.
