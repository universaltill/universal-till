# Code review: on-screen keyboard on login/setup (ut-docs#1096)

**Branch:** `fix/1096-osk-login-setup`
**Author:** autonomous SDLC pipeline (Dev: Sonnet, inline; Review: Opus
subagent, independent, isolated worktree, different model from the author)
**Scope shipped here:** the full software-verifiable half of ut-docs#1096.
The issue's final acceptance bullet (verified on a real Pi 5 touchscreen with
the keyboard physically unplugged) is **not** in this branch — see
"Deferred" below. `#1096` therefore stays **open**.

## The bug

`web/public/osk.js` — the till's own on-screen keyboard, which exists
precisely because kiosk Pis have no OS keyboard at all — was loaded by
`web/ui/layouts/base.html` only. `web/ui/pages/login.html` and
`web/ui/pages/setup.html` are standalone documents (their own `<html>`,
responsible for their own `<script>` tags) that bypass that layout
entirely, and both silently omitted it. Setup has 11 text inputs
(shop name, TSE business details, admin PIN); login has 2 (the first-boot
admin-PIN-creation form). On a keyboard-less touchscreen — the only hardware
this product ships to as a kiosk till — neither page could be typed into at
all. Same shape of gap as ut-docs#344 (htmx) and ut-docs#400
(autofill-suppression) on these exact two pages: most pages inherit a
central mechanism from `base.html`; these two don't, and each time
something new got added centrally, these two got forgotten again.

## What shipped

- **`web/ui/pages/login.html`, `web/ui/pages/setup.html`**: added
  `<script defer src="/public/osk.js?...">`, mirroring the existing
  `autofill.js` include already on both pages.
- **Second bug found and fixed in the same diff**: `data-osk="{{ oskmode }}"`
  added to both `<body>` tags, mirroring `base.html`. Without it,
  `osk.js`'s own `document.body.dataset.osk || 'auto'` read silently fell
  back to `'auto'` regardless of the operator's forced Settings choice —
  neither page ever emitted the attribute, so a shop that had explicitly
  set OSK to "on" (e.g. a non-touch device that still wants the virtual
  keyboard) would not have gotten it here even after the script-tag fix
  alone. `'auto'` still opens the keyboard correctly on real touch hardware,
  which is what the issue's own report needs — this closes the adjacent gap
  the same fix naturally touches.
- **`scripts/ci/guard-osk-loaded.sh`** (new) + regression test: modeled on
  `guard-autofill-suppression.sh`, but **input-aware**, not blanket — only a
  standalone document with a text-like input (mirroring `osk.js`'s own
  `wantsOSK()` type set exactly: text/search/password/email/url/number/tel,
  an untyped `<input>` which defaults to text, or a `<textarea>`) is
  required to load `osk.js`. Registered in `.github/workflows/ci.yml`'s
  `build` job.
- **`internal/pages/auth_page_test.go`**: `TestLoginAndSetupLoadOnScreenKeyboard`
  — handler-level, asserts GET `/setup` (first boot) and GET `/login` (after
  completing first-boot setup) both load `osk.js` and carry
  `data-osk="auto"`.
- **`e2e/tests/login.spec.ts`**: new test in the existing
  `first-boot setup and PIN login` serial describe — own touch-capable
  browser context (`hasTouch: true`), drives the real setup wizard to the
  store-name field, taps it, asserts `#osk` and a real key element actually
  become visible. Proves the keyboard *opens*, not just that the script
  tag is present.
