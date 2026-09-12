# Code review: ut-docs#2148 — `?err=`/`?msg=` query-param spoofing in error/success banners

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2148 — "?err= query param renders arbitrary
attacker-chosen text in the page banner, across at least 6 pages."
**PR:** universaltill/universal-till, branch `fix/2148-errkey-generic-fallback`
**Reviewer:** two independent fresh-context Sonnet subagents, isolated
worktrees, second round scoped to a fix the first round required
**Author:** Sonnet (this cycle, `lane:cloud-NN`)

## What shipped

Several page handlers read a page's conventional `?err=` (or, as review
found, `?msg=`) query parameter and pass it straight through to a
template's `{{ T .errKey }}` banner. `httpx.T` falls back to returning the
key itself, unmodified, when the key doesn't resolve to a real i18n
translation — so an unrecognised query value used to render **verbatim**
in the page's banner. Not exploitable as XSS (`html/template` still
escapes the text node) but a spoofing/social-engineering vector: a
crafted link makes the till's own UI display attacker-chosen,
official-looking text.

- `internal/config.I18n.Has(key string) bool` — existence check across
  ALL locales' base/overlay/shop maps (answers "is this a real key
  anywhere," not "does it resolve for locale X").
- `internal/httpx.QueryErrKey(r)` / `QueryMsgKey(r)`, both backed by a
  shared `queryBannerKey(r, param)`: query value passes through unchanged
  if `Has` confirms it's real; otherwise falls back to the existing
  `common.error.server` key (reused deliberately — avoids new-key churn
  across all four locales plus the external `ut-plugin-language-{de,es}`
  packs, same precedent as ut-docs#1663/#1620). No translator wired
  (untranslated test contexts) preserves old raw-passthrough behaviour,
  matching `T`'s own nil-safety.
- 11 call sites switched: `country_settings_page.go`,
  `kitchen_stations_page.go`, `promotions_page.go`, `tables_page.go`,
  `categories_page.go`, `locations_page.go`, `registers_page.go`,
  `fiscal_register_page.go`, `users_page.go`, `auth_page.go`'s
  `GET /pin`, and `fiscal_device_page.go`'s `?msg=` success banner.
- Confirmed already-safe, no change needed:
  `menu_layout_settings_page.go` (pre-existing `menuLayoutErrorKeys`
  allowlist — the precedent this fix generalizes), `bluetooth_devices_page.go`,
  `setup_page.go`, `self_order_shop.go` (all pass only hardcoded/internal
  key constants, never a raw query value).
- `open_orders_page.go` — named in the original issue as a 6th affected
  page, from the still-in-flight `universal-till#1103`/ut-docs#2138 work.
  It exists on `main` (review round 1 caught my own BA-step claim that it
  didn't as factually wrong), but doesn't read `?err=` at all — its
  template only calls `T` with hardcoded keys. Correctly out of scope,
  for a different reason than originally stated.

## Review findings

**Round 1 (full diff, fresh-context Sonnet, isolated worktree)** — one
blocking issue, everything else confirmed clean:

- **Blocker, fixed**: `internal/pages/fiscal_device_page.go:173` had the
  identical vulnerability under `?msg=` instead of `?err=`, feeding a
  **success** banner (`class="login-ok"`) — arguably worse, since a
  crafted link can make the till display a fake "confirmed" message.
  Missed by this PR's original scope (which only searched for `err=`
  literally). Fixed by extracting the shared `queryBannerKey` helper and
  adding `QueryMsgKey`.
- Confirmed via full diff read: all 10 originally-claimed `?err=` call
  sites correctly switched; `Has()` locking/scope correct (`-race` clean);
  `common.error.server` present and reused correctly in all 4 locales;
  the "already safe" pages independently verified, not trusted; TDD claim
  independently re-verified (revert → confirm real failure message →
  restore → confirm pass, atomic within one shell invocation).
- Non-blocking nit accepted as-is: `menu_layout_settings_page.go` blanks
  an unrecognized key to `""` (no banner at all) rather than the new
  generic fallback — pre-existing, inconsistent-but-safe, out of this
  card's scope.

**Round 2 (scoped to the fiscal_device_page.go fix only, per this
ecosystem's "second round earned by a blocker-class finding, scoped to
the fix" rule)** — **no further issues found**:

- Verified the `queryBannerKey` refactor didn't change `QueryErrKey`'s own
  existing behaviour (`go test -run TestQueryErrKey -race`, all pass).
- Independently re-verified the new `QueryMsgKey` TDD claim the same way
  as round 1's.
- Repo-wide grep of every `r.URL.Query().Get(...)` call site (55 total)
  against every `{{ T ... }}` template sink for a third instance — none
  found.
- Full gate re-run clean.

## Verified beyond automated tests

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean, full
  `go test ./...` (whole repo, every package) 0 failures, `golangci-lint
  run ./...` on touched packages 0 issues, `guard-i18n.sh`,
  `guard-data-access.sh`, `guard-page-http-error.sh` all pass.
- No UI surface/layout/route change — only a fallback error/success
  banner's *text content* in an already-existing conditional render — so
  no `web/help/` topic update, screenshot regeneration, or ADR
  implication is owed. No file writes in this diff, so the `os.MkdirAll`/
  `paths.Data(...)` bug classes this pipeline has repeatedly found don't
  apply here (confirmed by grep, not just assumed).
- Commit author `Farshid Mirza <farsid@taskrunnertech.co.uk>` (the
  pipeline owner's current GitHub-linked address per ut-docs#247/#790),
  with `Co-Authored-By: Claude Sonnet 5` — correct per this ecosystem's
  standing git-identity rule.

## Disposition

Approved after the round-1 finding was fixed and independently
re-verified in round 2. No further fixes required. Merge with
`merge_method: "merge"` (never squash/rebase, per ut-docs#250).
