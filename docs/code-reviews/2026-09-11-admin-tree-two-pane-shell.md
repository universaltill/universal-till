# Code review: `/admin` tree converted to the two-pane htmx shell (ut-docs#2116)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#2116 — "Admin tree navigation escapes the two-pane shell"
**Branch:** `fix/2116-admin-tree-two-pane-shell` (reviewed at WIP snapshot `da03eb0d`)
**Complexity:** medium (Dev: Sonnet, Review: Opus, fresh-context isolated worktree)

## What shipped

`/admin`'s six destinations — `/fiscal-register`, `/fiscal-device`,
`/locations`, `/registers`, `/translations`, `/country-settings` — were
converted from plain `<a href>` full-page navigation to the same two-pane
master-detail shell `/items` has used since ut-docs#1950:

- `web/ui/pages/admin.html` now wraps the tree and a new `#admin-panel` in
  the pre-existing `.items-layout` / `.items-rail-wrap` / `.items-panel`
  structure, and carries the same narrow-width (`<= 52rem`) capture-phase
  click interceptor `items.html` already has, so below the breakpoint a tree
  tap falls back to a real navigation instead of an in-panel swap.
- `web/ui/partials/admin_tree.html` rows gained
  `hx-get`/`hx-target="#admin-panel"`/`hx-push-url="true"` alongside their
  existing real `href`, plus `is-current`/`aria-current="page"` driven by a
  new `$.CurrentHref`, and the container gained `id="admin-tree"` +
  a conditional `hx-swap-oob="true"`.
- `internal/ui/admin_tree.go` (new) — `AdminTreeView`, a line-for-line
  mirror of `internal/ui/items_rail.go`'s `ItemsRailView`, so the tree can
  be rendered standalone for the out-of-band swap.
- `internal/pages/admin_page.go` gained `adminEmbedHeader`/`isAdminEmbed`,
  `writeAdminTreeOOB` and `embedAdminSection` — mirrors of
  `internal/pages/itemsnav.EmbedHeader`/`IsEmbed`/`WriteRailOOB` and
  `items_page.go`'s `embedItemsSection`. `registerAdmin` now auto-selects
  the first visible group's first entry and inlines its content into the
  panel, so the right pane is never empty on arrival.
- Each of the six destination handlers gained the identical dual-mode
  branch already used by `categories_page.go`'s `renderCategories`:
  `httpx.IsFragmentSwap(w, r)` → `httpx.RenderContentFragment(...)` +
  `writeAdminTreeOOB(...)`; otherwise the unchanged `httpx.Render(...)`
  full standalone page.
- `web/help/en/menu.md`'s Administration section describes the new
  behaviour.

