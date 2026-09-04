# ut-docs#1548 — pairing screens: overflow, duplicate PIN boxes, invisible required field, stacked join methods

**Card:** universaltill/ut-docs#1548 (p1, bug, `source:user`, `complexity:medium`)
**Branch:** `fix/1548-pairing-overflow-pin-tabs` · **Repo:** universal-till
**Review model:** Opus (Dev ran inline on Sonnet — model routing by complexity)

## What shipped

Three defects reported from the pilot pair's real-tablet testing (1280×800
and 1024×600), all in the tills/pairing UI:

1. **59px/40px horizontal overflow** on `/tills` at kiosk width — the
   pending-pairings table plus its manager-PIN input pushed the whole page
   past the viewport, forcing the operator to scroll sideways to reach
   Approve. Fixed with the same `display:block; overflow-x:auto` escape
   hatch `.users-list .table`/`.settings-grid .card .table` already use
   (new `.pairing-table` class, `web/public/app.css`), plus a `width: 12rem`
   cap on the manager-PIN input.
2. **Two manager-PIN boxes per pending pairing request** (one per
   Approve/Deny `<form>`) — an operator filling one and pressing the other
   button submitted an empty PIN. Now one shared `<input>` per row, both
   forms pulling it via `hx-include="#pin-{{ .ID }}"` (the same technique
   this page's own discovery button already used for a field outside its
   form).
3. **`required` alone is invisible until submit.** New `.field-invalid`/
   `.field-error-msg` CSS (both `var(--danger)`-based, no new hardcoded
   color) + a page-local `invalid`/`input` listener toggles a persistent
   red border and message, in both `tills.html` and `setup.html`.
4. **Two stacked "join" sections → two tabs sharing one till-name field.**
   Reused the existing `.tab-bar`/`.tab`/`.tab-panel`/`role=tablist` pattern
   and roving-tabindex keyboard nav already proven in `index.html`'s
   payment-method tabs — no new dependency, Alpine already loaded
   site-wide. Applied to both `tills.html` (Tills settings page) and
   `setup.html` (step 99, first-boot join).
5. `tills.html`'s join-tabs section is now hidden for an **established
   primary** (`not .SyncPrimary` AND `.Tills` non-empty — real satellites
   depending on it, where "join" would be destructive) per the card's own
   AC ("should not compete for attention"), but stays visible for a fresh
   standalone till (nothing to lose) and for a replica (a legitimate
   re-pairing escape hatch) — see the in-code comment on `tills.html` for
   the full reasoning, corrected during review (see below).
6. i18n: two new keys (`tills.join_tabs.discover`/`.code`) plus one more
   added during review (`tills.pairing.error.name_required_field` — see
   findings below), all four locales; manual (`web/help/{en,ar,fa,tr}/
   multitill.md`) updated to describe the new tabbed UI; screenshots
   regenerated (`make docs-shots`).

Two split-out follow-ups, deliberately kept out of this card's scope by
the BA step: ut-docs#1550 (dead-end "joined" screen) and ut-docs#1551
(pairing-request notification via the rail badge — blocked on ut-docs#1539,
mid-review in a sibling lane at pick-up time).

## Independent review (Opus, isolated worktree)

Verdict: **pass, with fixes applied.** The three behaviour fixes were
correct and verified working end-to-end (a real browser intercepting the
approve/deny POST bodies confirmed `hx-include` genuinely shares one PIN
box); the review's real findings were in the test suite and one i18n
reuse:

- **Blocker, fixed — the AC-1 overflow e2e test was a false pass.** It
  loaded `/tills` with an *empty* pending-pairings list, so the table that
  actually overflowed never rendered; deleting the fix's CSS and
  re-running still passed 4/4. Rewritten to seed a real pending request via
  `POST /api/sync/pair-request` and assert the table/PIN box are on screen
  before measuring (denied in a `finally` so the shared till server is left
  clean). Falsification re-confirmed both ways: CSS removed → fails with
  97px/117px overflow; CSS restored → passes.
- **Correction — PIN dedup does not, by itself, fix the overflow.**
  Measured independently: dedup on + CSS off still overflows. The
  `.pairing-table` rule is the load-bearing fix; explanatory comments in
  `app.css`/`pending_pairings.html` were factually wrong about *why* (an
  `<input>` doesn't size to its placeholder — it sizes to `size`, default
  20 — the generic `input` padding rule was the actual cause) and were
  rewritten.
