# Code review — Sale screen: move Deposit refund to the Menu page (ut-docs#1334)

- **Date:** 2026-08-31
- **Branch:** `feat/1334-deposit-refund-to-menu` (reviewed at WIP snapshot `162df67`)
- **Reviewer:** independent review pass, fresh context, no involvement in writing the diff
- **Verdict:** **Safe to merge**, with two reviewer fixes applied on top (below).

---

## What shipped

The Pfandrückgabe (deposit-refund) payout had two sale-screen-only entry
points, a leftover of ut-docs#1332's nav-rail split:

- a nav-rail icon in `web/ui/partials/nav.html`, gated on `{{ if .saleScreen }}`
  and `.nav-rail-only` (>480px), `data-testid="kiosk-pfand-open"`;
- a phone-width-only fallback button in `web/ui/pages/index.html`'s
  `.kiosk-header.phone-fallback-only` (≤480px),
  `data-testid="kiosk-pfand-open-phone"`.

Both are removed. The feature is now a single tile on the Menu launcher
(`web/ui/pages/menu.html`, `data-testid="menu-pfand-open"`), and the
`<dialog id="pfand-modal">` moved with it out of `index.html`. No Go logic
changed; one Go test was repointed from `GET /` to `GET /menu`
(`internal/pages/index_osk_test.go`), and five Playwright specs were
repointed at the new location. Help topic `menu.md` gained one sentence in
en/fa/ar/tr. No new i18n keys — the existing `pfand.action` / `pfand.modal.*`
are reused.

## Automated gate — all green

Run for real in an isolated worktree at `162df67`, then re-run after the
reviewer fixes:

| Gate | Result |
|---|---|
| `gofmt -l .` | clean (no output) |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` | pass (whole module) |
| all 32 CI-blocking guards in `.github/workflows/ci.yml`'s `build` job | 32/32 PASS |
| Playwright, 5 directly-touched specs (25 tests) | 25 passed |
| Playwright, full suite (263 tests) | 262 passed, 1 failed — **pre-existing**, see below |

The one full-suite failure is
`e2e/tests/settings-pos-notice-918.spec.ts:84` —
`route.continue: Route is already handled!` at line 95, a Playwright
route-interception race in a customer-search test. **Verified pre-existing**:
the identical failure reproduces with the tree reset to `main`
(`git checkout main -- web internal e2e`, same spec, same error, same line).
Nothing in this diff or in the reviewer fixes is reachable from that flow.
Not attributable to #1334; left alone.

## Independently re-verified TDD claim

Not taken on the implementer's word. In the isolated worktree, the **markup**
was reverted to `main` while the **new tests were kept**:

```
git checkout main -- web/ui/pages/menu.html web/ui/pages/index.html web/ui/partials/nav.html
npx playwright test --project=default tests/deposit-refund-osk-1248.spec.ts
```

Both tests in that spec failed, with a real error — not a skip, not a
false pass:

```
2) [default] › tests/deposit-refund-osk-1248.spec.ts:58:5 › deposit-refund dialog:
   the custom OSK stays a singleton across both fields

  Test timeout of 30000ms exceeded.

  Error: locator.click: Test timeout of 30000ms exceeded.
  Call log:
    - waiting for locator('[data-testid="menu-pfand-open"]')

    37 |   await page.goto('/menu');
    38 |   await page.waitForSelector('.menu-grid');
  > 39 |   await page.locator('[data-testid="menu-pfand-open"]').click();
       |                                                         ^

