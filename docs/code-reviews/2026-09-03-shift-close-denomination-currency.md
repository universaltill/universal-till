# Code review — shift-close cash-count grid hardcoded to GBP denominations (ut-docs#1291)

- **Date:** 2026-09-03
- **Branch:** `fix/1291-shift-close-denom-currency-aware` (see "Process
  finding" — a commit, `3105eb1c`, landed mid-review on this feature branch,
  not `main`; review fixes left uncommitted in the tree for the orchestrator
  to fold in)
- **Reviewer:** independent read at Opus, a different model from the
  implementing tier. No `Agent`/subagent-spawn tool was exposed in this
  session, so the `reviewer` skill's "spawn a different-model subagent" step
  was satisfied by the reviewer session itself being the different model,
  with the revert-then-restore verification run inline and atomically
  (never across a turn boundary) rather than in a worktree-isolated
  subagent. Recorded rather than silently skipped.
- **Verdict: SAFE TO MERGE**, with three review fixes applied (two
  denomination-data corrections, one test-quality fix). No blocking findings
  remain.

## What shipped

`web/ui/pages/shifts.html`'s `#denom-grid` — the optional per-denomination
cash-count block on the close-shift form — hardcoded GBP physical note and
coin denominations (`£50` … `1p`) regardless of the shop's configured
currency. Every non-GBP shop (IRT, TRY, AED, JPY, …) was offered a
meaningless GBP count grid.

- `httpx.CurrencyInfo` gains `Denominations []int64` (minor units, strictly
  descending), populated with real circulating banknote/coin values for all
  13 registry currencies (`internal/httpx/currency.go`).
- The grid now renders `{{ range currency.Denominations }}` with
  `{{ money . }}` labels, reusing the template func already registered in
  `internal/httpx/httpx.go` — so each label is locale- and
  currency-formatted for free, the same convention ut-docs#1274 established
  for this page's other currency-aware fields. No new template helper, no
  parallel labels slice to drift.
