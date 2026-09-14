# Code review: wire `ManifestVerifier.VerifyArtifact` at install time (ut-docs#2241)

**Date:** 2026-09-14
**Branch:** `fix/2241-wire-verify-artifact-staged-install`
**Card:** universaltill/ut-docs#2241 — "Delete-or-wire
`ManifestVerifier.VerifyArtifact` at `InstallFromStore` (staged-install
checksum gap)"
**Complexity:** medium (`security` label)
**Review model:** Opus, independent of the Sonnet implementation, per
`MODEL-ROUTING.md`'s medium-tier routing. Follow-up to
`2026-09-13-deadcode-plugins-slice-1566.md`, whose own independent review
surfaced this gap.

## What shipped

`installBundleFile` (`internal/plugins/installer_marketplace.go`) — shared by
the direct marketplace install (`Install`) and the staged install
(`DownloadToStore` → `GetStoreDownload` → `InstallFromStore`) — now re-hashes
the bundle file's actual bytes on disk against the marketplace-issued
`spec.Checksum` **before extraction**:

```go
if err := i.verifier.VerifyArtifact(spec.BundlePath, spec.Checksum); err != nil {
    return nil, fmt.Errorf("verify bundle file: %w", err)
}
```

Previously the function verified only what came *out* of the archive: the
extracted manifest's Ed25519 signature, its self-declared `ArtifactHash`
(attacker-controlled — it lives inside the bundle), and that the manifest
signature matched the marketplace metadata. Nothing re-hashed the file
itself, so on the staged path a bundle could sit on disk for up to
`storeDownloadTTL` (48h) between `DownloadToStore`'s streaming checksum and
`InstallFromStore`, with only `GetStoreDownload`'s `os.Stat` in between.

Also in the diff:

- `VerifyArtifact`'s doc comment rewritten (it previously documented itself
  as having no production caller, and pointed at this card as the follow-up).
- `scripts/ci/deadcode-baseline.txt`: the `VerifyArtifact` entry removed — it
  now has a real caller.
- New regression test `TestInstallBundleFileRejectsSwappedStagedBundle`
  (`installer_branches_test.go`): stages a genuinely signed bundle, overwrites
  the staged file's bytes on disk, keeps the **original** checksum as
  `spec.Checksum`, asserts rejection with a checksum-mismatch error and that
  no plugin directory is created.
- `TestInstallBundleFileRejectsCorruptArchive` updated: its placeholder
  `Checksum: "x"` replaced with the real checksum of its corrupt bytes, so it
  still reaches the extraction failure it is actually about rather than
  tripping the new earlier check. (The three other `installBundleFile` branch
  tests already passed real `checksumSHA256Hex` values and needed no change.)

### Delete-or-wire: resolved as "wire", broader than the card asked

ut-docs#2241 offered "wire it at `InstallFromStore`" or "delete it", with
"take the safer option if in doubt, per this repo's standing security-first
rule." Wiring is the right call and matches `CLAUDE.md`'s plugin rule ("Installed
plugins are Ed25519-verified before they run... Never run an unverified
plugin").

The implementation places the call in `installBundleFile` rather than in
`InstallFromStore`, which is a **superset** of what the card asked: both
install paths now get it. Reviewed and accepted as the better placement — it
is the single choke point every marketplace install goes through, so the
check cannot be bypassed by a future third caller, and putting it in
`InstallFromStore` would have left `installBundleFile` a function whose
safety depends on which caller reached it. The cost is one redundant
full-file SHA256 on the direct path (≤200 MB cap, file freshly written and
almost certainly still in page cache); the code comment says so honestly
rather than overclaiming.

## Independent review — verdict: **APPROVE**

Two small things fixed directly by the reviewer (below); nothing blocking.

### Verified, not taken on the implementer's word

**The TDD claim is real** — re-run first-hand, revert→test→restore as one
atomic command (never across a turn boundary), by restoring
`installer_marketplace.go` from `HEAD` while keeping the new tests:

```
--- FAIL: TestInstallBundleFileRejectsSwappedStagedBundle (0.10s)
    installer_branches_test.go:170: swapped staged bundle accepted:
        extract plugin bundle: gzip: invalid header
--- PASS: TestInstallBundleFileRejectsCorruptArchive (0.10s)
```

Exactly as claimed: without the production change the new test fails, and it
fails at *extraction*, not with a checksum error — so it is not a tautology
that would pass on any error. `TestInstallBundleFileRejectsCorruptArchive`
still passes against the pre-fix code too, confirming its checksum edit is
backward-compatible and not hiding a behaviour change.