2 failed
```

Restoring the fix (`git checkout 162df67 -- web/ui/pages/menu.html
web/ui/pages/index.html web/ui/partials/nav.html`) returned both tests to
green (`2 passed`). The revert→run→restore ran as one atomic script so no
turn boundary could land between the revert and the restore.

## Verified beyond the automated gate

**The two cited bug fixes survive the move byte-for-byte.** The
`#pfand-modal` block in `menu.html` was diffed against `main`'s copy in
`index.html`: 42 lines vs 42 lines, `diff -w` **identical** — the only
difference is a uniform 2-space dedent from leaving its old container. The
ut-docs#1249 fixes (`type="text"` + `inputmode="decimal"` + `oninput`, not
`type="number"` + `onchange`) and the field ids/`hx-post` target are intact,
including the explanatory comment. The opener's `onclick` body was extracted
and compared character-for-character against **both** removed triggers
(`nav.html`'s and `index.html`'s): **byte-identical**, so the ut-docs#1248
double-keyboard fix (blur-then-restore-focus after `.show()`) moved
unmodified.

**No leftovers.** `grep -rn "kiosk-pfand-open"` across the whole repo returns
only two hits, both in a historical `docs/code-reviews/` record — no live
template, Go, e2e or help reference to the removed testids or the
`.saleScreen`-gated block. `grep -rn "pfand-modal"` shows the dialog is
referenced only from `menu.html`, CSS, specs, and the repointed Go test.
A runtime sweep of `GET /` confirmed 0 pfand entry points and 0
`#pfand-modal` on the sale screen; a `[id]` uniqueness sweep of `GET /menu`
returned **no duplicate ids**.

**Workflow regression check (the risk the relocation actually carries).**
Deposit refund used to be reachable *without* leaving the sale screen; it now
requires navigating to `/menu`. Driven live: added a product to the basket
(`Butter 250g £2.15`), navigated to `/menu`, opened the dialog, cancelled,
navigated back. The basket came back **identical** — same line, same
Subtotal/Tax/Total. No mid-sale state is lost by the round trip.

**Layout parity, driven live.** At 1024×600 and at 360px in en/fa/ar/tr the
new tile measures the same box as its siblings (212×160 and 318×160
respectively), starts a fresh grid row cleanly, has no internal overflow, and
`document.scrollWidth === clientWidth` in every locale including the RTL
ones.

**Checklist items with nothing to report:** no real client/shop name anywhere
in the diff (it touches no seed data; the demo catalog is generic); no
secret-shaped literals; no file writes, so the `os.MkdirAll` / cwd-relative-
path-instead-of-`paths.Data(...)` classes this pipeline keeps catching don't
apply here; no new modal blocker on the checkout path (the dialog opens from
`/menu`, and it is non-modal `.show()`, deliberately, so the OSK stays
reachable); no new design tokens or hardcoded colours/spacing introduced.

## Findings

### 1. `.menu-tile` on a `<button>` lost the app's font stack — **FIXED**

`web/ui/pages/menu.html:20` applies `.menu-tile` to a `<button>` for the
first time; the class had only ever been used on `<a>`
(`web/public/app.css:338`), and it declares no `font` and no `cursor`. A
`<button>` does not inherit `font-family` from `body`, and `app.css` has no
global button reset.

Measured live on `/menu`, comparing the new tile against all 20 anchor tiles:

```
A  "🧾 Home"          font: -apple-system, BlinkMacSystemFont, "Segoe UI", …,
                            "Noto Color Emoji", "Segoe UI Symbol"   cursor: pointer
BUTTON "menu-pfand-open"  font: Arial                               cursor: default
```

The `.menu-ico` (♻️) and `.menu-label` inside it inherited `Arial` too. Two
real consequences on the target hardware, not just a nit:

1. one tile in a 21-tile grid renders its label in a different typeface (on
   the Pi kiosk, `Arial` is normally absent and fontconfig substitutes
   something else again);
2. the ♻️ glyph loses `body`'s **explicit colour-emoji fallback chain** —
   the very chain `scripts/ci/guard-emoji-font.sh` exists to protect — and
   falls back to implicit fontconfig substitution.

This is a regression against the trigger it replaces: `nav.html`'s button
carried `.nav-toggle`, which sets `font: inherit; cursor: pointer` (app.css
:1009 `.btn-tile` does the same, for the same reason).

