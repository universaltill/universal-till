# Code review: TSE reseller provisioning setup-wizard step (ut-docs#802)

**Date:** 2026-08-25
**Card:** ut-docs#802 (`universal-till` half of ADR-0053; `ut-cloud` half, ut-docs#801, already merged as ut-cloud PR #55)
**Complexity:** hard — build: Fable (subagent), review: Opus (fresh-context subagent, isolated worktree)

## What shipped

The till-side half of ADR-0053's cloud-mediated TSE reseller provisioning
flow:

- A new DE-only setup-wizard step (`web/ui/pages/setup.html`) collecting
  business identity + tax number, with the ADR-0045 §3 PUK-custody
  disclosure ("Universal Till — not you — holds this device's admin
  credential") and structurally no bank-details field (a closed
  `tseBusinessIdentity` struct with four fields, none of them banking).
  Skippable — an all-blank submission is a no-op, not an error.
- Offline-first kickoff (`internal/pages/setup_tse.go`): pending state
  persisted before one 5s time-boxed synchronous attempt against
  `ut-cloud`'s `/api/v1/stores/fiscal/tse/provision`, then a background
  retry ticker mirroring `setup_base_plugins.go`'s exact idiom, surfaced
  as a dismissible Settings chip.
- A new payload-less `fiscal_tse_ready` cloudsync directive
  (`internal/cloudsync/cloudsync.go`): on receipt, fetches the operational
  credential exactly once from the single-use cloud endpoint and stores it
  via a new till-side secret-storage primitive
  (`internal/fiscal/tse_credential_store.go` — 0600 file / 0700 dir under
  `paths.Data("fiscal", ...)`, mirroring `oauth/token_client.go`'s
  convention). `fiscal.tse_configured` is set true only after the stored
  credential is confirmed readable back off disk.
- i18n keys added to all four locales (en/ar/fa/tr); the manual topic
  claiming `/setup` (`web/help/*/users.md`) updated with the new step;
  screenshots regenerated via `make docs-shots`.

## Independent review (Opus, fresh-context subagent, isolated worktree)

Ran build/vet/gofmt/full test suite and all 16 CI-blocking guards from
`.github/workflows/ci.yml`'s `build` job — all green. Independently
re-verified three TDD claims by reverting the specific production-code
fix, re-running the relevant test, confirming a real (non-compile-error)
failure, then restoring:

1. `fiscal.tse_configured` only set after confirmed storage — reverted to
   an optimistic set, three tests failed with meaningful assertions.
2. `os.MkdirAll` before the credential write — removed it, three tests
   failed with a real "no such file or directory" error (first attempt hit
   a masking compile error from an unused import, correctly caught and
   re-run properly).
3. `paths.Data(...)` vs. a cwd-relative path — reverted, the default-path
   test failed and a stray directory appeared in the source tree, a live
   demonstration of the bug class.

Also confirmed structurally: no bank-details field on the wire, no
forbidden compliance-certification wording in any new copy (verified by
running `guard-compliance-claims.sh` and reading the new prose directly —
capability-descriptive throughout, no "GoBD-compliant"/"audit-proof"/
"revisionssicher"/§146a-AO-notification claim), the four changed
screenshot PNGs (`alerts`, `designer`, `translations`, `invoices`) are
non-deterministic capture noise from a full `make docs-shots` regen
(pixel-diffed: a log-row timestamp, a receipt-preview date, antialiasing —
`manifest.json` shows only the `users` topic's content hash actually
changed), and the unknown-directive-type rejection path is unaffected by
the new `fiscal_tse_ready` case.

## Finding — fixed before merge

**Blocking (fixed):** the idempotent-re-serve fast path in
`applyFiscalTSEReady` checked `store.Exists()` (a `os.Stat` only) rather
than reading the credential back. Combined with `Save` using a plain
`os.WriteFile` (which opens `O_CREATE|O_TRUNC`, so a write that failed
partway — full disk, IO error — left a zero-length file behind), a failed
store could leave a file that `Exists()` reported as "already stored,"
short-circuiting the directive handler straight to
`fiscal.tse_configured=true` over a credential nothing could ever read
back, with the directive acked and no retry. Confirmed empirically by the
reviewer (throwaway probes, since removed) before being handed back for a
fix.

Fixed two ways, together:

1. `internal/fiscal/tse_credential_store.go`'s `Save` is now
   write-tmp-then-rename instead of a direct `os.WriteFile` to the final
   path — `os.Rename` is atomic on the same filesystem, so the final path
   only ever holds "absent" or "fully written," never partial.
2. `applyFiscalTSEReady`'s fast path now calls `store.Load()` and requires
   `ok && len(cred) > 0`, the same confirmed-readable bar the success path
   already held itself to — belt-and-braces on top of (1), since the fast
   path shouldn't depend on `Save`'s atomicity alone to stay correct.

Two new regression tests pin this: `TestTSECredentialStoreSaveFailureLeavesNoPartialFile`
(forces a write failure via a directory collision at the `.tmp` path —
works under a root-run test, unlike a permission-bit failure — asserts no
file survives at the final path) and
`TestApplyFiscalTSEReady_CorruptExistingFileIsNotTreatedAsStored` (seeds a
zero-length file at the credential path, confirms the directive still
performs a real fetch and only configures after a genuine, readable
store). Also softened an overstated doc comment claiming an "allowlist-
based" support-bundle collector exists today — it doesn't; the exclusion
holds by absence of any collector walking that path, which the existing
`TestTSECredentialExcludedFromSupportBundle` pins for today but isn't a
substitute for re-checking this if a real collector is ever added
(reviewer's non-blocking note, addressed while already touching the file).

Re-ran the full gate after the fix (`gofmt -l .`, `go vet ./...`,
`go test ./...` — all packages, zero failures — plus every guard script
listed above) rather than just the touched packages, per this pipeline's
own re-run-the-whole-gate-on-a-finished-diff convention.

## What was verified beyond automated tests

- Structural: kickoff request body is a closed struct with no bank field
  (cannot smuggle one in even by accident).
- `guard-compliance-claims.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` all re-run clean after the fix (the docs-shots
  manifest hash needed a `make docs-shots` re-run after the fix touched
  `internal/pages/setup_tse.go` — no visible screen changed, the guard is
  a surface-hash check, not a pixel check).
- Two independent TDD-revert verifications (Dev subagent's original three,
  plus this review's own three) never overlapped in what they proved,
  giving genuinely broader coverage than either pass alone.

## Explicitly deferred / follow-up

- The 23 new `en.json` keys need matching entries in the external
  `ut-plugin-language-de`/`-es` packs (`lang-pack-drift` CI's existing
  advisory-then-blocking flow, per `CLAUDE.md`) — this bites harder than
  usual here since the step is DE-only, so a DE shop is exactly the
  audience that would see English fallback strings until the pack lands.
  Not a blocker for this PR (out of this repo's tree); flagging on the
  issue for the next `ut-plugin-language-de` pass.
- Non-blocking note (reviewer, not fixed): `tseCloudPostBody` reads the
  cloud response body without an `io.LimitReader` cap — consistent with
  the existing `cloudsync` precedent it follows, not a regression
  introduced here, so left as-is rather than widening this PR's scope.

## Verdict

Safe to merge. One blocking issue found and fixed with regression
coverage; full gate green after the fix.
