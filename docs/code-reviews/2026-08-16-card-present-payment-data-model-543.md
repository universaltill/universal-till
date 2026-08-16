# Review: Card-present payment reconciliation data model (ut-docs#543)

**Card:** universaltill/ut-docs#543 — "Card-present payment reconciliation
data model (masked PAN, trace/terminal IDs) on PaymentInput + receipt
rendering"
**Complexity:** medium. **Build:** Sonnet (inline). **Review:** Opus
(fresh-context subagent, independent of the build, isolated worktree).

## What was asked

Add optional, provider-agnostic card-present reconciliation fields —
masked PAN (scheme + last 4 digits only, never a full PAN), auth/approval
code, terminal ID, trace ID — to the payment data path: `pos.PaymentInput`
→ `data.InsertPayment` → `payments` table (new append-only migration) →
`SaleDetailPayment` (readback) → receipt rendering
(`web/ui/partials/receipt.html`). No live producer exists yet (the actual
card-terminal/ZVT integration is ut-docs#515, explicitly out of scope) —
this ships the data model, persistence and rendering plumbing only, with
zero behavior change for every existing payment method (cash, Stripe,
SumUp, QR-pay, demo).

## What shipped

- `internal/db/migrations/050_payment_card_present_fields.sql` — four
  nullable columns on `payments`.
- `internal/db/migrations/051_payment_archive_card_present_fields.sql` —
  the same four columns mirrored onto `payments_archive`, per
  `040_reset_archive.sql`'s own documented convention that every later
  `ALTER TABLE payments` must be mirrored there.
- `pos.PaymentInput`, `data.CardPresentFields`, `data.SaleDetailPayment`,
  `plugins.SalePayment` all carry the four fields (`MaskedPAN`,
  `AuthCode`, `TerminalID`, `TraceID`), snake_case JSON tags throughout.
- `pos.validateMaskedPAN` rejects anything that looks like an unmasked
  PAN at the `CompleteSale` persistence boundary (masking is the caller's
  responsibility; this is the defensive backstop, not the source of
  truth).
- `receiptPayment`/`receipt.html` shows the masked-PAN + auth-code line
  when present, falling back to today's generic `Reference` line
  otherwise.
- `resetArchiveTables`'s `payments` column list extended so the reset/
  restore archive mechanism carries the fields through instead of
  silently dropping them.
- `receipt.auth_code` added to every core locale (`en`/`ar`/`fa`/`tr`).

## TDD

Every production change was test-first: regression test written,
confirmed failing against the pre-fix code with the actual error, then
the fix applied and the test confirmed passing. This includes the two
follow-up fixes from independent review (below) — each has its own
red→green pair.

## Independent review (Opus, isolated worktree)

Full findings list, commands run, and the reviewer's own TDD
re-verification trail are preserved in the PR. Summary of what was
triaged:

**Fixed in this branch (all three "fix first" items from the verdict):**

1. **LAN-sync journal replay silently dropped all four fields**
   (`internal/pages/sync_sales.go`) — a card-present payment taken on a
   replica till arrived at the primary/back-office till with all four
   columns NULL, exactly the drift class this diff otherwise caught for
   `payments_archive`, missed one hop over. Fixed: the four fields now
   flow through `applyJournal`'s `PaymentInput` construction. Regression
   test `TestApplyJournal_CarriesCardPresentFields` confirmed failing
   (`converting NULL to string` scan error) before the fix, passing
   after.
2. **`validateMaskedPAN` used a bare ASCII digit range**, so a full PAN
   written in Arabic-Indic or fullwidth digits — a real input shape given
   this product ships `fa`/`ar` locales — bypassed the guard entirely.
   Fixed: `unicode.IsDigit` instead of `'0'-'9'`. Regression test
   `TestCompleteSale_RejectsUnmaskedPAN_NonASCIIDigits` confirmed both
   digit systems were accepted before the fix, rejected after.
3. **`lang-pack-drift` CI is blocking on push to `main`**, and the new
   `receipt.auth_code` core key would have gone unmatched in the external
   `ut-plugin-language-{de,es}` packs, turning `main` red on merge.
   Sequenced instead: opened universaltill/ut-plugin-language-de#47 (real
   German translation, "Autorisierung" — that pack already translates the
   rest of the `receipt.payments` cluster) and
   universaltill/ut-plugin-language-es#44 (added to the ratcheting
   `i18n-baseline/es.untranslated.txt` as an accepted gap — that pack
   already leaves the rest of the same cluster untranslated via the same
   mechanism, so one newly-translated key in an otherwise-English section
   would have been inconsistent, not an improvement). Both packs' own
   `check-key-drift.sh` verified locally against this branch's `en.json`:
   0 drift on each. Both pack PRs currently show an *expected* CI failure
   (their CI fetches core's *live* `main`, which doesn't have the new key
   yet) — documented on each PR, to be merged immediately after this PR
   lands on `main`.

