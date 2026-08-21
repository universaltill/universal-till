# Code review: dismiss-pending-base-plugin canonical_type/locale validation (ut-docs#868)

**Branch:** `feat/868-dismiss-pending-base-plugin-validation`
**Reviewer:** independent, fresh-context Sonnet subagent (complexity:easy tier — see
`scrum-master` skill's "Model routing by complexity")
**Verdict:** No blockers.

## What changed

`POST /api/settings/dismiss-pending-base-plugin` (`internal/pages/settings_page.go`)
used its `canonical_type`/`locale` form fields, unvalidated, in two places:

- Built into a CSS attribute selector for the elevation dialog's retry target
  (`fmt.Sprintf([id="pending-plugin-msg-%s"], canonicalType)`).
- Written verbatim as the `InsertAuditElevated`/`InsertAudit` `entity_id`.

Deferred finding from ut-docs#865's independent review (F6,
`docs/code-reviews/2026-08-21-settings-elevation-slice2-865.md`).

Fix: before `checkOrElevate` runs, the handler now loads the currently-pending
base-plugin specs (`loadPendingBasePlugins`) and rejects with 400 unless the
posted `canonical_type`+`locale` matches one of them — the same
"validate-before-elevate" convention already used by `till-register` and
`payments-fee` in this file. This closes the selector-injection/audit-pollution
gap and avoids burning an approver's PIN entry on a request that was always
going to be a no-op.

## Independent review

Fresh-context Sonnet subagent, read-only pass over the diff plus the
surrounding `internal/pages/settings_page.go` / `setup_base_plugins.go` code.

**No blockers.** Verified:

- Acceptance criterion 1 (mismatched pair → 400 before elevation runs):
  confirmed by reading the code (validation strictly precedes
  `checkOrElevate`) and empirically, by temporarily reverting the fix and
  observing the new regression test fail, then restoring it.
- Acceptance criterion 2 (valid pending pair still dismisses correctly): the
  pre-existing `TestSettingsDismissPendingBasePluginEndpoint` is unmodified
  and still passes.
- Acceptance criterion 3 (regression test for a `"`-containing value): present
  (`TestSettingsDismissPendingBasePluginEndpoint_RejectsMismatch`'s
  "selector-breaking value" subtest), gets 400, pending list left untouched.
- Error-vs-no-match distinction: a `loadPendingBasePlugins` error is a 500;
  a genuine no-match (including "nothing pending at all") is a 400 —
  mirrors `till-register`'s identical shape.
- The stale comment claiming `canonical_type isn't validated against the
  pending list before this point` was removed and replaced with a paragraph
  documenting the #868 fix; the surviving "needs escaping" rationale is still
  true as defense-in-depth and isn't stale.
- Test-quality check: the new test isn't a vacuous pass — reverting the fix
  makes it fail, both subcases.
- `TestSettingsEndpoints_RoleMatrix`'s `dismiss-pending-base-plugin` row now
  seeds a matching pending spec (`{"x", ""}`) so its cashier/manager/admin/
  super_admin assertions still exercise the elevation gate rather than
  tripping the new 400 first; no test ordering/mutation race (subtests run
  sequentially, no `t.Parallel()` in either file).
- No repository-pattern, i18n, or money-type violations — no new SQL, no
  new user-facing string (the two `http.Error` literals follow the
  pre-existing `till-register` convention, which `guard-i18n.sh` already
  scopes `http.Error` calls out of).

Nits (no action needed): error message wording is fine and consistent with
`till-register`'s; the linear scan over the pending list is fine given the
list is always tiny (a handful of country base-plugin entries at most).

## Verification (personally re-run after the independent review, not just trusted)

- `go build ./...` — clean.
- `go test ./...` (full repo) — green.
- `go test ./internal/pages/... -count=1` (fresh, no cache) — green, re-run
  after the reviewer's temporary revert/restore cycle to confirm the working
  tree still matches the intended final diff.
- `go vet ./...` — clean. `gofmt -l .` — clean.
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh` — all green.

## Non-goals

No new user-facing string, no help-topic change (validation-only, no new
page/route), no ADR needed (mechanical hardening of an existing, reviewed
pattern already used elsewhere in the same file).
