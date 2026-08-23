# Code review — live-basket demo-removal guard names which basket is blocking

**Date:** 2026-08-23
**Card:** universaltill/ut-docs#746 (p3, `complexity:medium`)
**Trigger:** follow-up from the independent review of ut-docs#633's fix —
`demoDataInLiveBasket` (guards "Remove sample data" against a demo
item/customer sitting in a currently-open, not-yet-held basket) returned
one combined bool for both the cashier's and the self-order kiosk's
baskets, so a kiosk-basket match showed the same generic "current basket"
message a manager at the Settings screen has no way to act on.
**Branch:** `fix/live-basket-guard-746`
**Dev:** inline (Sonnet, autonomous SDLC pipeline)
**Reviewer:** independent subagent, Opus (medium-tier), isolated worktree

## What shipped

`internal/pages/settings_page.go`:

- `demoDataInLiveBasket` now takes `(cashier, kiosk *pos.Service)`
  explicitly (was variadic) and returns a new `demoBasketMatch` result
  (`noBasketMatch`/`cashierBasketMatch`/`kioskBasketMatch`) instead of a
  bare `bool`, checking a fixed `{cashier, kiosk}` order so a
  simultaneous match on both baskets resolves deterministically.
- The `POST /api/settings/remove-demo-catalogue` handler branches the
  i18n message key on the result: `settings.data.demo_in_basket_cashier`
  vs `settings.data.demo_in_basket_kiosk` (replacing the old single
  `settings.data.demo_in_basket` key — confirmed via grep to have no
  other caller anywhere in the repo).
- Two new comments: one documenting that the guard deliberately
  over-blocks (checks list membership only, not `remove_demo.sql`'s and
  `remove_demo_customers_promos.sql`'s full "untouched" predicates —
  reimplementing either in Go would duplicate a safety-critical rule and
  risk drift between two copies); one documenting an accepted TOCTOU gap
  between the check and the `DELETE` (single-till, offline, low-value
  target, one HTTP request wide).
