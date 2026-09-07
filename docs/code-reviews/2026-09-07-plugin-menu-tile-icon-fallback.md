# Code review — plugin-contributed menu tiles render ▪️ black square (ut-docs#1722)

**Date:** 2026-09-07
**Branch:** `fix/1722-plugin-menu-tile-icon-fallback`
**Reviewer:** independent fresh-context Sonnet subagent (per the `complexity:easy`
model table — Sonnet wrote it, Sonnet reviews it in a clean context)
**Verdict:** SAFE TO MERGE. One real inaccuracy found in a code comment's stated
rationale; fixed directly. No test, build, or gate regressions.

## The bug and the fix

`internal/pages/menu_page.go`'s menu-tile `add` closure fell back to the literal
`"▪️"` glyph for any route with no entry in `iconFor`/`iconSVGFor`. Core can never
enumerate the routes a plugin brings with it, so any plugin-contributed tile —
concretely, `ut-plugin-faq`'s Help/FAQ tile — hit this fallback, and it read as a
rendering failure rather than a deliberate icon (the exact symptom ut-docs#1371
reported for `/orders`, fixed there only because that was a core route with a
map entry to add). The fix adds a `"puzzle"` entry to `internal/httpx/icons.go`'s
shared drawn-icon set (real Lucide "puzzle" path, ISC-licensed, same style as
every other entry) and a `genericFallbackIcon` const in `menu_page.go`, used as
the terminal fallback in the `add` closure instead of `"▪️"`.

## Acceptance criteria — verified against ut-docs#1722

