# Code review: black-and-white (monochrome) and dark built-in themes

**Card:** universaltill/ut-docs#2176
**Branch:** `feat/2176-black-white-and-dark-themes`
**Complexity:** medium — Dev inline (Sonnet), review at Opus (independent
subagent, different model from the one that wrote the diff)

## What shipped

Two new built-in themes, `web/public/themes/monochrome.css` and
`web/public/themes/dark.css`, selectable from Settings → Theme with no
marketplace install required. Both are picked up automatically by the
existing `availableThemes()` (`internal/pages/themes.go`) — it lists any
`.css` file under `web/public/themes/` — so no Go code changed. Theme keys
`monochrome`/`dark` don't collide with the four existing built-ins
(`amber`/`fresh`/`monarch`/`slate`) or with the real
`ut-plugin-theme-midnight` plugin's own `midnight` key.

A driven-run screenshot audit against the dark theme (switching the app to
it and actually looking, not just reading the CSS) found two classes of
app.css token reuse that broke under a genuinely dark surface, both fixed
in this diff:

- `.vg-img`'s hardcoded `#fff` thumbnail backdrop → `var(--surface-2)`
  (catalog variant editor, `/items`).
- `.products`'/`.tender-scroll`'s scroll-shadow cover gradients, hardcoded
  to `rgba(255,255,255,0)`, swapped to
  `color-mix(in srgb, var(--surface), transparent 100%)` — this exact fix
  was already flagged and deliberately deferred in both call sites' own
  comments (ut-docs#1313, ut-docs#2128) "pending a dark theme to verify
  against"; this card is that theme, so the deferred fix now lands.

New regression test: `TestThemesHandler_MonochromeAndDarkAreBuiltIn`
(`internal/pages/themes_test.go`) — both keys listed as `built-in`, no key
collisions, required CSS tokens present in each file, and monochrome's
`--success`/`--danger`/`--warning` are genuinely `#000000` (no
colour-only meaning).

Manual: `web/help/{en,de,fa,ar,tr}/display.md` gained a new step
documenting both themes, translated into all five shipped locales;
screenshots regenerated via `make docs-shots`.

## Independent review — findings

Spawned an Opus subagent in an isolated git worktree (different model
from Sonnet, which wrote the diff) with instructions to run the build/
tests/guards itself, re-derive the WCAG contrast math independently, and
specifically judge two things the implementer had already flagged as
judgement calls: whether `.pin-dot.filled` (login/PIN screen) and
`.offline-toggle`'s `accent-color` needed fixing for the dark theme.

**Verdict: NEEDS FIXES — 3 blockers found, all fixed before merge.**

| # | Severity | Finding | Fix |
|---|---|---|---|
| 1 | Blocker | `monochrome.css`'s own header comment claims the fiscal/sync nav-badge dot's presence/absence survives as a non-colour cue — but collapsing `--warning` to black also blacked out the dot itself (app.css: `background: var(--warning); border: 1px solid var(--brand)`, both black on a black nav). 1.00:1 against the rail. The dot is `aria-hidden="true"` and conditionally rendered — its visibility *is* the signal, no text fallback exists. | `.nav-badge { background: var(--accent-contrast); }` → white dot, 21:1. |
| 2 | Blocker | `dark.css` didn't override `--danger`. app.css's default (`#dc2626`) is tuned for a white surface (4.83:1 there) and only manages 3.67:1 on this theme's `--surface` / 3.88:1 on `--bg` — fails WCAG 1.4.3 (4.5:1) on `.field-error-msg`, `.split-tender-status.error`, `.record-dialog-msg`, `.stock-low`, `.pos-notice.error` (tender/split, dialogs, error page — all named acceptance-criteria surfaces). `--success` (5.38:1) and `--warning` (5.57:1) on `--surface` already clear 4.5:1 and were left alone. | `--danger: #f87171` → 6.41:1 on `--surface`, 6.77:1 on `--bg`. |
| 3 | Blocker | `dark.css` never declared `color-scheme: dark`. Native form controls and scrollbars kept rendering in their light skin (e.g. a bright white scrollbar track down every dark panel). | One line in `:root`. |
| 4 | Real, fixed | `dark.css`'s `--brand` sweep (h2/`.suggest-chip`/`.menu-tile:hover`) missed `.offline-toggle input { accent-color: var(--brand) }` (sale screen, `kiosk.offline`) — 1.01:1 against `--surface`, so ticking it looked like nothing happened. | `accent-color: var(--accent)` → 9.53:1. |
| — | Judged, not a bug | `.pin-dot.filled` also reuses `--brand` (1.01:1 against `--surface` in dark) — looked like a fourth blocker until the reviewer traced `web/ui/pages/login.html` and confirmed it's a **standalone** document that links only `/public/app.css`, never the theme `<link>` (`web/ui/layouts/base.html:23` is the only place that link is emitted). All five standalone pages (`login`, `setup`, `self_order`, `self_order_shop`, `order_tracking`) are structurally theme-immune. Not reachable, so not a defect. | None needed. |
| — | Nitpick, fixed | `monochrome.css`'s `--muted` comment claimed 7.83:1; recomputed exactly: 7.81:1. | Comment corrected. |
| — | Deferred (out of scope) | Monochrome still shows literal `rgba(22,163,74,.12)`/`rgba(220,38,38,.1)`/`rgba(217,119,6,.12)` tint *backgrounds* on `.pos-notice`/`.tag.warn`/`.sync-banner` — no token reaches them, so a "no colour" theme still shows faint green/red/amber washes behind black, legible text. Arguably a tint rather than a meaning-carrier (WCAG 1.4.1 is about conveying *information* by colour alone, and the text itself carries the meaning here), but it's a gap against the theme's stated intent. Filed as a candidate follow-up, not fixed here — fixing it means auditing every hardcoded semantic-tint rgba() in app.css, which is the "general design-system refresh" this card's own non-goals rule out. |
| — | Noted, not fixed | The new test's duplicate-key loop (`seen[o.Key]++`) can never actually fail as written — `themeTestDeps` in this test doesn't insert any `plugin_entries` rows, so it only ever iterates unique built-in filenames. Harmless (the collision-freedom claim in this review is still correct — verified by inspection instead — but the assertion itself is vacuous). Left alone to avoid widening the diff over a test-only nit. |

Contrast-ratio math was independently re-derived (sRGB relative luminance,
then `(L1+0.05)/(L2+0.05)`) for every ratio the CSS comments claim, not
just spot-checked — all matched to the stated decimal except the one
`--muted` typo above.

## Verified beyond automated tests

- **TDD claim re-verified personally** (by the independent reviewer, in
  its isolated worktree, three separate mutations): renaming both new
  theme files away → test fails naming exactly which key/file is
  missing and a 404 on the CSS route; stripping `--focus-border` from
  `dark.css` → test fails naming that exact token; reverting monochrome's
  `--danger` to a real colour → test fails naming that exact assertion.
  Each file was restored byte-for-byte after (md5-verified) before
  reporting green again.
- **Driven-run screenshot verification** (by the implementer, before
  handoff to review): started a real throwaway till, switched the theme
  setting via the actual `/api/settings/theme` endpoint, and looked at —
  not just read the CSS for — the sale screen, Settings, `/items`'
  two-pane admin shell, and a `record-dialog` (catalog item editor,
  including its Variants tab, to confirm the `.vg-img` fix) in both new
  themes at the 1024×600 kiosk floor. This is what caught findings 1-4
  in the first place (well, the pre-review versions of the `--cat-color`
  and `--brand` issues the implementer already found and fixed before
  the diff went to review) — a static CSS read would not have.
- Full `go build ./...`, `go vet ./...`, `go test ./...` (whole suite,
  not just `internal/pages`), `golangci-lint run ./...` (0 issues),
  `gofmt -l` (clean) — all green, run twice: once before review, once
  after applying the review's fixes.
- Every CI-blocking guard listed in `universal-till/CLAUDE.md`'s "Before
  committing" section run locally and green (`guard-shellcheck-version.sh`
  excluded — no `shellcheck` binary available in this session's sandbox,
  an environment limitation unrelated to this diff, not a regression it
  introduced).
