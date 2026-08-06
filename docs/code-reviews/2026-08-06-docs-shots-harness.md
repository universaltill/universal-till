# Review — manual screenshot harness (`make docs-shots`, per-locale, staleness guard)

Ticket: universaltill/ut-docs#327 (epic ut-docs#324)
Date: 2026-08-06

## What shipped

The manual (ut-docs#325) ships with prose only, no images — 40+ surfaces ×
4 locales is too many to maintain by hand, so the illustrations have to be
regenerable. This adds that harness:

- `e2e/tests-docs/docs-shots.spec.ts` + `e2e/playwright.docs.config.ts` — a
  **separate** Playwright config (not a third project in the existing
  `e2e/playwright.config.ts`), so the normal `npx playwright test` in
  `.github/workflows/e2e.yml` never picks it up. For each of the 10 (of 17)
  manual topics that declare a `routes:` front-matter field, captures the
  first route × 4 locales (en/fa/ar/tr) at the 1024×600 kiosk viewport into
  `web/help/img/<locale>/<id>.png`.
- `internal/manual/manual.go` — at load time, injects
  `<figure class="manual-shot"><img …></figure>` into a topic's rendered
  HTML when a screenshot exists for its actual *serving* locale (post
  English-fallback resolution), right after the topic's own `<h1>`.
- `internal/pages/help_page.go` — new `GET /help/img/{locale}/{file}`,
  serving only from the embedded `web.HelpFS` (no disk access), with
  traversal rejection on both path params.
- `scripts/ci/guard-docs-shots.sh` + `e2e/tests-docs/lib.js` — a freshness
  guard: independently-implemented (bash+python3 / Node) but
  spec-identical content hashing over each routed topic's markdown plus a
  shared "app surface" hash (`web/ui/**`, `web/public/**`,
  non-test `internal/pages/**.go`); CI fails when either drifted since the
  screenshots were last regenerated. Wired into `.github/workflows/ci.yml`
  alongside the existing `guard-i18n.sh`/`guard-data-access.sh` steps.
- `Makefile` — `docs-shots` target regenerates the PNGs and the manifest.
- 40 real screenshots + `web/help/img/manifest.json` committed.

Deliberately **out of scope**, not an oversight: the 7 route-less topics
(alerts, backups, claim, payments, printing, quickstart, updates) get no
screenshot this pass — they don't map to one dedicated route, and inventing
one for each was judged bigger than this card (follow-up backlog card
filed); hand-drawn callout annotations; touching `tests/e2e`/`e2e.yml`.

## What the review found

Independent review by a different model (Opus, reviewing Fable-authored
code) — see review-subagent transcript for the full reasoning; every claim
below was re-verified directly against this branch, not taken on the
implementer's or the reviewer's word.

**Fixed before merge:**

1. **The freshness guard's "app surface" omitted `web/public/**`.** All
   styling (`app.css`, theme CSS, `app.js`) lives there, not under
   `web/ui/`, so a CSS-only change — including the exact statusbar color
   the harness's own mask relies on — passed the guard silently. Live
   before/after: editing `web/public/app.css` used to leave the guard
   green; now it correctly fails. Fixed in both `guard-docs-shots.sh` and
   `lib.js` (must stay in lockstep) and documented why `web/locales/**`
   stays deliberately excluded (every i18n key touches every locale file,
   which would force all 40 screenshots stale on nearly any string edit —
   an accepted gap, now written down instead of implied).
2. **`TestHelpImgTraversalRejected` was a false-pass.** All three original
   cases 404 for reasons unrelated to the traversal guard (they resolve to
   paths that don't exist regardless). Deleting the guard block in
   `help_page.go` left the test green. Added a case that walks back to a
   real file (`en/sell.png` exists) — confirmed it 200s with real PNG
   bytes when the guard is removed, and 404s with it restored.
3. **The screenshot was injected *before* the topic's own `<h1>`,** so
   every topic opened with an unlabelled image, alt text duplicating the
   heading, and no heading as the document's first landmark. Moved to
   render immediately after `</h1>` instead; reordered the topic's own
   `id == filename` validation to run before the HTML is used, not after.
4. **`.sb-conn` (online/offline, client-side `navigator.onLine`) was
   unmasked** alongside the already-masked update chip — a network-isolated
   regen would produce a different chip color/text with zero guard signal.
   Now masked the same way.
5. **`reuseExistingServer: true` outside CI** meant a developer running
   `make docs-shots` after their own local e2e run would silently capture
   a till with completed sales / flipped settings / an installed plugin —
   defeating the harness's whole "reproducible artifact" purpose. Set to
   `false` unconditionally for the docs config (the normal `e2e/` suite's
   own config is untouched — that one's reuse is a legitimate dev-loop
   optimization, this one is a deliberate occasional run).
6. **A vacuous locale-guard test.** `TestImgDirIsNotALocale` passed even
   with the `if locale == "img" { continue }` skip in `manual.go` deleted
   — `help/img/` has no `.md` files, so nothing under it can ever become a
   topic regardless of the skip, with or without a fabricated fixture.
   Replaced with a comment explaining why the skip isn't independently
   testable rather than keeping an assertion that proves nothing.

**Accepted as documented follow-up, not fixed here:**

- The `designer` topic's receipt preview bakes in the real wall-clock time
  server-side (`internal/pages/receipt_designer.go`'s `sampleReceiptDoc`,
  unconditional `time.Now()`), so its screenshot legitimately differs on
  every honest regen — the whole receipt is one `<pre>` text node, so
  there's no sub-element to mask without hiding the content the topic
  exists to show. Documented in `docs-shots.spec.ts` rather than papered
  over. Real fix (a pinned-time override on the preview endpoint) is
  product code outside this task's scope — filed as a follow-up.
- Guard granularity is all-or-nothing: any one of 131 surface files
  invalidates all 40 screenshots, same as any prose edit to a routed
  topic's markdown. A real cost, not a defect — narrowing it needs a
  per-topic template-to-route map that doesn't exist yet. Noted as a
  design constraint for whoever picks up the follow-up work, not blocking
  this card.
- The `Manual screenshot freshness guard` CI step sits before `go
  build`/`go test`, matching this repo's existing convention for
  `guard-data-access.sh`/`guard-i18n.sh` — consistent with house style,
  not something this PR should special-case on its own.

## Verified beyond automated tests

- Full 40-screenshot regen actually run (not just unit-tested): `npx
  playwright test --config=playwright.docs.config.ts` — 40/40 passed,
  fresh throwaway tills both times (auth-off 8091, auth-on 8092).
- **Looked at the actual images**, not just their existence/size:
  `en/sell.png` (basket populated, RTL-neutral), `fa/sell.png` (basket
  populated, genuinely mirrored RTL layout, Persian numerals, no
  untranslated-fallback notice), `en/users.png`, `ar/reports.png` (RTL,
  empty-but-correctly-laid-out — fresh till has no sales), `en/designer.png`
  (receipt preview readable), `en/invoices.png` (empty-register state, not
  broken), `tr/multitill.png` (Turkish, no overflow/clipping). No
  overlapping, cut-off, or misaligned controls found in any of the eight
  spot-checked. Explicitly **not** individually eyeballed: the remaining
  ~32 of 40 — accepted on the strength of the harness being the same code
  path for every topic/locale pair and the `MIN_BYTES` blank-capture
  tripwire passing for all of them.
- Live server check (not just `httptest`): booted a real till,
  `curl`-fetched `/help/img/en/sell.png` (200, `image/png`, 75996 bytes),
  confirmed `GET /help/sell` embeds the exact `<img src>` tag, confirmed
  the traversal payload and an unknown-locale/file both 404 live. Killed
  the server afterward.
- TDD re-verification, not taken on report: reverted the traversal guard
  and confirmed the new test case fails with the real payload leaking PNG
  bytes; restored it and confirmed the suite passes again.
- `go build ./...`, `go vet ./...` clean. Full `go test ./...`: everything
  green except `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
  — a pre-existing, unrelated root-sandbox artifact (confirmed by running
  it against `main` with this diff stashed out; already tracked as
  ut-docs#258).
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-docs-shots.sh` all green
  after every fix above, not just once at the start.
- `du -sh web/help/img` → 2.7M total, largest single PNG well under the
  300KB sanity threshold.

## Not verified / deferred

- Not driven on real Raspberry Pi hardware — the 1024×600 viewport matches
  the kiosk target, but that's a desktop-Chromium observation.
- The 7 route-less topics have no screenshot and no route to derive one
  from; a follow-up backlog card is needed to give them either a
  representative route or a bespoke capture recipe.
- `invoices`/`reports` screenshots show an empty till (no seed sales) —
  correct and non-broken, but not maximally illustrative; a follow-up card
  could seed a couple of demo sales for richer report/invoice shots.
- `internal/issuereport`'s pre-existing root-sandbox test failure is
  unrelated to this change but still red on this branch — worth a
  `t.Skip` under `os.Geteuid() == 0`, tracked separately as ut-docs#258.

## Verdict

Safe to merge. Six real findings from the independent review were fixed
and re-verified live (not just re-read); the remaining findings are
genuinely accepted trade-offs, written down rather than silently absorbed.