- `web/locales/{en,ar,fa,tr}.json` (this repo's full locale set): old key
  removed, both new keys added, all four verified at exact parity by
  `guard-i18n.sh`.

`internal/pages/demo_seed_opt_in_test.go`: three new/changed tests —
`...BlocksWhileDemoItemInKioskLiveBasket` (TDD'd red→green against the
real pre-fix code, not just the intended behavior), tightened to also
assert the response does *not* read as the cashier message; and
`...BothBasketsLiveReportsCashierFirst`, added during review to pin down
the cashier-before-kiosk determinism the code comment promises (the one
behavior the refactor newly guarantees that nothing had exercised). Both
pre-existing cashier-basket tests (`...BlocksWhileDemoItemInLiveBasket`,
`...BlocksWhileDemoCustomerInLiveBasket`) pass unchanged.

**Companion fix, same trigger:** `ut-plugin-language-de`/`ut-plugin-language-es`
picked up the two new keys (translated) and dropped the old one — see
each repo's own `docs/code-reviews/`. Landed in the same pipeline cycle
specifically to avoid a `lang-pack-drift` regression on `main` (see
Independent review below).

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./...` (full suite, every package) — green, twice (once
  pre-review, once after the review-driven fixes below).
- `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-data-access.sh` —
  both pass.
- TDD: the new kiosk test was confirmed to fail against the pre-fix code
  with the actual on-topic error (generic cashier message, no "kiosk" in
  the body) before the fix landed, then pass after.
- No SQL added (data-access guard agrees — the only `DELETE` token in the
  diff is inside a comment); no money involved; no file writes (no
  `os.MkdirAll`/`paths.Data` question applies); no page/route/template
  changed, so no `web/help/` manual update or screenshot regen is
  needed — `guard-help-topics.sh` agrees.
- No real client/shop name used — the test fixture data
  (`itm001`/`SKU-0001`/"Coca-Cola Can 330ml") is the pre-existing seed
  row, not new.

## Independent review (Opus, isolated worktree) — 1 should-fix, 5 nitpicks, 0 blockers in the diff itself

Re-ran the full gate independently rather than trusting the dev report,
and separately re-verified the TDD claim by reconstructing the actual
pre-fix state (Go file *and* locale files — reverting only the Go file
first gave a misleading result, since old code referencing the
now-deleted key renders the raw key on an `httpx.T` miss).

**Should-fix (real, verified externally, now fixed):** merging as-is
would go green on this PR but push `main`'s `lang-pack-drift` workflow
red minutes later — that check is push-to-main-blocking (`universal-till/CLAUDE.md`)
and fetches `ut-plugin-language-de`/`-es`'s live packs, neither of which
had the new keys yet. Fixed by landing both companion pack updates in
this cycle; `check-key-drift.sh` (run locally against this branch's
`en.json`) now reports 0 drift for both packs.

**Nitpicks — 4 of 5 addressed, 1 explicitly deferred:**
- Comment scope (over-block rationale cited only `remove_demo.sql`, not
  `remove_demo_customers_promos.sql`'s differently-shaped customer
  predicate) — **fixed**, comment now names both.
- Determinism claim untested — **fixed**, new
  `...BothBasketsLiveReportsCashierFirst` test.
- New test's assertion could positively rule out the cashier message,
  not just infer it — **fixed**, added the negative assertion.
- `fa` kiosk translation used slightly different wording than the
  existing `settings.display.mode_self_order`/`kiosk_idle_reset.help`
  precedent ("کیوسک سفارش‌گیری خود" vs "کیوسک سفارش خود") — **fixed**,
  aligned to the precedent term.
- Cashier message still reads generically as "current basket" now that
  kiosk names itself — **deliberately deferred**, judgment call flagged
  by the reviewer, not a defect: fixing it would mean rewording the
  cashier key and touching the two pre-existing tests' `"current basket"`
  assertions, which was explicitly out of scope for this card (Architect
  non-goal: no UI/message redesign beyond the exact two-key split). Worth
  a future card if the product owner wants full symmetry.

No blockers found in the diff's own logic, ordering, nil-handling,
i18n parity, or test soundness — see the review transcript for the full
line-by-line detail (ordering/determinism, nil-skip preservation, no
inverted branch, i18n key-set diff confirmed empty for ar/fa/tr,
SQL-predicate comment cross-checked against the actual scripts,
`os.MkdirAll`/`paths.Data` and money/SQL bug classes confirmed genuinely
N/A rather than assumed, test confirmed to drive a real HTTP request
through the real mux against a fully-migrated real SQLite DB).

## CI-caught gap (found on push, fixed same-cycle)

Real CI on the PR failed `guard-docs-shots.sh`: `settings_page.go`
registers at least one screenshotted route, so touching the file at all
(even a backend-only change with zero visible effect) invalidates the
manual screenshot manifest's recorded surface hash — the guard is
deliberately over-inclusive here, by its own documented design, rather
than trying to isolate which function in a multi-route file actually
renders. Fixed by running `make docs-shots` and committing the
regenerated `web/help/img/manifest.json` (surface hash only — every
topic markdown hash is unchanged, confirming no manual content actually
changed) plus a handful of unrelated topics' PNGs that re-rendered with
minor pixel differences (accepted screenshot-tool nondeterminism, not a
regression — the guard checks the manifest record, not pixel content).
This should have been run locally before the first push (it's in
`universal-till/CLAUDE.md`'s "Before committing" guard list) — noted here
so a future cycle catches it earlier next time.

## Safe-to-merge verdict

Yes — should-fix resolved (companion locale-pack updates landed), 4 of 5
nitpicks folded in, the fifth explicitly deferred with reasoning, the
CI-caught docs-shots gap fixed, full gate green after all changes.
