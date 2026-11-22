# Review: detect a till folder copied to other hardware, owe the number jump (ut-docs#6907)

Date: 2026-11-22. Lane `lane:cloud-41`. Builds on ut-docs#6294 (universal-till#2662).

## What shipped

`enroll.CheckRestoredInstall` noticed a restored install only when `secrets/install-id` differed from
the DB's `till_identity.install_id`. A whole folder, disk or SD-card copy that carried `secrets/` looked
exactly like the original, so the copy continued at MAX()+1 and could re-issue receipt and Z numbers.

- `internal/enroll/restored_host.go` (new) `checkHostMove`, called from `CheckRestoredInstall` after
  the install-id check. The DB stores `till_identity.host_tag`: an HMAC (own key, not lantls') of
  the host id read by `lantls.ReadHostID` (Pi board serial, else Linux machine-id; macOS hardware
  UUID; Windows MachineGuid; none on phones). The raw id is never stored or logged.
  - No tag stored yet (first boot after upgrade) → record it, owe nothing.
  - Same tag → nothing (a reboot writes nothing).
  - Host id unreadable, or `UT_HOST_ID=off` → nothing.
  - Pin set or removed (`UT_HOST_ID=<value>`) → re-record without a jump.
  - Different host, main till → owe the #6294 jump under `copied-host:<install id>:<16 hex of the
    new tag>`, then record the tag. A retry of one move reuses the token; a later move gets a new one.
  - Different host, secondary till → the existing `clearClonedIdentity` treatment (provisional
    `R<4hex>-` prefix, bearer cleared, join again), no jump.
- Decisions (Architect, from BA's defaults): A1, a host move only owes the jump, with no cloud
  identity reset, so a false positive (OS reinstall, Mac migration) costs a number gap and nothing more.
  A2, the series prefix is kept, so ADR-0166 is unchanged. No ADR contradicted; none written.
- `internal/lantls/hostid.go`: exports `ReadHostID` and `HostTag`; lantls behaviour unchanged.
- `internal/config`: `HostID` from `UT_HOST_ID`.
- Docs: `web/help/en/backups.md` (what is and isn't detected, the pin, joined tills re-join),
  `make docs-shots` (manifest only), README env line, CHANGELOG.

## Review

- BA and Tester: session model (Opus 5.5). Architect: Opus 5.5. Dev: Sonnet 5.5.
  Independent review: Fable 5.2 (fiscal numbering = high risk), then a scoped second round on Opus 5.5
  because round 1 found a High.
- Round 1 verdict: approve after fixes.

| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | High | An install-id read error during a detected move recorded the new tag, so a real copy never jumped. | Fixed: warn and return without recording; `TestHostMove_InstallIDReadErrorKeepsOwing` (red before the fix, verified by the orchestrator). |
| F2 | Medium | A half-done `remark` (marker written, DB write failed or crash) made the next boot run the #5157 restore path: cloud identity wiped and a second jump. | Fixed: no remark in the host path; the token is unique per move. `TestHostMove_NoRemarkNextBootQuiet` (red before the fix, verified), `_TagWriteFailsRetriesSameToken`, the app test checks the install id, marker and `restored_at` stay put. |
| F3 | Medium | A `UT_HOST_ID` pin copied with the folder, or first set on the new machine, hides a copy. | Fixed in help + README: a different pin per machine, set on the original first. |
| F4 | Low | "Disk image" overclaimed; the Docker sentence was wrong for the shipped image. | Fixed in help, README, CHANGELOG. |
| F5 | Low | A host whose machine-id regenerates every boot burns ≥10 000 numbers per boot, with only a log line. | Deferred → follow-up card (visible notice pointing at `UT_HOST_ID`). |
| F6 | Low | A copied secondary till keeps the original's cloud token until it re-joins. | Accepted (consistent with A1); on the cloud-detection follow-up card. |
| F7 | Low | Two app stacking tests passed without the fix; one dead assertion. | Fixed: both assert the host check ran; dead clause removed. |
| D2 | Low (Tester) | No test pinned the separate HMAC key. | Fixed: `TestHostMove_OwnHMACKey`. |
| D1 | CI (Tester) | Help edited without `make docs-shots`. | Fixed: manifest regenerated, docs-shots guards green. |

Round 2 (Opus 5.5, scoped to the fix diff): **approve**. A retry cannot owe twice (the token comes
from the install id and the current tag, neither changed by the host path; a failed tag write re-owes
the same token → `AlreadyApplied`). Dropping remark leaves the replica path intact. H1→H2→H1 jumps
once per move. Accepted weaknesses: `_EmptyInstallIDStillOwes` never reaches the empty-id branch
(the boot fills the id first), and `_OwnHMACKey` compares against lantls' key rather than reading
the key the code uses; two rare cases (a second gap, a cleared bearer) need a failed settings write
and are unchanged from round 1.

## Extra verification

- Tester mutation run: 9 mutations of `checkHostMove`/`currentHostTag`, each red (the "ignore off"
  mutation was redone at `currentHostTag`); the HMAC-key swap was uncaught, hence D2.
- Driven run (Tester): built binary, temp data dir with `UT_HOST_ID=A`, folder copied, copy booted with
  `UT_HOST_ID=B`. The copy wrote one audit row, receipt floor 11000, Z floor 4, the `copied-host:` token,
  and kept its device id. The original, rebooted 3 times on A, owed nothing. No raw id stored.
  Not done: a real second machine with a different `/etc/machine-id`.
- Gates: `go test ./...` green (`internal/pages`, `internal/plugins` need CI's 20-min timeout under
  load); `-race` on enroll/lantls/config; golangci-lint 0; gofmt, shellcheck clean; 184/189 `build`-job
  bash guard steps green before D1, the rest being D1 (fixed) and `guard-catimport-version` (needs the
  catimport tags fetched; not this change).

Pre-flight: help topic `updated` (+ `make docs-shots`); `reference/` page n/a (no reference page
covers restore detection; the help topic and the code comment carry it); README `updated` (env line);
`en.json` n/a (no new keys, log lines only); ADR n/a (none contradicted, A1/A2 recorded here);
follow-ups `deferred → cards filed at close-out`.

## Deferred

- New receipt series for a detected copy (closes the parallel-original collision after ~10 000
  receipts); needs an ADR-0166 amendment.
- Cloud noticing two devices behind one till identity (also the copied replica's token, F6).
- Visible notice when the host changes on several boots in a row (F5).
- Whole-disk clones on x86 Linux/Windows carry the machine id and stay undetected (hardware-bound id).
- Android 12+ device-to-device transfer: `allowBackup=false` does not exclude `secrets/`; add
  `dataExtractionRules` (unverified on a device).
