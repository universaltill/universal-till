# Code review: Reports tab nav had no active/selected visual state (ut-docs#421)

## What shipped

`web/ui/pages/reports.html`'s six on-demand report tab buttons
(Sales trend / Items / Tax / Forecast / Payments & channels / Day-end)
had no `role="tablist"`/`role="tab"`, no `aria-selected`, and no visual
indication of which tab's content was currently loaded into
`#report-tab-panel` — a real gap since ut-docs#401 introduced the tabs
(found during that feature's own review, filed as this ticket).

Fix, plain htmx (no new JS framework, per the ticket's own constraint):

- `role="tablist"` on `.report-tabs`; `role="tab"`, `aria-selected`,
  `aria-controls="report-tab-panel"`, and a stable `id` on each button.
- `role="tabpanel"` on `#report-tab-panel`.
- A shared `hx-on:click` handler (one Go template string, reused across
  all six buttons) that: clears `active`/`aria-selected` from every tab
  (queried live via `.report-tabs [role=tab]`, so it also covers the
  manager-only 6th "Day-end" tab without special-casing it), marks the
  clicked tab active/selected, and points the panel's `aria-labelledby`
  at the clicked tab's `id`.
- `web/public/app.css`: `.report-tabs .btn.active` gets an accent
  background/text via the existing `--accent`/`--accent-contrast`
  tokens (same pairing already used by the base `.btn`/`.osk-on`/badge
  styles, so no new, unvetted color combination). Deliberately paired
  with `.report-tabs .btn.active:hover` too — `.btn.secondary:hover`
  elsewhere in the file shares the same specificity and comes later in
  source order, so without the explicit `:hover` pairing, hovering the
  active tab with a real mouse silently fell back to the plain
  unselected-hover gray. Caught by actually looking at a real-browser
  screenshot of the active state (not just asserting on markup), fixed
  before review.
- New e2e test in `e2e/tests/pages.spec.ts`: role/aria-selected/active
  class/aria-controls/aria-labelledby, including that switching tabs
  moves the state rather than accumulating it.
- `web/help/img/manifest.json` regenerated (`make docs-shots`) since
  `web/public/app.css`/`web/ui/pages/reports.html` are tracked app
  surface; all 23 topics × 4 locales recaptured, byte-identical except
  where the change is invisible pre-click (the tracked screenshots are
  all taken on page load, before any tab is clicked, so none show the
  new active-state color — expected, not a gap in this fix).

## Independent review

Fresh-context Sonnet subagent (complexity:easy → Sonnet review, fresh
instance rather than a different model, per the model-routing rubric),
isolated worktree. Verdict: **safe to merge**, no blocking findings.

What it verified independently, not taken on trust:

- `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` clean.
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
  `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-compliance-claims.sh`, `guard-htmx-loaded.sh` all clean.
- Ran the real e2e suite (`npx playwright test --project=default -g
  "reports"`) — all 5 tests pass, including the new one.
- **TDD claim re-verified independently**: reverted just
  `reports.html`/`app.css` to the pre-fix commit, re-ran the new test —
  genuine assertion failure (`expected "tablist", got ""`), not a
  timeout/crash. Restored the fix, reran — passes again.
- Traced the click handler's `querySelectorAll` against all six buttons
  (including the manager-gated one) to confirm no state leakage across
  tabs — corroborated by the two-click sequence in the e2e test.
- Traced every other `.btn`-affecting CSS rule (`:hover`, `:active`,
  `:disabled`, `.primary`, `.secondary`, `.danger`; no `.btn:focus`
  rule exists at all) to confirm nothing else can out-specificity or
  out-order `.report-tabs .btn.active`/`:active:hover` — the `:hover`
  fix found during dev is real and sufficient.
- Checked all four theme files (`amber`/`fresh`/`slate`/`monarch.css`);
  none redefine `--accent-contrast` except the three that set it to the
  same `#ffffff`, and theme CSS loads after `app.css` in
  `web/ui/layouts/base.html` (pre-existing, correct order) — no
  contrast regression in any theme.
- Confirmed no new user-facing strings (`guard-i18n.sh` clean; the JS
  itself contains no prose, only selectors/attribute names).
- Read `web/help/en/reports.md` in full: its existing "pick a tab"
  prose describes behavior, not visual/active state, so it remains
  accurate — no manual prose edit needed for this change.
- No file writes in this diff, so the two recurring bug classes
  (missing `os.MkdirAll`, cwd-relative path instead of
  `paths.Data(...)`) don't apply.
- No demo/seed data touched.

**Findings, triaged:**

- **worth-fixing-now (fixed before merge):** the initial diff added
  `role="tablist"`/`role="tab"`/`aria-selected` but not
  `aria-controls`/`role="tabpanel"`/`aria-labelledby` — the ARIA APG tab
  pattern's association between each tab and the panel it controls.
  Screen readers would announce "tab, not selected" per button but never
  connect any of them to `#report-tab-panel`. Fixed: added
  `aria-controls`, a stable `id` per button, `role="tabpanel"` on the
  panel, and an `aria-labelledby` update in the same click handler. New
  e2e assertions cover it. Re-ran the full gate (guards, e2e, build)
  after this fix, not just the one changed file.
- **defer, no ticket needed (explicitly accepted, matches the issue's
  own scope):** no roving `tabindex`/arrow-key (Home/End/Left/Right)
  navigation per the fuller APG tabs pattern — the ticket's acceptance
  criteria never asked for it, and this is a 6-button on-demand tab bar,
  not a keyboard-navigation-heavy surface. Noted, not filed as a new
  card — reopening this if a future accessibility pass wants it.
- **nit, no action:** this is the first use of `hx-on:click` (colon)
  syntax in the codebase; the one existing precedent
  (`web/ui/partials/buttons_admin.html:124`) uses the older
  `hx-on="htmx:afterRequest: ..."` form. Both work on the vendored htmx
  1.9.12. Cosmetic, not fixed.

## Verified beyond automated tests

Took and read real-browser screenshots (not just markup assertions) of
the active-tab state at default viewport, light theme, LTR — this is
what caught the `:hover`-specificity bug above before it ever reached
review. Confirmed via the `make docs-shots` regeneration that the
existing manual screenshots for `/reports` in en/fa/ar/tr all remain
valid (page-load state is unaffected by this change, as expected).
Did not drive a live click-triggered active-state screenshot in an RTL
locale (fa) — attempted via a cookie-based locale override that didn't
take effect against this app's actual locale-switching mechanism, and
not pursued further since the change adds no `left`/`right` literals,
no new spacing/positioning, and the docs-shots harness already confirms
the page loads correctly in ar/fa/tr with no layout regression from the
added `role`/`aria-*` attributes. Flagging this gap explicitly per the
Tester skill's rule, rather than implying full RTL visual coverage.

## Safe-to-merge verdict

Yes. No blocking findings; the one worth-fixing-now finding (ARIA
tab/panel association) was fixed and re-verified before this record was
written.
