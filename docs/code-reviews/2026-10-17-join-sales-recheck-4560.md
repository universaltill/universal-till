# Review — join over local sales: mid-join recheck, refusal brings the tick box (ut-docs#4560)

Follow-up to ut-docs#4507 (`2026-10-17-join-sales-confirm-4507.md`), which
refuses every join path for a till with sales unless `replace_local_data=1`
is sent. This closes the four gaps that review deferred.

## What shipped

1. **Sale during completeJoin.** `completeJoin` takes `replaceConfirmed`.
   After staging the restore and the replica identity it re-checks
   `hasSalesHistory`; unconfirmed and a sale has landed → new
   `db.UnstageJoin` withdraws both (identity first, both always tried,
   errors joined) and the join is refused with `joinErrHasSales`.
2. **Refusal with no tick box.** Every has-sales refusal (code joins: 409;
   pair-start; pair-status) also renders `ui/partials/join_replace_slot.html`,
   which fills `#pairing-replace-slot` above the join tabs out of band with
   the warning and tick box. Both tabs always `hx-include` and `validate:`
   `#pairing-replace-local` (a missing one is skipped). Only the refusing
   pair-status render brings it, never the 15 s re-polls. No new locale
   keys (reuses `tills.discovery.ambient.has_sales_*`), so no language-pack
   follow-ups.
3. **First-boot wizard.** `setup.html` has the same slot, include and
   guard, so its refusal is no longer a dead end.
4. **Bubble-only validation.** `inline-actions.js` `validate:` now also
   marks the field and its `aria-describedby` hint `.field-invalid` until
   it turns valid; `app.css` outlines the warning and the box.
5. Manual: `web/help/{en,de,tr,fa,ar}/multitill.md` step 3 says the box
   appears on a refusal, and that a join stopped part-way leaves an entry
   on the main till to revoke (or pick another name).

## Review

Independent review on **Sonnet** — the Fable review was refused (usage
credits exhausted), the documented fallback (`MODEL-ROUTING.md`). It ran
build, vet and the affected Go tests.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `UnstageJoin` stopped at the first failed removal and removed the restore before the identity; a failure could leave a half-joined identity or skip a removal. No unit test. | **Fixed**: identity removed first, both always tried, `errors.Join`; two unit tests (happy path, and a forced failure that still removes the identity). Kept the existing `stage_snapshot_failed` message for this practically unreachable path rather than add a key + four pack PRs. |
| 2 | minor | A sale between the recheck and the manager pressing Restart is still not caught; an external restart in the staged→unstaged instant could apply it. | **Accepted**: residual window, sales survive in `backups/pre-restore-*.db` as before; the help text doesn't claim it is closed. |
| 3 | minor | A mid-join refusal leaves an enrolment on the main till, so a same-name retry hits "name taken". | **Accepted, documented** in code and help (revoke it or use another name) — same as every other post-enrol failure. Follow-up ut-docs#4566: the replica withdraws its own enrolment. |
| 4 | minor | No mid-join test through pair-status; `/api/setup/join` OOB untested. | **Fixed** for pair-status (`TestPairStatus_UnstagesWhenSaleLandsDuringJoin`); the setup join shares `writeReplaceRefusal` with `/api/sync/join` and `TestSetupJoin_RefusesWithoutReplaceConfirmWhenTillHasSales` now asserts the slot. |

Checked fine by the reviewer: OOB + `hx-preserve` on vendored htmx 1.9.12
(a ticked box is kept, not duplicated); re-polls don't re-swap; setup's
4xx swap handler; null-safe `validate:`; logical CSS only; no new
user-facing JS strings; no SQL outside `internal/data`/`internal/db`.

## Verification beyond unit tests

- TDD re-verified by hand: with the recheck disabled,
  `TestSyncJoin_UnstagesWhenSaleLandsDuringJoin` fails (200 "Joined"
  instead of 409) and `TestPairStatus_UnstagesWhenSaleLandsDuringJoin`
  fails; both pass with it.
- Playwright `e2e/tests/join-replace-refusal-4560.spec.ts` against a real
  till: page rendered without a box, a real sale rung up, code join →
  409, the box appears above the tabs; an unticked retry sends nothing and
  marks warning + box; ticking clears it and the retry carries
  `replace_local_data=1`. 1024×600 and 360×740, no horizontal scroll.
  `tills-pairing-layout-1548.spec.ts` still green.
- Screenshots looked at: refusal state at 1024×600 and 360×740 (light, en);
  invalid state at 1024 in en and fa (RTL — outline and bar on the correct
  side). Not looked at: dark theme, the first-boot wizard in a browser (Go
  tests pin its markup; it shares the fragment).

## Verdict

Safe to merge.
