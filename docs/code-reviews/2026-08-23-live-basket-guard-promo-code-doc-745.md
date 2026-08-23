# Code review — document why the live-basket demo guard skips promo codes

**Date:** 2026-08-23
**Card:** universaltill/ut-docs#745 (p3, `complexity:easy`)
**Trigger:** follow-up from the independent review of ut-docs#633's fix.
**Branch:** `fix/live-basket-promo-doc-745`
**Dev:** inline (Sonnet, autonomous SDLC pipeline)
**Reviewer:** independent subagent, Sonnet (easy-tier, fresh context), isolated worktree

## What shipped

Pure documentation, no behavior change: a comment on
`demoDataInLiveBasket` (`internal/pages/settings_page.go`) explaining why
the function checks basket lines (demo items/variants) and the basket
customer, but deliberately does **not** check an applied demo promo code
(PROMO50/PROMO500/DISC10).

The reasoning, now written down next to the guard instead of only living
in `internal/data/seeddata/remove_demo_customers_promos.sql`'s header:
`sale_discounts` (`internal/db/migrations/001_init.sql`) records only the
resulting discount `amount` for a redeemed promo, not which code produced
it — so removing a demo promotion mid-basket can't leave a dangling
reference the way removing a demo item/customer could. If promo-code
redemption ever becomes durable (there's no promotions management UI yet),
this stops being true silently, and the comment says so — pointing a
future change back at this function.

## Verification

- `gofmt -l internal/pages/settings_page.go` — clean.
- `go build ./...`, `go vet ./internal/pages/...` — clean.
- `go test ./...` (full suite, every package) — green.
- `bash scripts/ci/guard-data-access.sh` — passes (the comment mentions
  `sale_discounts`/`001_init.sql` as prose, not query text; no SQL added).
- No SQL, money, user-facing string, i18n, plugin code, file write, or
  page/route touched, so no other guard and no `web/help/` manual update
  applies.
- No real client/shop name; no secret-shaped literal (only the existing
  public demo promo codes and an issue number are referenced).

## Independent review (Sonnet, fresh context, isolated worktree) — 0 blockers, 1 non-blocking nitpick

Re-derived the diff itself (`git diff origin/main..HEAD`) rather than
trusting the description, and independently checked the comment's factual
claim against the schema and the SQL script it cites:

- Confirmed `sale_discounts`'s columns (`id, sale_id, line_id, type,
  value, amount, reason`) have no column or FK referencing a promo code,
  and searched every migration for any such reference — none exists.
- Confirmed `remove_demo_customers_promos.sql`'s own header states the
  identical rationale, so the new comment is reusing an established,
  correct justification rather than inventing one.
- Re-ran build/vet/format, the six `TestSettingsRemoveDemoCatalogueEndpoint*`
  tests (all pass unchanged — two log a pre-existing, unrelated
  `audit_log` FK-constraint warning that was already there before this
  change), `guard-data-access.sh`, plus `guard-i18n.sh`,
  `guard-compliance-claims.sh`, and `guard-kiosk-engine.sh` for extra
  scrutiny — all pass, as expected for a comment-only diff.
- One non-blocking nitpick on phrasing ("there's nothing in this basket
  for the removed row to break" is slightly compressed) — reads correctly
  in context, not worth a revision.

**Verdict: safe to merge.**

## Deferred

Nothing deferred — this card's scope was documentation only.
