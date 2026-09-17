# Code review — auto-parked baskets can share a same-minute clock label (ut-docs#2194)

- **Date:** 2026-09-17
- **Ticket:** ut-docs#2194 (`complexity:easy`, `ux`) — split out of ut-docs#1919's
  independent review (finding F5).
- **Branch:** `fix/2194-auto-park-clock-label`
- **Reviewer:** independent pass, fresh-context Sonnet subagent working in an
  isolated worktree (per this card's `complexity:easy` routing — never saw the
  implementation reasoning).
- **Verdict: SAFE TO MERGE.** No blocking findings, no should-fix items.

## The bug

`parkCurrentBasket` (`internal/pages/hold_api.go`)'s no-name/no-typed-label
fallback always used `time.Now().Format("15:04")` (minute resolution),
regardless of caller. ut-docs#1919 made this fire far more often: every
auto-park of a busy basket during a resume-to-another-order switch
(`resumeHeldSale`) passes an empty `typedLabel` — there's no dialog to type
one into on that path. Two order-switches inside the same clock-minute then
produced two held rows both displayed under the identical label (e.g.
`15:28`) in the sale-screen strip / `/open-orders` list — a discoverability
nit, not data corruption (ids are already unique `hold-<UnixNano>`, and
`/open-orders` already disambiguates further by table/total/item-count/age).

## What shipped

- `parkCurrentBasket` gained `locale string, autoPark bool` parameters. Its
  fallback now branches: `autoPark=true` (the only caller: `resumeHeldSale`'s
  busy-basket auto-park) formats
  `fmt.Sprintf(httpx.T(locale, "hold.label.auto_parked"), time.Now().Format("15:04:05"))`
  — seconds resolution, translated prefix. `autoPark=false` (a genuine manual
  Hold with a blank name) keeps the exact old `time.Now().Format("15:04")`,
  unchanged.
- `resumeHeldSale` gained a `locale string` parameter, threaded from all three
  of its call sites: `hold_api.go`'s `POST /api/pos/hold` and
  `POST /api/pos/resume` (both already resolved `locale` locally), and
  `open_orders_page.go`'s `POST /open-orders/resume`, which previously never
  resolved a locale at all — a new `httpx.ResolveLocale(w, r)` call was added
  there.
- New locale key `hold.label.auto_parked` added to all four shipped locale
  files (`web/locales/{en,ar,fa,tr}.json`) with real per-language text:
  `"Auto-held %s"` / `"عُلّق تلقائيًا %s"` / `"نگه‌داشتهٔ خودکار %s"` /
  `"Otomatik bekletildi %s"` — following this file's own established
  `fmt.Sprintf(httpx.T(...), value)` pattern (e.g.
  `elevation.summary.buttons_add`).
- Two new tests in `internal/pages/hold_api_test.go`:
  `TestHoldHandler_BlankManualHoldKeepsPlainClockLabel` (manual-hold path
  unchanged, asserts `^\d{2}:\d{2}$`) and
  `TestResumeHandler_AutoParkUsesSecondsResolutionLabel` (auto-park path,
  asserts `^Auto-held \d{2}:\d{2}:\d{2}$`).
- The re-park branch (`origin` already set — `held.Label = origin.Label`
  kept unless a typed rename is given) is untouched; it never reads
  `autoPark`/`locale`.

## What the independent review found

Ran the full gate itself: `go build ./...`, `go vet ./...`, `gofmt -l`
(clean), `go test ./internal/pages/... -run 'Hold|Resume' -v` (all pass,
including both new tests), `guard-i18n.sh` (1729 keys, all locales match),
`guard-data-access.sh` (clean — no SQL touched).

**TDD re-verification, done independently by the reviewer**: reverted just
the fallback branch to the old single-line `time.Now().Format("15:04")`,
confirmed `TestResumeHandler_AutoParkUsesSecondsResolutionLabel` failed with
exactly the predicted old-format label (`"22:11"`, not matching the new
pattern), with the manual-hold test unaffected. Restored the fix, confirmed
both tests green again and the working tree clean.

Correctness checks, all clean: every caller of `parkCurrentBasket`/
`resumeHeldSale` updated consistently (grep-confirmed no other call sites
exist); the manual-hold handler passes `autoPark=false`, the only auto-park
path passes `true`; the re-park branch never touches the new parameters;
no money/offline-first/repository-pattern rules implicated (no SQL, no
money type, no network dependency introduced).

**Rendering-surface check**: the reviewer verified the claim that the new,
longer label (~29 chars for the Turkish value) renders safely. One nit
surfaced: `/open-orders`' own table page (`open_orders.html`'s
`.open-order-cell-label`) does have `overflow:hidden; text-overflow:
ellipsis; white-space:nowrap` — the longer label will ellipsize there under
column pressure. This is graceful degradation (ellipsis, not breakage),
pre-existing CSS this diff doesn't touch, and not a regression — accepted
as-is. The sale-screen strip chip (`.held-chip`) and the parked-orders popup
(`.parked-order-label`, unstyled) have no such constraint.

**Help docs**: no `web/help/**` topic documents the internal clock-label
format at this level of detail — nothing goes stale.

**Test data / secrets**: clean — no real shop/client name, no secret-shaped
literal.

One process finding, closed by this same step: the commit was still titled
`WIP:` with no review record — both fixed here (this record, plus dropping
the `WIP:` prefix and squashing with `--reset-author` before push).

## Explicitly deferred (not this card)

- The `/open-orders` table page's ellipsis behavior on a long label is
  pre-existing and out of this card's scope (it already ellipsizes a long
  typed label or customer name today, up to 64 runes) — not a new Backlog
  item, just noted as accepted behavior.

## Merge status

Auto-push/auto-release authorization confirmed current (ut-docs#2277,
resolved by the product owner 2026-09-17: no real users yet, standing
2026-07-29 authorization stands unchanged). Merging per that authorization.
