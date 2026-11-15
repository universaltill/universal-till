# Review: login-screen exit-to-OS PIN field, touch OSK e2e (ut-docs#1105)

Date: 2026-10-31 · Lane: `lane:cloud-41` · Complexity: easy · Test-only

## What shipped

A permanent touch-context Playwright test in `e2e/tests/login.spec.ts`
(auth project). It covers #1099's "operable by touch alone" criterion, which
had only been checked by hand. On a `hasTouch` cookie-less context the test:

1. Taps the `details.login-exit-os` summary on `/login`.
2. Taps the manager-PIN field and checks that `#osk` opens.
3. Checks the layout is numeric: `data-k="1"` is present, there is no `q`
   key, and there are fewer than 20 keys.
4. Checks the field is `inputmode="none"`, so the native keyboard is
   suppressed.
5. Types 4-7-1-9 through the OSK keys and checks the field value and focus.

It does not submit, so it spends none of the shared PIN-lockout budget.

## Verification

- `npx playwright test --project=auth tests/login.spec.ts`: 17/17 pass,
  before and after the review fix.
- Mutation: removed `inputmode="numeric"` from the PIN input in
  `web/ui/pages/login.html`. The new test failed at
  `button[data-k="q"]` toHaveCount(0) (the qwerty layout was shown). The
  other 15 tests that ran passed. Then restored the file.
- Guards: autofill-suppression, e2e-fixtures-import, netaccess,
  no-inline-handlers, osk-loaded, i18n, mail-addresses, competitor-naming,
  compliance-claims are all clean. No Go code changed.

## Independent review (Sonnet, fresh context)

No blockers and no majors. Three nits:

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The numeric assertions overlap. Optional extra: assert `inputmode="none"` to prove the native keyboard is suppressed. | **Fixed:** assertion added. |
| 2 | nit | Possible race in tap on `<summary>`, then tap on field, then key taps. | Accepted. Not racy: the waits retry automatically, and the setup-field OSK test uses the same pattern. |
| 3 | nit | `toBeFocused()` after the key taps could in principle depend on timing. | Accepted. osk.js refocuses on pointerup and `expect` retries. |

Isolation (own context, closed in `finally`), the wizard-state dependency,
the console watch with no exemptions, and the absence of hard-coded ports
were all checked and found fine.

## Not covered

- No visual check: this change adds no UI surface.
- Only Chromium was tested. WebKit is not covered (#3734 tracks running
  specs in WebKit).

**Verdict:** safe to merge.