1. **No-icon tile renders a deliberate generic glyph, not ▪️, not empty.**
   Confirmed: `genericFallbackIcon = "puzzle"` is always resolvable (guarded by
   `TestMenuPage_EveryDrawnTileIconNameResolves`, extended in this diff to cover
   it), and `menu.html`'s tile markup (`{{ if .IconSVG }}{{ .IconSVG }}{{ else }}
   {{ .Icon }}{{ end }}`) never renders empty once `IconSVG` is set.
2. **Plugin-contributed tiles get it.** `TestMenuPage_UnmappedRouteGetsFallbackIcon`
   uses `/some-plugin-page`, an unmapped route — structurally identical to how
   `ut-plugin-faq`'s route reaches `menu_page.go` (via `d.MenuSnapshot()`, which
   core cannot special-case per plugin) — and asserts `▪️` is gone and
   `data-icon="puzzle"` is present. This is the test ut-docs#1371's regression
   test structurally could not be, since `/orders` is a core route that can
   always get a specific map entry.
3. **Architect decision (no plugin-declared icon in this change) — re-verified,
   found one inaccuracy, fixed.** The doc comment above the `add` closure
   claimed `ManifestEntry.IconPath` is "persisted but never read for rendering
   anywhere today." **This is false as originally written** — grepped the
   codebase myself: `data.ButtonEntryRow.IconPath` (a different repo method,
   `ListButtonEntries`, feeding plugin *buttons* not *menu* tiles) **is** read
   for rendering, in `web/ui/partials/plugin_buttons.html`
   (`<img src="/plugin-icons/{{.PluginID}}/{{.PluginVersion}}/{{.IconPath}}">`),
   served through `internal/pages/plugin_icons.go`'s `registerPluginIcons` —
   a route with a tested path-traversal guard
   (`TestPluginIcons_BlocksPathTraversal`). So a safe, working, tested pattern
   for serving a plugin's own icon file already exists in this codebase.
   The scoping decision itself (don't wire this into #1722) is still correct,
   but for a different reason than stated: `data.MenuEntryRow` (what
   `d.MenuSnapshot()` actually returns, what this closure sees) has no
   `IconPath` field at all — `ListMenuEntries`'s SQL never selects
   `icon_path` — so there is nothing to read even if it were safe to. **Fixed**
   the doc comment in `menu_page.go` to state the accurate reason (schema/query
   gap, not "no safe mechanism exists") and left a pointer for #1734 to reuse
   the button-icon serving path as prior art rather than re-deriving it. Also
   left a clarifying comment on ut-docs#1734 itself, which repeated the same
   "never read for rendering anywhere ... dead data today" claim, so whoever
   picks that card up doesn't re-derive a safe-serving mechanism from scratch
   that already exists for buttons.
4. **Regression coverage.** `TestMenuPage_OrdersTileHasAMappedIcon` (ut-docs#1371)
   still passes unmodified. `TestMenuPage_UnmappedRouteGetsFallbackIcon` was
   rewritten (confirmed via `git diff`) from asserting `▪️` present to asserting
   it is absent and `data-icon="puzzle"` is present — a real behavioural
   flip, not a cosmetic edit.
5. **Manual screenshots regenerated.** `make docs-shots` was already run for the
   original diff; `bash scripts/ci/guard-docs-shots.sh` passed. Re-ran it again
   after my own comment fix (below) since it touches a hashed Go file.

## TDD claim — independently re-verified

Did **not** revert directly in the shared checkout (a `git stash` for an
unrelated card, #1587, sits in this same working tree). Instead:

1. Staged and committed the five diff files as `WIP: pre-review snapshot` on
   `fix/1722-plugin-menu-tile-icon-fallback` (git identity confirmed correct
   first: `4035824+farshidmirza@users.noreply.github.com`, no local override).
2. `git worktree add --detach <tmp-path> <that-commit-sha>` — fully isolated
   from the main checkout and the stash.
3. In the worktree: reverted **only** the `add` closure's fallback line
   (`svg = httpx.Icon(genericFallbackIcon)` → `icon = "▪️"`), ran
   `go test ./internal/pages/... -run TestMenuPage_UnmappedRouteGetsFallbackIcon`
   → **FAIL**, exactly on the `▪️`-must-be-absent assertion.
4. Restored the fix (`git checkout --`), re-ran the same test → **PASS**.
5. Removed the worktree. Confirmed the main checkout was never touched
   (`git status` clean at the WIP commit throughout) and the unrelated stash
   entry was never listed, popped, or touched.

Claim holds: the test is load-bearing and genuinely fails without the fix.

## Other checks

- **`TestMenuPage_BluetoothTileUsesTheBluetoothSymbolNotSignalBars`
  (ut-docs#1720) not regressed.** Its tile-scoped assertions (`data-icon=
  "bluetooth"` present, `data-icon="puzzle"` absent on *that tile*) and its
  page-wide `📶`-must-be-gone assertion all still pass. Its comments were
  correctly updated to stop claiming `▪️` appears elsewhere on the page (it no
  longer does anywhere) and now correctly describe the puzzle fallback as the
  *other* tile's legitimate behaviour, not a regression risk for this one.
- **Tests are not tautological.** Both new/changed assertions check real
  rendered markup (`data-icon="puzzle"`, absence of the literal `▪️` glyph)
  against a live `httptest` response body, not internal state.
- **UX guidelines.** Same drawn-icon rendering path and CSS as every other
  entry (`menu.html`'s existing `{{ if .IconSVG }}...{{ end }}` branch,
  `internal/httpx/icons.go`'s shared `iconSVGOpen` wrapper) — no new
  hardcoded styling. `web/help/en/menu.md` (the manual's Menu topic prose)
  doesn't describe icons in any way this change makes stale — no finding.
- **No real client/shop name, no secret-shaped literal** anywhere in the diff.
- **Full gate, all green:** `go build ./...`; `go vet ./internal/httpx/...
  ./internal/pages/...`; `go test` for both packages (one transient failure
  on an initial concurrent double-run under contention from two simultaneous
  gate invocations — reran clean, confirmed not a real regression);
  `gofmt -l` on the three changed Go files (no output); `bash
  scripts/ci/guard-docs-shots.sh`; `bash scripts/ci/guard-i18n.sh`; `bash
  scripts/ci/guard-data-access.sh`. Did not run
  `scripts/ci/guard-deadcode-baseline.sh` (known pre-existing sandbox gap,
  missing gtk+-3.0/webkit2gtk-4.1 dev packages, unrelated to this diff).

## What was fixed vs. accepted vs. filed

- **Fixed:** the `menu_page.go` doc comment's inaccurate claim that
  `ManifestEntry.IconPath` is never read for rendering anywhere — corrected to
  name the real reason (`MenuEntryRow` has no `IconPath` field to read, not a
  missing safety story) and point at the existing button-icon mechanism as
  prior art. Required regenerating `web/help/img/manifest.json`'s
  `surface_sha256` (only that one line changed) since the guard hashes
  non-test `internal/pages/**.go` content including comments; two unrelated
  screenshots (`sell.png`, `multitill.png` in ar/en/fa) picked up a few bytes
  of incidental rendering noise from the full `make docs-shots` re-run and
  were reverted rather than committed, since `guard-docs-shots.sh` never
  hashes PNG bytes, only the manifest's recorded hashes and file existence.
- **Accepted, no change needed:** the Architect's underlying scoping decision
  (no plugin-declared menu icon in this change) — sound, just needed its
  stated rationale corrected, not its conclusion.
- **Filed:** no new backlog card — added a clarifying comment on the already-
  filed ut-docs#1734 instead, since the same inaccuracy was present there too.

## Verdict

Safe to merge. All five acceptance criteria met; the one real finding (a
comment's factual claim, not the mechanism it argues for) was fixed in place
and re-verified against the full gate.