- `data-denom` stays the raw minor-unit integer.
- Tests: registry `Denominations` non-empty and strictly descending for
  every currency (nil for an unknown code, matching `CurrencyByCode`'s
  documented ut-docs#970 fallback convention), plus a page-level regression
  test rendering the grid under GBP vs JPY.
- `web/help/img/**` regenerated via `make docs-shots` (surface hash changed
  because `web/ui/**` did).

## Review findings

### F1 — MEDIUM, **fixed**: EUR omitted the €200 note, on a false premise

The EUR entry excluded both €200 and €500, justified in a comment as
"being phased out, rare in a till drawer". That is accurate for €500
(issuance discontinued 2019, actively withdrawn) but **not** for €200,
which is a current Europa-series note, still issued and unremarkable
legal tender. The count protocol is documented in the manual as a
piece-by-piece record of the drawer; a eurozone shop that takes a €200
note had no row to record it in, so the protocol could not represent the
drawer it exists to document.

Added `20000` to EUR (now 14 rows) and rewrote the comment to state the
real, and different, reasons €500 is out and €200 is in. €500 deliberately
stays excluded — the original reasoning holds for it.

### F2 — LOW, **fixed**: SAR omitted the 2-riyal coin

The Saudi sixth series (2016) circulates 1- and 2-riyal coins; only the
1-riyal (100 halalas) was listed. Added `200`, and corrected the entry's
comment, which listed the coins as halalas only.

### F3 — MEDIUM (test quality), **fixed**: the GBP half of the new page test was not load-bearing

`TestShiftsPage_DenomGridIsCurrencyAware` asserted against the whole
response body. Its fixture inserts a shift with `opening_cash=5000`, so
the page renders `£50.00` in the summary above the form **regardless of
what the grid contains** — while the old hardcoded grid's label was `£50`
(and `1p`, never `£0.01`). So `strings.Contains(body, "£50.00")` passed
against the pre-fix template: only the JPY half of the test actually
failed pre-fix, and the GBP assertions were decorative.

Fixed by scoping the label assertions to the `#denom-grid` block itself
via a `gridOf` helper (the grid holds only `<label>`/`<input>` children, so
the first `</div>` after `id="denom-grid"` is its own). The page-wide "no
`£` anywhere under a JPY shop" assertion is deliberately left page-wide and
commented as such. The `data-denom="…"` assertions were already
grid-specific and remain correct — note `data-denom="200"` is genuinely not
a substring of `data-denom="2000"` because of the closing quote, so that
negative assertion was sound.

Confirmed by measurement, not inspection: against the true pre-fix blob the
strengthened test now fails at `shifts_page_test.go:315`, the **GBP**
assertion, which it did not do before this change.

### F4 — LOW, **accepted, not fixed**: an unknown currency code now yields an empty grid

`ActiveCurrency()` → `CurrencyByCode()` returns `Denominations: nil` for an
unregistered code, so `{{ range }}` renders nothing and the operator gets an
expandable "Denomination count" `<details>` containing a hint paragraph and
no inputs — a dead-end panel. Previously they got a GBP grid.

Accepted, deliberately:

- It is not reachable through any shipped UI. Both currency pickers gate on
  `IsKnownCurrency` (`internal/pages/setup_page.go:535`,
  `internal/pages/import_page.go:482`). The only route in is an
  authenticated `POST /api/settings/upsert` of `store.currency` with an
  unregistered code, a tolerance the code documents on purpose
  (`internal/pages/settings_page.go:1971-1975`: an unknown currency "just
  falls back to a plain `CODE 1.23` format", unlike an invalid locale).
- In that already-degraded state, an empty grid is *more* honest than
  offering GBP denominations to a shop that is not on GBP. Nothing crashes:
  the page's JS finds no `.denom-count` nodes, leaves the hidden field
  empty, and no `count_protocol` is submitted — the documented
  "leave it empty to skip it" path.
- The one-line close is `{{ if currency.Denominations }}` around the
  `<details>`, but that edits `web/ui/**` and therefore forces another
  `make docs-shots` regeneration for a state no shipped UI can reach.
  Better as a follow-up card than as scope creep on this one.

### F5 — LOW, **observation, not changed**: possible IRR/IRT gap

IRR lists 1,000,000 and 500,000 rials (Iran-cheque bearer notes that
circulate as cash) but not 200,000, which I believe also circulates; IRT
mirrors IRR exactly at 1/10, so the same gap would apply at 20,000 toman.
Left alone on purpose — my confidence here is lower than on F1/F2, and the
Iran market is one the product owner knows first-hand. Worth a one-line
confirmation from them rather than a reviewer guess. The IRR↔IRT 10:1
correspondence is otherwise exactly right and is the part most likely to
have been got wrong.

### Non-blocking notes, not fixed

- **`Currencies()` returns the package-level registry slice directly**, and
  each entry now carries a mutable `[]int64`. A Go caller could mutate
  shared process state. Pre-existing shape (the outer slice was already
  shared and templates cannot mutate); no in-tree caller does. Noted only.
- **`Denominations []int64`, not `[]money.Money`.** Checked against
  CLAUDE.md's money rule and accepted: `internal/httpx` is the formatting
  boundary and its existing money-shaped API is already raw minor-unit
  `int64` (`FormatMoney(minor int64, …)`, `FormatMajorPlain(minor int64,
  decimals int)`, `MoneyPlaceholder(decimals int, example int64)`). It is
  also load-bearing for the template: `data-denom="{{ . }}"` must emit the
  plain integer the page's JS parses, which a `money.Money` element would
  not guarantee. Consistent with the file's own precedent.
- CLAUDE.md's guard snapshot is stale — `ci.yml`'s `build` job now also
  runs `guard-migration-version-collision.sh` and `guard-osk-loaded.sh`.
  Both were run here and pass. CLAUDE.md already warns the list drifts.

## Verified beyond automated tests

- **Denomination data sanity-checked against real circulating notes/coins
  for all 13 currencies**, not just spot-checked: GBP, USD, TRY, AED, IQD,
  AFN, INR, PKR and JPY are correct as written, including the deliberate
  and correctly-reasoned exclusions (₹2000 withdrawn 2023; ¥2000 legal but
  effectively Okinawa-only; US $2/50¢ rare). INR and PKR correctly collapse
  the note/coin value collisions (₹20/₹10, ₨10) to one row each, and
  correctly treat paise/paisa as obsolete despite `Decimals: 2`. IRT is
  exactly IRR/10 throughout. EUR and SAR were wrong; see F1/F2.
- **The template change is behaviour-preserving for the submitted payload.**
  `data-denom="{{ . }}"` renders the raw minor-unit integer; the page's own
  JS (`grid.querySelectorAll('.denom-count')` →
  `protocol[inp.getAttribute('data-denom')] = n`) is untouched and still
  produces the same flat `{"<minor>":<count>}` object.
  `pos.ValidCountProtocol` accepts it unchanged, and its 4096-byte cap
  stays comfortable — the largest set is now EUR at 14 rows, with PKR's
  6-digit keys the longest.
- **TDD claim independently re-verified twice** (see Process finding for
  why twice), atomically within a single shell invocation each time:
  reverting only `shifts.html` to the pre-fix blob makes the regression test
  fail; restoring returns it to green.
- **i18n:** the new labels are `money`-formatted numerals plus the currency
  symbol — not prose — so no locale keys were needed and none were added.
  `guard-i18n.sh` passes. Consequently there is no
  `ut-plugin-language-{de,es}` follow-up, and the `lang-pack-drift` PR check
  is correctly absent (it is `paths:`-scoped to `en.json`, which this diff
  does not touch).
- **Manual:** `web/help/en/reports.md` is the topic claiming `/shifts`, and
  it describes this feature currency-neutrally ("an optional
  per-denomination count (how many of each coin and note)") — no prose went
  stale, so no manual edit was owed. The grid sits inside a collapsed
  `<details>`, so no manual screenshot depicts it; the five regenerated PNGs
  (`sell.png`, `till-designer.png` in some locales) are capture
  non-determinism, as claimed, not a content regression.
- **Repository pattern:** no SQL outside `internal/data` in this diff. The
  new test's raw `INSERT`s live in a `_test.go`, outside the guard's scope;
  `guard-data-access.sh` passes.
- **Demo data / secrets:** the fixture uses `reg1`/`user1`/"Front Till" —
  no real shop or client name, no secret-shaped literal anywhere in the
  diff.
- UX-guidelines checklist: no new colors, spacing, tokens, modal blockers
  or `left`/`right` CSS — the grid's existing container and styles are
  untouched, and RTL/suffix currencies inherit the page's existing logical
  layout. The pre-existing 3rd-column overflow at narrow card widths is
  unchanged by this diff and out of scope.

## Process finding (for the orchestrator, not a code defect)

**A commit landed on `main` between reviewer turns** — `3105eb1c`,
containing the full 10-file diff — despite the review brief saying not to
commit. Two consequences worth recording:

1. **It silently invalidated a verification run.** My second
   revert-then-restore used `git checkout -- web/ui/pages/shifts.html`,
   which after the commit restored the *fixed* file from the new `HEAD`
   instead of the pre-fix one. The "pre-fix" run therefore passed — a false
   green that reads exactly like a broken test. Caught via `git status` /
   `git log` and redone against the explicit pre-fix blob
   (`git checkout 7ce4ee0d -- …`), which failed as it should. This is the
   ut-docs#386 hazard almost exactly, in its quieter form: not a corrupted
   commit, but a verification that stops verifying anything.
2. **I confirmed no corruption and a clean identity.** The committed
   `shifts.html` blob is byte-identical to the reviewed fixed file (checked
   with `git show HEAD:… | diff`), so no mid-revert state was captured, and
   `git log -1 --format='%an <%ae>'` is the pipeline owner's GitHub
   `users.noreply.github.com` identity, not an AI-tool default. Orchestrator
   note: the commit is in fact on `fix/1291-shift-close-denom-currency-aware`
   (created and pushed by the same stop-hook-forced commit), not `main` —
   `git branch --contains 3105eb1c` confirms `main` never carried it. My
   `git log`/`git status` reads mid-review predated the branch switch, which
   is what made it look like a main-branch landing; no reconciliation was
   actually needed.

My three review fixes (F1, F2, F3) are **uncommitted in the working tree**
(`internal/httpx/currency.go`, `internal/pages/shifts_page_test.go`) for the
orchestrator to fold in.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` (full suite, after review fixes) | pass, no failures |
| New registry tests (`TestCurrencyRegistry_DenominationsPresentAndDescending`, `TestCurrencyByCode_UnknownCodeHasNoDenominations`) | 2/2 pass, and they do cover the F1/F2 additions |
| `TestShiftsPage_DenomGridIsCurrencyAware` (strengthened) | pass post-fix; fails pre-fix at the **GBP** assertion (line 315), which it did not before F3 |
| All 20 CI-blocking guards in `ci.yml`'s `build` job | 20/20 pass, incl. `guard-docs-shots.sh`, `guard-i18n.sh`, `guard-help-topics.sh` |

`guard-docs-shots.sh` still passes after the review fixes without a second
`make docs-shots` run, as expected: the surface hash covers
`web/ui/**` plus non-test `internal/pages/**.go`, and F1–F3 touched only
`internal/httpx/currency.go` and a `_test.go`.

## Explicitly deferred

- F4 — empty denomination grid under an unregistered currency code. Worth a
  follow-up card (`{{ if currency.Denominations }}` around the `<details>`),
  not worth a docs-shots re-run on this branch.
- F5 — confirm with the product owner whether IRR should also list 200,000
  rials (and IRT 20,000 toman).
