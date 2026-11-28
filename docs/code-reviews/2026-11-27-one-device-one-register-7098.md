# Code review: one device = one register, ghost tills (ut-docs#7098, epic #7097), 2026-11-27

One combined Tester + one combined Reviewer (fresh agents, not the Dev). Scope: universal-till branch feat/7098-one-device-one-register, ut-cloud PR #1196.

## Verdict
Changes requested, then fixed. All findings below are resolved on the branch; full local gate re-run green (go vet, internal/pages, internal/plugins, data, db, tills, enroll, registeraudit, cmd, guards). docs-shots regeneration needs the Linux reference environment (macOS fonts refuse): left for the Linux lane.

## Findings and resolution
1. Blocker: migration 130 failed at boot when two tills shared a device_id (unique index after backfill). Fixed: newest enrolment keeps the link, older get none; test with 3 tills on one device.
2. F0 /registers missing from reservedPageRoutePrefixes (plugins test failed). Fixed.
3. F1 guard-deadcode-baseline: cmd/register-audit added as a root. Fixed.
4. F2 BackfillDeviceID had no production caller and no safe source (a self-asserted id must not be trusted). Deleted.
5. F3 cloud remove with the cloud flag off showed the generic error. New plain-words key. Fixed.
6. Nit: approved-replace branch linked the register to the new device when the device-id move failed. Now it does not and audits till_device_move_failed.
7. Nit: manager PIN checked before main-till and flag checks (burned PIN attempts). Reordered.
8. Old till against the new cloud list decodes (TestFleetOldReaderAgainstNewCloudPayload).
9. Audit CLI: empty lists as [], this till not reported as a gap.

## Open (not defects of this change)
- A cloud device row without till_id is not shown on Other tills (existing behaviour); the owner checks that the two ghosts have till ids in the real list before removal.
- tr/fa show the new English copy until Weblate translates it (ADR-0160).
- The manager-PIN box was not driven in the real app; covered by Go tests.
Full notes: temp/epic-pairing/{tester,review,dev-till}-notes.md.
