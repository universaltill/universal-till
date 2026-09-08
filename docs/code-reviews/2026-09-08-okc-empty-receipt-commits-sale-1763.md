# TR ÖKC: an `{"ok":true}` answer with no receipt number could still commit a sale (ut-docs#1763)

## What shipped

Turkey's fiscal-device (YN ÖKC) tender is supposed to be fail-closed: the
certified device must actually print a legal receipt (mali fiş) before a
sale is allowed to complete (`internal/fiscal/device.go`). The reference
`bridge` driver (`plugins/tax-tr/okc/bridge.go`) checked only the wire
response's `ok` boolean before treating a device answer as approved — it
never checked whether the device actually returned a usable `receipt_no`.
A device, or a buggy/malicious bridge process on the shop's LAN, answering
`{"ok":true}` with an empty, missing, or whitespace-only `receipt_no`
would approve the tender and let the sale commit with no real fiscal
evidence.

Fix: `BridgeDriver.Sale` and `BridgeDriver.Refund` now return a new
sentinel error, `okc.ErrNoReceipt`, whenever `strings.TrimSpace(receipt_no)`
is empty, instead of returning success. This flows through the plugin's
existing fail-closed mechanism unchanged (`plugins/tax-tr/main.go`'s
`declined(err)` → exit 1 → till refuses the tender, no sale row created).

Also updated `plugins/tax-tr/README.md`, which documented the pre-fix
behaviour ("evidence without `receipt_no` is ignored") — the wire contract
doc a bridge author would build against now states the requirement
explicitly.

## Independent review

Opus, fresh context, read-only pass in an isolated worktree (no shared
checkout with the orchestrating session). Verdict: **PASS, safe to merge**,
conditional on the README fix above (done before this commit).

The review:
- Re-ran `go build ./...`, `go vet ./...`, and the full affected-package
  test suite (including the real wasip1-compiled plugin binary against
  the simulator) — all green.
- Independently re-verified the TDD claim: reverted only the two
  enforcement blocks in `bridge.go`, confirmed the 3 new bridge-level
  tests and the new plugin-level subtest fail with the exact expected
  error/message, confirmed the other 5 subtests of the same table test
  are unaffected (so the new coverage isn't riding on a sibling's
  assertion), then restored and confirmed all green again.
- Ran its own bypass probe against a fake device (raw TCP responses):
  missing key, `null`, `""`, tab/space/NBSP, and a duplicate-key/last-
  empty case all correctly refuse; confirmed idempotency
  (`TestBridgeSale_IdempotentOnRequestID`) is unaffected and a cached
  receiptless answer declines consistently on retry; confirmed the
  refund leg has no legitimate receiptless flow in the documented
  contract (`internal/data/fiscal_device_repo.go` requires `ReceiptNo`
  for every receipt kind); confirmed no file I/O in the diff (the
  `os.MkdirAll` / `paths.Data` classes of bug don't apply); confirmed no
  real client/shop name or secret-shaped literal was introduced.

## Findings — fixed vs. deferred

**Fixed before merge:** the README's wire-contract section understated
the new behaviour (said invalid evidence is core-side "ignored"); updated
to state the driver itself refuses the tender.

**Deferred to Backlog** (each genuinely separate scope from this bug fix,
filed as its own card so the finding isn't lost):
- ut-docs#1779 (p2) — core (`pos_api.go`) has no independent fail-closed
  check for a plugin claiming method key `okc` that approves without any
  `fiscal_device` evidence at all; today's guarantee lives entirely in
  plugin code.
- ut-docs#1780 (p3) — the new guard is duplicated per-driver
  (`Sale`/`Refund`) instead of enforced once at `main.go`'s `approve()`
  choke point every `okc.Driver` implementation goes through.
- ut-docs#1781 (p3) — `strings.TrimSpace` (used by both the new guard and
  `DeviceEvidence.Valid()`) doesn't catch a zero-width-space-only
  `receipt_no`, which would pass as "usable" and render blank everywhere.
- ut-docs#1782 (p3, pre-existing, unrelated to this diff) — a numeric
  (non-string) `receipt_no` from a malformed bridge answer fails whole-
  response JSON unmarshal and surfaces as `ErrDeviceUnreachable` instead
  of a clearer, distinct decline reason.

**Accepted, no action:** a bridge that genuinely prints a receipt and then
answers with no `receipt_no` produces a refused tender with a real receipt
in the customer's hand and no sale row — the review confirmed this is the
correct fail-closed trade under Law No. 3100 (same shape as the existing
silent/unreachable paths), not a regression. Bridge protocol v0's lack of
device authentication (plaintext TCP, no signing/attestation) is
explicitly out of scope for this card — this fix closes the trivially-
empty forgery, not the broader trust-model question.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` clean on the touched
  packages.
- `bash scripts/ci/guard-data-access.sh` and `guard-i18n.sh` both pass
  (no data-access or i18n surface touched).
- Full `go test ./...` across the whole repo (not just the touched
  packages) — all green, no sibling-package regression.
- No UI/HTML/i18n-facing surface touched, so no manual/help-topic update
  or screenshot check applies to this diff.

## Safe-to-merge verdict

Yes — independent review passed, TDD claim independently re-verified,
full gate green, deferred findings tracked as their own Backlog cards
rather than silently dropped or scope-crept into this PR.