**The deadcode-baseline edit is correct, verified with the real tool** —
not hand-waved. `scripts/ci/guard-deadcode-baseline.sh` needs GTK/WebKit dev
headers (the `desktop`-tagged whole-program pass) that this sandbox lacks, the
same documented gap as the ut-docs#1566 slice. Ran the closest local
substitute the prior slice used — the real
`deadcode@v0.48.0 -test=false . ./cmd/unitill-uninstall`, skipping only the
desktop root: 70 unreachable functions reported, **`VerifyArtifact` no longer
among them**, while every other `internal/plugins` baseline entry
(`ComputeSHA256`, `EventBus.*`, `ListPluginPermissions`, `CatalogList`,
`GetRevokedPlugins`, `GetVersionHistory`, the `marketplace` subpackage ones)
still is — so the tool is working and the single removed line is the only
thing that changed. Note also that this guard is **one-directional**: a
baseline entry that disappears is an informational "burned down" message, not
a failure, while an entry that is still unreachable and *not* in the baseline
fails the PR. So if the wiring were somehow invisible to the real
desktop-tagged run, CI would fail loudly rather than pass silently — the
baseline edit is self-checking in CI.

**No format/normalisation regression.** The obvious way this change could
break real installs is a checksum-format mismatch: `installBundleFile`
already normalises elsewhere (`normalizeMarketplaceChecksum(manifest.ArtifactHash)`),
so a `sha256:`-prefixed or upper-case `spec.Checksum` would now fail. Checked:
`VerifyArtifact` compares raw lower-case hex with no normalisation, and
`DownloadManager.downloadOnce` compares `hex.EncodeToString(...) != req.ExpectedChecksum`
with *identical* semantics against the same `tokenResp.ChecksumSHA256` value
that reaches `spec.Checksum` on both paths (direct: passed straight through;
staged: round-tripped verbatim through the download metadata JSON). Anything
the new check rejects would already have failed the download. No new rejection
class for legitimate bundles.

**Empty `spec.Checksum` fails closed** (explicitly checked, since an empty
expected value compared with `!=` is the classic vacuous-pass shape).
`VerifyArtifact`'s computed checksum is always 64 hex chars, so it can never
equal `""` — an empty expected checksum is a rejection, not a pass. Both
producers also validate non-emptiness up front (`Install` and
`DownloadToStore` both reject incomplete download metadata, and
`DownloadManager.Download` refuses an empty `ExpectedChecksum` outright), but
`GetStoreDownload` does **not** re-validate the field it reads back off disk,
so the fail-closed property is what actually protects the staged path. Pinned
with a test (see fixes below) rather than left as an implicit invariant.

**No third caller.** `installBundleFile` has exactly two production call
sites (`installer_marketplace.go:154`, `installer_store.go:186`) and
`VerifyArtifact` exactly one (plus `TestVerifyArtifact`). Nothing else writes
into `storeDownloadsDir()` — only `DownloadToStore` (via
`PromoteToPermanent`) creates staged bundles, so no LAN-sync or peer-supplied
checksum reaches this path.

### Fixed directly by the reviewer

1. **Error prefix was path-inaccurate.** The wrap read
   `verify staged bundle: %w`, but the check now runs on the direct install
   path too, where nothing is staged — `internal/pages/plugins_store_page.go`
   surfaces installer errors verbatim (`install failed: %v`), so a direct
   install failing this check would have told an operator (or a support
   engineer reading the log) to go hunting for a staged download that does not
   exist. Changed to `verify bundle file:`. No test asserted on the old
   string; the regression test asserts on `checksum mismatch`, which comes
   from `VerifyArtifact` itself.
2. **Added an empty-expected-checksum assertion to `TestVerifyArtifact`**
   (`misc_coverage_test.go`). The function's fail-closed behaviour on `""` was
   previously incidental; it is now security-load-bearing for exactly the
   tamper case this card is about (a staged bundle swapped *together with* a
   truncated/blanked metadata JSON), and nothing pinned it.

Full gate re-run after both fixes, not just before them (see Gate below).

### Non-blocking findings — accepted, documented, not fixed here

- **The residual window this does not close, stated plainly.** `spec.Checksum`
  for the staged path is read from `downloads/<listing>.json`, which sits in
  the same directory, with the same permissions, as the bundle it describes.
  An attacker who can overwrite the bundle can generally also rewrite that
  JSON's `checksum_sha256` **and** `signature`, at which point the new check
  passes. What still stops that attack is unchanged and unweakened: the
  extracted `manifest.json` must carry a valid **Ed25519 signature over the
  marketplace's public key** (`VerifyManifest`), which cannot be forged. So
  this change closes the "bundle swapped, metadata untouched" window — the one
  the card describes — and the Ed25519 signature remains the real trust
  anchor, not the checksum. Worth being precise about, because the reverse
  reading ("the checksum check is what makes staged installs safe") would be
  wrong. A *signed-bundle substitution* (staging some other legitimately
  signed bundle plus its matching metadata — e.g. an older version of a real
  plugin) is caught by neither before nor after; that is a separate,
  pre-existing question about version pinning, not a regression here.
- **TOCTOU between the hash and the extraction.** `VerifyArtifact` opens and
  hashes `spec.BundlePath`, then `extractMarketplaceTarGz` re-opens the same
  path — a swap in that window is undetected. Accepted: the window shrinks
  from *hours-to-days* (download → install, the actual bug) to *sub-second
  in-process*, and the threat model already concedes a local writer with
  access to `pluginBaseDir` (per ut-docs#2241's own severity note,
  `wasm_runtime.go` performs no load-time hash or signature check, so such an
  attacker can tamper post-install regardless). Hashing from a single held
  file descriptor would close it properly, but that is a larger refactor of
  the extraction path and clearly beyond this card.
