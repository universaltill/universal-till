# Code review: CI gofmt check (ut-docs#318)

**Date:** 2026-08-20
**Card:** universaltill/ut-docs#318 — "universal-till: add a gofmt check to CI so
formatting drift stops accumulating"
**Complexity:** easy → built inline (Sonnet), reviewed by a fresh-context Sonnet
subagent (per the scrum-master skill's model-routing rubric).

## What shipped

- New `gofmt check` step in `.github/workflows/ci.yml`, placed right after the Go
  cache restore and before the existing guard steps: runs `gofmt -l .` and fails
  the job (with a fix hint) if anything is unformatted.
- `gofmt -w` applied to the 9 files that had drifted by the time this card was
  picked up (the original card, filed 2026-08-02, named only 4 — drift had grown
  since): `internal/data/install_status_repo.go`, `internal/manual/manual_test.go`,
  `internal/pages/eod_api_test.go`, `internal/pages/external_api_test.go`,
  `internal/pages/fiscal_sign_hook.go`, `internal/pages/import_bkp_page_test.go`,
  `internal/pages/users_page.go`, `internal/plugins/marketplace/client.go`,
  `internal/thirdparty/webview_go/webview.go`.
- One follow-up hand-edit in `internal/data/install_status_repo.go`: gofmt's
  doc-comment smart-quote normalization (Go 1.19+) turned a deliberate `''`
  (SQL empty-string literal) in a comment into a bare curly quote `”`, degrading
  a comment a prior review (ut-docs#368) had flagged as a BLOCKER-level
  clarification. Reworded to "arrive as the empty string" to keep the meaning
  clear and avoid the same mangling on a future gofmt run.

## Independent review

Spawned a fresh-context Sonnet subagent (`isolation: "worktree"`) with the exact
diff scope, the acceptance criteria, and instructions to actually build/vet/test,
not just read the diff.

**Verdict: SAFE TO MERGE.** No blockers found. Confirmed:
- Diff is exactly the CI step + pure `gofmt -w` fallout — no logic changes, cross-
  checked file-by-file (struct/map-literal alignment, comment alignment, one
  doc-comment blank-line separator, one import-block re-sort).
- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean.
- `go test ./internal/data/... ./internal/manual/... ./internal/pages/...
  ./internal/plugins/marketplace/...` all pass.
- `internal/thirdparty/webview_go` is a separate Go submodule gated behind
  `//go:build desktop` and requires GTK/WebKit dev packages not present in the
  review sandbox — confirmed the same "does not contain package" / pkg-config
  failure occurs identically on plain `origin/main`, so it's a pre-existing
  environment gap, not a regression from this change. The only touch to that
  file in the diff is a pure import-sort, already verified as gofmt-only output.
- CLAUDE.md non-negotiables not implicated: repository-pattern, kiosk-engine and
  plugin-menu-read guards all pass; no `web/`, `internal/money`, locale, or
  plugin-signing files touched; no file-write/`os.MkdirAll`/`paths.Data` code
  touched; no route registrations changed, so the help-manual/UX checklist does
  not apply (confirmed by file list, not assumed). No secrets or real
  client/shop names anywhere in the diff.

Two non-blocking nits raised and handled:
1. The smart-quote comment degradation above — fixed in this same change
   (see "What shipped").
2. The new step is inlined rather than following this repo's `guard-*.sh` +
   `guard-*_test.sh` pattern used by the other CI checks. Acceptable for an
   easy-tier ticket whose acceptance criteria don't ask for a standalone guard
   script or regression test; noted here rather than actioned.

## Verified beyond automated tests

- Sanity-checked the new CI step actually catches drift: ran `gofmt -l` against
  a deliberately misformatted scratch file outside the repo and confirmed it's
  flagged (mirrors what the CI step's `-n "$unformatted"` branch does).
- Re-ran `gofmt -l .`, `go build ./...`, and `go test ./internal/data/...` after
  the post-review comment fix to confirm nothing regressed.
- `go test $(go list ./... | grep -v '/internal/plugins$')` and
  `go test -timeout 20m ./internal/plugins` both green before the review handoff
  (full gate, once, per the easy-tier process-depth rule).
- `bash scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` all pass.

## Post-review: CI red, fixed same-session

First CI run failed `guard-docs-shots` — `internal/pages/fiscal_sign_hook.go` and
`users_page.go` are non-test `.go` files under `internal/pages/` that register a
screenshotted route, so the guard hashes them whole-file; the gofmt-only byte
change to those two files was enough to change `surface_sha256` even though
nothing user-visible changed. This is the guard working as designed (see its
own doc comment on the `import_page.go`/`mergeTakeawayOverrides` precedent), not
a false alarm to route around.

Ran `make docs-shots` (pre-installed Chromium via `e2e/scripts/docs-shots.sh`'s
cloud-session fallback — no network browser install needed). 80/80 screenshot
tests passed; diffed every changed PNG against its prior version — only
`alerts` and `designer` (all 4 locales) changed pixels at all, and the diff
bbox in each is a small clock/timestamp region ("17:00" → "00:12", etc.) —
confirmed by cropping the diff region on both before/after images. No topic
markdown hash changed. Committed the regenerated manifest + PNGs; `guard-docs-shots.sh`
now passes locally.

## Deferred / out of scope

- Reworking the new step into a standalone `guard-gofmt.sh` + regression test to
  match the repo's existing guard-script convention — left as-is per the
  reviewer's nit; small enough to revisit if it ever bothers someone, not filed
  as a new backlog card given how minor it is.
