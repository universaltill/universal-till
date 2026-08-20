# Code review: push the till's locale on owner notifications

**Issue:** ut-docs#658 · **Repo:** universal-till (companion half; the fuller
record with the independent review's findings lives in
`ut-cloud/docs/code-reviews/2026-08-20-notification-locale-658.md` — this
file covers only what changed on this side).
**Complexity:** medium · **Built by:** Sonnet (inline) · **Reviewed by:**
Opus (fresh-context subagent, reviewed both repos' diffs together as one
change).

## What shipped

`internal/alerts/alerts.go`'s `pushNotify` (the daily low-stock digest and
unusual-sales background pushes) now includes `"locale":
httpx.DefaultLocale()` in the JSON body posted to `/api/v1/stores/notify`
— the till's install-time `UT_DEFAULT_LOCALE`, the documented "no request"
source `DefaultLocale()` exists for exactly this kind of background job.

## Independent review finding that touched this repo

The review (see the ut-cloud record for the full list) found the original
comment here overclaimed what this value actually represents — it called
it "the till's own configured UI locale," implying a live, in-app-
switchable preference. In reality `DefaultLocale()` reflects
`UT_DEFAULT_LOCALE` as set at boot; the setup wizard's language step only
sets a per-browser `ut_lang` cookie and never persists a shop-wide
override that `DefaultLocale()` would pick up. Fixed by rewriting the
comment to say this plainly and pointing at the filed follow-up rather
than leaving the overclaim in place. No code-behavior change was needed on
this side — `DefaultLocale()` already returns the right *value* (the
till's actual configured locale, in BCP-47 form); the receiving end
(ut-cloud) needed the BCP-47-normalization fix, not this repo.

## Verified

- `go build ./...` clean; full `go test ./...` for this repo — all
  packages pass.
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-kiosk-engine.sh`, `bash scripts/ci/guard-i18n.sh`
  all pass (no new user-facing strings; no raw SQL added outside
  `internal/data`).
- `TestPushDigest` updated to assert the pushed `locale` field carries the
  real BCP-47 shape (`"de-DE"`, not a bare code) — this repo's test proves
  the till sends what it's actually configured with, unmassaged; the
  normalization is ut-cloud's responsibility and is tested there.

## Verdict

Safe to merge as the companion half of ut-docs#658 — see the ut-cloud
record for the full independent-review narrative and TDD re-verification.
