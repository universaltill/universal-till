# Review: off-site backup fixes, till side (#6607, #6608, #6612)

Independent review (Opus) of the till and cloud diffs: no blocking finding.
- Fixed: reason-less `device_revoked` (what the cloud sends for a plain removal) now deletes the key file (test `TestReasonlessDeviceRevokedDeletesTheKeyFile`).
- Deferred: approval expiry (timestamp instead of flag), my. button for the new-key approval, owner notice on displaced backups, 15-min window on slow 2 GiB downloads. Follow-up cards.
- Mutation check: each new test fails without its fix (dev agent, per-fix revert).
Pre-flight: build, vet, offsitebackup/enroll/pages tests green; internal/plugins timeout is pre-existing.
Verdict: merge.