- **Should-fix, fixed — the reused i18n key named the wrong action.**
  `tills.pairing.error.name_required` says "…before you can request
  pairing" in all four locales — accurate for its original caller
  (`pairing_join.go`'s pair-start handler) but wrong once this change also
  fires it for Join/paste-a-code (a token enrolment, not a pairing
  request). Added `tills.pairing.error.name_required_field` with
  action-neutral "this field is required" copy, matching the house pattern
  already used by `locations`/`registers`/`taxcodes`' own `.error.required`
  keys. The original key is untouched at its original call site.
- **Should-fix, fixed — RTL nit.** `.field-error-msg` used a physical
  `margin` shorthand; switched to `margin-block`/`margin-inline` per this
  repo's logical-properties rule.
- **Nit, fixed — the `.SyncPrimary`/`.Tills` gating comment had the wrong
  polarity** (code was already correct, comment said the opposite). The
  reviewer independently re-derived the semantics from `sync_api.go`
  itself rather than trusting the Dev-authored comment, confirmed `.Tills`
  is non-empty on a replica too (ut-docs#405), and rewrote the comment.
- **Nit, fixed** — two extra e2e assertions (a blocked submit must not
  reach `/api/sync/join`; the form must still submit after being blocked
  once), cross-checked against the vendored htmx 1.9.12 source directly
  rather than assumed.

Full command log (build/vet/test/all 35 `ci.yml` guards/`make
docs-shots`/full 326-test e2e suite, plus explicit falsification runs with
the fix reverted) is in the reviewer's own report; re-run independently
after merging the review's fix commit — see "Verified beyond automated
tests" below.

## Verified beyond automated tests

- Real browser network interception confirmed `hx-include` genuinely
  shares one PIN box between Approve and Deny (`bodies=["manager_pin=4321",
  "DENY:manager_pin=4321"]`), not just "the markup looks right."
- `TestPendingPairingsUI_OneManagerPINInputPerRequest` (new) reverted
  against the old two-form markup → fails with the exact expected message;
  restored → passes.
- Rewritten `tills-pairing-layout-1548.spec.ts` overflow test falsified
  both directions (CSS removed → 97px/117px over; CSS restored → clean) —
  the pre-review version of this test was not a real regression guard, see
  above.
- Full gate re-run by the orchestrator after merging the review's fix
  commit: `gofmt -l .` clean, `go build`/`go vet ./...` clean, `go test
  ./...` all green, all 18 CI-blocking guards from `ci.yml`'s `build` job
  green, full Playwright suite (both projects) **326/326 passing**.
- Manual (`web/help/en/multitill.md` + ar/fa/tr) read against the actual
  new UI, not just diffed — step 3 accurately describes the shared
  required till-name field and the two tabs in all four locales.

## Explicitly deferred (real, out of scope for this card)

- **Chromium version mismatch causes unrelated screenshot churn.**
  `make docs-shots` reuses a pre-installed headless_shell (141.0.7390.37)
  against a `@playwright/test` pin of 149.0.7827.55 (ut-docs#622's own
  documented, accepted gap) — each run on a mismatched runner slightly
  re-renders a handful of *unrelated* PNGs (confirmed: the Dev run and the
  review run each rewrote a different, non-overlapping pair of unrelated
  screenshots). `guard-docs-shots.sh` only checks a surface hash, so this
  never fails CI, but it's real drift worth its own card if it keeps
  showing up as unexplained screenshot diffs in unrelated PRs.
- **The 30s poll on `/ui/tills/pending-pairings` wipes a half-typed
  manager PIN.** Pre-existing (the input was always inside the polled
  container); this change doesn't worsen or fix it.
- **No e2e coverage for `setup.html`'s required-field visual state**
  specifically — the script is character-identical to `tills.html`'s
  (which does have coverage), and `login.spec.ts` already exercises the
  setup wizard's tab switch + shared field, so the residual risk is low.
- **`web/help/en/multitill.md`'s reference to "Settings → Tills → This
  till's register"** doesn't match current `tills.html` — pre-existing,
  unrelated to this change, not touched here.

## Safe-to-merge verdict

**Yes.** All three behaviour fixes are correct and independently verified
working (not just "tests pass" — falsified both directions). The one
real gap the review found (a false-pass overflow test) was in the test
suite, not the shipped behaviour, and is now fixed with a genuine
regression guard.