- **`web/help/img/manifest.json`**: regenerated via `make docs-shots`
  (surface hash only moved; 92/92 screenshots byte-identical across two
  independent regenerations — mine and the reviewer's).

## TDD claims re-verified personally (by the independent reviewer, not taken on faith)

Both levels re-driven from scratch in an isolated worktree:

- **Go**: with only `login.html`/`setup.html` reverted to the pre-fix
  commit, `TestLoginAndSetupLoadOnScreenKeyboard` fails with the real,
  specific errors (`response never loads osk.js`, `<body> missing
  data-osk="auto"`) on both routes; restoring the fix turns it green again.
- **E2E**: driven for real against Chromium
  (`/opt/pw-browsers/chromium`). Against the pre-fix templates the new test
  fails at exactly the load-bearing assertion
  (`expect(p.locator('#osk')).toBeVisible()` — element not found); with the
  fix it passes, and the full 8-test serial file (the whole wizard/PIN/lock
  flow) re-runs green, confirming no regression to the surrounding flow.

## Findings

### F1 — Guard false negative: multi-line `<input>` tags (medium, fixed)

`has_texty_input`'s explicit-type probe was a **line-oriented** `grep -qE`,
so `[^>]*` could not cross a newline. Meanwhile the untyped-input probe was
already `perl -0777` (slurped) and correctly saw a `type=` attribute sitting
on a *later* line than its own `<input` — and so correctly declined to
count that tag as untyped either. Net effect: an `<input>` written across
multiple lines fell through **both** probes and got a free pass. Not
theoretical — multi-line `<input>` tags are this repo's prevailing house
style, used in 19 templates under `web/ui/`, `index.html` and
`settings.html` included. Proved with a fixture before fixing (guard
incorrectly passed a page that needed `osk.js`). **Fix:** all three probes
now run in one slurped `perl -0777` pass so `[^>]*` spans newlines
correctly; covered by a new regression fixture.

### F2 — Guard false negative: `web/ui/layouts/base.html` was never in the required set (medium, fixed)

`base.html` itself contains zero `<input>` elements — every field on the
~29 pages it wraps lives in the page template or partial composed in at
render time — so the input-aware rule as first written **skipped it
entirely**. Deleting `base.html`'s own `osk.js` `<script>` tag would have
silently taken the keyboard off every base-layout page in the app at once
— strictly worse than the ut-docs#1096 bug this guard exists to catch —
while the guard stayed green throughout. This is precisely the "a document
silently stopped loading it" regression class the guard's own header
claims to protect against. **Fix:** added `is_layout()` (`*/layouts/*` is
required unconditionally, independent of its own input content); covered
by two new regression fixtures (an input-free layout without `osk.js` →
reject; the same layout with it → pass).

### F3 — `guard-docs-shots` was red, not green as first claimed (medium, fixed)

The diff edits `web/ui/pages/login.html` and `setup.html`, both inside
`guard-docs-shots.sh`'s hashed app surface, and the manual's screenshot
manifest hadn't been regenerated — a genuine CI-blocking failure, confirmed
red at the pre-fix commit and green afterward (not pre-existing). **Fix:**
ran the real `make docs-shots` (92/92 screenshots, ~1.6 min) and committed
the resulting `manifest.json`. Checked `web/help/en/display.md` before
assuming no prose update was also needed: it already states *"The
on-screen keyboard pops up automatically on touch screens"* — a claim that
was simply false on these two pages until now. This fix makes the product
match prose the manual already ships, so only the screenshot stamp needed
regenerating, not the topic text.

### F4 — Stale references to a spec file that was never created (low, fixed)

`guard-osk-loaded.sh`'s own header comment pointed twice at
`e2e/tests/osk-loaded-1096.spec.ts` — the planned filename from design, but
the actual e2e coverage landed inside the existing `login.spec.ts` (correct
call: it needs the AUTH project's real fresh-install server, which only
`login.spec.ts` is wired to drive — see `e2e/playwright.config.ts`'s
`AUTH_ONLY_SPECS`). **Fix:** retargeted both references, including the
guard's own runtime failure message, to the file an engineer chasing that
message would actually find.

### Checked and found nothing wrong

- **The two recurring bug classes this pipeline watches for** (a
  file-write handler missing `os.MkdirAll`; a cwd-relative path where
  `paths.Data(...)` belongs): not applicable — this diff writes no files
  at runtime. Scanned the diff for both patterns directly: clean.
- **Any other standalone document missed?** Grepped `web/ui/pages/`
  independently rather than trusting the issue's own audit table. Exactly
  six standalone documents exist: `layouts/base.html`,
  `pages/{login,setup,order_tracking,self_order,self_order_shop}.html`.
  The three self-order/tracking pages carry zero `<input>`/`<textarea>`
  today — the issue's audit was correct, nothing missed.
- **CSS**: both pages already load `/public/app.css`, which carries all
  `#osk` styling (no new CSS needed) — confirmed visually by the passing
  e2e assertion that `#osk` and a real key render.
- **i18n**: no new user-facing strings. `guard-i18n.sh` and both its
  regression tests pass.
- **RTL / kiosk mode**: unaffected — `data-osk` is a plain attribute with
  no CSS attached, and `osk.js` manages its own keyboard container
  independent of document `dir`; kiosk mode is a separate `<body>` class
  untouched by this diff.
- **Secrets / real client names**: none. The only credential-shaped string
  in the diff is `<input type="password" name="pin">` (a type attribute in
  a guard fixture) and the e2e test's existing hardcoded `482913`
  test-admin PIN, already used identically elsewhere in `login.spec.ts`
  before this change.

## Verification performed

- `gofmt -l .` → clean · `go build ./...` → clean · `go vet ./...` → clean
- `go test ./internal/pages/... -run TestLoginAndSetupLoadOnScreenKeyboard`
  → **ok**; full `go test ./...` (41 packages with tests) → **ok**, no
  failures
- Every CI-blocking guard listed in `universal-till/CLAUDE.md`'s "Before
  committing" section, run individually → **all green**, including the new
  `guard-osk-loaded.sh` (`6 standalone document(s) checked, 3 needed
  osk.js and load it`) and its 15-assertion regression suite
- `guard-docs-shots.sh`, `guard-docs-shots_test.sh` and
  `guard-docs-shots-cross-check_test.sh` → all green, cross-check hash
  agrees between the Python and JS implementations (`940a73cbbe9e…`)
- `e2e/tests/login.spec.ts --project=auth`, real Chromium, driven twice
  (once by Dev/Tester, once independently by Review) → **8/8 passed**
  both times, including the new touch-OSK test and the full unmodified
  wizard/PIN/lock flow

## Deferred (and why `#1096` must stay open)

The issue's final acceptance bullet — "verified on the real Pi 5
touchscreen with the keyboard physically unplugged" — needs physical
hardware this cold cloud session does not have. Everything else in the
issue's checklist is done and verified in software (real browser, real
touch emulation, real handler responses). A `blocked:env` Backlog
follow-up card is filed for the hardware confirmation, same shape as
ut-docs#1078 (which already tracks the equivalent hardware verification for
ut-docs#1039/#1099). This PR does **not** carry a `Closes` line for
`#1096` for that reason; it references the issue without closing it.

## Verdict

**Safe to merge.** The core fix is minimal and mirrors an already-proven
pattern (`autofill.js`) exactly; the `data-osk` addition is a real second
bug caught in the same pass, not scope creep. Independent review re-drove
both TDD claims from scratch and found three genuine defects before they
shipped — two guard false-negatives that would have let a real regression
through silently, and a red CI check the original verification pass
missed — all fixed and covered by new regression fixtures. Nothing
outstanding is a defect in what shipped.
