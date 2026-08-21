# Code review: wire settings_page.go's remaining UX-toggle/enrolment mutations onto manager-override elevation, slice 2 (ut-docs#865)

**Date:** 2026-08-21
**Card:** ut-docs#865 (split off ut-docs#796 during that card's BA scoping
pass; sibling ut-docs#866 covers the other 4 files)
**Complexity:** medium — build: inline (Sonnet), review: Opus (fresh-context
subagent, isolated worktree)

## What shipped

The 10 remaining mutating, previously-unaudited
`canPerform(d, r, "settings")`-gated handlers in
`internal/pages/settings_page.go` moved off a flat
`if !canPerform(...) { 403 }` gate onto the same generic
manager-override-elevation mechanism ut-docs#557/#796 already established
(`checkOrElevate`/`renderElevationPrompt`, dual-attribution audit via
`InsertAuditElevated`/`InsertAudit`):

`idle-lock`, `kiosk-idle-reset`, `window-mode`, `launch-on-startup`,
`telemetry`, `display-mode`, `dismiss-restore-prompt`,
`dismiss-pending-base-plugin`, `POST /api/enrol/claim-code`,
`POST /api/enrol/now`. None wrote an audit entry before this change; each
now does.

`launch-on-startup` was previously wired to a raw `fetch()` on a checkbox
`change` handler, not an htmx form at all — no elevation path was even
architecturally possible there. Converted to a real htmx binding
(`hx-post`/`hx-trigger="change"` on the checkbox itself, `hx-vals` for the
boolean) so it gets the same in-place PIN dialog as every other site.

New i18n: 12 `elevation.summary.*` keys, real translations in all 4 locales
(en/ar/fa/tr). Template changes in `web/ui/pages/settings.html`: new
elevation-retry `<span>` targets, and the idle-lock/telemetry/display-mode/
window-mode forms' reload/navigate handlers updated with the same
Content-Type guard #796 established for till-name/till-register/save (a
200 elevation-prompt response must not trigger the success reload/navigate,
which would destroy the just-opened dialog).

**Non-goals, explicitly out of scope and untouched**: `GET /api/enrol/devices`
(read-only); the 2 `fiscal_tse_override`-gated sites in this file;
`receipt_designer.go`/`print_api.go`/`invoice_page.go`/`menu_page.go`
(ut-docs#866); the pre-existing, cross-cutting ut-docs#867 finding that
elevation-wired settings forms sit inside `{{ if .isManager }}` and so
can't actually be triggered by the denied sessions the mechanism exists for
— every one of these 10 sites inherits that same gap, same as #796's 8.

## Independent review (Opus, isolated worktree)

Ran the full gate personally (build, vet, `go test -count=1
./internal/pages/...`, all 6 guard scripts, `gofmt -l`) against a WIP
snapshot commit. Read the diff against the established precedent
(payments-default/shop-type/till-name/etc., all unchanged) and traced the
htmx swap semantics (`hx-swap="none"` + OOB, `hx-swap="outerHTML"`
interaction with the dialog's fixed `hx-target`/`hx-swap="innerHTML"` retry
form) by hand against the vendored htmx source, not just by inspection of
the Go handlers.

Found no blockers. Gate correctness itself (every one of the 10 handlers
denies a cashier's mutation and only proceeds on `allowed`/`elevated`) was
confirmed clean. Two should-fix findings, judged real and fixed on this
branch before merge (both re-verified with the full gate afterward — no
second review round: neither is blocker-class (money/tax/data-loss/
security), and nothing else the review raised rose to that bar either):

1. **The two `dismiss-*` handlers' elevation dialogs were a dead end.**
   `renderElevationPrompt`'s `hxTarget` was set to the SAME node
   (`#restore-resume-block`, the per-chip `[id="pending-plugin-…"]`) the
   triggering button's own `hx-swap="outerHTML"` removes. A denial's hint
   span replaced that whole block/chip; the dialog's retry form then
   pointed its own `hx-target` at a node that no longer existed, so htmx
   raised `htmx:targetError` and never sent the approver's PIN — the
   mechanism looked wired but the actual approval path was unreachable.
   Masked today by ut-docs#867 (both blocks sit inside `{{ if .isManager
   }}`), and importantly **not fixed by #867 alone** — unhiding the form
   would surface a broken dialog, not a working one. **Fix**: gave both
   buttons a dedicated `#restore-resume-msg` /
   `#pending-plugin-msg-<canonical_type>` span with `hx-swap="innerHTML"`
   (the `remove-demo-catalogue` precedent from #796) and pointed
   `hxTarget` there instead.
2. **`display-mode`'s audit payload recorded the localized label, not the
   stored value.** `{"mode": modeLabel}` made the same action write a
   different string per operator UI language, and for `mode=register`
   recorded a real label for a value that's actually persisted as `""`.
   Every sibling handler audits the raw value (window-mode: `{"mode":
   mode}`; shop-type: `{"shop_type": v}`). **Fix**: captured `rawMode`
   before the `"register" -> ""` collapse and audit that; `modeLabel`
   stays only in the approver-facing summary text.

Also folded in while the branch was open (nits, but cheap and worth fixing
before merge):

3. `launch-on-startup`'s `hx-vals="js:{enabled: event.target.checked}"`
   depended on `window.event`, undefined for a request htmx re-issues from
   its own queue (two rapid toggles on a slow link) — the second request
   would silently drop. Switched to reading the checkbox by
   `document.getElementById(...)` instead of `event.target`.
4. The same checkbox's htmx conversion lost the old `fetch()` handler's
   revert-on-failure and stale-message-clear. Restored both via
   `hx-on::before-request`/`hx-on::after-request`.
5. `settings_page.go`'s validation-before-elevation ordering
   (idle-lock/kiosk-idle-reset/window-mode/launch-on-startup/display-mode
   all validate before calling `checkOrElevate`, matching the established
   convention) was correct in code but *unproven* by the existing bad-input
   tests — they all post as `&mgrUser`, who clears `canPerform` regardless
   of ordering, so an accidental reorder would still 400 for a manager.
   Added a cashier + bad-input subtest per site
   (`TestSettingsElevation_Slice2Sites_DenyAndElevate/*/deny_bad_input_before_pin`)
   that actually pins the ordering: 400, no elevation prompt shown.
6. Stale/inaccurate code comments — one claimed a handler called
   `settingsRespondSaved` when it doesn't (sets `X-UT-Response` inline
   instead, by design, to keep its pre-existing bare-empty-body success
   shape); another used a fictional example canonical_type
   (`"plugin.tax.de"`) when the shipped catalogue only ever produces
   `"language"`. Corrected both.