**Also fixed (small, directly overlapping with what this diff added, low
risk — confirmed no JSON-decode path targets `PaymentInput`):**

4. `pos.PaymentInput` had no JSON tags at all (pre-existing drift this
   diff would have widened by adding four more untagged fields) — its
   fields serialised as bare Go PascalCase in the `/api/pos/tender` JSON
   response, against `CLAUDE.md`'s snake_case rule and inconsistent with
   the identical wire vocabulary `data.SaleDetailPayment` already uses
   correctly. Added full snake_case tags to the struct. New test
   `TestTenderHandler_JSONResponsePaymentsAreSnakeCase` asserts the wire
   shape directly (confirmed failing pre-fix, passing after).

**Filed as follow-up Backlog cards (real, but out of this card's scope):**

- ut-docs#791 — which plugins should be allowed to see card-present data
  over the `sale.completed` event bus (ADR-0006 trust-chain question; zero
  exposure today, no live producer).
- ut-docs#792 — surface masked PAN/auth code on the Journal detail view
  for back-office reconciliation after the fact, not just on the
  once-printed receipt.

**Accepted as-is (reviewer's own read, not disputed):**

- The 4-digit total-count cap on `validateMaskedPAN` is stricter than
  ADR-0016's description of what a certified reader may expose (BIN +
  last 4 = up to 12 real digits) — ships this way deliberately, matching
  this card's own scope ("last 4 digits only"); revisit if #515's actual
  terminal output needs the wider form.
- Printed ESC/POS receipt (`internal/pages/print_api.go`) doesn't carry
  the fields — the ticket scoped rendering to the HTML receipt partial
  specifically.
- Several nitpicks (a no-op `t.Helper()` at test-function top level, an
  unfilled `ut-docs#...` placeholder in a comment) — fixed inline, no
  test impact.

**Verdict:** yes, with the three fixes above (all now landed) — no
blockers remain.

## Verified beyond automated tests

- Visual check: rendered the receipt partial standalone with real card-
  present data (masked PAN + auth code + tip, alongside a plain-cash
  payment with no card-present fields) and screenshotted it in both LTR
  and RTL (`dir=rtl`) via headless Chromium — new line renders cleanly
  under the payment method, correctly mirrors in RTL, no overlap/cutoff,
  and the fallback-to-`Reference` path for the cash line is unaffected.
- Ran the real Playwright e2e suite (`sale.spec.ts`, `rtl.spec.ts`,
  `tender-panel-reachable.spec.ts`) against a live built binary — all
  pass, confirming no regression to the existing checkout/receipt flow.
- Full `go test ./...` (38 packages, 0 failures) and all six repo guard
  scripts (`guard-data-access.sh`, `guard-i18n.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-help-topics.sh`, `guard-compliance-claims.sh`) green.
- Migration numbering (050/051) confirmed free before use; both are new
  files only — no existing migration edited (append-only rule respected).

## Help/manual coverage

Not updated — this change has no user-visible effect today. No existing
payment method populates any of the four new fields (no live producer
exists; #515 is out of scope), so a shop owner's receipt is byte-for-byte
unchanged from before this change. Revisit when #515 (or any card-present
integration) actually ships and starts populating these fields — that
follow-up card should include the manual update.

## Safe to merge

Yes.
