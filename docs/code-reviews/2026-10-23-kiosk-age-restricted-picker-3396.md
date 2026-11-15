# Code review: kiosk payment picker flags an age-restricted basket up front (ut-docs#3396)

Date: 2026-10-23. Lane: `lane:cloud-41`. Complexity: easy.

## What shipped

- `GET /api/self-order/checkout` (the payment picker's first render) now runs
  the ut-docs#3340 age-restriction check before it loads payment methods. If
  the basket holds a restricted item, or the read fails (fail closed), the
  picker:
  - shows `age_check.kiosk_blocked` inline as a `.pos-notice.error` (not the
    floating toast);
  - uses the heading `selforder.counter.title` ("Pay at the counter");
  - offers no payment method, only Cancel.
- The POST gate is unchanged in behaviour (409, same message, same log),
  but it now shares the helper `kioskBasketAgeRestricted`.
- Counter mode and table-QR sessions are unaffected. Their counter path
  returns before the check, and pay-at-counter stays deliberately unblocked.
- Help: `age-restricted-sales` is updated in en, de, tr, ar and fa. The
  structure is unchanged; one sentence says the message appears at payment
  and no method is offered.
- i18n meta: `selforder.counter.title` is now described, now that it has two
  call sites, and its line is removed from the shrink-only baseline.
- No new locale keys, so no language-pack follow-ups.

## Tests

- `TestSelfOrderShop_CheckoutPickerTellsAgeRestrictedUpFront`: written first.
  It failed against the old GET handler ("Choose a payment method" plus method
  buttons). It passes now.
- `TestSelfOrderShop_CheckoutPickerUnrestrictedListsMethods`: control.
- The existing #3340 POST tests still pass.
- `e2e/tests/self-order-age-restricted-picker-3396.spec.ts`: the item is
  flagged through the catalog editor, then the customer goes to the kiosk and
  taps Checkout. It runs at 1024×600, 360×740, and 1024×600 in Persian (RTL),
  with geometric asserts: the message sits below the title and above Cancel,
  inside the viewport.

## Visual check (Tester / UX)

I looked at the kiosk at 1024×600 (en, fa/RTL) and the phone at 360×740 (en),
light theme. The first driven run caught a real defect: the `.toast` is
`position: fixed` at the centre, so as the picker's only content it covered
the title and Cancel. The blocked state now renders the message inline. I
did not look at the dark theme; the message uses the shared `--danger-tint`
tokens.

## Independent review (Sonnet; the author was Opus)

No blocking findings. The reviewer re-verified the TDD claim by reverting
the GET hunk: the test fails, and passes again once restored.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Help in ar/fa/tr didn't say the message comes before a method is picked | Fixed: updated all three |
| 2 | nit | Reused `selforder.counter.title` had no meta description | Fixed: described, baseline line removed |
| 3 | nit | Confirmed counter mode is untouched (check runs after the counter early-return) | No change needed |
| 4 | nit | e2e barcode suffix `Date.now() % 1000` could collide on a rerun | Fixed: 6-digit suffix |

## Gate

- `go build ./...`, `go vet`, `gofmt`: clean. `golangci-lint`: 0 issues.
- `go test ./...`: green except `internal/plugins`, which hit the local 600s
  default timeout. That package is untouched, and CI runs it in its own
  `test-heavy` job.
- Every `scripts/ci/guard-*.sh` in `ci.yml` passes, with two environmental
  exceptions:
  - `guard-deadcode-baseline.sh`: the deadcode binary was built with Go 1.26.
  - `guard-shellcheck-version.sh`: no shellcheck binary on PATH.

  Both fail identically on `main` in this container.

## CI follow-up

On the first push, `build` failed `guard-docs-shots_test.sh`. The app surface
changed, so `web/help/img/manifest.json`'s `surface_sha256` was stale. No
docs-shots spec renders the self-order kiosk or its payment picker
(`e2e/tests-docs` has no self-order route), so no manual screenshot's pixels
change. I refreshed the hash with
`scripts/ci/update-docs-shots-surface-hash.sh` rather than regenerating
the shots.

## Verdict

Safe to merge. Deferred: nothing.