7. Added an explicit assertion (in the pre-existing
   `TestSettingsDismissRestorePromptEndpoint` /
   `TestSettingsDismissPendingBasePluginEndpoint`) that the plain-allowed
   (manager) path still answers with **no** `X-UT-Response` header — the
   header is elevated-path-only, and nothing previously proved the
   plain-allowed path stays untouched.

**Deferred, not fixed here:**

- **ut-docs#867** (pre-existing, cross-cutting) still applies to all 10 of
  these sites, same as #796's 8 — not this card's job to fix.
- `dismiss-pending-base-plugin`'s `canonical_type` form value is used
  unvalidated as a CSS attribute-selector fragment and an audit
  `entity_id`. Not exploitable today (`html/template` escapes the
  rendered selector; the real catalogue only ever produces `"language"`;
  an attacker can only affect their own request), but nothing stops a
  future caller from posting an arbitrary string that breaks the selector
  or pollutes the audit trail. Filed as a new Backlog card
  (ut-docs#868) rather than widening this diff to add a
  validate-against-the-pending-list check.
- `display-mode`'s elevated-approval path lands the operator back on
  `/settings` (via `X-UT-Response: ok`'s reload) instead of navigating to
  `/` the way a direct manager success does — a minor UX inconsistency
  for the `self_order`/`backoffice` profile-switch cases, not a
  correctness issue (the mode IS saved either way). Left as-is; matches
  every other elevated site's reload convention in this file, and
  special-casing it would need its own design pass.
- `claim-code`/`enrol-now`'s `settingsAudit` call on the elevated success
  path is unreachable in this offline test environment (both need a real
  marketplace) — same limitation `TestSettingsEndpoints_RoleMatrix`'s own
  doc comment already accepts for `enrol-now`'s "past the gate ≠
  succeeded" reasoning. Not fixable without a marketplace stub in this
  test harness; out of scope here.
- A pre-existing, unescaped-HTML nit in `enrol-now`'s error branch
  (`internal/pages/settings_page.go`, `reason`/`endpoint` interpolated
  without `html.EscapeString`, unlike its `claim-code` sibling) predates
  this diff and is untouched by it. Noted, not filed separately (low
  value, `reason` is either a fixed i18n string or a Go error message from
  this process, `endpoint` is operator-configured, neither is
  user-controlled at the HTTP boundary).

## Verified beyond automated tests

- Full `go test -count=1 ./...` green (zero failures) after the review
  fixes, not just the targeted `internal/pages` package.
- All 6 guard scripts green: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh` (1116 keys, all locales
  match), `guard-help-topics.sh`, `guard-compliance-claims.sh`.
- `gofmt -l` clean on every changed Go file.
- Traced the htmx swap/OOB interaction by hand for every one of the 10
  handlers' `hx-swap` values (`none` x5, `outerHTML` x2, `innerHTML` x3),
  not just trusted that "the dialog appears" — this is what caught finding
  1 above.
- No real client/shop name in any fixture; no secret-shaped literal (test
  PIN `555222` reuses the existing `seedElevationUsers` fixture from
  #796's own test file).
- `web/help/en/elevation.md` already documents the manager-override
  mechanism generically ("Some actions... and others") rather than
  enumerating specific endpoints — confirmed no manual update needed,
  same as #796 required none for its own 8 sites.

## Safe-to-merge verdict

Safe to merge. Both should-fix findings addressed on this branch and
re-verified with the full gate; deferred items are either pre-existing
(#867), genuinely out of scope for a focused slice (a new Backlog card for
the canonical_type hardening), or accepted trade-offs with reasoning
recorded above.
