# Review — voice bug-report: capture-time locale + cloud transcription progress

Ticket: universaltill/ut-docs#397
Date: 2026-08-07
Companion PR: `ut-cloud` (same branch name `fix/397-voice-locale-and-progress`,
same review pass — this is one cross-repo change, documented once per repo
per this pipeline's own convention)

## What shipped (this repo's half)

Field report: the in-till bug-report voice note always transcribes to
English, and the process looks stuck with no visible progress. Root cause
lived on both sides — this repo's half is capturing and carrying the
operator's locale through to the cloud:

- `internal/issuereport/bundle.go` — `Meta` gains a `Locale string` field;
  `Save`'s signature gains a `locale` parameter, threaded into the persisted
  `meta.json`. Empty is a valid, expected value (pre-existing bundles, a
  JS-less capture path) — never rejected.
- `internal/pages/issue_report_page.go` — the `POST /api/issue-reports`
  handler resolves the operator's UI locale the same way every page render
  does (`httpx.ResolveLocale`), **then validates it against the shipped
  locale set** (`httpx.AvailableLocales`, new) before saving — a
  review-round addition, see below.
- `internal/cloudsync/issue_reports.go` — the cloud upload always sends a
  `locale` multipart field, even when empty, so the cloud side can tell
  "no locale known" from "field never arrived" the same way on every
  upload.
- `internal/httpx/httpx.go` — new `AvailableLocales()`, a thin wrapper over
  the already-wired `config.I18n.Available()`, exported so a handler outside
  the template layer can validate a locale value before trusting it
  downstream.

Invisible plumbing — no new user-facing string, no screen a shop owner
sees changes, so no `web/help/*` update and no i18n locale-file changes.

## Independent review

Reviewed by a separate Opus subagent (this card is `complexity:hard`,
authored by Fable — review deliberately runs on a different, stronger model
per this pipeline's standing rule) against both repos' diffs together, since
the change is one cross-repo contract. The reviewer **built and ran**
everything (not a read-only pass): `go build`/`go vet`/`go test ./...` in
both repos, `guard-data-access.sh`/`guard-i18n.sh` here, `scripts/ci/verify.sh`
in `ut-cloud`, and independently re-verified the core TDD claim by reverting
just the Whisper `language`-param logic and confirming both a unit and a
service-level test fail with real assertions, then restoring and confirming
green again.

No blockers. Three real-but-non-blocking findings, all fixed in this pass
(not deferred — none needed a second full review round per this pipeline's
"only earned by a blocker" rule; these were scoped, verified fixes to the
same diff):

1. **A permanently-failing transcription would strand a report at
   "transcribing" forever** (the cloud-side `transcribeOne` never reset
   status on error) — inverts the ticket's own goal ("looks stuck" becomes
   "looks perpetually in-progress, indistinguishable from a live call").
   Fixed on the `ut-cloud` side (see that repo's own review record) by
   resetting to `received` on any failure path.
2. **The staff review page never auto-refreshes**, so the new
   `transcribing` pill only appears on a manual reload — also fixed on the
   `ut-cloud` side.
3. **Locale reached a downstream service call with no validation** — this
   repo's own finding. `ResolveLocale` trusts a raw `?lang=`/cookie value
   for template rendering (an unrecognized value there just misses
   translations, harmless), but this same value now also travels to
   Whisper's `language` param, and compounds with finding 1: an unknown
   code Whisper rejects would retry forever. **Fixed**: `isAvailableLocale`
   clamps to `httpx.AvailableLocales()` (en/fa/tr/ar), falling back to `""`
   (auto-detect) for anything else — TDD'd
   (`TestIssueReportAPI_UnknownLocaleFallsBackToEmpty`, confirmed failing
   against the pre-fix code with the raw value passing through, then
   passing after the guard).

Nitpicks noted, not fixed (genuinely cosmetic, out of scope):
`ResolveLocale`'s cookie-write side effect on an API path is unreachable in
practice (the client never sends `?lang=` on this endpoint); the pre-existing
`discarded` pill has no CSS rule in either `ut-cloud` admin template — predates
this diff, not touched by it, worth a follow-up but not blocking.

## Verified beyond automated tests

- TDD re-verified personally (not just on the implementer's word): reverted
  each of the two behavioral changes in isolation (locale threading in
  `Save`'s signature; the new `isAvailableLocale` guard), re-ran the
  specific tests, confirmed real compile/assertion failures with the actual
  expected-vs-got messages, then restored and confirmed green.
- Full gate re-run personally after every fix, whole repo: `go build ./...`,
  `go vet ./...`, `go test ./...` (one pre-existing failure —
  `TestSaveCleansUpDirectoryOnWriteFailure`, confirmed via `git stash` to
  fail identically on unmodified `origin/main`; this sandbox runs as root,
  which ignores the test's 0500 read-only directory — an environment
  artifact, not a regression), `guard-data-access.sh`, `guard-i18n.sh`, all
  green. `gofmt -l` clean on every touched file.
- No real client/shop name in any test fixture; nothing secret-shaped.
- Checked for this pipeline's two recurring bug classes (missing
  `os.MkdirAll`, cwd-relative path instead of `paths.Data`) — not
  applicable, this diff adds no new disk writes.

## Explicitly deferred

- The pre-existing unstyled `discarded` pill in the `ut-cloud` admin
  templates — real, but predates this diff and isn't part of #397's scope.
  Worth a small follow-up card if it bothers anyone; not filed separately
  given how minor it is.

## Verdict

Safe to merge. Builds, tests, and guards green; independent review found
and this pass fixed three real issues, re-verified with tests of their own;
no client-visible behavior changed on this repo's side (invisible plumbing).
