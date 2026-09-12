# /items?lang=fa first load: panel rendered in English (ut-docs#2114)

## What shipped

On a first-ever visit to `/items?lang=fa` (no `ut_lang` cookie set yet),
the left rail rendered in Persian but the Catalog panel's own top-action-row
buttons rendered in English. Pre-existing bug, found during independent
review of ut-docs#2095.

**Root cause**: `internal/pages/items_page.go`'s `registerItemsPage`
resolves the request's locale (`httpx.RequestLocale(r)`, query → cookie →
default) and embeds the selected section's panel via `embedItemsSection`,
which replays the request as an in-process sub-request through the same
mux (`httptest.NewRequest` + `mux.ServeHTTP`). That sub-request's `href`
carries no `?lang=` query param, and on a first `?lang=fa` visit there is
no `ut_lang` cookie yet either (the outer response hasn't been written at
the point `embedItemsSection` is called) — so the sub-request's own locale
resolution silently fell back to the default locale instead of inheriting
`fa` from the already-resolved outer request.

**Fix**: `embedItemsSection` now takes the caller's already-resolved
`locale` as an explicit parameter. It no longer copies any `ut_lang`
cookie from `r.Cookies()` unfiltered; instead it sets one explicitly on
the sub-request from `locale`, so the sub-request's own resolution always
agrees with what the outer page decided, regardless of whether the
`ut_lang` cookie has been written yet.

Files touched: `internal/pages/items_page.go` (behavior fix, doc comment
update), `internal/pages/items_page_test.go` (new regression test only) —
no template/CSS/markup changed, so the UI-surface checklist
(`reference/ux-guidelines.md`, `web/help/**`, screenshots) does not apply;
confirmed via `git diff --name-only`, no `web/`/`.html` file in the diff.

## Independent review

Fresh-context Sonnet subagent (`complexity:easy` → Sonnet review, per
`scrum-master`'s "Model routing by complexity"), isolated worktree, no
prior context of the implementation.

**Verdict: safe to merge**, one non-blocking finding, fixed before this
push:

- `embedItemsSection`'s error-fallback branch re-derived
  `locale := httpx.RequestLocale(r)` a second time, shadowing the
  function's own `locale` parameter with the identical value — harmless
  but a needless second call and a confusing shadow. Fixed by reusing the
  parameter directly.

The reviewer independently re-verified the TDD claim (not taken on the
implementer's word): reverted only `items_page.go` to its pre-fix state
(test file untouched), re-ran the new test, confirmed it fails with the
exact predicted assertions (Persian button absent, English "Add item"
present), restored the fix, confirmed it passes again, and re-ran
build/vet/the full `TestItemsPage` suite post-restore.

The reviewer also traced `httpx.RequestLocale` to confirm it never
returns an empty string (query → cookie → configured default → `"en"`),
confirming the new explicit-cookie behavior is a true no-op for a bare
`/items` visit (no query, no cookie) and for a normal, already-cookied
visit — only the first-ever-`?lang=` case actually changes behavior.
Confirmed exactly one call site of `embedItemsSection` (the signature
change is exhaustively applied), and that the two recurring bug classes
this pipeline watches for (missing `os.MkdirAll` on a file write, a
cwd-relative path where `paths.Data(...)` belongs) don't apply — no file
I/O in this diff.

## Verified beyond automated tests

- `gofmt -l`, `go build ./...`, `go vet ./...` — clean.
- `go test ./...` — full suite green (run twice: before and after the
  reviewer's fix).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  — both green (no SQL outside the data layer; no hardcoded user-facing
  string introduced).
- `golangci-lint run ./internal/pages/...` — 0 issues.
- TDD re-verified twice: once by Dev (implementer) and independently a
  second time by the reviewer, both via a real revert → confirmed-red →
  restore → confirmed-green cycle against the actual test.
- Not run: a Playwright e2e pass. This is a pure server-side
  cookie-propagation fix verified at the real-HTTP-request level
  (`httptest`, real mux, real rendered response body asserting on the
  actual translated string) — no existing e2e spec targets this specific
  regression, and no markup/layout changed for a driven browser check to
  add over the httptest-level assertion.

## Safe-to-merge verdict

Yes. No blocking issues. No deferred items.
