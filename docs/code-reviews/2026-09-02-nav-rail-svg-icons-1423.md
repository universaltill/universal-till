# Code review: sale-screen nav rail → inline SVG icons — ut-docs#1423

**Branch:** `fix/1423-nav-rail-svg-icons` · **Reviewer:** independent
fresh-context Sonnet subagent in an isolated worktree (complexity:easy →
fresh-context review per the `scrum-master` skill's model-routing table)

## Change

Third report of the same symptom from the product owner, live on the
TECLAST tablet (v0.9.1): the 🔒 lock and 🐞 bug-report icons in the sale
screen's left rail rendered visibly smaller than their siblings, after
#1332's second pass and #1348 had each bumped the emoji font-size per glyph
(`.ico-boost`) and verified on desktop Chromium. Root cause is structural,
not a tuning miss: an emoji is a glyph from whatever colour-emoji font the
platform ships (Apple Color Emoji on macOS, Noto Color Emoji on Android and
the Linux CI box), and each font pads each glyph differently inside its
em-square, so a per-glyph bump tuned on one platform does not carry to the
next.

- **`internal/httpx/icons.go` (new)** — 11 Lucide (ISC) icons as bare path
  data in one map, rendered by a new `{{ icon "name" }}` template func
  through ONE shared `<svg viewBox="0 0 24 24" stroke="currentColor"
  stroke-width="2" aria-hidden="true" focusable="false" data-icon="…">`
  wrapper, so no icon can drift on its own again. Unknown name renders
  nothing; the name is HTML-escaped into `data-icon`. Registered in
  `baseFuncs` (`httpx.go`), so every renderer (`FuncsFor` copies it) has it.
- **Templates** — `nav.html` (🧾 ☰ 📦 🛎️ ?), `session_chip.html` (👥 🏷️ 🌐 👤
  🔒), `bugreport_chip.html` (🐞) now call `{{ icon }}`. The bug-report
  toggle gains a `.visually-hidden` text label (`issuereport.nav_label`,
  key already existed): the emoji used to be its only text node, and the
  phone-width layout guard (#413 spec) requires every nav control to have
  text. Deliberately `.visually-hidden`, not `.nav-toggle-label` (which
  becomes visible in the ≤480px top bar, whose width budget never had room
  for this label).
- **`web/public/app.css`** — `.nav-toggle-ico svg { 1.5rem × 1.5rem }`;
  `.ico-boost` removed (dead). Second finding from the same tablet
  screenshot: the two rail items that are `<button>`s (bug-report, Lock)
  measured 48px wide against 59.5px for the `<a>` tiles — a `<button>`
  shrink-wraps even as a stretch flex item / inside a block `<form>` —
  fixed with `.nav button.nav-toggle { inline-size: 100% }`, reset to
  `auto` in the ≤480px top-bar block where controls sit in a row.
- **`android/.gitignore`** — `.kotlin/` (see review finding 1).
- **Manual screenshots** regenerated (`make docs-shots`, 96 PNGs +
  manifest); the rail appears in every page shot.

## Tests (TDD — red confirmed before green)

- `internal/httpx/icons_test.go`: every icon uses the shared wrapper and
  carries no width/height of its own (size is CSS-only); unknown name →
  empty; **every `{{ icon "x" }}` in `web/ui/**` resolves and no
  `.nav-toggle-ico` span carries text/emoji any more** (≥11 references).
- `e2e/tests/nav-rail-svg-icons-1423.spec.ts` (default project): every
  rail icon is an SVG with no text; all rail SVGs share one rendered box
  (<1px); ≤480px top bar still shows icon + visible label.
- `e2e/tests/nav-rail-svg-icons-lock-1423.spec.ts` (auth project — the
  session chip only renders with a real session, same reason as #1346's
  exemption; added to `AUTH_ONLY_SPECS` and the fixtures-import guard's
  `EXEMPT_FILES` with that rationale): all **11** icons incl. `lock` share
  one box; every `.nav-toggle` tile (`<a>` and `<button>` alike) has one
  width and one background.
- `nav-rail-icon-consistency-1348.spec.ts` third case now asserts the SVG
  instead of the superseded `.ico-boost` class.
- Red proof, done twice: by the author (stash `web/`) and independently by
  the reviewer (`git checkout main -- web/ui web/public` in its worktree):
  Go test fails "expected the rail's 11 icons to be referenced via {{ icon
  }}, found 0" (plus the three emoji-span hits); the e2e spec fails 3/3
  "element(s) not found" on `svg[data-icon=…]`. Restore → both green.

