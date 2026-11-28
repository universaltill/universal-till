# Service-shops batch: tip at checkout (ut-docs#7107), with pointers to the batch's other three parts

Combined Tester + Reviewer, 2026-11-27 (Fable; all code and docs by Sonnet).
One reviewer for the whole batch, never the Dev agent. This file is the batch
record and the full record of the tip change. The other parts have their own
record where they land:

| Part | Where | Record |
|---|---|---|
| N1 tip at checkout (#7107) | this branch, `feat/service-tip-entry-checkout` | this file |
| Plugin-owned SQLite, slice A (#3157) | `feat/3157-plugin-sqlite-slice-a` | `2026-11-27-plugin-sqlite-slice-a-3157.md` (that branch) |
| A1 scheduling engine (#7112) | new local repo `ut-plugin-page-appointments` (no remote yet) | `docs/code-reviews/2026-11-27-a1-scheduling-engine.md` there |
| ADR-0221 (Proposed), contracts, walkthrough | ut-docs `docs/service-shops-adr-0221-appointments` | ut-docs `code-reviews/2026-11-27-service-shops-adr-0221.md` |

Findings of the other parts in one line each: plugin SQLite — a migration
PRAGMA could lift the storage quota (high, fixed), `BEGIN` through `Exec`
lost later writes (medium, fixed), TEMP tables were unbounded (medium,
fixed); engine — a negative buffer booked over an existing appointment
(medium, fixed), `completed` and `no_show` free the resource (confirmed,
pinned by a test, contract text corrected); docs — no accepted ADR
contradicted.

## What shipped (tip)

A tip row above the method buttons of the payment panel: **No tip**
(pre-selected), the shop's preset percentages each showing the amount it
adds, and **Other** for a typed amount; a **Tip amount** field per split
leg. The tip is stored in `payments.tip_amount`, the payment `amount`
includes it (core's one convention, #2571), recipient default per ADR-0061.
Settings `payments.tips_enabled` (unset = on for shop type `service`, off
otherwise; an unticked box saves an explicit `0`) and `payments.tip_presets`
(default `10,15,20`), on Settings → Payments, elevation + audit like
`payments-default`. Help topic `payments.md` has the new section.

## Money rules checked

- **Rounding.** `pos.TipForPercentBP` = `Money.MulDiv(bp, 10000)`: integer
  minor units, half up, no float. 12.5% of 0.99 = 12, of 1.01 = 13, of 0.04
  = 1. The screen preview (`Math.floor((total*bp+5000)/10000)`) is the same
  rule, and the server recomputes from its own total, so screen and charge
  cannot drift.
- **Percent base** is the payable total (tax, service charge and voucher
  issues included). Confirmed as sensible: it is the figure on the screen and
  the receipt, and what a card terminal's own tip prompt uses. A shop that
  wants "percent of net" is a later setting, not a bug.
- **Tip never above the bill.** Quick-pay: a percent is capped at 100%; a
  typed amount above the total is 400 with nothing recorded. Split: see F1.
- **Coverage.** `netPayments` counts `amount − change − tip` toward the bill,
  so a tip can never pay for goods (split 120 with a 12 tip on a 120 bill is
  refused).
- **Voucher legs** never carry a tip (dropped on the form path, refused by
  `CompleteSale` on the JSON path).
- **Reader-reported tips** (`applyPluginReportedTip`) add on top of a typed
  tip, which is correct: the reader was asked to charge amount + typed tip
  and reports only what the customer added on the reader.
- **Refund after a tip** returns goods money only; the original tip row is
  untouched (test, and driven: refund of a 1.38 cash sale with an 0.18 tip
  paid back 1.20).
- **End of day.** Driven with a real shift: opening 100.00, cash sale 1.20 +
  0.18 tip, cash sale 1.20, card sale with tip. X report: cash sales 2.40,
  **Tips held out 0.18**, expected 102.58. After a 1.20 cash refund the close
  expected 101.38 and matched the count; the Z report lists cash tips under
  `tips_held_out`. So a cash tip is kept out of cash *sales* but stays in the
  expected drawer cash until paid out — see F2.
- **Receipt** shows `Cash £1.50` / `Tip £0.30` (looked at on screen).
- **Fiscal / tax.** Sale totals and tax are unchanged by a tip; only the
  payment row grows. No fee is added to any customer payment.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | medium (money) | The slip guard existed only on the quick-pay row. On the Split tab, typing a tip of 500 for 5.00 and pressing **Fill remaining** produced a leg of 5.50 on a 1.20 bill; the server accepted it (reproduced with a handler test: card 620 with tip 500 on a 120 bill → 200). On a card leg that charges the customer. | **Fixed**: `/api/pos/tender` refuses request-carried tips that together exceed the total (400, nothing recorded, before any payment plugin is called); the Split form refuses the same with the existing `tender.tip.too_large` text. Tests `TestTender_SplitTipsAboveTheBillAreRefused` (red first: 200 instead of 400, both cases) and `TestTender_SplitTipEqualToTheBillIsAccepted`. Reader-reported tips are not limited by this. |
| F2 | medium (manual wrong) | The new help text said a cash tip "is held out of the till's expected cash". The opposite is true (and `reports.md` says so): it is held out of cash **sales** and shown as "Tips held out", and the drawer is still expected to hold it. A shop following the text would have reported a false overage. | **Fixed**: wording corrected in `web/help/en/payments.md`; verified against the driven X and Z reports. |
| F3 | low (UX) | A typed tip above the bill showed the red message, but the method buttons still sent the tender; the server's 400 came back as the generic "Something went wrong" banner, which also pushed the Card button out of view at 1024x600. | **Fixed**: the tap (and the card-reader form submit) is stopped in a capture-phase listener while the tip is above the bill, focus returns to the field. First attempt (`htmx:beforeRequest` + `preventDefault`) left htmx unable to send the next tender; caught in the driven run and replaced. Covered by the new e2e spec. |
| F4 | low (test gap) | No Playwright coverage of the tip row. | **Fixed**: `e2e/tests/tip-row-7107.spec.ts` — geometry at 1024x600 (five options on one row, 44px floor, method buttons inside the panel), over-bill tip sends no request, a 15% cash tip lands on the receipt as `Tip £0.18`, 360px has no sideways overflow. Passed 5 runs in a row. |
| F5 | low (UX), accepted | With **Other** open at 1024x600 (and 360x740) the tip field pushes the second method button partly below the panel; the panel scrolls (`#panel-pay` scrollHeight 414 vs 348). With a preset or No tip everything fits. | Accepted for N1; a one-line tip field is a UX follow-up on #7107. |
| F6 | low, accepted | Preset labels (`10%`) are not digit-shaped for fa/ar; money is. New strings show in English in other locales until Weblate (ADR-0160). | Accepted (Dev note 5/6). |
| F7 | low, accepted | A partly invalid preset list (`12.5, 20,abc`) saves the valid part without saying a value was dropped; a wholly invalid list is refused with a message. | Accepted. |
| F8 | note | The customer-facing display and the self-order kiosk have no tip step. | Out of scope for N1; noted on #7107. |
| F9 | note | The commit carried a `[no-review: …]` marker although it is reviewed here. | Removed from the (unpushed) commit message. |
| F10 | note | `TestTender_SplitTipMustNotCoverSaleMoney` asserts "no payment recorded" but not the status code. | Accepted: the money assertion is the one that matters. |

Checked and found sound: tips disabled → `tip_bp`/`tip` are ignored
server-side, not just hidden; malformed, negative and >100% values are 400
before `EnsurePaymentMethod`; settings are shop-wide, sensitive tier, not
remote-writable, pinned by the registry tests; the settings POST is
demo-allowed like its siblings and audited; no raw SQL outside
`internal/data`; amounts are `money.Money` with conversion only at the form
boundary; logical CSS properties only; selected state is weight + outline,
not colour alone; every new key has `en.meta.json` metadata; no modal
dialog added; nothing touches the network (offline-safe).

## Verification done personally

- Real binary of this branch on a throwaway data dir, shop type `service`,
  Chromium via Playwright, touch enabled:
  - **1024x600** (kiosk): tip row default, 15% → `Total with tip: £1.38`,
    cash sale recorded `cash 138 / tip 18 / employee`; Other 5.00 → message,
    no request; Other 0.30 → `cash 150 / tip 30`; Split card 0.70 + cash
    with tip 0.18 → `card 70`, `cash 68 / tip 18`, pill "incl. tip £0.18";
    split tip 5.00 → refused with the message. Screenshots read.
  - **360x740** (phone): options wrap (four + Other full width), Other 0.50
    on card → `card 170 / tip 50`. Screenshots read.
  - **fa (RTL)** and **tr** at 1024x600: row mirrors, money localised,
    nothing clipped (geometry dump, screenshot read for fa).
  - Settings → Payments form at 1024x600: default ticked for a service shop,
    `12.5, 20` saved canonically, `0,150` refused with the message.
  - Shift open → sales → X report → refund → shift close → Z report, figures
    above.
  - NOT looked at: dark theme, the German long strings (no de pack in this
    checkout), real touch hardware / Pi / on-screen keyboard, 360px in RTL,
    the settings form at 360px, a real card reader.
- `go test -timeout 100m -p 3 ./...` on this branch rebased on main
  `d25ce05da`, with F1–F3 applied: 128 packages `ok` (`internal/pos`,
  `internal/data`, `internal/plugins` 1207 s), `internal/pages` (1027 s) had
  one failure, `TestSetupGBJoin_AutoInstallsEligibleTaxPlugin`, a setup
  wizard test with no relation to this change, during a machine load average
  above 140; re-run alone 3 of 3 pass, and the same test passed in the
  plugin-SQLite branch's full run on the same base. Treated as a load flake.
- Mutations, each reverted: truncating instead of `MulDiv` (5 rounding cases
  red), removing the `tip > total` refusal (`…/tip=121` red), not adding the
  tip to the amount (percent cases red), removing F1's guard (red before the
  fix).
- `gofmt`, `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0
  issues), `shellcheck scripts/ci/*.sh`, licences check, and all 74 guards
  named in `ci.yml`'s build job: pass, except `guard-armv7-seccomp.sh`
  (needs `qemu-arm`, not on this Mac; CI has it).
- Playwright: 18 payment-panel spec files (64 tests) pass with tips off
  (the default for the e2e till), plus the new tip spec.

## Docs-shots (not regenerated — Linux lane must do it)

`make docs-shots` needs the Linux font set and cannot run on this Mac.
`guard-docs-shots.sh` fails on this branch (surface files changed:
`index.html`, `settings.html`, `card_reader_picker.html`, `app.js`,
`app.css`, four `internal/pages` files, plus `payments.md`). The change is
visible: the Settings page gains the tip form, so the surface-hash escape
hatch does not apply and the manifest must not be edited by hand. CI policy
(`run-docs-shots-guard.sh`, ut-docs#2794): on the pull request this is a
warning; on the push to `main` after merge the step is red until the Linux
lane runs `make docs-shots` on `main` and commits `web/help/img/**` and
`manifest.json`.

Pre-flight: help topic updated (`web/help/en/payments.md`, corrected by F2);
`make docs-shots` deferred → Linux lane, tracked on #7107; `en.meta.json`
updated for every new key (no key added by the review fixes);
`docs/till-settings-registry.json` regenerated by Dev; README not stale (it
does not list payment-panel features); ut-docs walkthrough rows updated in
the docs branch; no ADR change (ADR-0061 recipient default reused); review
record this file.

## Deferred (on #7107 unless said otherwise)

Docs-shots regeneration; one-line tip field (F5); tip step on the customer
display and the kiosk (F8); "percent of net" option if a shop asks; touch
check on the tablet and the Pi.

Verdict: ship.
