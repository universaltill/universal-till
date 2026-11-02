# Review: confirm before pairing a registered till (ut-docs#3557)

Branch `feat/3557-pair-confirm-replace`. Built by a Sonnet dev agent from an
Opus Architect brief and a UX design pass. Reviewed by an independent Opus
reviewer. Tested by a separate Tester agent with a driven run of the real
binary.

## What shipped

- `POST /api/enrol/pair` now runs three steps before `enroll.Pair`:
  1. The new read-only `enroll.PairPrecheck`: empty or overlong code, no
     cloud address, env-pinned identity, replica without a store. So a
     refusal never costs a PIN.
  2. On a till that is registered, has no lock-out chip and is not
     "replaced", the shared confirm dialog: `renderConfirmPrompt`, opened
     with `.show()` over `#ut-scrim`, never `showModal`. It names the shop
     and the typed code, both bidi-isolated. Confirm is a red `Danger`
     button and Cancel is autofocused.
  3. Elevation. A cashier's PIN retry carries `code` and
     `confirm_replace=1`.
- Unregistered, ViaMainTill-only, locked-out and replaced tills keep the
  one-step path. ADR-0116 D3/D4 re-vouches a replica automatically, so a
  wrong code there heals itself.
- `confirmPrompt.Danger` is opt-in. The other caller (display mode) renders
  the same markup as before.
- New keys: `settings.enrol.pair_replace_{title,body,confirm}` in en, ar, fa
  and tr, with `en.meta.json` metadata. The de, es and pt packs get them in
  follow-up PRs on `i18n/3557-pair-confirm-replace`.
- Help: the "Pair with a shop" section of `web/help/{en,ar,de,fa,tr}/claim.md`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | major (test) | RoleMatrix's `enrol-pair` row really ran `enroll.Pair` and left the package-global enrolment identity cleared. `-run 'Pair\|Enrol\|Confirm\|RoleMatrix\|Elevation'` failed on order-dependent `TestResolveAndInstallBasePlugin_*`. | **Fixed**: `t.Cleanup` re-inits enroll, as `pairDeps` does. The same filter now passes. |
| m1 | minor | A cashier sees precheck refusals and the confirm dialog (shop name or store id) before entering a PIN. | **Accepted**: this is the designed order (confirm, then PIN). The data is low-sensitivity and the store id is already on the card. |
| m2 | minor | The dialog names the local shop name, which can differ from the cloud shop's name. | **Accepted**: the local name is what the operator sees on this till. |
| n1 | nit | A stray PDI or override character in the shop name or code could break out of the isolate. | **Fixed**: `stripBidiControls` before `bidiIsolate`, pinned by `TestPairConfirmDisplayHelpers`. |
| F1 (tester) | minor | The code wrapped at its hyphen (`AB12-` / `CD34-EF56`). | **Fixed**: `unbreakableCode` adds a WORD JOINER after each hyphen in the displayed code. The hidden field keeps the raw code. |
| n2, n3 | nit | Dead `seedElevationUsers` result in one test; the order of one Turkish help sentence. | Accepted. |
| F2, U1, U2 | follow-up | No focus return after Cancel, Escape does not close, the hint stays in `#pair-msg`. All three apply to every confirm dialog. | Follow-up card ut-docs#5275. |

## Verified beyond unit tests

- **TDD re-verification (reviewer).** With the handler and template
  reverted, 8 new handler tests fail for the claimed reason: the till pairs
  without asking, or the PIN prompt comes first. With only the template
  reverted, the Danger test fails. Restored, all pass.
- **Driven run (tester).** Real binary, fake cloud, real "Register now".
  Viewports and locales:
  - 1024×600 in en, fa and tr;
  - 360×740 in en, fa, ar, tr and de.

  Results:
  - No inner scroll and no overflow; buttons are 51px tall.
  - Confirm is `btn danger`; focus lands on Cancel.
  - RTL is mirrored, and the code reads in the right order.
  - Cancel makes 0 POSTs and 0 cloud calls.
  - Double-tapping Replace makes exactly 1 `/pair` call.
  - A wrong code shows "not registered", as the dialog warns.
  - An unregistered till gets no dialog.
  - Pressing Enter twice lands on Cancel, which is safe.

  **Not looked at:** the cashier PIN path in a browser (httptest only), a
  real dark theme, kiosk mode with the on-screen keyboard, real hardware.
- **Gate:**
  - `gofmt` clean, `go build ./...`, `go vet ./...`, and
    `golangci-lint` (0 issues);
  - `go test ./internal/...`: all packages green in the tester's run, with
    `internal/plugins` timing out on a shared machine. That package is not
    touched;
  - after the fixes: `go test ./internal/pages/ ./internal/enroll/`;
  - all i18n, help, showModal, RTL, compliance, core-neutral, data-access,
    naming and mail guards.

## Verdict

Safe to merge once CI is green. Merge core first. The pack PRs follow and
stay open until pack `main`'s `key-drift` is green: it currently fails on
17 keys from ut-docs#4128 and #3208.

Root fix (keep the identity until the cloud accepts the code, which needs an
ADR-0116 D5 amendment): ut-docs#5276.
