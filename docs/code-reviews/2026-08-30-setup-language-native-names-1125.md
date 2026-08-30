# Code review — setup wizard/settings/menu language pickers show native names, not bare codes (ut-docs#1125)

- **Date:** 2026-08-30
- **Branch:** `fix/1125-setup-language-native-names`
- **Reviewer:** independent reviewer (Opus, different model from the Sonnet
  implementer — `complexity:medium` per the model-routing table), run as a
  worktree-isolated subagent
- **Verdict: SAFE TO MERGE.** One blocking finding from the independent
  review was fixed in this branch before merge; a follow-up help-topic
  accuracy issue was also fixed; everything else was either confirmed clean
  or is a deliberately-deferred non-blocker.

## What shipped

The setup wizard's language step mixed two label styles: core locales
(ar/en/fa/tr) showed bare two-letter codes while marketplace-catalog
locales (Deutsch/español) showed proper native names — a non-technical
shop owner doesn't know "fa" means Persian.

**Fix:** added `httpx.NativeLanguageName(code string) string` (x/text's
CLDR self-names, fully offline) — moved out of a private duplicate
(`nativeLanguageName`) that already existed in
`internal/pages/setup_language_catalog.go` for the marketplace tiles, so
there is now exactly one native-name source for the whole codebase, not
two that could drift. Registered as the `nativelocalename` template func
and used at all three sites in this codebase that render
`httpx.AvailableLocales()` as button/option labels:

- `web/ui/pages/setup.html` — the wizard's language step (the ticket's
  primary target)
- `web/ui/pages/settings.html` — the shop's default-locale picker (the
  ticket's own acceptance criterion: "no bare locale code shown anywhere,
  on the wizard or in settings")
- `web/ui/pages/menu.html` — the staff `/menu` page's language row (a
  third render site found while fixing the other two, same bug — not in
  the original ticket text, but the identical fix and exactly what the
  ticket's own "check whether the same list is rendered anywhere else...
  so the two do not drift" note was asking for)

**Flag decision** (the ticket's other acceptance criterion — "recorded on
the ticket before implementation"): native name only, no flag. Recorded
as a comment on ut-docs#1125 with sources before writing any template
code: UX/accessibility consensus (a flag represents a country, not a
language — one language can span many flags) and SumUp's own language
switcher (no flags there either).

Also regenerated `web/help/img/**` + `manifest.json` via `make docs-shots`
(required — `web/ui/**` and `web/public/**` are both in the guard's
surface hash) and updated the `quickstart.md` help topic's "Change the
language" step in all four locales (en/fa/ar/tr) — it described the old
bare-code UI ("language letters, e.g. EN or FA").

## Independent review findings (Opus, worktree-isolated subagent)

### Blocking — fixed: `.menu-lang-btn` CSS mangled the native names it now receives

`web/public/app.css`'s `.menu-lang-btn` carried `text-transform: uppercase`
and `letter-spacing: .04em`, written for the old two-letter labels and
harmless on them. Applied to native names they are actively destructive:

- `text-transform: uppercase` turned "Türkçe" into the shouted "TÜRKÇE" —
  not the language's own name anymore.
- `letter-spacing: .04em` on Arabic-script names (`العربية`, `فارسی`)
  pulls the cursively-joined glyphs apart, which reads as broken text to
  a native speaker — a direct violation of the ticket's own "native names
  render correctly in RTL" acceptance criterion.

Invisible to `make docs-shots`: the `/menu` language row sits below the
tile grid (`.menu-lang { margin-top: 1.6rem; }`, after `.menu-grid`), so
the topic's screenshot (`menu.png`, captured above the fold) never shows
it. `setup.html` and `settings.html` were confirmed clean (no such
properties on their equivalents).

**Fixed:** dropped both properties from `.menu-lang-btn`, kept
`min-width`/`font-weight`/`justify-content`, with a comment explaining why
they must not come back (`.menu-lang-btns` is `flex-wrap: wrap`, so a
longer native name is fine without the width floor being a cap).

### Non-blocking, fixed: two of the three render sites had zero test coverage

Proven with a partial revert: reverting `settings.html` and `menu.html` to
bare codes (keeping `setup.html`'s fix and all tests) left the **entire
test suite green** — the wizard's own test only exercises `/setup`.
**Fixed** — two tests added, each independently confirmed red-then-green:

- `internal/pages/menu_page_test.go` →
  `TestMenuPageLanguageRowShowsNativeNamesNotBareCodes`
- `internal/pages/settings_page_test.go` →
  `TestSettingsPageDefaultLocalePickerShowsNativeNamesNotBareCodes` (also
  pins that the `<option value="...">` stays the bare code — only the
  visible label changes; the form posts the code, and
  `settings_page.go` validates it against `AvailableLocales()`)

### Non-blocking, fixed: two comments misidentified `/menu` as the self-order kiosk

`internal/httpx/httpx.go` and its test both called the third picker the
"self-order menu." It isn't — `/menu` is the staff menu page (Journal /
Reports / Settings / Shifts tiles, `menu` help topic), auth-gated, wired
through the cashier's normal session. The actual self-order kiosk
(`self_order.html`) is a structurally different, auth-exempt surface
under the ADR-0020 kiosk-isolation rules this codebase enforces by CI
(`guard-kiosk-engine.sh`) — a wrong breadcrumb here could mislead a future
change into thinking this touches that isolation boundary. **Fixed**:
reworded to "staff menu page (/menu)" in both comments.

### Follow-up, fixed in this branch: the rewritten help prose didn't match the UI

The first pass at `quickstart.md`'s "Change the language" step said "Tap
your language's own name **at the top of the screen** (for example
**Deutsch**...)". Two problems: "Deutsch" isn't a shipped locale (only
ar/en/fa/tr ship — Deutsch only appears on the wizard's separate
install-a-language-pack tiles, a different affordance), and the language
row isn't at the top of the screen — it's on `/menu`, reached via
**☰ Menu** from the sale screen, below the tile grid. **Fixed** in all
four locales: "Open ☰ Menu and tap your language's own name near the
bottom (for example Türkçe/English or فارسی)" — examples chosen to be
shipped locales and to never be the reader's own language in each
locale's version.

