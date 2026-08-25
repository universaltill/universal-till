# Code review: refund/return path bypassed the German TSE hard gate (ut-docs#731)

**Branch:** `feat/731-refund-tse-gate` · **PR:** universal-till#TBD
**Reviewer:** independent Opus subagent (complexity:medium → Opus review, per
scrum-master's model routing), isolated worktree · **Author:** Sonnet (this
pipeline cycle)

## What shipped

ADR-0048's German TSE hard gate (ut-docs#715) was wired into
`completeTender` — the shared cashier/kiosk **sale** path — only. Two call
sites were found calling `pos.CompleteSale` directly, bypassing it entirely:

1. `internal/pages/refund_page.go`'s `POST /api/refund` handler (the till's
   own refund screen).
2. `internal/pages/inventory_api.go`'s `CreateReturn`
   (`POST /api/inventory/return`, `web/ui/pages/inventory.html`'s return
   form).

A refund/return moves real money and is `aufzeichnungspflichtig` under
KassenSichV the same as a sale — the product owner decided 2026-08-18 (on
the issue) that it must be gated identically. Only one call site
(`refund_page.go`, described loosely as "CreateReturn") was named in the
ticket; scoping the fix found the second, real gap
(`inventory_api.go`'s actual `CreateReturn`) and fixed both.

Fix: extracted a shared `enforceFiscalGate` helper in `pos_api.go` (a pure
move of `completeTender`'s existing gate-check block — verified
behaviour-preserving), called from both new call sites before their own
`pos.CompleteSale`, ahead of every state-changing side effect (including
`refund_page.go`'s payment-provider refund webhook, which can itself move
real money at the provider). Both sentinel block errors map to a
localized `409`; a same-shaped `unsigned_override` audit marker is written
on `AllowedWithOverride`, attached to the new return's own sale row.

i18n: two new keys in all 4 locales (`refund.error.fiscal_never_configured`,
`refund.error.fiscal_tse_failing`), reused by both call sites. `sell.md`'s
existing "German shops: TSE and real sales" section (all 4 locales) now
says refunds are covered too; `make docs-shots` regenerated the manifest
(text-only change, no screen changed). `fiscal.banner.override_active`
(all 4 locales) updated to say "sales and refunds" — it's a system-wide
override-state banner, not scoped to one screen, so this is accurate
regardless of which screen currently shows it.

New tests: `internal/pages/refund_fiscal_gate_test.go`,
`internal/pages/inventory_return_fiscal_gate_test.go` — no-TSE-configured
block, shadow-mode exemption, non-German regression, TSE-failing-without-
override block, admin-override-unblocks-and-audits, and a translated-
refusal test for each surface (mirroring `fiscal_gate_test.go`'s own
`TestFiscalGate_RefusalIsTranslated`).

## Independent review — verdict on first pass: NOT safe to merge; fixed, now safe

Full independent pass (different model, fresh context, isolated worktree).
Verified correct and unchanged: gate ordering (checked before every
money-moving side effect on both surfaces), the `enforceFiscalGate`
extraction (pure move, line-by-line), the audit marker (right row id,
right payload, matches what `print_api.go`'s outage-notice lookup keys
on), no other bypassing call site (full sweep of `pos.CompleteSale`),
kiosk isolation, money type, repository pattern, `{data,error}` envelope,
ADR-0040 compliance wording, and the `sell.md` accuracy across all 4
locales.

**Blocker found and fixed — B1**: `inventory_api.go`'s original fix used
`respondReturnError(w, r, 409, err.Error())` — the sentinel's raw,
un-localized `Error()` string ("fiscal gate: shop is system of record but
no TSE is configured") rendered verbatim into the `#return-result` slot of
an otherwise fully-translated page, confirmed via the real render path
(`writeHTML` → `text/html` → app.js's `htmx:beforeSwap` force-swap on
non-2xx). Fixed: routes through the same locale keys `refund_page.go`
already ships, via `httpx.T`.

**Should-fix, found and fixed — S1/S2**: `enforceFiscalGate` can also
return a wrapped *settings-store read* error (from `fiscal.EvaluateGate`),
not just the two block sentinels — both original fixes defaulted an
unrecognized error to "no TSE configured", telling an operator to buy a
TSE they may already have, on what's actually a DB fault (also wrongly
statused 409 instead of 500, keeping a real fault out of the Problems
ring). Fixed on both surfaces: a three-way `switch` on `errors.As`
(`fiscalTF` → 409 tse_failing, `fiscalNC` → 409 never_configured, default
→ 500 `refund.error.server`), matching the pattern the existing sale-path
call sites (`pos_api.go`, `self_order_shop.go`) already use.

**Should-fix, found and fixed — S3/S4**: the original tests asserted only
`strings.Contains(body, "TSE")` / status-code-only — both satisfied
identically by the raw un-localized sentinel text, so neither would have
caught B1. Fixed: message assertions now check the localized copy
specifically (distinguishing phrases not present in the raw `Error()`
strings), plus a `TestFiscalGate_Refund{,CreateReturn}RefusalIsTranslated`
on each surface mirroring the sale path's own translated-refusal test.
**Re-verified myself** (not taken on trust): reverted `inventory_api.go`'s
fix back to raw `err.Error()`, confirmed both strengthened tests now fail
and show the exact leaked string, restored, confirmed green again.

**Fixed — S5**: `internal/fiscal/fiscal.go`'s package doc still said
enforcement "lives at the single shared tender path
(internal/pages.completeTender)" — now names all three call sites and
records why `sync_sales.go`'s replica replay is deliberately excluded
(already gated where it was originally rung).

**Noted, not changed — N3/N4**: `log.Printf` instead of `internal/logging`
in the two handlers is a faithful mirror of `pos_api.go`'s own existing
convention at this call site, not a new inconsistency. Gating at the end
of `CreateReturn`'s validation (vs. first-thing in `completeTender`/refund)
has no correctness impact — everything above it is a read.

## Commands run (this checkout, post-fix)

- `gofmt -l .` — clean.
- `go vet ./...` — clean.
- `go build ./...` — clean.
- `go test ./internal/pages/...` — pass (71s), includes all 19
  `TestFiscalGate_*` tests (17 existing/new + 2 translated-refusal).
- `go test ./internal/pages/... -race -run 'TestFiscalGate'` — pass
  (8.5s), all 19 tests race-clean.
- Full-package `go test ./internal/pages/... -race` (no `-run` filter):
  the review's own independent run confirms **pass in 1057.6s** in its
  isolated worktree. Independently in this checkout it timed out twice at
  900s on two different, unrelated pre-existing tests (`internal/plugins`
  WASM sandbox tests; `sync_plugins_test.go`'s
  `TestSyncPullTick_PinsPrimaryVersionNotMarketplaceLatest`) — consistent
  with this container's shared-load being slower than the review's
  isolated worktree, not a regression: `internal/plugins` alone passes in
  85s without `-race`, and both timeouts landed on code this change never
  touches. CI itself never runs `-race` (`.github/workflows/ci.yml`), so
  this is a container-speed observation, not a CI-relevant finding.
- `bash scripts/ci/guard-i18n.sh` — pass (1216 keys, all locales match).
- `bash scripts/ci/guard-data-access.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-makefile-version.sh`,
  `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
  `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh` — all pass.

## TDD re-verification (both the reviewer's, and mine on the fixes)

Reviewer, pre-fix (both surfaces): replaced the handler's gate call with a
no-op `Gate{}`, re-ran — 3 of 5 tests failed on each surface with the
`CreateReturn`/refund succeeding when it should have been blocked (200 +
`success:true` instead of 409); shadow-mode and non-German regression pins
correctly kept passing. Restored, all pass again, `git diff` empty.

Mine, on the S3/S4 fix specifically: reverted `inventory_api.go`'s
localized-error mapping back to the pre-review-fix raw `err.Error()`,
re-ran `TestFiscalGate_CreateReturnBlockedWhenTSENeverConfigured` and
`TestFiscalGate_CreateReturnRefusalIsTranslated` — both failed, showing
the exact raw sentinel string
(`{"data":null,"error":"fiscal gate: shop is system of record but no TSE is configured"}`)
in the assertion output. Restored via the saved copy, `go build`/full
`TestFiscalGate` suite green again.

## Deferred — new Backlog cards filed, not fixed here (real, out of scope)

- **[ut-docs#998](https://github.com/universaltill/ut-docs/issues/998)**
  (Admin Review — needs a product/compliance decision) — `RecordCashAdjustment`
  (`internal/pages/shifts_api.go`) moves real cash out of the drawer on a
  negative amount but isn't a `pos.CompleteSale` call, so #731's sweep
  never reached it. Same "moves real money → gate it" question,
  unresolved. Highest-value follow-up.
- **[ut-docs#999](https://github.com/universaltill/ut-docs/issues/999)**
  (Admin Review — needs a product/compliance decision) — neither refund
  path dispatches `fiscal.sign.ask` (ADR-0044) the way `completeTender`
  does; #731 gates refunds but leaves them permanently unsigned and
  undeclared. Natural sequel to this ticket.
- **[ut-docs#1000](https://github.com/universaltill/ut-docs/issues/1000)**
  (Backlog, security, complexity:easy) — `respondReturnError`
  (`inventory_api.go`) HTML-interpolates its message (including
  caller-supplied `line_id`) with no escaping — a pre-existing
  reflected-XSS-shaped gap, not introduced by this change, found while
  reading the surrounding code.
- **[ut-docs#1001](https://github.com/universaltill/ut-docs/issues/1001)**
  (Backlog, complexity:easy) — the sale screen shows a banner during an
  active TSE override (`TestFiscalGate_SaleScreenBannerDuringOverride`);
  `/refund` doesn't, so a cashier refunding under an active override gets
  no warning until they submit. `sell.md`'s own wording ("a banner stays
  on the sale screen") isn't wrong, but the refund surface is now gated
  and unbannered.

## Safe-to-merge verdict

**Yes**, after the fixes above (B1/S1/S2/S3/S4/S5 all resolved and
re-verified). No remaining blockers or should-fix items.