**Fix:** added `font: inherit; cursor: pointer;` to `.menu-tile`, with a
comment. Provably a no-op for the existing `<a>` tiles — they already compute
those inherited values, and `menu.png` came back **byte-identical** from
`make docs-shots` after the change. Re-measured after the fix: the button's
`fontFamily`, `.menu-ico` font, `.menu-label` font, `line-height` and
`cursor` now match the anchor tiles exactly.

### 2. Two comments still point at `index.html` for `#pfand-modal` — **FIXED**

`web/ui/partials/elevation_prompt.html:18` and `web/ui/layouts/base.html:56`
both cite "index.html's `#modifier-modal`/`#hold-modal`/`#pfand-modal`" as
the precedent this codebase follows for dialogs outside a `<form>`. Two of
the three are still there; `#pfand-modal` is not. In a codebase that leans
this hard on cross-referencing comments as navigation, a pointer to a file
that no longer contains the thing is a real (if low-severity) papercut.

**Fix:** both now read "index.html's `#modifier-modal`/`#hold-modal` (and
menu.html's `#pfand-modal`, moved there in ut-docs#1334)". Comment-only; no
rendered output changes. `web/ui/**` is in the docs-shots surface, so
`make docs-shots` was re-run and `manifest.json`'s `surface_sha256` updated.

### 3. `menu.png` cannot show the new tile — **ACCEPTED (deferred, not a blocker)**

Tester flagged this; independently confirmed. At the docs-shots harness's
fixed 1024×600 **non-full-page** viewport, `/menu` has a
`document.scrollHeight` of **1298px**, and the new tile's box is at
**y = 992.6, height 160** — entirely below the fold. `menu.png` came back
byte-identical from `make docs-shots`, which is the correct and expected
outcome, not a missed regeneration.

This is a **pre-existing harness limitation, not something #1334 introduced
or should fix**: the shot already cannot show `Registers` (y = 814) or
several other tiles either. The manual prose *is* updated and accurate, which
is the part that matters for the ship-the-manual-with-the-feature rule.
Fixing it properly means teaching the docs-shots harness a per-topic
full-page or taller-viewport mode — worth a Backlog card, out of scope here.

### 4. `make docs-shots` emits nondeterministic screenshot churn — **ACCEPTED (pre-existing, out of scope)**

Noticed while regenerating for fix #1. Two back-to-back `make docs-shots`
runs with **zero source change** produce differing `sell.png` bytes
(`cmp` differs at byte 52198), and a later run additionally churned
`invoices/my-reports/order-status/reports/tax-codes` in en/tr — pages that
presumably paint a clock or a generated-at stamp. `guard-docs-shots.sh` only
checks the surface hash and the topic hashes, not image bytes, so this never
fails CI; it just adds gratuitous binary churn to unrelated PRs. Left alone
here, and the reviewer's regenerated images were restored to `162df67`'s
bytes so this PR carries **no** spurious PNG churn — only the
`surface_sha256` line actually changed. Worth its own Backlog card.

### 5. Manifest `algorithm` string understates the surface set — **ACCEPTED (noted, out of scope)**

`web/help/img/manifest.json`'s `algorithm` field describes the fileset as
"web/ui/** + non-test internal/pages/**.go". The guard's actual error message
is "web/ui/**, **web/public/****, or internal/pages/**.go" — `web/public/**`
is in the hash but missing from the documented description (this is how the
CSS fix above turned out to need a regeneration at all). Cosmetic
documentation drift, unrelated to #1334.

### 6. `index_osk_test.go` now hosts a `/menu` test — **ACCEPTED (false alarm on closer read)**

The repointed `TestPfandDialogStaysKeyboardReachableAndUsesEnglishLabel`
drives `GET /menu` from a file named `index_osk_test.go`. The file still
legitimately owns `TestIndexScanRowKeyboardIsOnDemand` (a genuine `/`
test), the move is called out in an added comment, and the test correctly
reuses the existing `newMenuPageTestDeps` helper from `menu_page_test.go`
rather than duplicating setup (which also shrank it by 13 lines). Both
assertions still bite — proven by the revert run above. Not worth a file
split; leaving it.