### Non-blocking, deferred: `e2e/tests-docs/lib.js`'s manifest `algorithm` string is stale

Pre-existing, unrelated to this ticket: the string it writes into
`manifest.json` describing its own surface omits `web/public/**`, though
the actual hash (both `lib.js` and `guard-docs-shots.sh`) does include it.
Cosmetic (a comment/label, not the hash itself) — noted for a future
one-line fix, not filed as a separate card since it's this low-priority.

### Nits, not changed

- The bare-code regression regexes in the three new/existing tests are
  anchored on the exact `href="...">CODE</a>` (or `<option>`) shape
  immediately following; a template restructure that adds another
  attribute would make the guard pass vacuously rather than fail loudly.
  Accepted — matches this test file's existing style, and over-triggering
  (the flag-emoji regex's non-scoped search, the opposite failure mode) is
  the safer direction where it appears.

## Verification performed

| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l .` | pass / pass / empty |
| `go test ./internal/httpx/... ./internal/pages/...` | pass |
| `go test ./...` (whole repo, run twice — before and after the review fixes) | pass |
| `-race` on `internal/httpx` (whole package) and the touched `internal/pages` tests | pass |
| `bash scripts/ci/guard-data-access.sh` | pass |
| `bash scripts/ci/guard-kiosk-engine.sh` | pass |
| `bash scripts/ci/guard-plugin-menu-read.sh` | pass |
| `bash scripts/ci/guard-i18n.sh` | pass (no new key needed — this is a rendering-source change; names come from CLDR at runtime) |
| `bash scripts/ci/guard-compliance-claims.sh` | pass |
| `bash scripts/ci/guard-docs-shots.sh` | pass (after `make docs-shots`, twice — once for the template change, once for the CSS fix) |
| `bash scripts/ci/guard-help-topics.sh` | pass |
| `bash scripts/ci/guard-webkit-version.sh` | pass |
| `bash scripts/ci/guard-kiosk-launch-flags.sh` | pass |
| `bash scripts/ci/guard-android-status-address.sh` | pass |
| `bash scripts/ci/guard-android-i18n.sh` | pass |
| `bash scripts/ci/guard-emoji-font.sh` | pass |
| `bash scripts/ci/guard-htmx-loaded.sh` | pass |
| `bash scripts/ci/guard-autofill-suppression.sh` | pass |
| `bash scripts/ci/check-brand-assets.sh` | pass |
| `bash scripts/ci/guard-makefile-version.sh` | pass |

### TDD re-verification (done independently, both by the implementer and the reviewer)

**Implementer, before commit** — reverted only the production files
(`internal/httpx/httpx.go`, `internal/pages/setup_language_catalog.go`,
the three templates), confirmed the new tests failed for the stated
reason:

```
internal/httpx/template_helpers_test.go:75:13: undefined: NativeLanguageName
...
setup_page_test.go:873: GET /setup body missing native language name "العربية"
setup_page_test.go:878: GET /setup still renders bare locale code "ar" as a button label
```

Restored, green again.

**Reviewer, independently, in an isolated worktree** — same revert,
same failures reproduced exactly. Also ran an additional partial revert
(restore only `httpx.go`/`setup_language_catalog.go`/`setup.html`, leave
`settings.html`/`menu.html` reverted) to prove the settings/menu gap —
**entire suite still passed**, which is what motivated the two new tests
above. After adding those tests and reverting `settings.html`/`menu.html`
again:

```
menu_page_test.go:264: GET /menu language row missing native language name "العربية"
menu_page_test.go:272: GET /menu still renders bare locale code "ar" as a button label
settings_page_test.go:1387: settings default-locale picker missing native language name "العربية"
settings_page_test.go:1396: ... still labels an option with the bare code "ar"
```

Restored, green again.

## Checked and found clean

- **Reuse, not duplication** — `httpx.NativeLanguageName` is the single
  source; the old private `nativeLanguageName` is deleted, and the
  marketplace install-tile path (`setupLanguageCatalogEntries` →
  `httpx.NativeLanguageName`) still passes its own existing test
  (`setup_language_catalog_test.go`, asserts "Deutsch").
- No leftover/unused imports (`golang.org/x/text/{language,display}`
  moved from `internal/pages` to `internal/httpx`, confirmed by `go
  build`/`go vet`).
- **RTL correctness** — no extra markup needed. Each native name is the
  sole content of its own `<a>`/`<option>`, so the bidi algorithm resolves
  it correctly in both `dir=ltr` and `dir=rtl`. `.setup-langs`/`.menu-lang`
  use `gap`/`flex-wrap`/block-direction margins, no physical `left`/
  `right` — compliant with `CLAUDE.md`'s logical-CSS RTL rule.
- **`guard-i18n.sh` genuinely needs no new key** — this is a
  rendering-source change (`{{ . }}` → `{{ nativelocalename . }}`), not
  new copy; the names come from CLDR at runtime.
- **No missed render site** — exactly three `range locales` template
  sites exist repo-wide; all three fixed.
- **`menu.html`'s inclusion is in-scope, not scope creep** — same bug,
  same one-token fix; shipping two of three sites would have left the bug
  visibly half-fixed on the exact page the ticket's own "check elsewhere"
  note was warning about.
- No file I/O in the diff at all (template/render-source change only), so
  the two recurring bug classes this pipeline watches for
  (`os.MkdirAll`, `paths.Data(...)`) don't apply.
- No real client/shop name, no secret-shaped literal.
- Output is `html/template`-escaped; input is either the embedded
  `AvailableLocales()` set or a marketplace code already filtered by
  `isPlausibleLocale` — no XSS surface change.
- fa/ar/tr help-topic translations (both the original three lines and
  this review's rewritten ones) read as natural, grammatical, idiomatic —
  no garbled machine-translation tells.

## Deferred / known gap

- The wizard's own acceptance criterion asking for "screenshot from the
  real Pi touchscreen, not a desktop browser" is not obtainable from a
  cold cloud pipeline session — no physical device access. The Playwright
  `make docs-shots` screenshots (headless Chromium) are real evidence the
  fix renders correctly, including the pixel-level diff on `sell.png`
  proving the fa/tr native names actually changed on screen — but they
  are not the real-device photo the ticket asked for. Noting this
  explicitly rather than silently treating the AC as satisfied; a human
  with the device can confirm at the next opportunity.

## Merge

`merge_method: "merge"` (never squash/rebase — ut-docs#250), after CI is
green on the PR.
