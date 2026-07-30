# Code review — coverage batch 5: `internal/plugins` (48.3% → 82.1%)

- **Date:** 2026-07-30
- **Branch:** `test/coverage-internal-plugins`
- **Author:** SDLC pipeline (fable), cycle 11 of the coverage push
- **Independent reviewer:** different model (opus), full report summarized below
- **Verdict:** **SAFE TO MERGE** — no blocking findings; 3 nits, one fixed
  pre-commit, two accepted with rationale.

## Scope

Coverage batch 5 of the [[QUEUE.md]] test-coverage push: `internal/plugins`,
the till-side plugin platform (marketplace installer, store download flow,
manual importer, download manager, revocation, rollback, update checker,
manager/catalog, wasm runtime sync, event bus bookkeeping). Hermetic
throughout: real migrated sqlite DBs (`appdb.Open` on temp files), httptest
marketplace servers, a real compiled wasip1 guest module, `t.TempDir()`
everywhere; whole package passes with `HTTP(S)_PROXY` poisoned to a dead
port, `-race` clean, all 5 CI guards green, full repo suite green.

## Real bugs found TDD-first (proven red before each fix)

1. **Unsigned manifest passed verification with a public key configured**
   (`manifest_verifier.go`, medium-high). `VerifyManifest` set
   `SignatureVerified=false` for a missing signature but appended no error —
   and the manual-import path (`importer.go`) checks only the error, so an
   unsigned plugin imported cleanly despite CLAUDE.md's "never run an
   unverified plugin". Now fails closed when a key is configured; dev mode
   (no key) unaffected — both directions pinned by tests. The marketplace
   install path was already safe (it separately requires the manifest
   signature to equal the marketplace-issued one), independently re-verified
   by the reviewer reading `installBundleFile`.
2. **Plugin resume download never worked** (`download_manager.go`, medium).
   The resume path opened the `.part` file `O_WRONLY|O_APPEND` and then
   tried to *read* it to seed the SHA-256 — EBADF on every resume attempt,
   since the feature shipped. Found by writing the first real resume test;
   fixed with `O_RDWR`.
3. **Checksum mismatch was retried five times** (`download_manager.go`,
   medium). `isRetryable` compared the whole error string to the literal
   `"checksum mismatch"`, which never matches the formatted message — so a
   tampered/corrupt artifact was re-downloaded with escalating backoff,
   *resuming from the already-complete corrupt `.part` file*. Also
   `err == context.Canceled` equality could never match wrapped errors. Fixed
   with an `errors.Is` sentinel. Side effect: the package suite dropped from
   ~37s to ~8s (existing failure-path tests had been sleeping through real
   backoffs).
4. **The plugin update checker could never find an update in production**
   (`internal/data/plugin_repo.go`, medium). `ListInstalledPlugins` selected
   `'' as author` (hardcoded empty), so `UpdateChecker`'s `author/name`
   catalog key never matched anything — the updates API surface was silently
   dead. Fixed with `COALESCE(author, '')`; six hand-rolled test fixtures
   that had drifted from the real schema (001_init.sql has `author`) were
   aligned. Reviewer confirmed the other three `ListInstalledPlugins`
   consumers ignore `Author`, so the fix can only mend, not break.
5. **MinPOSVersion compared lexicographically** (`manifest_verifier.go`,
   medium). `"0.2.5" > "0.2.49"` — plugins requiring ≥0.2.5 were rejected on
   a 0.2.49 till, and a ≥0.10.0 requirement would be accepted on 0.9.0.
   Fixed with the numeric `compareVersions`.
6. **No bound on decompressed plugin-archive output** (medium). The archive
   *file* was size-checked, but a small zip/gzip bomb could fill the till's
   disk before `checkDiskBudget` (which only walks the tree *after*
   extraction) ever ran. Added a 1GB extraction budget (matching the
   documented per-plugin budget) failing loudly — never truncating silently —
   wired into `extractZip`, `extractTarGz` and the marketplace installer's
   `extractMarketplaceTarGz`. Same bug class batch 2 fixed in selfupdate.

Drive-bys in `importer.go`: pid-derived shared temp extraction dir (two
same-process imports would extract over / delete each other) → `os.MkdirTemp`
(uniqueness is stdlib-guaranteed; no deterministic red test exists for the
race, disclosed rather than faked); a pointless manifest reopen whose
deferred Close was bound to the *old* handle (fd leak) → removed.

## Dead code removed (zero callers, verified by author and reviewer grep)

- `types.go` (`PluginType`/`ValidPluginTypes`/`IsValid`/`String`): a drifted
  19-type duplicate of the ADR-pinned taxonomy — ADR-0002's own text names
  `plugins.CanonicalTypes` (21 types incl. `theme` + ADR-0010's `language`)
  as the single source of truth. Covering the duplicate would have been
  coverage theater; had anyone ever used it, valid theme/language plugins
  would have been rejected.
- `authorizer.go` (`Authorizer`/`CheckPermission`/`AuditLog`): never called,
  and its RBAC logic failed open (restricted actions allowed for any role
  whenever `requireManagerPIN` was false) — a misleading security surface.
  The live `plugins.CheckPermission` (permissions.go) is a different
  function, untouched.
- `install.go` `record{Install,Uninstall,TrustChange}Audit`: unexported,
  uncalled.

## Verification

- **TDD arcs:** all six proven red against pre-fix code, green after.
  Reviewer independently re-proved #1 (incl. confirming the dev-mode
  companion stayed green during the red) and #3 (observed the 6 server hits)
  by revert.
- **Mutation probes:** author 6/6 caught (one probe re-run after producing a
  non-compiling mutant — recorded honestly, not counted). Reviewer ran 3
  fresh probes of their own (bomb-guard threshold weakened, author COALESCE
  reverted, compat `>` → `>=` boundary): 3/3 caught.
- **Hermeticity:** poisoned-proxy run green (reviewer re-ran it themselves);
  no real marketplace, no downloads, no binaries executed beyond the wasip1
  test guest inside wazero.
- **`-race`:** package green (79s).
- **Full repo:** `go build ./...`, `go vet ./...`, `go test ./...`, all 5
  `scripts/ci/guard-*.sh` green.

## Reviewer findings

1. *nit* — `compareVersions` drops pre-release suffixes (`2.5.0-rc1` ==
   `2.5.0`). Pre-existing in the reused helper, consistent with its "simple,
   not full semver" contract. Accepted.
2. *nit* — the repo-level `ListInstalledPlugins` test didn't assert the
   author column (reviewer's own mutation P2 demonstrated it). **Fixed
   pre-commit**: `TestPluginRepo_ListInstalledPlugins_ActiveOnly` now pins
   the author round-trip, and the hardened assertion was re-proven to catch
   the exact `'' as author` regression.
3. *nit* — the "authorizer fails open" characterization is unverifiable
   post-deletion; removal stands on zero-callers alone. Accepted (the
   pre-deletion source is in git history).

## Honestly untestable remainder (documented, not faked)

- `supervisor.go monitorProcess` (12.8%): real child-process restart/backoff
  loop — branches are OS-process/timing glue; the supervisor's observable
  contracts are covered by `supervisor_test.go`.
- `wasm_hostfns.go` guest-memory error branches (`readGuest`/`writeGuest`
  failure rungs) and per-hostcall denial paths not reachable without a
  hostile guest build.
- Local-IO error branches on just-written files (installer/rollback/
  download), same class as prior batches.
- `ipc.go Publish` drop/audit-failure branches requiring precise channel
  saturation timing.
