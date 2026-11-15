# 2026-10-26 — Phone drawer: Lock row lines up with the other rows (ut-docs#3407)

**Lane:** lane:cloud-24 · **Complexity:** easy · **Built by:** Sonnet subagent ·
**Reviewed by:** Opus 5.5, fresh context (different model from the author)

## What shipped
- `web/public/app.css`: in the ≤480px block, `.nav-drawer .btn-lock` padding
  `.5rem .75rem` → `.6rem 1.1rem`, the same padding every other drawer row
  (`.nav-toggle`, ≤480px) gets. The Lock icon/label had started 0.35rem
  (≈5.9px) nearer the start edge. Base `.btn-lock { padding: .5rem .3rem }`
  (tablet rail, record dialog) unchanged.
- `e2e/tests/phone-drawer-lock-align-3407.spec.ts` (new, `auth` project — the
  auth-off project renders no session chip): at 390×844 the Lock icon's start
  edge equals every visible drawer `a.nav-toggle` icon's ±1px on `/` and
  `/settings` (LTR) and `/` (RTL, right edges); a 1024×768 guard pins the rail
  Lock's .3rem inline padding.
- `e2e/playwright.config.ts`: spec added to `AUTH_ONLY_SPECS`.

## TDD (re-verified by the reviewer in a separate worktree)
- Without the CSS fix: 3 failed — LTR `/` and `/settings` `lock 22.94 vs row
  28.88`, RTL `lock 367.06 vs row 361.13` (5.94px off). Tablet guard passes
  (expected: it guards against spread to the rail).
- With the fix: 4 passed; with `phone-drawer-admin-links-3358` and
  `phone-drawer-admin-3297`: 15 passed.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | WIP commit message, no review record | fixed (this record, real commit message) |
| 2 | nit | RTL checked on `/` only | accepted — the rule is page-independent |
| 3 | nit | tablet guard cannot catch the bug itself | accepted — it is a no-spread guard by design |
| 4 | info | `left`/`right` in the spec are `getBoundingClientRect()` keys, not CSS | no change; `guard-rtl-logical-css.sh` passes |

Also checked: `.nav-drawer .btn-lock` matches only `session_chip.html`'s button;
drawer rows' `min-height: 48px` keeps the row height, so #3358's "Lock scrolls
clear of the status row" is unaffected; no `body.kiosk` rule touches the lock.

## Gate
CI-workflow guards run locally: all pass except `guard-deadcode-baseline.sh`
(container Go 1.26 vs module go1.27) and `guard-shellcheck-version.sh` (no
shellcheck in the container) — environment only, no Go/shell change here; CI
runs both. No Go change, no new strings, no help-topic change (visual fix only).

## Visual attestation
Looked at (via geometric assertions): phone drawer 390×844 LTR and RTL on `/`,
`/settings`; tablet rail 1024×768. Not looked at: dark theme, kiosk Pi
1024×600 (rail, unchanged rule).

**Verdict:** safe to merge.
