# Till self-heal engine (ut-docs#7097, ADR-0217 slices T1 and T3)

Status: **stub, the Reviewer completes it** (a different-model pass over the
diff is required by the pipeline; none has run on this code yet).

Scope: `internal/credheal` (engine, chip data, persisted state),
`internal/enroll/recover.go` (challenge, sign, recover, one token adoption),
`internal/approvalkey` (`Has`, `SignRecover`, golden vector), the dead-credential
signal in cloudsync, catalogpull and stockupload (through cloudsync), cloudlink,
offsitebackup and the approval-key report, the plain-words chip and its English
keys. Binding inputs: design.md and `review-adr.md` (B1-B6, M5, M6, M8, H2).

## To attack

1. No identity key is written before the cloud's 200 (`TestRecover_EveryRefusalClassLeavesTheIdentityUntouched`).
2. A healthy credential is never rotated because one route misbehaves (probe rule, envelope rule).
3. The engine never runs on a request goroutine; a sale completes while it heals.
4. No token, key, nonce, signature or hash in any log line.
5. The off-site backup key file is deleted only for `replaced`, `moved`, `removed`.
6. Timing rules from the security review: wait = date minus the answer's `Date` header (cap 73 h), 0-60 s jitter before the first attempt, 30 s floor after a lost `/recover` answer, 65-minute look after `recover_refused`.

## Findings

(none yet)


## Combined review 2026-11-27 (ut-docs#7097 batch, one Reviewer: not the Dev, not the Tester)

Independent security pass by a different-model subagent (Fable) plus the Reviewer's own reading, against the binding review-adr.md conditions (B1-B6, H1-H2, M1-M9). Pre-flight: walked PREFLIGHT.md for the touched surfaces; the batch's combined Tester report (tester-report.md) is the test evidence; no real shop or person names in the added tests.

Findings and outcome:

1. MED, fixed. The round slept its 0-60 s jitter and then called Recover without looking again: a till paired or healthy meanwhile could have a healthy credential rotated (and an owner notice, one of three daily recovers). A generation counter (bumped by `reset` and `working`) and a second `CanRecover` now follow the pause. Test `TestRoundDoesNotRecoverACredentialThatBecameHealthyDuringTheJitter` (red when the check is removed).
2. MED, fixed (tester item B). A credential dead at boot with no cloud plan was not noticed for 15 minutes: the first approval-key 401 fired before the engine existed. `enroll` now keeps the first early dead-credential answer and replays it once when the engine installs its hook (5 minute freshness, cleared when the hook is removed). The engine still probes with a real check-in first, so nothing is rotated on a stray 401; this stays inside ADR-0148 as amended by ADR-0217. Tests `heal_hooks_early_test.go`.
3. MED, fixed. The cloud's `moved_to` is the target shop's id, not a name: it is no longer kept as the heal detail.
4. LOW, fixed. Server `code` and `reason` words are clamped (40 chars, no control characters) before logging or saving; state is saved with the state key last; a 5xx on `/recover` gets the 30 s lost-answer floor (M8).
5. Tester item D, fixed. `recover_vector.json` is now byte-identical to the cloud's (kid added), independently verified.
6. Tester item A, fixed. Unwired `approvalkey.SignRejoin` and its test removed (the till side of same-device pairing, T6, is a later slice; it must add `rejoin_vector.json` then). No baseline growth.
7. Accepted: a stale-signal filter of 10 s after a heal (a very slow uplink could spend a second heal; the third inside a day is "in use elsewhere"); the probe runs before `CanRecover` for stopped or env-pinned identities; a nil probe hook is not fail-closed (production always wires it).

Verdict: SHIP after the fixes above.
