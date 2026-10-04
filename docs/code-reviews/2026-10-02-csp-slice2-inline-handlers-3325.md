# Code review — CSP slice 2: inline handlers moved to a delegated dispatcher (ut-docs#3325)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3325 (slice 2 of #2913's CSP hardening; slice 1
  merged, slices 3/#3326 and 4/#3327 are separate, later cards).
- **Branch:** `feat/3325-csp-slice2-inline-handlers`.
- **Author:** Opus (this cycle's build model, `complexity:hard` — bumped
  from the groomed `medium` by this cycle's Architect once the true scope
  (~35 files, a new repo-wide dispatch convention) was mapped).
- **Reviewer:** independent pass, Fable (a different model from the
  author, per `MODEL-ROUTING.md`), isolated worktree, no visibility into
  the implementation reasoning.
- **Verdict: SAFE TO MERGE**, after the fixes below (two by Tester, four
  by Reviewer; all with regression tests, all re-verified green).

## What shipped

- `web/public/inline-actions.js` (new): one delegated listener per
  mechanism — `click` (bubble-safe via `closest('[data-action]')`),
  `error` (capture phase — `error` doesn't bubble), `htmx:afterRequest`/
  `htmx:beforeRequest`/`htmx:beforeSwap`/`htmx:afterSettle`/
  `htmx:responseError`/`htmx:sendError`, and `input`/`change`/`submit`/
  `close`. A small closed step-vocabulary DSL (`name:arg`, `;`-chained,
  stop-on-`ok`/`fail`/etc.) replaces what used to be arbitrary inline JS.
  Loaded last among `web/ui/layouts/base.html`'s deferred scripts so its
  document-capture listeners still run before `app.js`/`list-reorder.js`'s
  earlier-registered capture listeners (capture phase is root-to-target
  before any bubble listener fires, independent of registration order —
  reviewed and confirmed, see findings).
- 98 `hx-on` + 72 inline `on*=` attributes (170 call sites, ~35
  `web/ui/{pages,partials}` templates, plus 3 hand-built strings in
  `internal/pages/pos_api.go`/`import_page.go`) converted to the matching
  `data-*` attribute. `web/ui/partials/parked_orders.html`'s deliberate
  "no close here" case left untouched, as designed.
- `scripts/ci/guard-no-inline-handlers.sh` + test (new), modelled on
  `guard-autofill-suppression.sh`'s two-invariant shape, now four
  invariants (see findings F2/F3). Wired into `.github/workflows/ci.yml`.
- `scripts/ci/update-docs-shots-surface-hash.sh` run (one-line fingerprint
  refresh in `web/help/img/manifest.json`) — no rendered pixel changed, so
  no help-topic prose needed updating; confirmed, not assumed.

## Review findings

1. **MEDIUM (fixed by Tester, re-verified by Reviewer): a real regression,
   `html/template`'s JS-vs-HTML attribute-escaping sniff.** Go's
   `html/template` strips a leading `data-` before classifying an
   attribute's escape context; the original `data-on-before-request`/
   `data-on-after-request`/etc. family (starting with `on-` after the
   strip) sniffed as a JS-context attribute and got JSON-escaped, silently
   corrupting any interpolated id (e.g. `parked-move-{{ $id }}` rendered
   `parked-move-&#34;h1&#34;`), breaking a downstream `byId()` lookup and
   losing focus-restore after a held-table move. A second, previously
   undetected live instance hit `pending_pairings.html`'s wrong-PIN
   `unhide:pin-error-{{ .ID }}` hook — masked by a test that only did a
   `strings.Contains` prefix check. **Fix:** the whole hook-attribute
   family renamed to drop the `on-` infix (`data-before-request`,
   `data-after-request`, …) across `inline-actions.js` and 23 templates (93
   occurrences); two new regression tests
   (`TestParkedOrders_MoveOptionRememberFocusIDNotJSEscaped`,
   `TestPendingPairingsUI_AfterRequestAttrIDNotJSEscaped`) assert the id
   byte-matches the real element's plain `id=` attribute, catching this bug
   *class*, not just these two instances. Reviewer independently reverted
   just the rename and confirmed both tests fail with the claimed
   corruption, then confirmed green again restored.
2. **LOW (fixed by Tester, test-only): a stale e2e selector**, not an app
   bug — `htmx-senderror-refund-shifts-1707.spec.ts` selected the
   now-gone `button[onclick*="/refund/"]`; the app's real behaviour was
   never broken. Fixed to `button[data-action*="go:/refund/"]`.
3. **MEDIUM (fixed by Reviewer): `data-action` is a URL-context attribute
   to `html/template`**, not plain-attribute like the renamed hooks —
   `attrType` matches `"action"` → `contentTypeURL`
   (`html/template/attr.go`), so a value is percent-normalised and a
   leading `{{ }}` is scheme-filtered to `#ZgotmplZ`. A Go repro confirmed:
   today's four templated `data-action` values are all pure-ASCII receipt
   numbers (`T<n>-NNNNNNNNN` from `NextReceiptNo`), so **no live bug** —
   but the shape is a trap for the next person who templates a `data-action`
   value starting with `{{ }}` or containing `#`/`?`. Fixed: corrected
   `inline-actions.js`'s header (it had wrongly claimed `data-action` was
   plain-attribute-safe), documented the rule, and added
   `internal/pages/inline_actions_data_action_url_context_test.go`
   (walks `web.FS` for the breaking shapes; a second test executes real
   `html/template` to prove the `#ZgotmplZ`/percent-encoding behaviour
   against 3 bad + 0 good fixtures).