- `guard-docs-shots.sh`: the CSS changes in this diff (`color-mix()` swap
  in `app.css`, plus the two theme files) turned out to move a handful of
  pixels in the *default*-theme (monarch) screenshots after all — verified
  by actually re-running `make docs-shots` in full rather than assuming
  `color-mix(in srgb, white, transparent 100%)` renders byte-identically
  to a literal `rgba(255,255,255,0)` (it doesn't, at the sub-pixel level,
  in this Chromium build) — so the committed screenshots are a real
  regeneration, not a hash-only shortcut.

## Deferred / follow-up candidates (not fixed here, filed separately)

- Monochrome's hardcoded semantic-tint `rgba()` backgrounds on
  `.pos-notice`/`.tag.warn`/`.sync-banner` (see table above) — filed as
  universaltill/ut-docs#2192.
- `web/help/img/manifest.json`'s own `algorithm` description string
  under-states its file scope (says `web/ui/** + internal/pages/**.go`,
  omits `web/public/**`) — pre-existing, unrelated to this card, noticed
  only because editing `web/public/themes/*.css` demonstrably changed the
  hash it computes.

## Verdict

**Safe to merge.** All three blockers the independent review found are
fixed and re-verified in this diff; the one deliberately-deferred item
(monochrome's tint backgrounds) is a real but small gap against the
theme's stated intent, not a regression, and is left for a follow-up
card per this card's own "not a general design-system refresh" scope
note.
