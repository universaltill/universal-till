# Test coverage batch 19: menu launcher page

2026-07-29

`internal/pages/menu_page.go` — the touch-tile menu launcher (`GET
/menu`): builds tiles from `d.Menu` with mapped emoji icons (fallback
"▪️" for unmapped routes), always appends `/help`, and conditionally
appends `/users`/`/translations` for managers only. Previously zero
coverage.

## What's covered

- Configured tiles render with their mapped icons and preserve `d.Menu`
  order (the actual on-screen tile layout).
- A route not in `iconFor` gets the "▪️" fallback icon.
- `/help` always renders, even with an empty configured menu.
- `/users`/`/translations` are absent for a non-manager request and
  present once `UT_AUTH=off`.
- The language-switcher links (`/menu?lang=…`) render.

The test harness explicitly calls `httpx.InitI18n` before rendering,
continuing the pattern established in batches 16-18 after discovering
render tests that skip this only pass by riding on some other test file
in the package having already initialised i18n first.

## Independent review (opus) — clean, two optional strengthenings applied

The review found no bugs. It specifically checked whether reusing one
`*http.Request` across two `mux.ServeHTTP` calls in the manager-gating
test (once before, once after `t.Setenv("UT_AUTH","off")`) risked a
false pass from request mutation between calls — traced
`isManagerOrAuthOff` and confirmed the handler never calls `ParseForm`
or touches the body/context, so the second call's result can only come
from the env var, not a first-call side effect. Left as-is (a fresh
request per call was suggested as future-proofing only, not a fix).

Two optional, cheap strengthenings were applied: an assertion that tile
order follows `d.Menu` order (the real on-screen layout, previously
unverified), and a language-switcher-links assertion (previously
zero coverage of that template branch).

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
