# Review — plugin HTTP header wait follows the job deadline (ut-docs#4035)

**Branch:** `fix/4035-job-header-wait` · **Card:** ut-docs#4035 (p1, medium)
· **Built by:** Opus 5.5 · **Reviewed by:** Fable (independent subagent)

## What shipped

- `internal/plugins/wasm_egress.go`: the plugin `http_request` / `http_open`
  client no longer uses `Transport.ResponseHeaderTimeout: 30s`. A
  `headerWaitTransport` RoundTripper applies the 30 s header wait
  (`pluginHeaderWait`) per hop **outside a job**, plus the old 2-minute
  whole-request backstop (`pluginRequestBackstop`, body read included).
  Inside a job (`WithJob`, deadline `limits.long_call_s` ≤ 300 s) the job's
  own deadline is the only bound, so a non-streamed call to a slow
  self-hosted model (headers only when generation ends) can finish. The
  client timeout moved to `maxJobDeadline + 10s` as a pure backstop.
- Tests: `wasm_egress_headerwait_test.go` — slow loopback server (scaled:
  header wait 300 ms, headers after 1.2 s): `http_request` refused in an
  ordinary event, answered in a job with a 120 s deadline, still bounded by
  a short job deadline; `http_open` refused/answered the same way.
  `runGuestPayloadCtx` helper in `wasm_hostfns_test.go`.
- Docs: `docs/plugin_guidelines.md` (jobs section); ut-docs
  `reference/plugin-host-functions.md` → HTTP (separate ut-docs PR).

## Findings (Fable)

| # | sev | finding | outcome |
|---|---|---|---|
| 1 | minor | timer firing just after headers arrived returned a body that reads as cancelled | **fixed** — `!timer.Stop()` now fails the call consistently (same shape as `http_read`'s idle timer) |
| 2 | minor | short-deadline subtest could block forever if the guest died before sending | **fixed** — bounded `select` |
| 3 | minor | raising the client timeout lifted the 2-min whole-request cap for ordinary events | **fixed** — cap kept per request outside jobs |
| 4 | minor | `http_open` under a job claimed in docs, untested | **fixed** — subtest added |
| 5 | nit | test mutates package var `pluginHeaderWait` | accepted, noted on the var (package has no parallel tests) |
| 6 | nit | ctx-cause substitution could relabel an egress denial | **fixed** — no substitution except the header-wait timeout |

Reviewer also checked: the job marker only reaches `startPluginJob` →
`AskPlugin` (never published, scheduled or sale-path events); every cancel
path is released (error, body close, stream release, redirect hops);
demo-mode guard still denies before any timer; errEgressDenied still maps
to `-2`.

## Verified beyond the unit tests

- TDD: the new test fails on `main`'s transport (`http_code = 0 (status
  200), want -3`) and passes with the fix.
- **Real-duration run of the card's AC** (temporary copy of the test,
  unscaled): 30 s header wait, server answering headers after 40 s —
  ordinary event refused at ~32 s; job with `long_call_s` 120 answered after
  ~42 s; job with a 20 s deadline cut at ~22 s.
- Gate: gofmt clean, `go build ./...`, golangci-lint 0 issues, every
  `ci.yml` build-job guard (except the two shellcheck-binary checks —
  shellcheck isn't installed in this cloud container; no script touched),
  `go test ./...` (all packages; `internal/plugins` with CI's
  `-timeout 20m`: ok in 645 s), re-run of HTTP/Egress/Stream/Job tests after
  the review fixes: ok.

## Verdict

Safe to merge. Unblocks ut-docs#2851 on this point.
