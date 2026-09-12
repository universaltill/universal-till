# Review: admin tree two-pane shell (ut-docs#2116)

**Card:** universaltill/ut-docs#2116 — "Admin tree navigation escapes the
two-pane shell — nodes open as full pages with no menu selection, and
New/Edit should be closable overlays"
**Branch:** `fix/2116-admin-tree-shell`
**Complexity:** medium — build: Sonnet (subagent), review: Opus (fresh,
different-model, isolated worktree, per this pipeline's model routing)

## Scope

Converts `/admin`'s six destinations (`/fiscal-register`, `/fiscal-device`,
`/locations`, `/registers`, `/translations`, `/country-settings`) from
plain full-page `<a href>` navigation (a deliberate ut-docs#2008 decision,
now reversed) to a two-pane master-detail shell mirroring the already
shipped `/items` rail pattern (ut-docs#1950): the tree stays in a left
pane, a click swaps content into a right pane (`#admin-panel`) via
`hx-get`/`hx-target`/`hx-push-url`, the active node is marked selected via
an out-of-band tree refresh, and a real `href` remains as the no-JS/
narrow-width (52rem) fallback. Stronger than `/items`' own precedent: a
bare direct GET to any of the six destination URLs also renders inside the
shell with the correct node pre-selected (a new `internal/httpx.
RenderContentFragmentToString` helper plus a shared `renderAdminDestination`
in `internal/pages/admin_page.go`).

**Explicitly out of scope** (split out during Architect scoping, filed as
universaltill/ut-docs#2124): converting each destination's own inline
New/Edit forms into closable overlay dialogs — none of the six currently
navigate to a separate page for this, so it's a new UI pattern to
introduce, not a regression this card's report describes. `Inventory`
(part of the *Items* shell) was investigated and already satisfies both
halves of the original report (rail-based two-pane nav + closable
`#stock-dialog` overlay, ut-docs#2011) — confirmed by inspection, no code
change.

## Independent review

Opus, fresh context, isolated worktree, given the diff plus full repo
access to verify claims rather than trust them. Findings and disposition:

1. **BLOCKER (on test evidence, not the implementation) — fixed.** The
   e2e spec's assertions (URL, `is-current`, bounding boxes) could not
   distinguish a real htmx in-panel swap from a plain full-page navigation
   to the same destination, because this card's own "bare GET also renders
   the shell" property makes both paths converge on an identical visible
   DOM/URL state. Proven with a control mutation: deleting `admin_tree.
   html`'s `hx-get`/`hx-target`/`hx-push-url` attributes (i.e. reverting to
   the exact pre-fix behaviour this card exists to remove) left the full Go
   suite AND all 3 e2e tests green. **Fix applied and independently
   re-verified by me**: the e2e spec now sets a `window` sentinel before
   each click and asserts it survives (wide viewport — proves an in-place
   swap, no document teardown) or is lost (narrow viewport — proves a real
   navigation); a new Go test, `TestAdminPage_TreeRowsCarryHtmxSwapAttributes`,
   pins the actual `hx-get`/`hx-target="#admin-panel"`/`hx-push-url="true"`
   attributes on each tree row directly. Re-ran the reviewer's exact
   mutation myself after the fix: the e2e test and the new Go test both now
   fail red with the real, specific error; restored, both green again.
2. **should-fix, deferred as a follow-up — not fixed here.** A 403/500
   during a panel swap is a silent no-op: `httpx.RenderError` has no htmx
   awareness and neither `admin_shell.html` nor `items.html` installs an
   `htmx:responseError` handler, so an error mid-swap (e.g. SQLite busy
   during backup) leaves the tap looking like it did nothing. Inherited
   from the `/items` precedent, not introduced by this PR — affects both
   surfaces equally, so fixing it here would silently widen this PR's
   scope into `/items`' own code. Filed as universaltill/ut-docs#2162
   (also covers finding 5 below, the same shape).
3. **nit — fixed.** An empty `groups` (e.g. every administration entry
   regrouped out via a `layout` plugin amendment, ADR-0088 Decision F) still
   rendered the two-pane shell, reserving a blank tree-column gutter next to
   nothing. `renderAdminDestination` now falls back to the plain standalone
   render when `len(groups) == 0`.
4. **nit — fixed.** `renderAdminDestination` didn't set `.BackHref`, read
   only by `admin_shell.html`'s empty-state branch; unreachable on any
   current path (`PanelHTML` is always non-empty on success) but a
   defensive one-liner (`"BackHref": "/menu"`) removes even the theoretical
   broken-href risk if that ever changes.
5. **nit — deferred with #2 (ut-docs#2125).** `document.title` goes stale
   after an in-panel swap (confirmed identical on the shipped `/items`
   precedent — not introduced here, now present on six more surfaces).
6. **nit — process note, not a defect.** `admin.select_section` is a new
   `en.json` key; `lang-pack-drift` is advisory on this PR and blocking on
   `main` — DevOps step to watch for the advisory annotation naming the
   `ut-plugin-language-{de,es}` follow-up.

Everything else the reviewer checked came back clean, independently
re-verified rather than taken on trust: gating/authorization on all six
handlers (gate runs and `return`s before `renderAdminDestination` is ever
called; `requirePrimary` untouched, still wired only to POST mutations);
market conditionality (dumped the rendered tree directly — `/fiscal-device`
correctly absent with no TR+plugin, present with `is-current` once both are
true); `fiscal_device.html`'s `/plugins/*` links correctly left as plain
out-of-shell navigation; `translations.html`'s pre-existing `hx-get="/ui/
translations-table"` fragment mechanism verified live, no collision (102
rows load inside the swapped panel, one `#admin-tree`, zero console
errors); `IsFragmentSwap`/`Vary`/history-restore reused correctly, not
reinvented (Back/Forward verified live — full chrome, correct selection,
ut-docs#2091's bug class not reintroduced); i18n present and grammatical in
all four locales; the `admin.html` deletion has no surviving reference
anywhere in the repo; help-topic route coverage confirmed by reading front
matter directly, not by trusting the guard's green alone;
`RenderContentFragmentToString` correctly uses `ClonedTemplate` (no
template-reuse data race) and `RequestLocale` (not `ResolveLocale` — avoids
the double-`Set-Cookie` class of bug `items_page.go`'s own doc comment
warns about); new CSS is logical-properties-only (no `left`/`right`) and
doesn't collide with `.items-*`.

## Verified beyond automated tests

- `gofmt -l .` (silent), `go vet ./...`, `go build ./...` — clean.
- `go test ./...` — full suite green, both before and after the review
  fixes.
- `golangci-lint run ./...` — 0 issues.
- Guards: `guard-data-access.sh`, `guard-i18n.sh`, `guard-page-http-error.sh`,
  `guard-htmx-loaded.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-docs-shots.sh`, `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`
  — all pass. `shellcheck scripts/ci/*.sh` — 0 issues.
- `make docs-shots` run for real against the merged tree (124/124 e2e
  screenshot assertions passed); a real screenshot (`fiscal-register`, en)
  was generated and visually inspected — the two-pane shell renders
  correctly at the 1024×600 kiosk viewport, tree grouped by domain cluster
  on the left, destination content on the right.
- **TDD, both layers, proven with a real revert-then-restore cycle, not
  just claimed**: Dev's own Go-level proof (stashed all production
  changes, 5 of 7 new tests failed with real errors, restored, all pass) is
  independently corroborated by my own post-review mutation test above
  (the actual client-visible swap mechanism, at both the e2e and Go-test
  layers).
- e2e (`e2e/tests/admin-tree-shell-2116.spec.ts`) run against a real
  server in a real Chromium browser (not just template-rendered HTML
  assertions): wide-viewport in-place swap with sentinel proof, direct/
  deep-link + reload landing inside the shell with correct selection,
  narrow-viewport (375px) plain-navigation fallback with sentinel proof —
  all 3 pass, zero console errors (`watchConsole`).
- **Not verified**: real device/touch hardware (text-based session; no
  physical tablet available) — deferred, consistent with this card's own
  AC anticipating a cloud session's limits. Locale translations for
  `admin.select_section` (ar/fa/tr) are this pipeline's own best-effort,
  not run through the self-hosted NAS translation model (unreachable from
  this sandbox) — reviewer judged all three grammatical and accurate on
  inspection; not authoritative.

## Follow-ups filed

- universaltill/ut-docs#2124 — New/Edit-as-overlay conversion for the six
  admin destinations (split out during scoping, this card's original
  second requirement).
- universaltill/ut-docs#2162 — silent no-op on a mid-swap error, and stale
  `document.title` after a swap; affects both `/admin` and the pre-existing
  `/items` shell, filed as a cross-cutting follow-up rather than fixed
  inside this PR's scope.
