# Review: key proof to re-register, Reconnect / Register as a new shop (ut-docs#5145, part of #5127)

- **PR**: universal-till#2502 (`fix/5145-anon-reregister`), with main merged in
  and the manual screenshots regenerated in the Linux reference environment.
- **Reviewer**: Fable 5.2, independent security review of the whole batch
  (universal-till#2502, ut-admin-app#73, ut-docs#6077, ut-cloud#981 as merged),
  2026-11-16. Earlier rounds: ut-docs `code-reviews/2026-11-12-5145-anon-reregister.md`.
- **Pre-flight**: help topic `web/help/en/claim.md` updated; new keys English
  only with `en.meta.json`; no new modal on the sell screen; approval key under
  `paths.Data("secrets")` with `os.MkdirAll`; no token/PIN in logs or audit;
  no real shop or person in test data.

## What shipped

The till signs the cloud's `key_required` challenge with its approval key and
retries once; a second refusal is final and quiets the lazy path. A locked-out
till of an ownerless shop offers **Reconnect this till** (same shop,
`expect_store_id`) or **Register as a new shop** (new device id, confirm), both
manager-only and operator-initiated; the till shows its device id and key
fingerprint for the staff override.

## Findings (Fable, 2026-11-16)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | M | `register()` adopted a 2xx naming a different store on a Reconnect | **Fixed**: mismatch persists nothing, returns `store_not_reusable`; `TestReconnectRefusesAnAnswerForAnotherShop` (seen failing first) |
| M2 | M | `POST /api/enrol/reconnect` hidden but not server-refused outside the anonymous lock-out | **Fixed**: gated on `tillLockedOutAnonymous` like new-shop; `TestReconnect_RefusedOnAHealthyTill`, `..._OnAClaimedShop` (claimed seen failing first) |
| L1 | L | Not-registered key-refused till is told to "register as a new shop", which it cannot | Card |
| L2 | L | Plugin install error still says "Register it as a new store" | **Fixed**: points at Reconnect (en + meta + fallback text) |
| L3 | L | new-shop wipe behind `checkOrElevate`, not `checkStepUp` | Card |
| L4 | L | `StartAsNewStore` wipes identity before the cloud answers | Card (deliberate per design) |
| L5 | L | Unmapped Reconnect failures all say "Could not reach the cloud" | Card |
| L6 | L | ar/fa/tr lose the dead key | Accepted (no leftovers) |
| C1, C2 | L | ut-cloud follow-ups | Fixed in ut-cloud#1047 |

Surface hash refreshed with `update-docs-shots-surface-hash.sh`: the M2 change
is a server-side gate with no rendered pixel change.

## Verification

Fable: build/vet clean; `enroll`, `approvalkey`, `cloudsync`, full `pages` pass;
three TDD claims re-verified by revert. After the fixes: `internal/enroll`,
`internal/pages` (reconnect/new-shop/register set), `internal/plugins` install
tests pass; full suites run once for the batch by the Tester.

**Verdict**: approve after M1/M2 (fixed here).
