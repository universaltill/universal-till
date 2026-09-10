# 2026-09-10 — fiscal_device.html: locale-formatted last-receipt timestamp (ut-docs#1938)

## What shipped

`web/ui/pages/fiscal_device.html`'s "last receipt" table row rendered
`.latest.IssuedAt`/`.latest.CreatedAt` as a raw, unwrapped RFC3339 string
instead of going through the project's locale-aware `{{ datetime }}`
template func (ut-docs#1130/#1632's convention, already used by
`audit.html`'s `CreatedAt` column). Found by the independent review of
#1894 (a prior raw-timestamp-display sweep that named 6 other sites but
missed this one — out of scope for that PR's diff, filed separately as
#1938).

Fix: wrap both `.latest.IssuedAt` and its `.latest.CreatedAt` fallback in
`{{ datetime ... }}`. `datetime` (not `date`) is the right func because
time-of-day is meaningful for a fiscal receipt's issued-at timestamp —
same judgement `audit.html`/`#1632` already applied to journal/audit rows.
No behavior change beyond display formatting; `IssuedAt`/`CreatedAt` are
plain `string` (RFC3339) on `data.FiscalDeviceReceipt`, so `datetime`'s
string-parsing branch fires correctly, not its `default: return ""` branch.

## What was verified beyond automated tests

- Added `TestFiscalDevicePage_RendersLocaleFormattedIssuedAt` in
  `internal/pages/fiscal_device_page_test.go`, mirroring
  `TestAuditPage_RendersLocaleFormattedCreatedAt`'s existing pattern:
  pins `time.Local` to UTC for determinism, seeds a fiscal device receipt
  with `IssuedAt: "2026-09-03T10:00:00+03:00"`, GETs
  `/fiscal-device?lang=de-DE`, and asserts the raw RFC3339 string is gone
  and the de-DE-formatted, UTC-converted string (`03.09.2026 07:00`) is
  present.
- `make docs-shots` re-run (this session has pre-installed Chromium):
  112/112 Playwright screenshot specs passed across en/fa/ar/tr,
  including 4 `fiscal-device` shots; `guard-docs-shots.sh` passes with
  the regenerated `web/help/img/manifest.json` (only `surface_sha256`
  changed — no topic-hash/manual-prose drift). A one-byte incidental
  diff the same run produced in `web/help/img/ar/sell.png`
  (pre-existing PNG-encoder nondeterminism, unrelated to this page) was
  reverted to keep the diff scoped.
- Full local gate: `gofmt -l .` (clean), `go build ./...` (clean),
  `go test ./...` (all packages green), `golangci-lint run ./...`
  (0 issues), every CI-blocking guard in `ci.yml`'s `build` job (all
  green, including `guard-docs-shots.sh` after the regen above).
  `shellcheck` was not runnable in this session (binary not installed)
  but no `.sh` file is touched by this diff, so it's not applicable here.

## Independent review

Spawned a fresh-context Sonnet subagent (card is `complexity:easy`) in
an isolated worktree. Verdict: **SAFE TO MERGE**, no blocking findings.
It independently:
- confirmed `datetime` (vs `date`) is the correct func and is in scope
  for this template via `FuncsFor(locale)`;
- confirmed `IssuedAt`/`CreatedAt` are `string` so the string-parsing
  branch fires;
- ran `go build ./...`, `go test ./internal/pages/... -run
  TestFiscalDevicePage -v` (pass), the whole `internal/pages` package
  suite (pass), `gofmt -l .` (clean), `golangci-lint run
  ./internal/pages/...` (0 issues);
- **independently reproduced the TDD claim**: reverted only the
  template line back to the raw form, re-ran the new test, confirmed it
  **failed** with the expected "must show the de-DE-formatted date+time"
  complaint, restored the fix, confirmed it **passed** again;
- checked for secrets/real client names (none), UI/RTL concerns (none —
  a one-line template wrap, `.latest` already null-guarded), and the two
  standing recurring bug classes (file-write without `os.MkdirAll`,
  cwd-relative path instead of `paths.Data(...)`) — neither applicable
  to this diff;
- confirmed the `fiscal-device` help topic's prose never referenced the
  old raw-timestamp format, so no manual update is needed for this pure
  display-format fix.

(The review agent flagged that its assigned worktree was initially
pointed at an unrelated, already-merged commit rather than this
branch — an environment/tooling quirk unrelated to this diff — and
worked around it via its own detached worktree on the correct branch
before reporting the results above.)

## Safe-to-merge verdict

Yes. Minimal, well-tested, no deferred items.

## Deferred / out of scope

None.
