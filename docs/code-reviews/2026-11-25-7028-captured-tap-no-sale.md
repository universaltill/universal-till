# Review — a captured tap leg with no sale at restart is listed for a manager (ut-docs#7028)

Date: 2026-11-25 · Lane: lane:cloud-41 · Built by: Dev (Sonnet) · Reviewed by: independent Fable subagent
(money path, hard card) · UX gate: Opus subagent · Tester: separate subagent · ADR: ADR-0136 amendment,
universaltill/ut-docs#7032 (merges first).

## What shipped

At boot a `captured` tap/phone leg with no sale (the customer was charged) was only logged, so nobody saw it.
Now:

- **Boot** (`internal/pages/leg_reconcile.go`): a leg is a tap if a `payment_attempts` row exists for its
  request id or its plugin entry says tap. A tap leg is appended `warned` / `tap_charged_no_sale` (new
  ADR-0136 9.2 edge) and counted `TapChargedNoSale`; nothing is refunded or sent to the provider. If no read
  shows a tap and either read failed, the leg stays `captured` (never reverse on doubt). Non-tap legs are
  reversed as before. A second boot does nothing (`ListBefore` does not read `warned`).
- **Data** (`internal/data`): `Append` accepts `captured -> warned` only with reason `tap_charged_no_sale`, and
  refuses that reason on any other move. New `PaymentAttemptRepo.MarkChargedNoSale`: stamps reason
  `charged_no_sale` on an `approved` attempt (state unchanged, so the tap gate refuses the key afterwards);
  idempotent. No migration (migration 115's CHECK already allows both states).
- **Settle** (`POST /api/pay/legs/resolve`, `resolveChargedNoSale`, under `tapLegInFlight`, PIN `prelude` as
  the siblings): decision `charged_refund_at_provider` stamps the approved attempt, appends `resolved`, writes
  one `tap_resolve` audit (`mode` `core_confirmed`, or `manager_override` when the attempt was approved by an
  override). No broker call, works offline. An attempt in another state: the journal leg is closed only, the
  attempt untouched, its state/reason in the audit. `not_charged`, no decision, second POST: 409, nothing
  written.
- **Card**: a "Charged, no sale" row (own chip and explanation, one button "Charged — refund at the provider",
  no reason radios, no "Not charged"). New English keys + meta; `pay.attention.help` widened (text only).
- **Docs**: `web/help/en/payments.md` (the old line said every captured payment is returned — wrong for taps),
  `docs/data-model.md`.

## Findings (Fable review) and what happened

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | "never reverse on doubt" for a failed attempt read untested (removing `attErr` kept all tests green) | Fixed: 3 single-read tests; orchestrator re-verified (mutation → `TestLegReconcile_AttemptUnreadablePluginSaysNotTapStaysCaptured_7028` fails; restored → passes) |
| 2 | minor | listed leg with a non-`approved` attempt could never be closed | Fixed: journal-only close + audit; `TestChargedNoSale_NonApprovedAttemptStillClosable_7028` |
| 3 | minor | audit `mode` always `core_confirmed`; retry audit lost the prior reason | Fixed: mode follows prior reason; retry audits `already_stamped:true`; 2 tests |
| 4 | minor | data-model.md "neither can be read" mismatched code/ADR | Fixed |
| 5 | minor | no test for the conflict 409 | Path removed by #2; its key + meta dropped; remaining 409s covered |
| 6 | nit | `tap_charged_no_sale` accepted on other moves into `warned` | Fixed: refused; `TestLegJournal_TapReasonRefusedOffCaptured_7028` |
| 7 | nit | a leg left `captured` by a failed live-refund journal write would show as charged | Accepted: card intro tells the manager to check the provider first; rare |
| 8 | nit | WIP commit / missing record | Fixed: one clean commit on a fresh branch; this record |

UX gate: PASS with copy changes C1–C7 (PIN summary says the till does not refund; "Card taps can still be
taken"; flash no longer contradicts the explanation; card intro; journal_failed names the button; help step
split) — all applied.

## Verification beyond unit tests

Tester (separate agent): false-pass check on a pre-fix worktree (13/14 new tests fail there for the right
reasons; the 14th pins old fail-safe behaviour) plus four deliberate breakages, each caught. Real driven run:
till built from the branch, captured tap + approved attempt seeded, restart → one warn line, journal
`warned/tap_charged_no_sale`; Settings → Payments shows exactly one "Charged, no sale" row (£13.50, ref …1234)
at 1280 and 360 px, no overflow; wrong PIN records nothing; right PIN removes the row, attempt stamped,
exactly one audit; next boot logs and appends nothing.

Gate: gofmt clean, build, vet; `go test ./internal/data/... ./internal/pages/...`; `-race` on
`internal/data -run 'Leg|Attempt'` and `internal/pages -run 'ChargedNoSale|LegReconcile|_7025|LegAttention|HxConfirm|UTConfirm'`
(full `internal/pages -race` times out on this container — CI runs it); guards i18n, i18n-meta, data-access,
no-showmodal, no-native-confirm, rtl-logical-css, help-topics, fixed-dates, compliance-claims pass.

Pre-flight: help topic `web/help/en/payments.md` updated (no screenshots on that topic, docs-shots n/a);
`en.json` keys → `en.meta.json` updated (English only); amended ADR → ut-docs#7032 with its own record;
`reference/` n/a (no page lists leg-journal moves), `docs/data-model.md` updated; README n/a; follow-ups →
Backlog cards filed at close-out (in-till refund of a charged-no-sale tap; JSON settle for this kind).

Verdict: ship.
