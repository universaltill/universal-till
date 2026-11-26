# Review: Reconnect / Register as a new shop follow-ups (ut-docs#6424)

- **Branch**: `fix/6424-reconnect-followups`. Follow-ups L1, L3, L5, D6, T1 and
  `move_region_mismatch` from `2026-11-16-5145-reconnect-key-proof.md`.
  L4 (wipe before the cloud answers) and T2 (OOB chip swap) stay open on the card.
- **Author**: Sonnet 5.5 (Dev subagent). **Reviewer**: Opus 5.5, a fresh
  independent subagent, one round. Tester: Opus 5.5, a separate subagent.
- **Pre-flight**: new keys are English-only, each with an `en.meta.json` entry
  (`review: standard`) and no placeholders; no translation was touched; help
  topic `web/help/en/claim.md` is updated; no `showModal()`, no native confirm;
  no token or PIN in logs or audit; no real shop or person in test data.

## What changed

| Item | Change |
|---|---|
| T1 | A register answer naming another shop on Reconnect is `enroll.ErrReconnectOtherShop` (not a synthesised 409 `store_not_reusable`) and gets its own text `reconnect_other_shop`. |
| L5 | `enroll.IsServerError` (cloud 5xx): Reconnect and new-shop show `reconnect_server_error`; "Could not reach the cloud" is shown only for network failures. After a wipe, no "Nothing changed" reason is appended. |
| D6 | `reconnectRefusalKey`: `reconnect_unavailable` only for a replica or an environment-pinned till; every other refusal gets `reconnect_not_locked_out`. |
| L1 | A not-registered, key-refused till shows `key_refused_unregistered` (no "register as a new shop" promise) and no help line. |
| L3 | `POST /api/enrol/new-shop` uses `checkStepUp` (fresh PIN, same-origin) after the confirm step. |
| region | `move_region_mismatch` maps to `settings.move.refused_region` at pairing and at approval, where it is terminal. |

## Findings (Opus 5.5, 2026-11-26)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | M | `move_region_mismatch` is shown as final at approval, but `moveTerminal` omits it, so the till keeps a stale pending move | **Fixed**: added to `moveTerminal`, and its ended status maps to the region text; `TestMoveApprove_RegionMismatchIsTerminal` now asserts the pending move is gone (seen failing first) |
| L1 | L | The new-shop audit assertion cannot tell a PIN step-up from no step-up | **Fixed**: asserts the PIN-path audit field |
| L2 | L | The "no reason after a wipe" check compares rendered strings | Accepted (works; optional refactor) |
| L3 | L | The not-locked-out text reads oddly on a revoked till (stale page / hand-made POST only) | Accepted |
| L4 | L | A failed settings read can change which refusal text shows; the request is still refused | Accepted |
| L5 | L | Cross-origin / cashier tests check the outcome but not the message | **Fixed**: text asserted |

The reviewer also noted that the other approve-time move refusals (currency mismatch,
shop has tills, ...) have the same `moveTerminal` gap on `main`. That is out of scope
here and is filed as a Backlog line.

## Verification

- Tester (Opus): reverting each fix makes its new tests fail (T1, L3 ×4, wipe+500,
  D6 ×5, region ×2). `go test -race ./internal/enroll/` passes, and the targeted
  `internal/pages` set passes. The full `internal/pages` suite passed without
  `-race` (640 s; `-race` on that package exceeds this container's time).
  68/74 `ci.yml` build guards pass; 2 were skipped (no `DOCS_READ_TOKEN`) and
  4 are environment-only (qemu-arm, catimport tags, deadcode tool on go1.26,
  shellcheck missing; no `.sh` changed).
- Driven run: the till was built and run with no cloud. Both endpoints return the
  new D6 text on a till that is not locked out. Settings › Till registration lays
  out cleanly at 1024x600. The locked-out state (confirm → PIN prompt, text after
  a wipe) needs a refusing cloud, so it is covered by handler tests through the
  real routes with a fake cloud only.

**Verdict**: approve after M1 (fixed here).