## Translation sanity check (fa / ar / tr, hand-translated)

Dev hand-translated these because the Ollama pipeline's homelab NAS was
unreachable from the sandbox, and asked for a sanity check. All three read
correctly and are **consistent with the shipped UI strings**, which is the
strongest available signal:

| Locale | Help sentence uses | `pfand.action` in `web/locales/` | Match |
|---|---|---|---|
| fa | بازپرداخت ودیعه | بازپرداخت ودیعه | ✅ |
| ar | استرداد الوديعة | استرداد الوديعة | ✅ |
| tr | Depozito iadesi | Depozito iadesi | ✅ |

Each sentence says what the en one says ("Deposit refund opens in place as a
small form instead of navigating away"). No untranslated English left in; ZWNJ
used correctly in the fa text (`به‌جای`, `به‌صورت`, `همین‌جا`); sentence-final
punctuation matches the surrounding paragraph in each file; register matches
(declarative, same as the en sentence, alongside the imperative steps around
it). Each locale's term also matches what its own `display.md` already uses
for the same concept. Nothing to flag.

## UX-guideline check

- **Design tokens:** reuses `--surface`/`--border`/`--radius-lg`/`--shadow`/
  `--text`/`--brand` via the existing `.menu-tile`/`.menu-ico`/`.menu-label`
  classes. No new colours or spacing. The reviewer fix adds only
  `font: inherit; cursor: pointer` — normalisation, not a new token.
- **Long strings:** driven at 360px in en/fa/ar/tr; no overflow, no
  horizontal document scroll, box identical to siblings in all four.
- **RTL:** the tile lays out correctly under `dir="rtl"` (fa/ar measured);
  the dialog keeps its existing logical-property positioning and is covered
  by `form-label-layout-300`'s fa RTL test, which passes against the new
  `/menu?lang=fa` location.
- **No modal blocker on the checkout path:** the dialog is opened from
  `/menu`, never from the sale screen, and it is non-modal `.show()` (asserted
  by the Go test and by `deposit-refund-osk-1248`). The sale screen strictly
  *lost* UI here; nothing was added to it. Offline-first is untouched — no
  network call is involved in opening it.
- **Manual shipped with the feature:** `web/help/{en,fa,ar,tr}/menu.md`
  updated in the same branch, prose accurate; screenshot situation is
  finding #3.

## Reviewer changes made on top of `162df67`

Four files, all additive; the two `web/ui/**` edits are comment-only:

- `web/public/app.css` — `+font: inherit; cursor: pointer;` on `.menu-tile`,
  plus an explanatory comment (finding #1).
- `web/ui/partials/elevation_prompt.html` — comment cross-reference corrected
  (finding #2).
- `web/ui/layouts/base.html` — same (finding #2).
- `web/help/img/manifest.json` — `surface_sha256` only, from re-running
  `make docs-shots` as those `web/ui/**` and `web/public/**` edits require.
  Regenerated PNGs were deliberately restored to `162df67`'s bytes so no
  nondeterministic image churn rides along (finding #4); the guard passes
  that way.

Full gate re-run after these changes: gofmt clean, build/vet/test pass,
**32/32 guards pass**, 25/25 touched Playwright tests pass.

## Deferred

- **`menu.png` cannot show the new tile** (finding #3) — needs a docs-shots
  harness change (per-topic full-page or taller viewport), not a #1334 change.
- **`make docs-shots` screenshot nondeterminism** (finding #4) — repo-hygiene
  Backlog card.
- **Manifest `algorithm` string omits `web/public/**`** (finding #5).
- **Arabic and Turkish dialog-render coverage**, already flagged by Tester as
  not checked. Confirmed still true: `form-label-layout-300` drives the
  dialog in en, de-simulated and **fa** only; ar/tr get the tile measured at
  360px (this review) but the dialog's own contents are not rendered in those
  two locales by any spec. Pre-existing coverage shape, unchanged by this PR.
