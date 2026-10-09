# Review — Settings text-response trusts only refused bodies (ut-docs#3978)

**Card:** ut-docs#3978 (finding 1 of the ut-docs#3247 review).

## What shipped
- `web/public/inline-actions.js` `text-response`: shows a response body only
  when the server marked it `X-UT-Response: refused` with a `text/plain`
  body (the same test `save-error` uses), and hides the page-wide generic
  "server error" alert only then. An unmarked body (an untranslated developer
  string, HTML, empty) writes nothing and clears any earlier message, so the
  generic alert stays up.
- `POST /api/settings/display-mode`: the invalid-mode 400 was an English
  `http.Error`; it is now `httpx.RefuseText` with the new key
  `settings.display.err_invalid_mode` (en/ar/fa/tr; de/es/pt in their packs).
- `POST /api/settings/report-retention`: the 409 (needs subscription) and the
  400 (invalid mode) are now `httpx.RefuseText`. The 400 goes to
  `save-error`, which now shows it inline instead of the generic banner.
- Comments in `settings.html` updated.

All four `text-response` users were checked branch by branch: display-mode
400 (invalid mode, satellite block), report-retention 409 (entitlement gate;
the settings-sync 409 was already RefuseText), till-name 422 and
staff-languages 400 (already RefuseText).

## Review
Independent Opus review of the Sonnet-built diff.
- **Blocker, fixed:** `e2e/tests/settings-till-name-taken-3308.spec.ts`
  stubbed the 422 without the `refused` header, so the stricter
  `text-response` left the span empty and the spec failed. The production
  handler already marks it; the stub now does too.
- **Nit, fixed:** no test pinned "an unmarked response clears an earlier
  refusal". Added a third test to the 3978 spec.
- **Nit, done:** language-pack follow-ups for the new key (de/es/pt PRs,
  separately reviewed).

## CI follow-up: pages-shuffle (seed 22)
Adding this PR's test file reshuffled the seed-22 order and exposed an
existing leak: `TestSettingsAndStatusBar_ReplicaRegisteredViaMainTill`'s
cleanup re-ran `enroll.Init` from `d.Settings`, which still held the vouched
device, so `enroll.CurrentStatus().ViaMainTill` stayed true for later tests
(`TestEnrolCheckPlan_UnregisteredOrViaMainSaysNotRegistered`,
`TestSettingsPage_ElevationWiredFormsVisibleToCashier` failed). Proven
pre-existing: `main` plus two empty tests under this file's name fails the
same way. Fixed: the cleanup resets from an empty store like its siblings.
Seeds 22 and 1791386291628608919 pass locally.

## Verified
- TDD re-verified by the reviewer: with the production changes reverted, both
  new Go tests fail (`en translation missing`, `X-UT-Response = "", want
  refused`); restored, both pass. The e2e unmarked-body test fails against the
  old `inline-actions.js` and passes with the new one.
- e2e: 3978 (3 tests), 3247 and 3308 specs pass on a real till (6/6).
- `gofmt`, `go build`, `go vet`, `go test ./internal/...`, guard-i18n,
  guard-page-http-error, guard-help-topics, guard-data-access.
- `golangci-lint` not run locally (installed binary built with an older Go);
  CI runs it.
- No help topic change: these are error-path messages only.

**Verdict:** safe to merge. New `en.json` key → `lang-pack-drift` is red on
`main` until the de/es/pt pack PRs merge (same cycle).
