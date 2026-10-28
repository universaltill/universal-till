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
  paths) and `POST /v1/stores/register`; `approval_key` + `approval_kid` in
  every /sync device report, main and additional till alike (fix H2). No
  usable key reports nothing and never blocks anything.
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
| 2 | major | The dedicated key-report call (`POST /v1/stores/devices/approval-key`, at start and after enrol) is not implemented; redeem and `devices/register` bodies carry no key. | Deferred on purpose: the cloud endpoint does not exist until CL-2a, and the /sync report already carries the key every check-in. Needs its own card (client call + Settings → Cloud conflict text, with UT-1b). |
| 3 | minor | No fsync around the rename: a power cut could leave a short file that reads as unusable forever. | Fixed (file and directory fsync). |
| 4 | minor | Vector lacked the empty-store PoP actually sent at pair/register. | Fixed (both forms pinned). |
| 5 | minor | Loose pre-existing dir/file modes never tightened. | Fixed (`tightenMode`, tested). |
| 6 | nit | With a nil config the /sync PoP is signed with an empty store id. | Unreachable in production; accepted. |
| 7 | nit | `approval_kid` duplicates `approval_key.kid`. | Intentional (design H2 names `approval_kid`); cloud contract must say which it reads. |

Also: `Report` runs a one-time `selfCheck` (sign a probe through
`SignApproval`, verify against the reported key) so the signer is reachable
for the whole-program deadcode baseline guard until UT-1b lands; remove it
there.