- **A bundle that fails this check stays on disk.** `InstallFromStore` only
  calls `DeleteStoreDownload` on success, so a tampered staged bundle keeps
  failing every retry until the 48h `sweepStoreDownloads` TTL. Not a
  regression (pre-fix it failed at extraction/signature, equally without
  cleanup) and the operator has a real recovery path today — the store page's
  `POST /api/plugins/store/delete-download`. Judged not worth a backlog card.
- **Installer errors reach the operator un-localised.** `plugins_store_page.go`
  formats every installer error into the response with `%v`. Pre-existing for
  the whole error class (signature mismatch, incompatible architecture, …);
  this diff adds one more member to it and changes nothing structural.
  `guard-i18n.sh` passes.

### Correction to the implementation notes handed over

The hand-off stated the `guard-deadcode-baseline.sh` GTK/WebKit limitation is
"documented in this repo's `STANDING-CONTEXT.md`". No such file exists in
either `universal-till` or `ut-docs`. The limitation *is* genuinely documented
— in `scripts/ci/guard-deadcode-baseline.sh`'s own header comment, and in
`docs/code-reviews/2026-09-13-deadcode-plugins-slice-1566.md` — so the
substance holds and the routing-around concern does not apply; only the
citation was wrong. Recorded so the next cycle doesn't go looking for a file
that isn't there.

The hand-off's other claims all checked out first-hand, including the
pre-existing, unrelated wazero JIT crash under `-race` in
`TestHostHTTPRetryThenFreshCallNotCached`: `.github/workflows/ci.yml`
deliberately never runs `-race` on `internal/plugins`, so it is not a
CI-relevant regression from this diff. Not re-run here; no cause to doubt it.

## Verified beyond automated tests

- Read `installer_marketplace.go`, `installer_store.go`, `manifest_verifier.go`
  and `download_manager.go` end to end, tracing `tokenResp.ChecksumSHA256`
  through both paths to `spec.Checksum` to confirm the format and
  non-emptiness reasoning above rather than assuming it.
- Re-ran the TDD revert/restore first-hand (result quoted above).
- Ran the real `deadcode` tool to confirm the baseline removal, instead of
  accepting the hand-verified call site.
- Read ut-docs#2241's own body to confirm the "wire, don't delete" direction
  and that the broader placement is a superset of, not a deviation from, what
  it asked for.
- Confirmed the test helpers do what the test's comments claim:
  `signedMarketplaceArtifactWithManifest` produces a genuinely Ed25519-signed
  bundle and populates `manifest.Signature`, so the swapped-bundle test would
  really have reached extraction and installed pre-fix; and
  `newTestMarketplaceInstaller` points `pluginBaseDir`/`downloadTmpDir` at
  `t.TempDir()`, so the "no plugin directory created" assertion is a real
  filesystem assertion against an isolated root, not against `paths.Plugins()`.
- Confirmed nothing else in the repo writes into `storeDownloadsDir()`.

## Gate (re-run after the reviewer's two fixes)

```
gofmt -l internal/plugins/                          clean
go vet ./internal/plugins/...                       OK
go build ./...                                      OK
go test ./internal/plugins/...                      ok (4 packages, ~95s)
bash scripts/ci/guard-data-access.sh                PASS
bash scripts/ci/guard-i18n.sh                       PASS
bash scripts/ci/guard-help-topics.sh                PASS
deadcode@v0.48.0 -test=false . ./cmd/unitill-uninstall
                                                    VerifyArtifact no longer
                                                    unreachable; no new entries
```

`scripts/ci/guard-deadcode-baseline.sh` itself was not runnable here (needs
GTK/WebKit dev headers for its `desktop`-tagged whole-program pass) — real CI
is the gate, and the rootless run above is the closest local substitute.

## No `CLAUDE.md` rule at risk

No SQL outside `internal/data`/`internal/db` (no queries added). No money type
touched. No i18n-visible string or locale key added. No UI surface, kiosk
path, or help topic affected — the only operator-visible change is one more
failure mode on an install that was already failing. No ADR implicated: the
plugin trust chain is strengthened, not redefined, so ADR-0007's
document-first rule does not trigger a superseding ADR.

## Safe to merge

Yes. Behaviour change is a strict tightening on a path that was previously
unverified, with no new rejection class for legitimate bundles (argued from
`DownloadManager`'s identical comparison semantics, not from test evidence
alone), a regression test proven to fail without the fix, and the dead-code
baseline edit independently confirmed with the real tool.

## Explicitly deferred

- TOCTOU between hash and extraction (single-fd extraction) — accepted,
  rationale above.
- Bundle substitution with a matching rewritten metadata JSON — bounded by
  Ed25519 manifest verification; version-pinning is a separate question.
- Localising installer errors on the plugin-store page — pre-existing, whole
  error class, out of scope for this card.
