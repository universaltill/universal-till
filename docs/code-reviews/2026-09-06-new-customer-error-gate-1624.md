# Review: tender-footer New Customer close-gate matches New Sale (ut-docs#1624)

## What shipped

`web/ui/pages/index.html`'s "New Customer" button in `.tender-footer.single`
(inside `#payment-overlay`) used to close the overlay via an unconditional
`onclick="document.getElementById('payment-overlay').close();"` that ran
**before** its `hx-post="/api/pos/reset"` request even completed — unlike
every "New Sale" button elsewhere in the same file (the in-overlay copy at
`data-testid="payment-overlay-new-sale"`, the quick-pay buttons, the
per-method tender buttons), all of which only close the overlay via
`hx-on::after-request` after a successful response carrying no inline
`#toast-message.error`. An operator tapping New Customer against an errored
`/api/pos/reset` lost the Tender panel immediately and never saw the error,
while New Sale two rows up would have kept it open and shown it.

Fix: New Customer's `onclick` is replaced with the exact same
`hx-on::after-request` gate string already used by every New Sale button:

```
if (event.detail.successful) { var t = document.getElementById('toast-message'); if (!(t && t.classList.contains('error'))) { var d = document.getElementById('payment-overlay'); if (d && d.open) d.close(); } }
```

`hx-post`/`hx-target`/`hx-swap`/`hx-sync` on the button are untouched.

New spec `e2e/tests/new-customer-error-keeps-overlay-open-1624.spec.ts`
(two tests): happy path (New Customer still closes on a real successful
reset) and error path (route-intercepts `/api/pos/reset`, fulfills a fake
response shaped exactly like `web/ui/partials/basket.html`'s
`.pos-notice.error#toast-message` fragment, asserts the overlay stays open
and the error toast is visible).

`web/help/img/manifest.json`'s `surface_sha256` was regenerated (`make
docs-shots`) because the comment/attribute text in `index.html` changed the
file's content hash, even though nothing renders differently on the happy
path — no screenshot actually changed.

## What I found

**No blocking issues.** One pre-existing, out-of-scope finding (not
introduced or worsened by this diff, and not something #1624 claimed to
fix):

- `web/ui/partials/table_picker.html`'s `.table-picker-clear` and
  `.table-picker-option` buttons (lines ~37-47) have the identical
  antipattern this card fixed: `onclick="document.getElementById(
  'table-modal').close()"` fires unconditionally, racing an `hx-post
  ="/api/pos/table"` that can legitimately return an error toast
  (`internal/pages/pos_api.go`'s table handler sets
  `cur.ToastMessage = httpx.T(locale, "basket.table.occupied")` when a
  concurrent claim wins the race). An operator picking a table that just
  became occupied would see `#table-modal` close immediately instead of
  staying open on the error, same user-facing failure mode as #1624's bug,
  in a different dialog. This is out of scope for #1624 (which is scoped to
  the payment-overlay/New Customer button specifically) — flagging as a
  follow-up candidate, not blocking this PR.

Grepped the rest of `web/ui/**/*.html` for `onclick=".*\.close()"` — the
only other hits (`catalog_barcode_backfill.html`, and payment-overlay's own
`✕`/`payment-overlay-close` button) are pure dismiss buttons with no
`hx-post` on the same element, so no race is possible there; not an
instance of the bug class.

## Verified independently (not taken on faith)

