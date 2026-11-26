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
