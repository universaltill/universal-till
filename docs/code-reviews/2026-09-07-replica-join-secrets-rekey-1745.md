# Code review — replica join doesn't re-key to the shop's secrets key (ut-docs#1745)

**Date:** 2026-09-07
**Branch:** `fix/1745-replica-rekey-secrets`
**Reviewer:** independent Opus subagent, in an isolated git worktree (different
model from the implementer, per `complexity:medium` routing)
**Verdict:** SAFE TO MERGE. No blocker-class findings; one should-fix (this ADR
correction) and five nits, all addressed in this same round — see below.

## The bug

ADR-0082 added `internal/secrets.KeyStore`, a shop-scoped AES-256-GCM key used
to seal/open plugin secret settings (Stripe/SumUp credentials) at rest. A
**standalone** till can already have minted and persisted its own local key
file through ordinary use — e.g. it had a secret plugin setting configured
before ever joining a shop. When that till later **joins a shop as a
replica**, `internal/db.ApplyReplicaIdentity` (run right after `db.Open` on
the snapshot-restored DB, well before `internal/app.Run` wires up
`secrets.SetDefault`) applied the new sync identity but never touched that key
file — it lives outside the SQLite DB by construction, so the snapshot restore
doesn't disturb it either. The till would keep sealing/opening everything
under its own stale standalone key forever, and re-entering a credential there
would propagate a wrongly-keyed value back to the whole shop.

## The fix

- `internal/secrets.ClearLocalKeyFile()` (new, exported): removes the key file
  at the canonical `paths.Data("secrets", "plugin_settings_key.bin")` path
  (the exact same path expression `NewKeyStore` uses — no duplicated
  literal), a no-op when absent. Also removes any `.tmp` sibling
  `persist()`'s write-tmp-then-rename could have left behind on a crash
  (review finding 3 below).
- `internal/db.ApplyReplicaIdentity` calls it as part of applying the identity
  (after the settings-row writes, before the identity file is removed), and
  also calls `secrets.SetDefault(nil)` to invalidate any already-registered
  `KeyStore` (review finding 2 below).
- The next `Load()` — via the fetch closure registered later at startup —
  therefore takes the replica-fetch path (`GET /api/sync/secrets-key`) instead
  of continuing to read/write under the stale key.

## What the reviewer verified independently

Spawned at `complexity:medium`'s mapped review model (Opus), in
`isolation: "worktree"` (never the shared checkout, per ut-docs#386) branched
from a WIP commit of the diff.

- **Ordering assumption, traced at the source, not assumed:**
  `db.ApplyReplicaIdentity` runs at `internal/app/app.go:155`;
  `secrets.SetDefault(secrets.NewKeyStore(...))` at `internal/app/app.go:282`.
  `secrets.NewKeyStore` has exactly one non-test call site, and every startup
  step between the two lines (`LoadRuntimeConfig`, `SaveRuntimeConfig`,
  `AdoptDefaultPrinterCharset`, `bootstrapPluginDirectories`, `enroll.Init`)
  was checked and none reaches `plugin_settings` or `secrets.*`.
- **Import-cycle / layering**, via `go list -deps ./internal/secrets`: no
  `internal/db` in the dependency graph.
- **Mutation-tested the path construction**: hand-rolled a
  `filepath.Join("data", "secrets", ...)` in `ClearLocalKeyFile` in place of
  `paths.Data(keyRelPath...)` — `TestClearLocalKeyFileRemovesExistingKey`
  failed, confirming the path is genuinely pinned, not coincidentally correct.
- **TDD claim, independently re-verified**: reverted the new
  `secrets.ClearLocalKeyFile()` call in `ApplyReplicaIdentity`, re-ran
  `TestApplyReplicaIdentityClearsLocalSecretsKey` — failed with the expected
  behavioural message (not a compile error) — restored, re-ran the full
  `TestApplyReplicaIdentity*` set: all pass.
- **Test isolation**: confirmed no `t.Parallel()` anywhere in `internal/db` or
  `internal/secrets`, and that `paths.Init`'s `"./data"` fallback never
  creates a directory on `os.Remove` of a non-existent path.
- **Security lens** (the card carries the `security` label): traced that after
  the clear, `SecretsKeyFetcher` sees `sync.primary_url`/`sync.bearer` already
  written earlier in the same function and so fetches rather than declining;
  confirmed `Load`'s failure mode (`ErrNoKeyYet`) never falls back to
  plaintext and a non-sealed value short-circuits at `IsSealed` without
  touching the store either way — strictly safer than the pre-fix behaviour of
  silently using the wrong key.
- Ran the full gate itself in the worktree: `gofmt`, `go build ./...`,
  `go vet ./...`, `go test ./internal/secrets/... ./internal/db/... -race`,
  full `go test ./...`, `golangci-lint run ./...` (0 issues),
  `guard-data-access.sh` — all green.

## Findings and what was done

1. **[SHOULD-FIX] ADR-0082 stated the opposite of the code.** The ADR's own
   text (`docs/adr/0082-plugin-secret-settings-encryption-at-rest.md`, in
   `ut-docs`) said the join sequence "needs no new hard-fail step and no
   special handling at all" — exactly the claim this bug disproves for a
   previously-standalone till. **Fixed**: added a correction paragraph in the
   same section (marked `Correction — ut-docs#1745`), and listed this card in
   the ADR's own `Related` line, in the same PR-adjacent change (`ut-docs`
   repo, same session).
