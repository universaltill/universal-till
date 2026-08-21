# 2026-08-21 — Remove real client name from hold-sale example data (ut-docs#521)

## Context
`internal/pages/hold_api.go`'s doc comment and `internal/pages/hold_api_test.go`'s
test data used `"Haaft 1"` as a held-sale example/test tab name (added
2026-08-01, ut-docs#46). That commit's own review record
(`docs/code-reviews/2026-08-01-hold-sale-named-tabs.md`) explicitly asserted
*"No real client/shop name used — 'Haaft 1' is quoted directly from the
reference café POS."* Ticket #511 (2026-08-09) revealed Haaft is in fact a
real German café's real name (a genuine prospect) — so that 2026-08-01
assessment was wrong, unknowingly, eight days before anything existed to
reveal it. This is exactly the case the pipeline's standing rule exists for:
*"Never commit a real client/shop name into any doc/commit as demo/seed/test
data — a generic name only."*

## Design
Not a functional change — a pure literal string swap, `"Haaft 1"` →
`"Tab 1"`, everywhere it appeared as demo/test data, plus a correction to the
one review record that made the now-inaccurate claim. No behaviour, i18n key,
or test-assertion logic changed — only the literal value asserted against.

## What changed
- `internal/pages/hold_api.go` — doc comment example.
- `internal/pages/hold_api_test.go` — test request body literal and the
  assertion it's checked against (`TestHoldHandler_ExplicitLabelWithNoCustomer`).
- `e2e/tests/hold-named-tab.spec.ts` — comment + the two `fill`/`toContainText`
  literals in the e2e test.
- `web/ui/pages/index.html` — an HTML *comment* (never rendered UI copy; the
  actual rendered placeholder text is the i18n key `hold.modal.placeholder` =
  `"e.g. Table 4, Sarah"` in `web/locales/en.json`, which never contained
  "Haaft" and needed no change).
- `docs/code-reviews/2026-08-01-hold-sale-named-tabs.md` — appended a
  `## Correction (2026-08-21, ut-docs#521)` addendum flagging the original
  "No real client/shop name used" claim as wrong, without rewriting the
  historical record itself.

`"Tab 1"` (not the ticket's other suggested option, `"Table 4"`) was picked
deliberately: `"Table 4"` is already used in this same test file as a
distinct sentinel for a different scenario
(`TestHoldHandler_ExplicitLabelOverridesCustomerName`), so reusing it here
would read as ambiguous even though the two tests wouldn't actually collide.

## Independent review (Sonnet, fresh-context subagent)
No blocking issues, no nits. Verified independently:
- The diff touches exactly the files the ticket named, nothing missed,
  nothing over-scoped.
- `git grep -in haaft` at repo root: only 8 remaining hits, all inside three
  pre-existing dated `docs/code-reviews/*.md` files describing what was
  found/reviewed historically — no live-code, test-data, or UI-copy leak
  remains.
- `"Tab 1"` vs. the pre-existing `"Table 4"` sentinel: distinct test
  functions, distinct scenarios, distinct failure messages — no real
  ambiguity.
- Confirmed every changed `.go`/`.ts` line is a literal swap only; no
  assertion logic, control flow, or behavior changed.
- Confirmed the `web/ui/pages/index.html` change is inside an HTML comment,
  never rendered, and the actual rendered placeholder string (i18n key
  `hold.modal.placeholder`) never contained "Haaft" — no locale/help-doc
  update needed.
- Confirmed the correction addendum is accurate, appends rather than
  silently rewrites history, and correctly tells readers not to read the
  original sections as still describing current code.
- Ran build/tests/guards itself (see Gate below) rather than trusting the
  Dev/Tester report.

## Verified beyond the automated suite
- `git grep -in haaft` run personally after the fix — confirmed clean
  outside historical review records.
- Manual/help-topic check: this change touches no UI a shop owner sees
  (comment-only in `index.html`; the rendered placeholder string was never
  "Haaft" to begin with) — no `web/help/` topic update needed, confirmed not
  just assumed.

## Gate
`go build ./...`, `go test ./...` (full suite, all green, no pre-existing
failures this run), `guard-data-access.sh`, `guard-kiosk-engine.sh`,
`guard-plugin-menu-read.sh`, `guard-i18n.sh` — all clean. Review subagent
independently re-ran `go build ./...`, `go test -count=1 ./internal/pages/...`
(no cache), and all four guards plus `guard-compliance-claims.sh` — all
passed a second time, in a fresh context.

## CI caught one more gap
The PR's first CI run failed `guard-docs-shots.sh` — not one of the three
guards this repo's `CLAUDE.md` pre-commit checklist names, but another CI
also enforces. `web/ui/pages/index.html` changed (an app-surface file, hashed
whole-file regardless of whether the changed line is a rendered element or
an HTML comment — confirmed the comment itself is never rendered, but the
guard's surface hash doesn't distinguish that), so the manual's screenshot
manifest was stale per the guard's own bookkeeping even though nothing
visibly changed. `internal/pages/hold_api.go` did NOT trigger this — its
only registered routes (`/api/pos/hold`, `/api/pos/resume`, `/ui/held`) are
none of them a screenshotted page route, so the guard's own file-level
exclusion correctly skips it.

Ran `make docs-shots` (this session has the pre-installed-Chromium fallback
`e2e/scripts/docs-shots.sh` uses) — all 80 screenshot specs passed, manifest
rewritten. 10 of 96 PNGs came out with a different byte hash on re-render
(`alerts`×4 locales, `designer`×4, `fa/translations`, `tr/invoices`) —
normal run-to-run rendering variance (dynamic data/timestamps in those
screens), not caused by this diff: the `sell` topic's screenshots (the one
page that actually renders `index.html`, the file that triggered the guard)
came out byte-identical, confirming the comment change is genuinely
invisible. `guard-docs-shots.sh` passes clean after.

## Verdict
Safe to merge.
