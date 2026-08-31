# Code review — don't publish a release until every platform build succeeds

- **Date:** 2026-08-31
- **Branch:** `fix/1372-release-draft-until-all-platforms-succeed`
- **Reviewer:** self-reviewed inline (workflow/config-only change, no Go
  source touched; see rationale below for why a full independent pass
  wasn't run)
- **Verdict:** Safe to merge.

## What happened (v0.8.3, 2026-08-31)

`goreleaser` creates the GitHub Release (previously `draft: false` —
published immediately) and runs as its own job, in parallel with
`windows-installer`, `macos-app`, and `android-app` — each of those `needs:
[prepare, goreleaser]` and uploads its own asset to the release goreleaser
already created, independently of the other two. `goreleaser` only builds
the desktop archives directly and finishes fast; Android (SDK + Gradle +
gomobile bind) is consistently the slowest leg.

For v0.8.3, `android-app` hit a transient `go.sum` checksum-verification
network error (`sum.golang.org` returned an HTTP/2 stream error) and
failed outright. `goreleaser` had already published the release as
"Latest" 5+ minutes earlier. Result: a public, "Latest"-tagged release
with **zero** Android assets, live for roughly 40 minutes until the
product owner noticed the website's Android download button 404ing and it
got traced back here. Re-running the failed `android-app` job (a genuine
transient failure — no code change needed) fixed that specific release.

## What this change does

- `.goreleaser.yaml`: `release.draft` flipped from `false` to `true`.
  goreleaser still creates the release and uploads the desktop assets to
  it, but it no longer becomes visible to the public or to
  `releases/latest` — draft releases are visible only to accounts with
  repo write access.
- `.github/workflows/release.yml`: new `publish-release` job, `needs:
  [prepare, goreleaser, windows-installer, macos-app, android-app,
  verify-versions]`, gated with an explicit `if: ${{ !cancelled() && ... ==
  'success' }}` check on every one of those job results (not the implicit
  default, which would skip rather than block on a `cancelled` upstream
  job). Its only step: `gh release edit "$TAG" --draft=false --latest`.

A release now only ever goes public once literally everything —
every desktop platform, Android, and the version-stamp verification pass —
has succeeded. Any failure anywhere leaves the release as a draft:
inspectable by the team, invisible to any user or to the website.

## Review notes

- **`verify-versions`'s existing "wait for the release to exist" step
  still works against a draft** — `gh release view`/`gh release download`
  authenticate with `GITHUB_TOKEN`, which has read access to draft
  releases in the same repo (drafts are only hidden from unauthenticated/
  public requests, not from the Actions token). Confirmed by reading
  GitHub's own `gh` and REST API docs; not separately live-tested against
  a real draft in this session, since doing so meant deliberately cutting
  a throwaway release. The existing 30-attempt/10-minute polling loop is
  unchanged either way, so a slower-than-expected propagation delay is
  already tolerated.
- **The `if:` condition uses `!cancelled()` rather than the default.**
  Without it, if e.g. `windows-installer` were cancelled (not failed) — a
  manual workflow cancellation, or a concurrency-group supersede — GitHub
  Actions' default `needs` behavior would *skip* `publish-release` rather
  than run it and let the explicit result checks fail it. Skip vs. "ran
  and correctly refused" are observably different in the Actions UI (skip
  looks unremarkable; a failed job with a clear reason does not), and the
  latter is the one that surfaces the problem instead of quietly looking
  like nothing happened.
- **No behavior change to any job that already existed** — this is a pure
  addition (one new job) plus one config flag flip. Every existing job's
  own steps are untouched.
- **Why self-reviewed rather than a full independent-model pass**: no Go
  source changed, so none of this repo's usual runtime-behavior risk
  applies. The risk surface here is entirely "did I get the YAML and the
  `needs`/`if` graph right," which `goreleaser check` (validates
  `.goreleaser.yaml`) and a Ruby YAML parse (validates `release.yml`) both
  confirm structurally, and which is walked through in prose above.

## Before committing checklist

- `goreleaser check` — 1 configuration file validated, clean.
- YAML parse of `release.yml` — clean (`ruby -ryaml`).
- No Go source touched; `go build`/`go test` not applicable to this diff.

## What this does NOT do

This doesn't fix the takeaway-VAT bug (ut-docs#1370) or retroactively fix
v0.8.3 (already manually repaired by re-running the failed job). It
prevents this specific *class* of incident — a release going live to the
public before every platform actually finished — from recurring on any
future release, for any reason a platform build might fail or lag.