## Independent review — findings

1. **Stray build artifact in the WIP snapshot (should-fix, fixed).**
   `android/.kotlin/sessions/kotlin-compiler-….salive` (0 bytes, a Kotlin
   daemon session lock left by the local APK build used for the on-device
   check) had been swept in by `git add -A`. Removed from the index and
   `.kotlin/` added to `android/.gitignore` so it cannot recur.
2. **Manual prose still names the rail buttons by their old emoji (real,
   out of scope, filed).** `web/help/en/bug-reporting.md`,
   `order-status.md`, `sell.md`, `users.md`, `menu.md`, `quickstart.md`
   (and the fa/ar/tr copies) say "the 🐞 button", "the 🛎️ icon", "(👤)".
   The icons still mean the same thing, so the steps are not wrong, but a
   reader scanning for a ladybug emoji finds a line-art bug. Filed as a
   Backlog card (linked from ut-docs#1423) rather than touching seven
   locales' wording in this branch.

Everything else checked clean by the reviewer: html/template escaping
sound (constant SVG bodies, escaped name, no user input reaches the func);
`currentColor` inherits the rail's `#fff`; the `tag` icon's
`fill="currentColor"` dot is fine under a `fill="none"` parent; no new
i18n keys, no hardcoded strings (`guard-i18n` 1338 keys resolve); no
physical left/right CSS added; no new colours; the `{{ }}`-inside-an-HTML-
comment bug class (hit once during dev — Go templates parse actions inside
HTML comments — and fixed) does not recur in any template; the two
recurring pipeline bug classes (missing `os.MkdirAll`, cwd-relative path)
do not apply (no file I/O); no secrets, no real shop names.

**Delta after the reviewed snapshot** (author-verified, not re-reviewed):
the `.nav button.nav-toggle` width fix + its auth-spec assertion, the
gitignore, and the second screenshot regeneration. Full rail suites rerun
green after it: auth 18/18, default 33/33; `guard-docs-shots` fresh.

## Verification beyond automated tests

- **On the real tablet, twice.** Built a release-signed APK locally with
  the Key Vault keystore (`versionName 0.9.1-1423`/`-1423b`,
  `versionCode 9002`), installed over v0.9.1 via adb, screenshotted with
  `screencap`: every rail icon — lock and bug included — now renders at one
  size, and after the width fix every tile is one width and one shade
  (the "lighter" look of the two buttons was the narrower tile). Catalog
  and session survived the upgrade. Keystore deleted from disk after each
  build. The tablet is left on the dev build; the next real release
  (v0.9.2 → versionCode 9002 as well) installs over it normally.
- Regenerated `web/help/img/en/sell.png` inspected: six-icon rail
  consistent.
- Gate: `gofmt -l .` clean, `go build ./...`, `go vet`, `go test ./...`
  (43 packages ok; the one failure, `internal/server`
  `TestListenWithFallback_WildcardHostFallsBackToLoopback`, fails on `main`
  too and is the known macOS-only ut-docs#1413), all 18 applicable
  `scripts/ci/guard-*.sh` pass locally (`guard-commit-attribution` needs
  CI stdin).

## Verdict

**Safe to merge.** Deferred: the manual-prose follow-up (finding 2).
