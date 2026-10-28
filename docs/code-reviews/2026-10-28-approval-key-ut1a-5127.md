# Review — till approval key and its reporting (UT-1a of ut-docs#5127)

Date: 2026-10-28 · Branch `feat/5127-ut1a-approval-key` · Built by Sonnet
(dev, TDD); reviewed by an independent Opus subagent (security-sensitive:
key custody).

## What shipped

- New `internal/approvalkey`: a per-till Ed25519 key, 32-byte seed in
  `paths.Data("secrets", "approval-ed25519.key")` (dir 0700, file 0600,
  fsynced temp-file-then-rename, never rotated in place, an unparsable file
  logged once and left alone). `KID` = first 16 hex of SHA-256 over the
  public key. `ApprovalMessage` / `SignApproval` build the design's §3
  domain-separated bytes (`universaltill/till-move-approval/v1`, refusing
  empty fields, line breaks and any decision but approve/decline);
  `PopMessage` / `Report` give `{kid, public_key, pop}`.
- Golden vector `internal/approvalkey/testdata/approval-v1.json`, generated
  independently with Python `cryptography`: seed, public key, kid, approve
  and decline messages + signatures, and the PoP for both the /sync form and
  the pair/register form. **ut-cloud CL-2a/CL-2b must assert the same bytes**
  from a byte-identical copy in their `testdata/`.
- Reporting: `approval_key` in the `POST /v1/stores/pair` body (both pair
  paths) and `POST /v1/stores/register`; `approval_kid` (only the key id) in
  every /sync device report, main and additional till alike (fix H2). No
  usable key reports nothing and never blocks anything. The key itself
  reaches an already-paired cloud by the till's own `POST
  /v1/stores/devices/approval-key` (see the M-1 follow-up below); /sync alone
  never reports the key, because the cloud decodes only `approval_kid` from it.
- `TestApprovalKeyNeverTravelsInBackups` (internal/db): the key is absent from
  the snapshot in four encodings, stays outside the backups folder, and a
  restore onto another device (the replace-main-till path) neither brings it
  nor changes the receiving till's own key.
- Test hygiene: `internal/enroll` and `internal/pages` get a throwaway data
  root (`TestMain`) because every pair, register and check-in now creates the
  key; `**/data/secrets/` is gitignored.

## Findings (Opus)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Pages tests wrote a real key into the repo's `./data/secrets/`, not ignored by git. | Fixed: pages `TestMain` sets a temp data root; `.gitignore` rule; stray file deleted. |
| 2 | major | (superseded by M-1 below) The dedicated key-report call (`POST /v1/stores/devices/approval-key`, at start and after enrol) is not implemented; redeem and `devices/register` bodies carry no key. | Deferred on purpose: the cloud endpoint does not exist until CL-2a, and the /sync report already carries the key every check-in. Needs its own card (client call + Settings → Cloud conflict text, with UT-1b). |
| 3 | minor | No fsync around the rename: a power cut could leave a short file that reads as unusable forever. | Fixed (file and directory fsync). |
| 4 | minor | Vector lacked the empty-store PoP actually sent at pair/register. | Fixed (both forms pinned). |
| 5 | minor | Loose pre-existing dir/file modes never tightened. | Fixed (`tightenMode`, tested). |
| 6 | nit | With a nil config the /sync PoP is signed with an empty store id. | Unreachable in production; accepted. |
| 7 | nit | `approval_kid` duplicates `approval_key.kid`. | Intentional (design H2 names `approval_kid`); cloud contract must say which it reads. |

Also: `Report` runs a one-time `selfCheck` (sign a probe through
`SignApproval`, verify against the reported key) so the signer is reachable
for the whole-program deadcode baseline guard until UT-1b lands; remove it
there.

## Strict review, finding M-1 (fixed)

The first version claimed "the /sync report already carries the key every
check-in". False: ut-cloud CL-2a (PR #713) decodes only `approval_kid` from the
/sync device report and drops `approval_key`, and the till never called the
report endpoint, so an upgraded till pinned nothing (found against the cloud's
`stores_approval_key.go`, `docs/openapi.yaml` and `pop_vector.json`).

Fix: `internal/enroll/approval_report.go` POSTs `{store_id, approval_key}` to
`/v1/stores/devices/approval-key` with the device bearer. The PoP's store line
is the store's external id on this endpoint (empty only on pair/register), so
`Report(deviceID, storeID)` is called with the live store id. When: once per
start (end of `Init`), and after pair, register and device registration;
at most once per identity (device + store) per process, in the background,
never blocking. A failure (offline, 5xx) retries slowly (15 min, 1 h, 6 h) then
waits for the next start. 404 (older cloud), 403 (legacy token) and 429 are
logged at debug and ignored; 409 is logged once and remembered
(`enroll.ApprovalKeyConflict`) for the future Settings > Cloud text (no UI
built here). The dead `approval_key` field is gone from the /sync report;
`approval_kid` stays on every sync (H2). `Store.KeyID` supplies the kid.

Tests: cloud's `pop_vector.json` copied byte-for-byte into
`internal/approvalkey/testdata` and asserted (`TestCloudPopVector`); stub-cloud
tests assert one call, path, bearer, store line and a PoP that verifies with
hand-assembled bytes, 404/403/429 harmless and not retried, 409 remembered,
offline harmless, once-per-identity dedupe, bounded slow retry; the pages tests
assert sync carries the kid only. `selfCheck` stays: `SignApproval` is still
reachable only through it until UT-1b (deadcode baseline guard).

Not done (no signal exists): re-reporting "when the sync answer shows the key
is missing" - the cloud's sync answer carries no such field.