4. **LOW (fixed by Reviewer): guard regex bypass** — the original pattern
   missed `onclick ="x()"` (whitespace before `=`), which browsers run
   identically. Fixed with `on[a-z]+(=|[[:space:]]+=[[:space:]]*["'])`
   (glued `=` catches any value; whitespace-before-`=` only with a quoted
   value, which no JS assignment in `base.html`'s own `<script>` blocks
   matches — verified against those real false-positive candidates before
   picking this pattern). Two new `expect_fail` fixtures.
5. **LOW (fixed by Reviewer): nothing pinned the bug class in finding 1
   going forward** — after Tester's rename, no guard stopped the *next*
   `data-on-foo` from reintroducing the same corruption. Added a fourth
   guard invariant: `data-on[a-z0-9-]*=` must not appear anywhere in
   `web/ui`, pointing at the header comment. Confirmed empty on the real
   tree.
6. **Trivial (fixed by Reviewer): two stale comments** (`ci.yml`,
   `pending_pairings_test.go`) still named the pre-rename `data-on-*`
   attributes.

### Verified, no finding

- **Listener ordering** (base.html script order; capture-vs-bubble
  reasoning) traced end to end — holds, no counter-example found.
- **DSL injection risk**: every `{{ }}` inside a DSL attribute value is
  server-generated (`uuid.NewString()`, `fmt.Sprintf("hold-%d", …)`, a
  receipt number) — no user-controlled text reaches a DSL attribute's
  parsed position; `data-confirm-text` and similar carrier attributes are
  read as opaque text, never parsed.
- **Capture-phase `error`/`close` walk**: matches the old per-element
  attribute's target exactly (the non-bubbling target itself, not an
  ancestor); no nested-dialog/nested-image case breaks it.
- **3 Go-rendered conversions** (`pos_api.go`, `import_page.go`): static
  strings, no user data, valid attribute syntax.
- **File writes / paths**: grep for `os.WriteFile`/`Create`/`OpenFile`/
  `MkdirAll`/`filepath.Join` across the diff — none; the two recurring bug
  patterns this pipeline watches for don't apply to this change.
- **Other `on`-prefixed names**: repo-wide grep after the fix — only
  `data-action` (URL-context, finding 3) and the intentionally-URL
  `data-fallback-src` strip to a sniffed name; Tester's rename was
  otherwise exhaustive.
- **Help/docs**: `web/help/img/manifest.json`'s one-line surface-hash
  change is the only diff there; no user-visible behaviour changed, so no
  topic needed updating — confirmed, not assumed.
- Semantic spot-check of ~10 representative converted call sites (hold
  modal chain, staff-languages triple chain, pfand result swap, tab
  activation, journal row navigation) against their pre-conversion
  behaviour — all equivalent.

## Verified beyond the automated tests

- **TDD claims re-verified independently by the Reviewer**: reverted the
  finding-1 rename on a throwaway basis, ran the two new regression tests,
  confirmed they fail with the exact claimed corruption
  (`parked-move-&#34;h1&#34;`, `pin-error-&#34;<uuid>&#34;`), restored,
  confirmed green. Worktree never left mid-revert.
- **Go gate**: `go build ./...`, `go vet ./...`, `gofmt -l .` clean;
  `go test ./...` full suite green (78 packages); `golangci-lint run
  ./...` — 0 issues, run before and after the review's own changes.
- **New guard + its test**: both pass; an independently reintroduced real
  `onclick=` was caught at the exact file:line, then reverted.
- **e2e**: full suite run to completion by Tester — 1083/1084 passed; the
  one remaining failure (`phone-layout-sweep-3297.spec.ts`) triaged as a
  pre-existing cross-spec-pollution flake (file untouched by this diff,
  100% pass in isolation — 86/86 for the whole spec file, standalone
  retest green). Reviewer independently re-ran `held-table-move-2702`,
  `htmx-senderror-refund-shifts-1707` and the only pending-pairings-layout
  spec 3× each in a fresh worktree (9/9 passed) and the CSP inventory spec
  3× (`script-src-attr` absent from `by_directive` every time — the card's
  observable goal, confirmed in a real browser, not just by the Go-side
  fix).