- **Byte-for-byte comparison**: extracted every `hx-on::after-request="..."`
  attribute value from `index.html` with
  `grep -o 'hx-on::after-request="[^"]*"' | sort -u` — exactly two distinct
  strings exist in the whole file: the plain gate (used identically by New
  Sale's several buttons and now New Customer) and the hold-modal variant
  (which additionally does `m.close(); this.reset();` for its own form,
  expected difference, different context). New Customer's string is an
  exact match to the plain gate, not a lookalike.
- Diffed New Customer's `hx-post`/`hx-target`/`hx-swap`/`hx-sync` against
  the pre-fix version (`git show <parent>:web/ui/pages/index.html`) — byte
  for byte identical; only the close mechanism changed.
- **TDD re-verification (the real proof, not the pre-existing spec suite)**:
  since this is a behavior-shape fix rather than a classic red/green bug
  fix, reverted *only* `web/ui/pages/index.html` to the pre-fix commit
  (`git checkout HEAD~1 -- web/ui/pages/index.html`, production code only,
  new spec file untouched) and ran the new spec against that old code:
  - `New Customer closes the overlay on a successful reset` — **passed**
    (expected; happy path is unaffected by the bug).
  - `New Customer leaves the overlay open when the reset response carries
    an error toast` — **FAILED** against old code, exactly as expected:
    `expect(locator('#payment-overlay')).toBeVisible()` got `hidden` — the
    old unconditional `onclick` closed the dialog regardless of the
    injected error toast. This is the genuine proof the new test
    discriminates real behavior rather than false-passing.
  - Restored the fix (`git checkout HEAD -- web/ui/pages/index.html`),
    working tree clean again, and re-ran: both tests pass.
- Ran the new spec plus its three named close siblings together
  (`new-sale-closes-payment-overlay-1386.spec.ts`,
  `payment-overlay-footer-reachable-1542.spec.ts`,
  `tender-panel-reachable.spec.ts`) — **15/15 passed**, no regressions.
- Checked `web/help/` for any topic describing New Customer's
  overlay-closing behavior specifically — none found; this is an
  internal-implementation-detail fix with no documented/visible behavior
  change on the happy path, so no manual update is required.
- No file I/O in this diff (the two recurring bug classes this pipeline
  watches for — missing `os.MkdirAll`, cwd-relative path instead of
  `paths.Data(...)` — don't apply; diff is HTML/JS + a regenerated JSON
  manifest hash).
- No real client/shop name, no secret-shaped literal anywhere in the diff.

## Commands run and results

- `gofmt -l .` — clean, no output.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `golangci-lint run ./...` — `0 issues.`
- `go test ./...` — full suite green (all packages `ok`).
- `bash scripts/ci/guard-i18n.sh` — passes (`1430 template keys resolve; all
  locales match en.json; no hardcoded inline-JS status strings found; ...`)
  — the new `hx-on::after-request` string is pure JS/DOM logic, no
  user-facing prose, so it doesn't trip the inline-`<script>`/status-string
  rule.
- `bash scripts/ci/guard-docs-shots.sh` — passes (`25 routed topics × 4
  locales screenshotted and fresh`) — confirms the regenerated
  `surface_sha256` in `web/help/img/manifest.json` is actually correct for
  the current tree, not stale.
- `bash scripts/ci/guard-e2e-fixtures-import.sh` — passes (`85 spec(s)
  checked, all import test/expect from ./fixtures`).
- `cd e2e && npx playwright test
  new-customer-error-keeps-overlay-open-1624.spec.ts
  new-sale-closes-payment-overlay-1386.spec.ts
  payment-overlay-footer-reachable-1542.spec.ts
  tender-panel-reachable.spec.ts --project=default` — **15 passed**, 0
  failed.

## Verdict

**SAFE TO MERGE.** No blocking issues. The fix is a narrow, verified
byte-for-byte match to an established pattern already used seven other
places in the same file; the new test provably discriminates old vs. new
behavior (confirmed by reverting production code only and watching the
error-path test fail, then restoring and watching it pass); no CI guard,
lint, vet, build, or full test-suite regression.

## Deferred / out of scope

- The identical premature-close antipattern in `web/ui/partials/
  table_picker.html`'s table-clear/table-pick buttons (see "What I found"
  above) is a real, separate bug of the same class, but out of scope for
  #1624. Recommend filing a follow-up card rather than folding it into
  this PR.
