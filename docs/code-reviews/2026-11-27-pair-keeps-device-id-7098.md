# Review record: pairing with a code keeps the till's device_id (part of ut-docs#7098, ADR-0222 §9)

Date: 2026-11-27. Author: Claude (Sonnet 5.5), BA/Architect/Review by Claude (Opus 5.5), routine `:41`.
Reviewer: independent Opus 5.5 subagent (different model from the author). Tester: separate subagent.

**Status: NOT MERGED.** The card ran past its time budget before the fix round (Admin Review, `budget-exceeded`). The open findings below must be fixed before merge.

## What changed
- `internal/approvalkey/rejoin.go` (new): `RejoinMessage`, `RejoinCodeHash` (the cloud's code normalisation), `(*Store).SignRejoin` (never creates a key). `testdata/rejoin_vector.json` and `recover_vector.json` are byte-identical copies of ut-cloud's.
- `internal/enroll/pair.go`: after the unchanged move leg, when `/version` advertises `"rejoin": "v1"` and the till has a usable approval key, `/pair` carries the current `device_id` plus the rejoin block. 200 keeps the id; 409 `rejoin_refused` / `device_exists` / `device_revoked` (all rolled back before the code is spent) falls back once to a fresh id with the same code; any other error leaves the identity untouched. `resetIdentityForPair` now runs only after a 200 and the replica store check. An answer naming another device is refused (§5).
- `internal/enroll/move.go`: one cached `/version` fetch serves `pair_move` and `rejoin`.
- Copy (English only): `settings.enrol.pair_replace_body` + meta, `web/help/en/claim.md` steps 3–4.

The change is inert in production until ut-cloud's `/version` advertises `rejoin` (not built yet) and `TILL_REJOIN_ENABLED` is on.

## Evidence (Tester)
- False-pass: HEAD `pair.go`/`move.go` with the new tests → 13 tests fail on behaviour. 22 targeted mutations, 20 killed, 2 equivalent survivors.
- End to end against the real ut-cloud pair handler (throwaway test, deleted): the till's exact bytes → 200 under the same `device_id`, new token authenticates; a signature for another code → 409 `rejoin_refused` with the code still usable; flag off → 409 `device_exists`.
- Vectors `cmp`-identical. gofmt, build, golangci-lint 0 issues. All packages pass (race set under `-race`; the full `-race` run did not finish on the 4-CPU box). i18n, help-topic, data-access, compliance, fixed-date guards pass.

## Findings — open (fix before merge)
1. **Blocker (CI):** `run-docs-shots-guard.sh` fails: `en/claim` changed since its screenshot. Run `make docs-shots` or refresh the `claim` hashes in `web/help/img/manifest.json`.
2. **M3 (medium):** the new `pair_replace_body` text and `claim.md:116` say a code for another shop changes nothing. On a main/standalone till without a move offer, a valid other-shop code pairs the till into that shop as a new device; only a replica refuses. Suggested: "If the code is wrong or expired, nothing changes. A code from another shop moves this till's connection to that shop."
3. **L1:** `claim.md:115` "keeps its device ID when the shop still knows this till" is not true until the cloud advertises rejoin, and never for removed tills. Reword or ship with the cloud switch.
4. **L2:** `pair_replace_body` keeps its key while its meaning reverses; existing Weblate translations keep the old warning. Use a new key.
5. **N1:** stale "Pair clears the identity first" comments: `internal/pages/settings_page.go:1595-1597`, `web/ui/pages/settings.html:274-279`, `web/ui/partials/elevation_prompt.html:94-96`.
6. **N2:** `rejoinProof` rejects only `\n`/`\r`; the cloud 400s on any control character (400 does not fall back). Reject all control characters.
7. **N3:** add tests for a device_id mismatch on the fallback answer, and for a cached `rejoin=v1` followed by a cloud that ignores the block.
8. The `rejoinCapability` comment cites `paircontract.RejoinCapabilityVersion`, which does not exist yet; say it is the planned cloud counterpart.

## Findings — cloud side (card checklist, must land before rejoin is switched on)
- **M1:** with `TILL_REJOIN_ENABLED` off, or during a rolling deploy, the cloud ignores the rejoin block and answers 200 under the old id, so one `device_id` could live in two stores within the 5-minute capability cache. Cloud fix: 409 `rejoin_refused` when a block is present but the flag is off, and a `data.rejoined` marker on the 200.
- **M2:** `/version` must advertise `rejoin: v1` tied to `TILL_REJOIN_ENABLED`, with a contract test.

## Accepted (noted)
- L3: a lost 200 for a rejoin into the till's own shop leaves a rotated-out token; self-heal recovers it (one of three daily recovers). A check-in 401 during an in-flight pair can queue a needless Recover.

Pre-flight: no migration, no data access, no money, no new route; one English string + one help topic changed; secrets (code, hash, signature, token) never logged.

Verdict: ship after fixes.
