# Code review: buttons add/remove elevated actions now audit-logged (ut-docs#2358)

- **Date**: 2026-09-17
- **Card**: ut-docs#2358 (follow-up from ut-docs#2312)
- **Complexity**: easy
- **Author (Dev)**: Sonnet, inline
- **Reviewer**: Sonnet, fresh-context subagent, isolated worktree

## What shipped

ut-docs#2312 gated all four `/api/buttons/*` mutation routes on
`catalog_management`, with `reorder`/`move` writing a dual-attribution
audit row (`InsertAuditElevated`) on a PIN-approved elevation. `add`/
`remove` didn't, because they delegate to `ui.ButtonsHTTP.Add`/`Remove`,
which write their own HTTP response directly with no success/failure
signal back to the caller.

- `internal/ui/buttons.go`: `ButtonsHTTP.Add`/`Remove` now return `bool`
  (true only once the underlying store write actually succeeded; false
  on every early-return/validation failure).
- `internal/pages/buttons_api.go`: the `/api/buttons/add` and
  `/api/buttons/remove` routes now call `auditButtonsElevated` when the
  bool is true AND `elev.Outcome == elevated`, mirroring `reorder`'s own
  `elev.Outcome == elevated` check. Action names `buttons_add`/
  `buttons_remove`, payload keys `snake_case` (`item_id`, not `itemId`
  — fixed post-review, see below).
- `internal/pages/buttons_audit_test.go` (new): four tests —
  `TestButtonsAdd_ElevatedPINWritesAuditRow`,
  `TestButtonsRemove_ElevatedPINWritesAuditRow`,
  `TestButtonsAdd_ElevatedPINButStoreRejects_NoAuditRow` (a rejected
  store write must never audit, even with a valid PIN — proves the `ok`
  gate is load-bearing), and
  `TestButtonsAdd_NonElevatedManager_NoAuditRow` (added post-review — a
  manager's own direct, non-elevated action must not audit either).

## Independent review findings

A fresh-context Sonnet subagent, isolated worktree, reviewed the diff
before this record was written. Findings, all addressed:

1. **[Fixed] Payload key casing.** `"itemId"` in the add audit payload
   broke this codebase's snake_case convention for `data_json` (every
   other `InsertAudit*` call site uses snake_case: `requested_by`,
   `source_till`, `error_code`, …). Renamed to `"item_id"`.
2. **[Fixed] Stale comment.** `auditButtonsElevated`'s doc comment said
   "all four routes" — only three call it (`add`/`remove`/`reorder`;
   `move` no longer exists, per ut-docs#2339). Corrected to "three".
3. **[Fixed] Coverage gap.** No test proved a non-elevated manager
   action writes zero audit rows — the correctness of the whole feature
   rests on `elev.Outcome == elevated` staying exactly that check.
   Added `TestButtonsAdd_NonElevatedManager_NoAuditRow`.
4. **[Noted, not fixed — pre-existing, out of scope]** Audit-write
   ordering differs between reorder (audits before responding) and
   add/remove (necessarily after, since `ButtonsHTTP.Add`/`Remove` write
   the response internally) — both swallow the audit error
   (`_ = posRepo.InsertAuditElevated(...)`), so an abrupt client
   disconnect could silently lose the audit row either way. This risk
   already existed in the reorder code this diff mirrors; not a
   regression. Worth a separate backlog card if stronger guarantees are
   wanted (e.g. logging on `InsertAuditElevated` error) — not filed, low
   value for the risk.
5. **[Verified, no fix needed]** `targetID` choice: add uses `itemID`
   (always non-empty per `ButtonStore.Add`'s own validation, even though
   it isn't the literal persisted barcode when one was synthesized);
   remove uses `code` (the real barcode operated on). Both reasonable
   and consistent with a real incident-review reader's needs.
6. **[Verified]** Acceptance criteria met: `buttons_add`/`buttons_remove`
   follow the same dual-attribution shape as `buttons_reorder`.

## Verified beyond automated tests

- TDD claim independently re-verified twice (once by the review
  subagent, once by this session after applying the review fixes):
  reverted the `if ok := ...; ok && elev.Outcome == elevated { ... }`
  gating back to a bare call, confirmed both `*_WritesAuditRow` tests
  fail with "no rows in result set", restored, confirmed green again.
- Every existing caller of `ButtonsHTTP.Add`/`Remove` (7 sites in
  `internal/ui/buttons_http_test.go`, all discarding the return value)
  still compiles and passes unchanged — Go permits ignoring a return
  value.
- Manual/help-topic check: this is a pure audit-trail/compliance change
  with no new UI surface — `web/help/en/elevation.md` and
  `web/help/en/till-designer.md` already describe the PIN-approval flow
  generically without naming which specific actions are audited, so
  nothing in the manual goes stale. No update needed.

## Full gate (after review fixes applied)

- `gofmt -l .` — empty.
- `go build ./...` — clean.
- `go test ./internal/ui/... ./internal/pages/...` — all green
  (`internal/pages` 132.8s, `internal/ui` cached, others cached/green).
- `golangci-lint run ./internal/ui/... ./internal/pages/...` — 0 issues.
- `bash scripts/ci/guard-data-access.sh` — ✓ (no new raw SQL; uses the
  existing `POSRepo.InsertAuditElevated`).
- `bash scripts/ci/guard-i18n.sh` — ✓ (no new user-facing string).

## Verdict

**Safe to merge.** All review findings addressed (three fixed, one
verified-fine, one noted as a pre-existing, out-of-scope observation).

---
_Generated by [Claude Code](https://claude.ai/code)_