2. **[NIT, security hardening] Clearing the file doesn't evict an
   already-cached in-memory key.** A long-lived process that can run
   `ApplyReplicaIdentity` a second time without a real restart (Android's
   in-process `app.Run` reuse, `mobile/mobile.go`) could keep a
   `secrets.Default()` store registered from an *earlier* run, still serving
   its cached key (`KeyStore.Load` never re-stats the file once cached) —
   latent, not live (nothing in the current window between `app.go:155` and
   `app.go:282` reads a secret), but closeable by construction rather than by
   "nothing happens to use it today." **Fixed**: `ApplyReplicaIdentity` also
   calls `secrets.SetDefault(nil)`. New regression test
   `TestApplyReplicaIdentityInvalidatesRegisteredKeyStore` (confirmed failing
   before the fix, passing after).
3. **[NIT] The `.tmp` write-then-rename sibling survives the clear.**
   `persist()` writes `path+".tmp"` then renames; a crash between those two
   steps leaves a plaintext-key `.tmp` file that "clearing the key" was
   silently leaving in place forever. **Fixed**: `ClearLocalKeyFile` also
   removes `path+".tmp"`. Two new tests
   (`TestClearLocalKeyFileRemovesTmpSibling`,
   `TestClearLocalKeyFileRemovesOrphanTmpWithNoRealFile`).
4. **[NIT, deferred] No aside copy of the key for the pre-restore-backup
   rollback path.** `ApplyPendingRestore` preserves the pre-join DB as
   `backups/pre-restore-*.db`, but the key that could open that snapshot's
   sealed rows is now destroyed with no equivalent aside. Bounded impact (a
   rolled-back till reads its old secret settings as "not configured" and
   needs re-entry, not a silent wrong value) — **not fixed here**; filed as
   ut-docs#1753 and noted in the same ADR-0082 correction paragraph as an
   accepted one-way door pending real-world signal that it matters.
5. **[NIT] Three pre-existing sibling tests now reach `paths.Data(...)`
   without sandboxing it.** `TestApplyReplicaIdentityReissuesDeviceID`,
   `TestApplyReplicaIdentitySetsProvisionedRegisterID` and
   `TestApplyReplicaIdentityClearsTillRegisterID` all call
   `ApplyReplicaIdentity` to a successful `applied=true`, so they now
   transitively exercise `ClearLocalKeyFile`'s `paths.Data(...)` call without
   a `paths.Init(t.TempDir())` of their own — verified harmless in practice
   (nothing is created on a no-op remove, and no test binary runs with
   cwd = a real data root), but a passing test operating outside its own
   sandbox is a landmine for the next change to this area. **Fixed**: added
   `paths.Init(t.TempDir())` + cleanup to all three. (The two
   `backup_more_test.go` callers the reviewer also flagged,
   `TestApplyReplicaIdentityNoFileIsNoop` and
   `TestApplyReplicaIdentityRejectsCorruptFile`, both return before reaching
   the settings-apply loop at all — confirmed by reading `ApplyReplicaIdentity`
   — so they never reach the new code and were left as-is.)
6. **[NIT] The db-side test didn't pin its own path the way its secrets-side
   twin does.** `internal/secrets/keystore_test.go` asserts `ks.Path()`
   before proceeding; the db-side `TestApplyReplicaIdentityClearsLocalSecretsKey`
   didn't. Not a false-pass risk today, but caps the blast radius of a future
   `paths.Init` regression. **Fixed**: added the same assertion (`ks.Path()`
   is under `paths.DataDir()`).

## Verification beyond automated tests

- Full `go test ./...` (all packages) green, re-run after every fix round —
  no sibling package's assumptions broken.
- `go test ./internal/secrets/... ./internal/db/... -race` clean throughout.
- `golangci-lint run ./...`: 0 issues. `bash scripts/ci/guard-data-access.sh`:
  passes (no SQL added outside `internal/data`/`internal/db` — this diff adds
  none at all).
- Backend-only change (`internal/db`, `internal/secrets`) — no UI, no i18n
  keys, no help topic, no compliance wording touched, so those guards and the
  manual/screenshot rule don't apply.
- No real client/shop name or literal credential anywhere in the diff (test
  fixtures use `http://primary.local`, `till-2`, placeholder strings only).

## Cross-repo follow-up owned by this change

- `ut-docs/adr/0082-...md` amended in the same session (ADR correction,
  Related line) — not a separate follow-up, landed alongside this PR.
- ut-docs#1753 opened for finding 4 (deferred, non-blocking).
