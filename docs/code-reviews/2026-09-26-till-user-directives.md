# Review — till user directives: main-till key, HPKE open, save_user / set_user_pin / deactivate_user (ut-docs#2810)

- **Date:** 2026-09-26 · **Lane:** `lane:cloud-24` · **Complexity:** hard
- **Author:** Opus 5.5 dev subagent · **Reviewer:** Fable (independent, different model)
- **Contract:** ut-docs `reference/till-user-directives.md` §1, §2 (open side), §4, §5, §7; ADR-0115 §2 amendment (2026-09-25)
- **Companion:** ut-docs PR adding `reference/contracts/till-user-pin-v1.json` (shared vector), the `created_by` wire-field sentence in §4, and `architecture/pos-auth.md`

## What shipped

- `internal/hpke`: stdlib-only RFC 9180 base mode, DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / AES-128-GCM. `Open`, plus `SetupBaseS`/`SealDeterministic` for vectors (test-only reachable, in the deadcode baseline).
- `internal/directivekey`: main-till key at `paths.Data("secrets","directive-x25519.key")` (0700/0600, temp+rename, mutex-guarded create, never overwrites an unparseable file), `kid`, `Report`, `AAD`, `OpenPIN` (every failure returns the contract sentence or "bad pin_sealed"; nothing echoed).
- `internal/cloudsync`: `save_user`, `set_user_pin`, `deactivate_user` in `mainTillOnlyTypes`; strict payload decoder; `Hooks.SaveUser/SetUserPIN/DeactivateUser`; directive `created_by` read when present.
- `internal/pages/cloud_user_directives.go`: phase 1 outside any transaction (protected-target refusals, open, `ValidatePINFormat`, uniqueness via `VerifyPIN` against other active users, `HashPIN`); phase 2 one `BEGIN IMMEDIATE` tx (re-read target, username check, guarded active/role writes, PIN write, session revocation, audit row with `{"via":"cloud","actor":…}`).
- `internal/data/auth_repo.go`: `GetUserTx`, `CreateUserWithID`, `UpdateUserProfile`, `UsernameTakenByOther`, `SetUserPINTx`, `RevokeUserSessionsTx`.
- `cloudsync_wire.go`: `directive_key` in the device record and `users` in the config report, main till only.
- `CHANGELOG.md` (new): Unreleased entry; ut-cloud's `DirectiveMinTillVersion` = the release that ships it.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Where `created_by` rides on the wire was unspecified; ut-cloud doesn't send it yet → actor `""` | **Fixed in contract** (ut-docs §4: top-level `created_by`); ut-cloud side is #2811 |
| 2 | minor | Replayed `save_user{create}`/`set_user_pin` re-hashes and revokes the target's sessions again | **Accepted** — state-idempotent as the contract requires; ADR-0115 already lists "a lost result post can re-apply … revoking that user's sessions once more" as an accepted residual |
| 3 | minor | `Report()` logged every tick when the key couldn't be persisted (read-only data dir) | **Fixed** — once per store (`reportWarned`) |
| 4 | nit | Write-lock test failed only via the 10-min package timeout when regressed | **Fixed** — hook call bounded by a 20 s context |
| 5 | nit | `display_name: ""` on update silently kept | Accepted (decoder trims; empty display name is never valid) |
| 6 | nit | `errors.Unwrap` could print `<nil>` in one log line | Accepted, cosmetic |
| — | note | Audit row written inside the tx rather than via `auditCloudDirective` | Accepted — stricter (atomic with the write) |

No blockers, no majors.

## Verified beyond the automated tests

- Reviewer re-derived A.1.1 and every field of the shared vector with an independent HMAC-only Go implementation (no repo code, no `crypto/hkdf`); the dev had re-derived the key schedule in pure Python. A.1.1 constants match Go's own `crypto/internal/hpke/testdata/rfc9180-vectors.json`.
- TDD re-verified by the reviewer: dropping the requested-role `super_admin` refusal fails `TestCloudUserDirective_Refusals`; moving PBKDF2 into the transaction fails `TestCloudUserDirective_PBKDF2RunsWithoutTheWriteLock`.
- PIN leakage hunted across logs, errors, result messages, audit payloads and the users report — none.
- No UI surface touched (backend only); the owner-facing screen is ut-my-shop (ut-docs#2757).

## Gate

`gofmt -l .` empty; `go build ./...`; `go vet ./...`; `golangci-lint run ./...` 0 issues; `go test ./...` (see PR); `-race` on hpke/directivekey/data/cloudsync; guards data-access, core-neutral, i18n, kiosk-engine, docs-shots (surface hash refreshed — no screen changed), page-http-error, deadcode-baseline (3 test-only helpers added; the two `internal/logging` entries it reports locally are `cmd/unitill-desktop`-reachable and only appear because this container lacks the GTK headers).

## Verdict

Safe to merge.

## Deferred

- ut-cloud side (seal, key recording, endpoints, `created_by` on served directives): ut-docs#2811.

## Addendum (post-merge)

v0.28.0 was tagged from the commit just before this PR merged, so the
feature ships in the next release; `CHANGELOG.md` corrected in a follow-up
PR (docs-only, no code).