- **Visual/driven run** (Tester, real throwaway till via `run-till.sh`):
  sale screen at 1024×600 (kiosk) and 360px (phone) clean; a converted
  modal-close interaction closed cleanly with zero console errors; a
  forced broken-image URL confirmed the capture-phase `error` fallback
  still hides it, no layout shift; a converted admin page (Catalog) clean;
  RTL (`?lang=fa`) on the sale screen mirrors correctly, zero console
  errors. **Not checked** (said explicitly, not silently): dark theme (the
  settings theme control wasn't reliably drivable headlessly in the time
  available), the longest shipped locale (German/Persian) for layout
  overflow, a 10-inch-tablet-distinct viewport, and no real touch hardware
  was available in this environment — all genuine gaps, not claimed as
  covered.
- `guard-data-access.sh`, `guard-i18n.sh`, `shellcheck` on both new
  scripts, every other `scripts/ci/guard-*.sh` in the CI `build` job — all
  green. `guard-deadcode-baseline.sh` fails identically on an untouched
  `main` baseline in this sandbox (no GTK/WebKit headers here, so
  `cmd/unitill-desktop` callers look unreachable) — pre-existing and
  environmental, not this diff; real CI is the authority, checked at
  DevOps.

## Deferred — filed as Backlog cards, not silently dropped

- `csp-report-only-2913.spec.ts` only *inventories* CSP violations; it
  never asserts `script-src-attr` is absent, so this card's own goal has
  no durable automated pin beyond the new guard (which only checks the
  markup, not a live browser's report). Candidate: strengthen that spec.
- `guard-no-inline-handlers.sh` scans only `web/ui/**/*.html`; hand-built
  markup in `internal/pages/*.go` (e.g. `setup_update_check.go`'s
  `el.onclick=` inside an inline `<script>`, which is a `script-src`
  concern for slice 3/#3327, not `script-src-attr`) and Alpine's
  `x-on:`/`@click` attributes are outside this guard's scope. Candidate:
  decide their home when #3327 is picked up.

No real client/shop name in any test data or fixture. No money, SQL, or
plugin-signing surface touched.

## Supersession note — a concurrent run of the same lane

While this branch was in Tester/Reviewer, a **separate, later firing of
this session's own routine** (`lane:cloud-41b` fires hourly; this long
session's BA/Architect/UX notes at 17:49–17:53 predate it by ~3h of real
wall-clock time, during which this session was still actively running
Dev/Tester/Reviewer subagents) independently picked up ut-docs#3325,
found its own Dev work harder-shaped than designed, re-scoped the card
down to just the CI guard (merged as universal-till#1643, `00849b8`), and
split the real conversion work into three new cards — ut-docs#3508
(sale-screen), #3509 (settings/users/shifts), #3510 (remaining surfaces +
Go-rendered handlers + CI wiring) — closing #3325 itself as Done at the
reduced scope.

This branch already contains the **complete** conversion (all three
slices' scope, plus the full CI wiring) at the point that happened,
independently reviewed and tested. Per the lane-ownership supersession
check (`scrum-master/SKILL.md` rule 7a): rather than discard validated
work or fight the other run's decision, this PR is retargeted to close
**#3508, #3509 and #3510** instead of the already-Done #3325, after
reconciling the one real conflict — the other run's `main`-merged
`guard-no-inline-handlers.sh`/`_test.sh` (a narrower 2-invariant,
comment-stripping foundation) against this branch's 4-invariant,
deliberately-raw-grep version. This branch's version is kept: it already
independently covers the other's review fixes (the pipefail-`grep -q`
trap is avoided by construction — a here-string, never a pipe; the
whitespace-before-`=` and upper-case-attribute cases are both in this
branch's pattern and its own test fixtures) and additionally catches the
`data-on-*` escaping-context class (finding 1) that the foundation
version predates.

Specifically verified before retargeting, not assumed: `reports.html`'s 9
templated per-tab `hx-on:click` handlers (the other run's Dev flagged
these as needing special handling) are `data-action="activate-tab:..."`
in this branch, with the translated tab label left as ordinary `{{ T }}`
button text, never baked into the action string; `receipt.html`'s
print/refund logic (translated text + `{{ .ReceiptNo }}`) is
`data-action="receipt-print:{{ .ReceiptNo }}"` with the translated label
in its own `data-done-text` carrier (exactly finding 3's URL-context
case, already confirmed safe); `setup.html`/`tills.html`'s
`htmx.process()`-based dynamic inserts still call `htmx.process()` (that
part is htmx's own dynamic-content mechanism, unrelated to inline
handlers) with no remaining `onclick=`/`data-on-*` in either file. The 3
Go-rendered handlers (`pos_api.go:2395`, `import_page.go:1534`,`:1710`)
are converted. `#3510`'s remaining acceptance item — extending
`guard-no-inline-handlers.sh`'s *scope* to statically catch a *future*
Go-rendered violation (not just converting today's 3 known instances) —
is explicitly **not** done here; ut-docs#3506 (filed by this review,
independently of the other run's identical finding) already tracks
exactly that gap and stays open rather than being duplicated.