New/Edit-as-overlay is deliberately out of scope (split to ut-docs#2124).

## Independent review — verdict

**Safe to merge.** One blocking CI failure found and fixed in-branch; two
non-blocking test-coverage gaps found and closed; no correctness or
security findings in the shipped production code.

## What I ran myself (not taken from the implementer's report)

All from the isolated review worktree at `da03eb0d`:

| Gate | Result |
| --- | --- |
| `gofmt -l .` | clean |
| `go build ./...` | OK |
| `go vet ./...` | OK |
| `go test ./...` (full suite) | all packages pass |
| `golangci-lint run ./...` | `0 issues.` |
| `guard-data-access.sh` | pass — no inline SQL outside `internal/data`/`internal/db` |
| `guard-i18n.sh` | pass — 1649 template keys resolve, all locales match `en.json` |
| `guard-help-topics.sh` | pass |
| `guard-help-drift.sh` | pass (only pre-existing baselined entries) |
| `guard-htmx-loaded.sh` | pass |
| `guard-page-http-error.sh` | pass |
| `guard-compliance-claims.sh` | pass |
| `guard-kiosk-engine.sh` | pass |
| `guard-plugin-menu-read.sh` | pass |
| `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`, `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`, `check-brand-assets.sh`, `guard-makefile-version.sh` | pass |
| `guard-docs-shots.sh` | **FAILED on the snapshot** — see Finding 1 |
| `guard-shellcheck-version.sh` | cannot run — no `shellcheck` binary in this container (environmental; the diff touches no shell script) |

## Findings

### Finding 1 — BLOCKING (fixed in-branch): `guard-docs-shots.sh` red

`scripts/ci/guard-docs-shots.sh` is a CI-blocking guard in `ci.yml`'s
`build` job. On the snapshot it failed with:

```
guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go)
  changed since the manual's screenshots were last taken
guard-docs-shots: topic markdown changed since its screenshot was taken (locale/topic):
  - en/menu
guard-docs-shots: run `make docs-shots` and commit the result
```

I verified this is a regression introduced by *this* diff, not a
pre-existing red: extracting the base commit `333f7157` into a scratch
tree and running the same guard there gives
`✓ docs-shots guard: 31 routed topics × 4 locales screenshotted and fresh
(surface 658e03b9d4cb…)`. Both halves of the failure are genuinely this
change's doing — the surface hash covers `web/ui/**` and non-test
`internal/pages/**.go` (both touched), and `en/menu`'s topic hash changed
because `web/help/en/menu.md` was edited. This is exactly the
"a regenerated screenshot where the screen itself changed" rule in
`universal-till/CLAUDE.md`.

**Fixed:** ran `bash e2e/scripts/docs-shots.sh` (`make docs-shots`) in the
worktree — 124 Playwright captures passed, `web/help/img/manifest.json`
rewritten (`surface acfa044ac6bf…`), and all 121 PNGs under
`web/help/img/{en,fa,ar,tr}/` regenerated. The guard is now green. I spot-
checked `web/help/img/en/menu.png` visually: it renders the `/menu` tile
grid correctly, unchanged in substance. Note `menu.md`'s `routes[0]` is
`/menu`, so the manual screenshots `/menu`, not `/admin` — the new
two-pane `/admin` screen itself has no screenshot in the manual (that is
pre-existing and outside this card; the prose describes it).

### Finding 2 — MINOR (fixed in-branch): no test pinned the "doubled tree" regression

The `adminEmbedHeader` / `isAdminEmbed` early-return in
`writeAdminTreeOOB` is the only thing stopping a bare `GET /admin` from
emitting the tree twice under a duplicated `id="admin-tree"` — the exact
bug the ut-docs#1950 review caught on `/items` ("a bare `GET /items`
rendered 10 rows and two rails instead of 5 and one"). Nothing in the new
test set pinned it; `TestAdminPage_BareGetEmbedsFirstVisibleEntryAndMarks-
ItCurrent` only checks document order, which still holds with a doubled
tree. This is precisely the kind of plumbing a later refactor drops
silently.

**Fixed:** added `TestAdminPage_BareGetRendersTheTreeExactlyOnce`
(`internal/pages/admin_page_test.go`) — asserts exactly one
`id="admin-tree"` and no `hx-swap-oob=` marker at all on a full page load.
Verified it is not a false pass: neutering the guard
(`if false && isAdminEmbed(r)`) makes it fail with
`bare GET /admin rendered id="admin-tree" 2 time(s), want exactly 1 — the
embedded destination must not append its own OOB tree copy`; restored and
it passes.

### Finding 3 — MINOR (fixed in-branch): no history-restore test on any of the six

The six new dual-mode handlers inherit `httpx.IsFragmentSwap`'s
`HX-History-Restore-Request` exclusion (ut-docs#433 / ut-docs#2091) — htmx
re-requests a restored history entry with *both* headers set and expects
the full page back; answering with a bare fragment leaves the restored
screen chrome-less. That rule is pinned on `/help` and `/inventory` only.
Since these six now push URLs into htmx history for the first time, they
are newly exposed to it and nothing covered them.

**Fixed:** added `TestRegistersPage_HXHistoryRestoreReturnsFullPage`
(`internal/pages/registers_page_test.go`) — one destination, exactly as the
`/items` rail covers this once on `/inventory`. Verified not a false pass:
replacing `httpx.IsFragmentSwap(w, r)` with the naive
`r.Header.Get("HX-Request") != ""` in `registers_page.go` makes it fail
(`a history-restore request must get the full standalone page, got: <bare
fragment>`); restored and it passes.

### Finding 4 — ACCEPTED (no change): a mutation still leaves the shell

The six destinations' forms are plain `method="post"` forms that 303-redirect
back to the destination's own URL, so submitting one from inside the panel
(rename a register, add a location) performs a real browser navigation and
lands on the destination's *standalone* page — the tree is gone until the
operator goes back to `/admin`. Not introduced by this card and not a
deviation: `/items`' own five destinations behave identically today
(`categories_page.go` + `categories.html`), and the handler comments say so
explicitly ("a plain browser GET — deep link, or the redirect a mutation
falls back to — still gets the exact same full standalone page"). The
in-panel mutation story is what ut-docs#2124 (New/Edit as overlay) exists
for. Accepted as-is rather than widened here.

### Finding 5 — ACCEPTED (no change): `adminGroupsFor(visibleAdminEntries(d, r))` is evaluated before the embed short-circuit

`writeAdminTreeOOB`'s first statement is `if isAdminEmbed(r) { return }`,
but its arguments — including the tree rebuild — are evaluated at the call
site regardless. Wasted work on exactly one request per `/admin` load, on
an in-memory slot snapshot with no I/O. Identical to
`itemsnav.WriteRailOOB`'s call sites. Not worth diverging from the
precedent for.

## Correctness checks against `universal-till/CLAUDE.md`

- **Repository pattern** — the diff adds no SQL anywhere;
  `guard-data-access.sh` green. The only data access added is
  `embedAdminSection` replaying a request through the same mux, which
  reaches each destination's existing repository calls unchanged.
- **i18n** — no new locale keys, no new user-facing literals. Every string
  in the new markup goes through `{{ T }}` (`menu.group.administration` as
  the `<aside>`'s `aria-label`; `admin.group.*` headings unchanged). The
  new inline `<script>` in `admin.html` contains no prose at all — it only
  calls `matchMedia`/`closest`/`stopPropagation` — so the ut-docs#205
  inline-JS rule has nothing to catch; `guard-i18n.sh` green.
- **RTL** — no new CSS at all. Confirmed `web/public/app.css` is *not* in
  the diff (18 files, none CSS) and that `.items-layout`,
  `.items-rail-wrap`, `.items-panel`, `.items-row`, `.items-row.is-current`
  and `.admin-tree .items-row` all already exist there. Grepped every added
  line in `internal/` and `web/` for physical `left`/`right` properties:
  none.
- **Offline-first** — nothing here touches checkout, sync, or the network;
  no modal blocker added on any path. `/admin` is back-office, and the nav
  rail (status/lock/exit) is untouched on every surface — the two-pane
  wrapper sits inside `content`, not over the rail.
- **Money** — not applicable; no monetary value is added, read or rendered
  by this diff. Checked rather than assumed.
- **Recurring bug class: file-write handler missing `os.MkdirAll`** —
  checked and not applicable: the diff performs no file writes. The only
  new I/O-shaped code is `bytes.Buffer` → `io.Writer` in
  `writeAdminTreeOOB` and an in-memory `httptest.ResponseRecorder`.
- **Recurring bug class: cwd-relative path where `paths.Data(...)` belongs**
  — checked and not applicable: the diff contains no filesystem paths at
  all. Template paths (`"ui/pages/admin.html"`,
  `"ui/partials/admin_tree.html"`) are `//go:embed` FS keys resolved by
  `httpx`, not disk paths.
- **Secrets / real shop names** — grepped the whole diff for
  password/secret/token/api-key/private-key/`sk-`/`AKIA` patterns: nothing.
  The only identifiers added are test fixtures already in use in this
  package (`"m1"`, `"Manager"`).

## Correctness checks specific to this change

- `registerAdmin`'s new `groups[0].Entries[0].Href` cannot panic: the
  `len(visible) == 0` branch 403s first, and `adminGroupsFor` never appends
  a group with zero entries, while any entry no named cluster claims lands
  in the `admin.group.other` catch-all — so a non-empty `visible` always
  yields a non-empty `groups` whose first group has a non-empty `Entries`.
  The inline comment says exactly this and it holds.
- The hardcoded `currentHref` literals passed to `writeAdminTreeOOB`
  (`"/locations"`, `"/registers"`, …) match the rows' `.Href` values:
  `uislot.Entry.Key` equals `Href` for core entries (`slot.go:70-74`), and
  `adminGroupOrder` buckets on exactly those path strings.
- The OOB tree cannot leak into an unrelated page: grepped all of `web/`
  and found no other `hx-get` pointing at any of the six routes, and no
  page-level `hx-boost` that would turn ordinary links into htmx requests.
- All six destination templates define *only* `{{ define "content" }}` and
  carry no `<script src>` outside it, so `RenderContentFragment` captures
  everything each page needs — no page-specific JS is stranded in
  `base.html` when the page is swapped into the panel.
- `IsFragmentSwap` sets `Vary: HX-Request` on both branches, so the
  ut-docs#2091 cache-poisoning class does not reappear on six newly
  dual-mode URLs. Each destination has its own
  `TestXxxPage_VaryHXRequestOnBothBranches`; I ran them.
- No double back-button in the panel: `admin.html`'s `page-head` carries
  the "Menu" affordance, and each destination's `content` begins with a
  bare `<div class="page-head"><h1>…</h1></div>` — the same shape `/items`
  already produces.

## Independent TDD re-verification

The Tester pass re-verified `admin_page_test.go` and
`locations_page_test.go`. I picked a different one —
`internal/pages/registers_page_test.go` — and did the revert/run/restore
myself, atomically within one turn, in this isolated worktree.

Reverted `internal/pages/registers_page.go` to its pre-change version
(`git checkout da03eb0d^ -- internal/pages/registers_page.go`), leaving the
new tests in place:

```
--- FAIL: TestRegistersPage_HXRequestReturnsContentFragmentWithOOBAdminTree (0.18s)
    registers_page_test.go:458: htmx request re-rendered the whole page shell: <!DOCTYPE html>
    registers_page_test.go:462: fragment missing the OOB admin-tree swap: <!DOCTYPE html>
FAIL
FAIL	github.com/universaltill/universal-till/internal/pages	0.209s
```

and, in the same reverted state:

```
--- FAIL: TestRegistersPage_VaryHXRequestOnBothBranches (0.09s)
    registers_page_test.go:503: fragment branch: Vary header = "", want "HX-Request"
    registers_page_test.go:510: full-page branch: Vary header = "", want "HX-Request"
```

Both are real, on-topic failures — the handler genuinely returned the full
page shell with no OOB tree and no `Vary` header — not a compile error or
a fixture mismatch. Restored the file
(`git checkout da03eb0d -- internal/pages/registers_page.go`), confirmed
the working tree clean for that path, and re-ran the three `/registers`
tests: `ok github.com/universaltill/universal-till/internal/pages 0.455s`.

The two tests I added (Findings 2 and 3) were each put through the same
revert-then-restore treatment; their failing output is quoted in those
findings.

## What the earlier Tester + UX pass covered (summary, per their attestation)

A driven run of the real app at 1280x800, 1024x600 and 360px width: the
tree swaps `#admin-panel` without a full page load at tablet width and
wider, `is-current` follows the click, the URL updates and each
destination remains directly linkable; at 360px the panes stack and a tap
navigates to the destination's own full page. Also checked under `fa`
(RTL) and across a theme switch. All acceptance criteria confirmed. One
non-blocking UX observation was raised — a table inside the narrower panel
now needs horizontal scrolling — and deferred as a follow-up rather than
fixed here. I did not re-drive the app; the visual behaviour is theirs,
this record is the code-quality/correctness half.

## UX-guidelines checklist (diff touches `internal/pages` and `web/`)

- Existing design tokens/classes reused, no new CSS — **verified
  independently**, not just claimed: `web/public/app.css` has no diff, and
  every class the new markup uses already exists in it.
- RTL-safe — no new physical `left`/`right` anywhere; the reused classes
  are already logical-property based.
- No new modal blocker on a checkout-critical path — not applicable, and
  confirmed: `/admin` and its six destinations are back-office, the kiosk
  and sale flows are untouched, and nothing here covers the nav rail.
- Error/empty states — `embedAdminSection` renders a translated,
  `common.error.server` fallback card (and logs the real status/body) on
  any non-200 embed rather than a silently blank panel; the
  zero-visible-entries case still 403s through `httpx.RenderError`.
- Long-string locale — the tree reuses `.admin-tree .items-row`'s existing
  truncation rules; no new fixed-width text container was introduced.

## Manual

`web/help/en/menu.md` already claimed `/admin` in its `routes:`, so no new
topic or `?` link is needed. I read the new Administration paragraph
against `web/help/en/catalog.md`'s established two-pane wording and it is
accurate, not merely present: same "on a tablet-width screen or wider …
two-pane screen", same "on a phone-width screen the two panes stack, and
tapping … opens its own full screen instead", same "each … is still its
own page with its own address" reassurance. Every claim matches what the
code does. The one thing `catalog.md` says that `menu.md` does not is
which entry is preselected ("Catalog is selected by default"); left as-is
deliberately, because unlike `/items` the `/admin` default depends on which
clusters are non-empty for that viewer, so naming one destination would be
wrong for some shops. No screenshot regeneration was needed for the prose
itself, but see Finding 1 — the manual's whole screenshot set was
regenerated regardless, as the guard requires.

## Explicitly deferred

- **ut-docs#2124** — New/Edit as an overlay inside the panel. Out of scope
  by design; Finding 4 above is the same territory.
- **Horizontal-scroll table in the narrower panel** — the UX pass's
  non-blocking observation, deferred to a follow-up.
- **`/admin` itself is not screenshotted in the manual** — `menu.md`'s
  `routes[0]` is `/menu`, so the two-pane screen has no captured image.
  Pre-existing property of the topic's route list, not a regression here.

## Verdict

**Safe to merge**, with the docs-shots regeneration (Finding 1) included —
without it the PR's `build` job would have gone red on a CI-blocking guard.
The production code is a faithful, comment-for-comment mirror of the
`/items` shell it was asked to follow, the six handlers all route through
the shared `httpx.IsFragmentSwap` rather than re-implementing the rule, and
every gate is green.
