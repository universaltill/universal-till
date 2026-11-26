# Review record: off-site backup says why the cloud refuses the till's credential (ut-docs#7058)

**What shipped:** the till's off-site backup no longer reports every cloud 401/403 as `other` or retries every 6 h on a dead device credential. Before this change, the owner's main till showed "failing (other) for 163h0m0s" next to `checkin returned 401`.

- **New reasons.** A 401/403 now maps to one of: `pair_again`, `till_replaced`, `till_moved`, `till_removed`, `service_unavailable` or `owner_approval`.
- **Waiting and self-clear.** A credential refusal waits 24 h, like a 402. The wait ends as soon as a later successful check-in is recorded.
- **Replaced till.** Its gate is now closed.
- **Chip.** The duplicate "Cloud backup failing" chip is hidden when the gate is closed or a refusal is stored.
- **Problem text and logs.** The Problem text names the reason and a readable duration (`6d 19h`). Logs carry the reason and the HTTP status only.
- **Card.** The Settings → Backups card shows a line per reason, plus an "Open Till registration" button.
- **Strings and docs.** 8 new en.json keys with en.meta.json metadata. The help sentence in `web/help/en/backups.md` is updated. ut-docs `reference/design-5066-offsite-backup.md` gains AC1.8a.

**Pipeline:** BA → Architect (Opus) → UX (copy and link row; changes adopted) → Dev (Sonnet) → Tester (Opus) → Reviewer (Opus, a different model from the author).

Pre-flight: web/help topic updated (backups.md); docs-shots n/a (no screenshotted state changed, the line only shows in a refusal state); reference page updated (ut-docs design-5066 AC1.8a, same session); README n/a (not stale); en.meta.json updated for all 8 keys; follow-ups filed as universaltill/ut-docs#7067.

## Tester (independent)

- **Red proof, offsitebackup.** The production files were reverted to main, with a names-only stub so the package compiles. All 8 new offsitebackup tests then failed for the right reason. For example, `refused_reason` was empty, the till called the cloud again with a dead credential, and the text read "(other) for 163h0m0s".
- **Red proof, pages.** The pages card and chip tests failed on main's chip and template.
- **Package runs.** `go test ./internal/pages/ -timeout 40m` passed (746 s). offsitebackup, enroll and cloudsync passed. `golangci-lint run ./...` reported 0 issues.
- **Guards.** 70 of the 71 build-job guards exit 0. The shellcheck guard needs a binary this sandbox does not have, and no `.sh` file was touched.
- **Driven run, visual check.** I served the real mux with `refused_reason=pair_again` and looked at /settings in Chromium at 360x780 and 1024x600.
  - The reason line wraps with no clipping and no horizontal scroll.
  - "Open Till registration" sits on its own row as a 51 px secondary button. Clicking it lands on `#registration`.
- **Not looked at:** dark theme, RTL, German (it comes from Weblate) and a real cloud.

## Review findings


### L1 (low): part of `TestReplacedTillGateIsNotMain` proves nothing

**Where:** `internal/offsitebackup/credential_refusal_test.go:225-235`.

**Problem:** The fixture is not entitled, so the unreplaced gate is `GateNotEntitled` (3). The mutation run showed "gate = 3" once the replaced check was disabled. As a result:
- `got == GateNotMain` would pass for any non-main state.
- `e.round(true)` followed by `authCalls == 0` would pass even with no replaced check, because the round stops on NotEntitled.

Only the middle assertion (`!= GateNotMain` after marking the till replaced) does real work.

**Fix:** Make the fixture entitled (cached plan with `CapCloudBackup`). Assert `== GateOpen` before marking the till replaced, so the round assertion means something.

### L2 (low): `service_unavailable` now has no status-bar signal

**Where:** `internal/pages/backup_copy_chip.go:113`.

**Problem:** Any stored refusal now hides the "Cloud backup failing" chip. For the 401 reasons the cloud-auth chip takes over after 3 consecutive 401s. A 403 `service_unavailable` gets no chip at all: there is only the card line, and the Problem after 48 h. The same applies to the pre-existing 402/409 refusals. Before this change they could show the chip, but only from a stale `failing_since`, so hiding them is arguably a fix.

**Fix:** Accept this, or let `service_unavailable` keep the chip. A UX call.

### L3 (low): the `device_mismatch` mapping is dead code

**Where:** `internal/offsitebackup/uploader.go:99`.

**Problem:** The cloud's `till_backups` handlers never answer `device_mismatch`. Only telemetry, diagnostics, downloads, issue reports and `stores.go:2085` do. If a backup endpoint ever did answer it while check-ins keep succeeding, the till would loop every hour: refuse, clear, retry. It would also show "pair again" text that would not help.

**Fix:** Drop the case, or leave it as a failure with backoff.

### L4 (low): the equal-second boundary is untested

**Where:** `internal/offsitebackup/uploader.go:175`.

**Problem:** If the refusal and a check-in fall in the same RFC3339 second, the refusal is not cleared until the next check-in. The behaviour is harmless and conservative, but no test covers it: the `c.After` → `!c.Before` mutation survived.

**Fix:** Optional: add a test case for an equal timestamp.

### L5 (low): the Problem test can flake on global state

**Where:** `credential_refusal_test.go:137-154`.

**Problem:** The test reads the process-global problem registry. If an earlier test in the package leaves `ProblemOffsiteFailed` open, `raise()` skips the new Problem and the test fails on stale text. It would fail, not falsely pass.

**Fix:** Call `logging.ResolveProblems(ProblemOffsiteFailed)` before the round, not only in Cleanup.

### L6 (low): restore-only codes can show restore text on the card

**Where:** `internal/pages/offsite_backup_api.go:552`.

**Problem:** `cardReasonKey` falls back to `offsiteErrKeys`, which also holds restore-only codes (`busy`, `not_found`, `expired`, …). A 402/409 upload refusal stored with one of those codes would now show restore-specific text. The old template showed the generic line. The cloud's upload refusals do not use those codes today.

**Fix:** Optional: whitelist the upload reasons explicitly.

## Not covered by this review

- golangci-lint and the full `go test ./...`.
- The cloudsync tests (cloudsync was not touched).
- The docs-shots screenshot for the new card line and link. The Tester owns that.

## Triage

- **L1: fixed.** The test now gives the fixture a cached active `shop` plan and asserts `GateOpen` before the till is marked replaced. Re-verified: with the replaced check disabled, the test fails. Restored, it passes, and the package passes.
- **L2–L6: accepted for now.** All are low severity and none is reachable today. They are listed in universaltill/ut-docs#7067 with FU-1 and FU-3–FU-9 from the design. FU-2 is #7057 (re-pairing after a main replace), which is likely the owner's real blocker.

**Verdict: ship.**
