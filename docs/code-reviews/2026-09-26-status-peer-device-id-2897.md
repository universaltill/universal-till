# Review — status peers carry the cloud device id (ut-docs#2897)

**Change:** a replica's fleetlink hello gains optional `cloud_device_id`
(its #2730 per-till cloud identity; clipped to 64 and charset-checked,
garbage dropped); the main till's cloud-link `status` frame sends it as
`device_id` per peer next to `till_id` (LAN pairing id, kept for compat).
ut-cloud: `tilllink.PeerStatus.DeviceID` (clipped + charset-checked),
`livePeerStatus` mirror. my.: the Live panel names a peer by `device_id`,
falling back to `till_id`. Till peer cap lowered 64 → 32 (ADR-0114's
32-links-per-main limit) so the worst-case frame fits ADR-0117 §7's 16 KiB.
Authors: Sonnet (till), Opus 5.5 (cloud, my., fixes). Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Cloud clipped `device_id` but didn't charset-check it (till does) | charset check added; test |
| 2 | minor | Worst case 64 peers × 5 × 64 B ≈ 21 KiB > 16 KiB ReadLimit → cloud closes, till loops (pre-existing headroom issue) | till cap 32; test asserts the worst-case encoded frame ≤ 16 KiB (failed at 64) |

**Checked, no issue (reviewer):** LAN compat both ways (non-strict JSON;
old replica → empty); the id is display-only — a spoofed peer id can at
worst borrow another till's name in the same store's Live panel (tenant-
scoped lookup), never authorisation (the main's own row uses the
authenticated socket id); struct conversion field order identical; React
escapes text; not-enrolled replica → empty → fallback to `till_id`.

**Verification:** universal-till `go build ./...`, race fleetlink/cloudlink,
pages `CloudLink|Status|Link`; ut-cloud tilllink + api tests; ut-my-shop
`npm run check` (357 tests) + typecheck. Deploy order: ut-cloud first (older
cloud ignores the field anyway), then my., till with the next release.

**Verdict:** safe to merge.
