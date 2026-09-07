# Code review: Journal "unknown" tender-type i18n (ut-docs#1579)

**Date:** 2026-09-06
**Card:** ut-docs#1579
**Complexity:** easy — build: inline (Sonnet), review: fresh-context Sonnet
subagent, isolated worktree, read-only. One round; no blocker-class finding,
so no second round.

## What shipped

Independent-review follow-up from ut-docs#1561 (finding F1): ut-docs#1561
lets a zero-marginal-net partial refund persist with no payment rows at
all. `internal/pos/sales.go`'s `deriveTenderType`'s
`len(payments) == 0 -> "unknown"` branch — dead code before ut-docs#1561,
since `netPayments` used to reject every empty-payments call outright — is
now reachable in production, and the resulting `sales.tender_type =
'unknown'` was rendered **raw** by both journal templates
(`web/ui/partials/journal.html:44`, `web/ui/pages/journal_detail.html:52`).
An operator landing on `/journal/<receipt>` for one of these returns saw
the literal English word "unknown", untranslated in every locale including
RTL ones (fa/ar) — a presentation-only gap (the underlying sale/return
itself is correct), but a real one, and it recurs every time this
zero-marginal-net path is hit.

- `internal/httpx/httpx.go`: new locale-bound template function
  `tenderLabel`, registered in `FuncsFor(locale)` alongside `T`. Maps the
  two sentinel values `deriveTenderType` can itself produce — `"unknown"`
  (no payments) and `"split"` (2+ distinct payment methods) — to new
  locale keys `journal.tender.unknown` / `journal.tender.split`. Any other
  value is an open-ended, plugin-defined payment `MethodID` (cash, card,
  voucher, sumup, ...) and is returned unchanged, exactly as before this
  card — deliberately NOT translating `"cash"`/`"card"` too, since those
  aren't a closed enum this till owns the vocabulary for.
- `web/ui/partials/journal.html` / `web/ui/pages/journal_detail.html`:
  `{{ .TenderType }}` / `{{ .Sale.TenderType }}` → `{{ tenderLabel
  .TenderType }}` / `{{ tenderLabel .Sale.TenderType }}`. No other markup
  changed.
- `web/locales/{en,ar,fa,tr}.json`: new keys `journal.tender.unknown` /
  `journal.tender.split`, all four locales.
- `web/help/img/en/sell.png`, `web/help/img/manifest.json`: regenerated via
  `make docs-shots` (required by `guard-docs-shots.sh` since `web/ui/**`
  changed). The `sell.png` diff is unrelated pixel noise from a Chromium
  version mismatch the script itself documents as non-fatal (reused
  headless_shell 141 vs. the pinned 149) — no journal/tender content is
  visible in that screenshot.

## Tests

- `internal/pages/refund_page_test.go`:
  `TestPostRefund_ZeroMarginalNetReturnShowsTranslatedTenderType` — same
  repro shape as `TestPostRefund_ZeroMarginalNetPartialRefundSucceeds`
  (3×100 gross, 299 discount, refunded 1 unit at a time): the second
  `POST /api/refund` request lands its own marginal net at exactly 0,
  producing a genuine `sale_type='return'`, `tender_type='unknown'` row.
  The test then `GET /journal/<receipt>?lang=fa` and asserts the response
  does NOT contain the raw `>unknown<` and DOES contain the fa translation
  ("ناشناس").

**TDD verified twice, independently**: the implementer reverted just the
fix files (`internal/httpx/httpx.go`, both templates, all four locale
files — leaving the new test in place) and confirmed the new test fails,
then restored and confirmed it passes. The reviewing Sonnet subagent, in
its own isolated worktree, independently repeated the same
revert → fail → restore → pass sequence against the real diff and got the
same result.

## Independent review (fresh-context Sonnet, isolated worktree)

Ran `gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
`go test ./internal/httpx/... ./internal/pages/... ./internal/ui/...
./internal/pos/...` (all green), `golangci-lint run ./internal/httpx/...
./internal/pages/...` (0 issues), `bash scripts/ci/guard-i18n.sh` (locales
match), `bash scripts/ci/guard-docs-shots.sh` (fresh),
`bash scripts/ci/guard-help-topics.sh` (clean).

Confirmed every render path for both templates builds its `FuncMap` via
`httpx.FuncsFor(...)` (directly, or via `httpx.Render`/`RenderPartial`,
which call it internally) — `internal/pages/journal_page.go` (both
routes) and `internal/pages/pos_api.go` (all call sites) — so no path can
panic from a missing `tenderLabel` function at template-parse time.

Checked the manual (`web/help/en/{sell,payments,reports}.md`): none
documents the journal's tender-type column values or an
"Unknown"/"Split" label there. Judgment: no manual update warranted — this
is a display-correctness fix on an existing, already-covered page/column,
not a new screen, field, or workflow.

**One real, non-blocking finding (filed as ut-docs#1617):**
`internal/plugins/manifest.go`'s `validatePaymentEntryKeys` doesn't reserve
the literal keys `"unknown"`/`"split"` — only the built-in `cash`/`card`/
`gift` are occupied. A plugin *could* legally register a payment method
keyed `"unknown"` or `"split"`; if used as a sale's sole tender,
`deriveTenderType` would return that literal string (genuinely the
plugin's own method, not the derived sentinel state), and `tenderLabel`
would now mistranslate it. Narrow and currently hypothetical — no
first-party plugin uses either name today — so this card was not blocked
on it; filed as a follow-up to reserve both keys in
`validatePaymentEntryKeys`.

Also noted: the `tenderLabel` doc comment describes the `"unknown"`
sentinel as "no payments at all" but `deriveTenderType` can also reach it
via a payment with an empty/whitespace-only `MethodID`. Cosmetic —
`tenderLabel` translates correctly either way — not fixed as out of scope
for this comment-accuracy nit.

## Verdict

**Safe to merge.** No blocker-class finding. One low-severity edge case
tracked as ut-docs#1617, deliberately not fixed here (out of this card's
scope — reserving plugin payment-method keys is manifest-validation
hardening, not a journal-display bug).
