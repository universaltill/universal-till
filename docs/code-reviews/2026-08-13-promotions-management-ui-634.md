# Code review: promotions management UI (create/edit/deactivate/list promo codes)

**Date:** 2026-08-13
**Author (Dev):** scrum-master pipeline, Sonnet
**Reviewer:** independent Opus subagent (isolated worktree, fresh checkout of
`feat/634-promotions-ui` @ `803281b`)
**Card:** universaltill/ut-docs#634

## What shipped

The `promotions` table has always carried real read/redeem logic
(`POSRepo.FindActivePromo`, `/api/pos/scan`'s discount-code path) with no
in-product way to create a code — once migration 038's sample-data opt-out
removes the three seeded rows, a shop's only route to a new promo was the DB
itself. This adds the management layer around that existing logic:

- `internal/data/pos_repo.go` — four new methods (`CreatePromotion`,
  `UpdatePromotion`, `SetPromotionActive`, `ListPromotionsForAdmin`) plus
  `PromotionInput`/`PromotionAdmin` and a distinct `ErrPromotionCodeExists`,
  added *after* `FindActivePromo`, which is byte-identical to `main`.
- `internal/pages/promotions_page.go` — manager-gated `GET /promotions`,
  `POST /api/promotions`, `POST /api/promotions/{code}/edit`,
  `POST /api/promotions/{code}/active`; audit-logged; redirect-with-`?err=`
  on failure, mirroring `locations_page.go`.
- `web/ui/pages/promotions.html`, a `/promotions` nav entry in
  `session_chip.html` (manager-only, matching the `/users` precedent), 27
  new `promotions.*` keys in all four locales, and a new `promotions` help
  topic in all four locales.
- Tests: `pos_repo_promotions_test.go` (8), `promotions_page_test.go` (8 as
  shipped), `promotions_scan_regression_test.go` (the
  `FindActivePromo`-unaffected proof).

`value` stays a raw `int64` at rest — minor units for `amount`, basis points
for `percent` — matching the schema comment in `001_init.sql`
(`-- minor units or basis points (1% = 100)`), the `DISC10` seed row
(`percent`/`1000` = 10%), and `pos_api.go`'s own branch
(`SetDiscountPercent(value)` vs `SetDiscount(money.FromMinor(value))`).
Conversion from human input happens only at the form boundary.

## Verified beyond automated tests

- **The critical regression claim, verified independently, not read from the
  commit message.** `git diff main -- internal/pages/pos_api.go` is
  **zero lines**. The `internal/data/pos_repo.go` diff is a single
  pure-addition hunk starting at line 2885 — `FindActivePromo` (lines
  2863–2886) is untouched, only new methods added around it.
- **TDD, revert-then-restore, four independent probes** — each reverted in
  the implementation only (never the test), run, failure message read, then
  restored and re-run green:
  1. `isUniqueViolation` → `return false`:
     `TestCreatePromotion_DuplicateCodeIsDistinctError` FAILED with
     `duplicate code must return the distinct ErrPromotionCodeExists, got:
     create promotion: constraint failed: UNIQUE constraint failed:
     promotions.code (1555)`.
  2. `CreatePromotion` made to silently skip persistence:
     `TestPromotionCreatedViaAdminIsRedeemableAtScan` FAILED with
     `SaleDiscount = 0, want 150` — **the regression test is load-bearing,
     not decorative**: it fails on exactly the "admin path stops feeding the
     redeem path" class of break it exists to catch.
  3. Percent conversion `pct * 100` → `pct`:
     `TestPromotionsPageCreate_Percent` FAILED (`value=10, want
     percent/1000`) and `TestPromotionsPageEdit_UpdatesButNotCode` FAILED —
     the off-by-100 hazard is genuinely covered on both create and edit.
  4. `requireManager`'s condition disabled:
     `TestPromotionsPagePermissions` FAILED with `cashier GET /promotions =
     200, want 403`.
  Working tree confirmed byte-identical to `803281b` after all four restores
  (`git diff` empty) before any fix was applied.
- **Money/value handling traced end to end.** `amount`: `"5.00"` →
  `money.FromMinor(round(5.00*100)).Minor()` = 500 minor units, asserted
  against the DB in `TestPromotionsPageCreate_AmountAndList`. `percent`:
  `"10"` → 1000 bp, matching `DISC10`. **No off-by-100** in either
  direction — the display/prefill inverse (`float64(p.Value)/100` →
  `"10.00%"`) round-trips, and `internal/pos/service.go:428`
  (`(sub*bp + 9999)/10000`) confirms 1% = 100 bp is the engine's own reading.
- **Duplicate-code path read, not inferred from a green test title.**
  `isUniqueViolation` string-matches `"UNIQUE constraint failed"` (the same
  approach `reset_archive_repo.go`'s `isForeignKeyViolation` already uses,
  because `modernc.org/sqlite` exports no typed error) → wrapped as
  `ErrPromotionCodeExists` → `errors.Is` in the handler →
  `?err=promotions.error.code_exists` → rendered via `{{ T .errKey }}`. No
  SQL text and no 500 reaches the user.
- **Manager gate is server-side on every route**, verified by reading each
  handler: all four call `requireManager` as their first statement and
  return `403 manager or admin role required` before touching the DB. The
  nav link is only an additional client-side convenience. This is the
  *stricter* of the two in-repo patterns (`requireManager` as in
  `users_page.go`/`locations_page.go`, not `isManagerOrAuthOff`) — correct
  for an admin CRUD page.
- **Audit logging present on all four actions, not just one** —
  `promotion_create`, `promotion_edit`, `promotion_deactivate`,
  `promotion_activate`, each through `POSRepo.InsertAudit` with the same
  `(ctx, nil, actorID, entityType, entityID, action, nil, now, "")` shape
  `locations_page.go` uses. `TestPromotionsPageDeactivateReactivate` asserts
  a count of exactly 3 rows for one code across create + deactivate +
  reactivate, so a silently-missing call would fail.
- **Soft delete confirmed** — no `DELETE` statement exists anywhere in the
  new repo code; deactivation is `UPDATE promotions SET is_active = ?`, and
  `is_sample_data` (migration 038) is deliberately left at its column
  default so merchant rows are never swept by the sample-data opt-out.
- **Help "?" resolution checked mechanically**, not assumed: `httpx.Render`
  binds `withHelpHref(..., r)` per request, so `/promotions` resolves to the
  new topic via its `routes:` claim — no explicit `helpLink` needed (that
  form is only for a section inside an already-claimed page).
- **Translations read, not counted.** All four locales carry the same 27
  keys; ar/fa/tr are genuine target-script prose, not English copies and not
  transliterations — e.g. `promotions.title` is «العروض الترويجية» (ar),
  «کدهای تخفیف» (fa), "Promosyonlar" (tr), and each error string is an
  idiomatic sentence in its own language. Turkish diacritics are correct
  throughout.
- **Cross-branch overlap (ut-docs#591) checked, as instructed.** This
  branch's `internal/pages/init.go` change is one appended line in the
  registration list, and each locale change is one contiguous block of new
  keys — self-contained and correct standing alone.
- **Translation provenance** — the reviewer independently re-verified the
  homelab Ollama NAS is genuinely unreachable from this sandbox (TCP connect
  to `192.168.1.231:11434` times out; the host does not resolve), so the
  ar/fa/tr strings — including this review's own help-topic edits — were
  authored directly rather than through the documented self-hosted flow.
  This is a real, disclosed deviation from `reference/translation.md`, not
  an oversight; the strings themselves were checked for sense and accuracy
  by the reviewer.

## Independent review — findings

**No blockers.** No data loss (soft deactivate only, no `DELETE`), no
money-type misuse, no security regression, and the checkout redeem path is
provably untouched. Three real defects found and **fixed in this review
round**, all at the form boundary — none required touching `FindActivePromo`
or `pos_api.go`:

**F1 — end date excluded its own last day (functional defect, fixed).**
`FindActivePromo` filters on `datetime(ends_at) >= CURRENT_TIMESTAMP`, and
`datetime('2026-08-13')` is `2026-08-13 00:00:00` — midnight at the *start*
of that day. Storing the bare `<input type="date">` value therefore expired
a promo at the beginning of the day the shop chose as its last. Reproduced
empirically before fixing: a promo created through the new UI with
`starts_at = ends_at = today` was **not redeemable at all**
(`FindActivePromo` returned `ok == false`, stored `ends_at="2026-08-13"`) —
a one-day promotion, the most ordinary case there is, silently never worked.
This was latent in `FindActivePromo` but harmless until this card made the
dates settable by a shop owner, so it becomes this card's defect. Fixed by
`endsAtInclusive` storing `YYYY-MM-DD 23:59:59`; the view trims it back via
`promoDateOnly` for the list column and the `type="date"` prefill (also
tolerant of legacy DB-authored rows carrying a full timestamp), and
`parsePromoDate` re-trims on edit so a re-save can't drift the bound
outward. `FindActivePromo`'s query is unchanged — only the value written.
New test `TestPromotionsPageCreate_EndDateIsInclusiveOfThatDay` covers
redeem-on-the-last-day, no timestamp leaking into the page, and
re-save-doesn't-drift; verified load-bearing by reverting `endsAtInclusive`
to return the bare date (FAILED: `a promo starting and ending today must be
redeemable today (ends_at="2026-08-13")`) and restoring.

**F2 — percent accepted above 100% (validation gap, fixed).** The handler
rejected `pct <= 0` but not `pct > 100`, so a typo of `500` stored 50000 bp.
The engine clamps the basket total at zero, so no negative charge is
possible — but 500% and 100% then behave identically at the till while the
stored value misstates what the shop intended, and the list column happily
displays "500.00%". `settings_page.go`'s payment-fee percent already
enforces exactly `0 < pct <= 100`; the same bound now applies here, with
`max="100"` added to both percent inputs as the matching client-side hint.
New test `TestPromotionsPageCreate_PercentOver100Rejected` also asserts the
rejected row is not stored, and that 100% off stays legal.

**F3 — date fields accepted arbitrary text (validation gap, fixed).**
`starts_at`/`ends_at` were trimmed but never format-checked, so a
hand-crafted POST could store junk. SQLite's `datetime()` yields `NULL` for
it, and `FindActivePromo`'s comparison then silently never matches — a promo
that looks saved in the list but can never be redeemed, with nothing
anywhere to explain why. `parsePromoDate` now requires a real ISO-8601
calendar date (empty still means "no bound"), covered by
`TestPromotionsPageCreate_MalformedDateRejected`. This also satisfies
CLAUDE.md's "validate all external input".

**F4 — attribute-injection hardening in the customer picker (fixed).** The
new page's inline JS built `data-cust-pick="…"` / `data-cust-name="…"` from
an `esc()` helper that escapes via `textContent`→`innerHTML`. That escapes
`&`, `<`, `>` but **not quotes**, which is safe between tags and unsafe
inside a double-quoted attribute — and `c.Name` is free text a customer
chose. The pattern was copied from `settings.html`'s erasure picker (where
only the internal `c.ID` lands in an attribute), so this card widened a
pre-existing weakness rather than inventing one. `esc()` in the new file now
also escapes `"` and `'`; `getAttribute` decodes them back, so the picker's
behaviour is unchanged. `settings.html`'s copy is left alone — out of scope
here, worth a follow-up card.

**F5 — manual claimed a capability the page doesn't have (fixed).** The help
topic said an existing code's "customer target" could be edited, but the
edit row only carries the current `customer_id` in a hidden field — it can
be neither changed nor cleared. Under the standing "the manual is never
ahead of the product" rule (ut-docs#324) the prose was corrected in all four
locales, and the same edit documents the new 0–100 percent range and the
end-date-includes-that-whole-day semantics from F1.

Nits (noted, no action taken): `{{ T .errKey }}` renders an
attacker-suppliable `?err=` query value — unknown keys fall through to the
raw string, escaped by the template, so this is reflected *text* only, and
it is character-for-character the pattern `locations.html` already ships;
pre-existing, not introduced here. `"title": "Promotions"` is a hardcoded
English string in the render map, exactly as `users_page.go` and
`locations_page.go` do it — consistent with precedent and accepted by
`guard-i18n.sh`.

**Suggested follow-up (new Backlog card, out of scope):** editing a promo's
target customer (the field exists in the schema and is settable at create
time, but not editable afterwards); and migrating `settings.html`'s
erasure-picker `esc()` to the quote-safe version for parity with F4.

## Gate — all green (re-run after the fixes, not before)

```
go build ./...                    BUILD exit=0
go vet ./...                      VET exit=0
go test ./internal/pages/... ./internal/data/...
  ok  github.com/universaltill/universal-till/internal/pages          94.802s
  ok  github.com/universaltill/universal-till/internal/pages/catalog   0.518s
  ok  github.com/universaltill/universal-till/internal/pages/common    2.813s
  ok  github.com/universaltill/universal-till/internal/data           29.846s
go test ./...                     no failures (full suite, not just the two packages)
✓ data-access guard: no inline SQL outside internal/data / internal/db
✓ i18n guard: 1017 template keys resolve; all locales match en.json; no
  hardcoded Go-side response strings found; no hand-written hx-vals literals
  found; no hardcoded inline-JS status strings found
✓ help-topics guard: no route conflicts, every topic parses, all shipped
  locales complete, every page route has a claiming topic
✓ kiosk-engine guard: no self-order route handler references the cashier's Engine
✓ plugin-menu-read guard: no unlocked read of Pm.Installed / Pm.MenuPlugins /
  Menu under internal/pages
```

`gofmt -l` reports no new unformatted files (the six it lists are
pre-existing on `main` and untouched here).

## Post-review fixup: real manual screenshots via `make docs-shots`

`guard-docs-shots.sh` correctly flagged `/promotions` as a routed topic with
no screenshot at all (a brand-new page, no prior baseline to hash-patch
against — unlike ut-docs#591's zero-pixel-impact case in the same cycle,
this genuinely needed new images). `make docs-shots` is actually runnable in
this cloud session (ut-docs#622's pre-installed-Chromium fallback) — it
initially failed on all 4 `promotions` screenshots with a blank `dir`
attribute (the same silent-403 symptom ut-docs#326/#516 already diagnosed
for `users`/`translations`/`kitchen-stations`): `GET /promotions` uses the
same `requireManager` closure with no `UT_AUTH=off` bypass, so the default
auth-off docs-shots till 403s it. Fixed the actual cause — added
`"promotions"` to `docs-shots.spec.ts`'s `AUTH_TILL_TOPICS` list, the same
fix already applied for the three prior topics in this exact situation, not
a workaround. Re-ran; all 72 screenshots (18 topics × 4 locales) passed,
including 4 new `promotions` ones — reviewed visually (en list+form, ar RTL
mirroring correctly).

The re-run also regenerated 5 unrelated topics' PNGs (`alerts`, `designer`,
`kitchen-stations`, `translations`, `users`) with pixel-only diffs — no
underlying markdown or app-surface change explains them (confirmed via
`manifest.json`: their topic hashes are unchanged, only `surface_sha256` and
the new `promotions` entry differ), consistent with the reused-browser
rendering variance ut-docs#632 already tracks. Reverted those 5 PNGs to
their committed originals so this PR's diff stays scoped to what it
actually changed.

## Verdict

**Ready to open a PR**, with the five review fixes applied plus the
docs-shots harness fix above. Single repo, no ADR needed — this adds a
management surface over an existing table using the
`locations_page.go`/`users_page.go` shape the codebase already established;
it introduces no new architectural decision, and deliberately does *not*
introduce `money.Money` at rest for `promotions.value`, which would have
contradicted the schema's own documented dual-meaning (minor units *or*
basis points) convention.
