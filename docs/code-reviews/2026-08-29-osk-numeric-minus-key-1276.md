# Code review — osk.js's numeric keyboard gets a minus key for signed fields (ut-docs#1276)

- **Date:** 2026-08-29
- **Branch:** `fix/1276-osk-numeric-minus-key`
- **Reviewer:** independent reviewer (fresh-context subagent, different
  model — Opus, this pipeline's `complexity:medium` review tier),
  isolated worktree
- **Verdict: PASS — safe to merge.** No blocking findings; five
  non-blocking findings, four fixed in this branch after review, one
  (a UX/muscle-memory nit) explicitly left as the reviewer's own call, not
  the reviewer's to make.

## What shipped

`web/public/osk.js`'s numeric (`num`) on-screen-keyboard layer was digits
+ `.` + `⌫` + `↵` only — no `-` key, and no way to expose one — so a field
that legitimately accepts a negative amount could never have one typed on
a real touch till (kiosk Pis have no physical keyboard). Found by
independent review of ut-docs#1272's fix; the exact gap ut-docs#1272's own
review record filed as its finding 5.

**Fix:**
- A second numeric layout, `numSigned` (same digits/`.`/`⌫` rows as `num`,
  now hoisted into a shared `NUM_ROWS` array so the two can't drift, plus
  a literal `-` key — no `press()` change needed, it falls through to the
  same `default: insert()` path every digit/`.` key already uses).
- `isSigned(el)`: gates on the field's own HTML `pattern` already starting
  with `-?` (optionally anchored, `^-?`) — the exact convention
  `shifts.html`'s payout/adjustment amount and `inventory.html`'s stock
  quantity/override fields already use for "this amount can be negative"
  (`pattern="-?[0-9]+(\.[0-9]{1,2})?"`), rather than a second opt-in
  attribute to remember. Excludes `type="number"` (pattern is inert
  there, so that combination is never legitimate — see finding 2).
- `show()`'s layer selection picks `numSigned` over `num` when
  `isNumeric(el) && isSigned(el)`.
- Audit of every field this could apply to (`git grep 'pattern="-?' --
  web/ui/`): exactly four — `shifts.html`'s `#adjust-pounds`,
  `inventory.html`'s `quantity`/`qty_before`/`qty_requested`. Checked and
  ruled out as needing it: `#skim-pounds`, `#pfand-amount` (both entered
  positive, negated server-side), the basket discount field (never
  negative, documented in `web/help/en/sell.md`).

New e2e spec `e2e/tests/osk-signed-minus-key-1276.spec.ts`: a gating test
(no signed pattern → no `-` key rendered; signed pattern → `-` key
visible), and an end-to-end test typing `-25.00` via the real OSK into
`#adjust-pounds` with `type=payout` and submitting it. Two pre-existing
specs updated off workarounds this fix removes the need for:
`osk-decimal-admin-fields-1275.spec.ts`'s inventory-quantity test now
types the negative value through the real OSK instead of `page.fill()`;
`shifts-tips-osk-1272.spec.ts`'s stale "no `-` key yet" comment removed.
`web/help/en|fa|ar|tr/inventory.md` and `reports.md` each get one sentence
on how to enter the negative amount on a touch till (standing "manual
ships with the feature" rule) — `web/help/img/**` + `manifest.json`
regenerated via `make docs-shots` (required since `web/public/**`
changed; confirmed pixel-diffs are encoder noise, not real content
change).

## Verification performed

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | empty / pass / pass |
| `go test ./...` (full suite) | pass |
| `guard-i18n.sh` / `guard-help-topics.sh` / `guard-compliance-claims.sh` / `guard-docs-shots.sh` / `guard-osk-loaded.sh` | all pass |
| New spec + full OSK/shifts regression subset (`osk-signed-minus-key-1276`, `osk-decimal-admin-fields-1275`, `shifts-tips-osk-1272`, `osk-central-guard`, `settings-osk`, `deposit-refund-payout-osk-1249`) | 41/42 pass; the one failure (`shifts-tips-osk-1272.spec.ts`'s `setOskMode` navigation race) is the **already-tracked** flake ut-docs#1288, reproduced as flaky (not consistently failing) across repeated runs both with and without this branch's changes — not caused by this diff |

### TDD claim, verified twice (Dev, then independently by Reviewer)

Both verified the same way: revert only `web/public/osk.js` to `main`,
re-run the new spec, confirm the minus-key-dependent tests fail red for
exactly the diagnosed reason, restore, confirm green again.

- **Dev:** `osk-signed-minus-key-1276.spec.ts`'s two minus-key tests and
  `osk-decimal-admin-fields-1275.spec.ts`'s converted quantity test failed
  (`toBeVisible()` timeout waiting on `#osk button[data-k="-"]`, which
  never renders on unfixed code); the gating "no signed pattern" test
  correctly still passed (no minus key exists anywhere pre-fix, so "no
  minus key" trivially holds). Restored → all green.
- **Reviewer (independent):** same revert via `git checkout <main-sha> --
  web/public/osk.js`, confirmed via `grep -c "numSigned\|isSigned"` → `0`;
  reran the same two spec files → 3 failed / 11 passed, the three being
  exactly the minus-key ones (with the actual timeout/assertion errors
  quoted in the review). Restored → 14/14 passed.

## Findings and disposition

1. **Non-blocking, fixed — `isSigned()` recognized only one spelling of
   "signed."** `pattern.indexOf('-?') === 0` missed the equally legal,
   equally common `pattern="^-?[0-9]…"` (anchored) form — a future signed
   field written that way would silently get no minus key, the exact
   failure mode this card exists to close, re-armed for the next field.
   **Fixed:** `/^\^?-\?/.test(pattern)` (`osk.js`'s `isSigned()`), with a
   comment recording why. All four current fields still match.
2. **Non-blocking, fixed — latent false positive on `type="number"`.**
   `isSigned()` never checked the input type; a `type="number"` field
   carrying `pattern="-?…"` (pattern is inert on `type="number"`, so an
   author could plausibly leave a stale one) would get the minus key, and
   `insert()`'s number/email branch would then corrupt the entry —
   reviewer measured `-25.00` producing `"00"` live, the exact
   ut-docs#1249/#1275 defect class, newly reachable. No such field exists
   today. **Fixed:** `isSigned()` now also checks `el.type !== 'number'`.
3. **Non-blocking, fixed — `numSigned` duplicated `num`'s rows with
   nothing keeping them in sync**, so a future change to the shared keys
   (e.g. a decimal-comma key for de/es, ut-docs#1047) could silently leave
   signed fields on a stale keypad. **Fixed:** hoisted the four shared
   rows into `NUM_ROWS`, both layouts now `.concat()` their own last row
   onto it.
4. **Non-blocking, fixed — the manual got regenerated screenshots but no
   prose.** This change makes a previously-impossible operator action
   possible on kiosk hardware (a negative payout, a negative stock
   adjustment for waste/breakage) with no physical keyboard to fall back
   on; `inventory.md`/`reports.md` described the *feature* (adjustments,
   payouts) without ever saying how to type the sign. **Fixed:** one
   sentence added to each, in all four shipped locales (en/fa/ar/tr),
   matching each locale's existing "on-screen keyboard" terminology.
5. **Non-blocking, fixed — the new payout e2e test left state on the
   shared server.** `ensureShiftOpen()` only opens; the original test
   never closed the shift it opened + adjusted, unlike its neighbors'
   convention (`shifts-tips-osk-1272.spec.ts`'s own `ensureNoShiftOpen`).
   Harmless today only because of test file run order. **Fixed:** added
   the same `ensureNoShiftOpen()` teardown, called at the end of the
   payout test.
6. **Nit, accepted as-is — `numSigned`'s `↵` sits next to `-` instead of
   alone-and-centered like `num`'s does**, a small muscle-memory
   inconsistency between the two keypad variants. Reviewer's own
   assessment: real but minor, and a UX layout call outside this fix's
   scope — left for a future pass, not filed as a separate card (too
   small to be worth one).

## Checked and found clean

- **Malformed minus input is safe** — reviewer measured `--25`, `25-`,
  `2-5`, and a lone `-`: each leaves the field pattern-invalid, the hidden
  minor-units field at `0`, and native constraint validation blocks the
  submit entirely (no request reaches the server). Identical to how the
  widget already handles any other malformed input (e.g. two `.`s);
  nothing new or worse.
- **The gate is a real gate** — `#stock-cost` (positive-only pattern)
  renders zero `-` keys; `num` itself is byte-for-byte unchanged via the
  `NUM_ROWS` refactor, so no other numeric field in the app gained a key.
- **No i18n change** — `-` is a raw glyph in a JS layout table, same as
  the existing digits/`.`; no locale key added; `guard-i18n.sh` green
  (`web/public/**` is outside that guard's scope regardless — a
  pre-existing, documented gap this change neither uses nor widens).
- **Pure front-end JS** — no SQL, no `internal/` change, no file writes;
  data-access/kiosk-engine guards trivially satisfied; the two recurring
  guard classes (missing `os.MkdirAll`, a cwd-relative path where
  `paths.Data(...)` belongs) don't apply.
- **Server-side gating for the newly-reachable direction is intact** —
  `RecordCashAdjustment` (`internal/pages/shifts_api.go`) already rejects
  a positive `payout`/`skim`, and requires manager-PIN approval for *any*
  negative amount (sign-gated, not type-gated) — unchanged by this diff,
  and the new key cannot produce an ungated cash-out.
- No real shop names or secret-shaped literals in the new spec or docs.
- `guard-docs-shots.sh` green with the regenerated surface hash;
  `guard-help-topics.sh` green.

## Follow-up cards filed

None — the audit this card's own acceptance criteria called for
(`git grep 'pattern="-?'`) found exactly the four fields already fixed,
with no further gap left open.
