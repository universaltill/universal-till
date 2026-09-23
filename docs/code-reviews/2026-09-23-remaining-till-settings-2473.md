# 2026-09-23 — Remote-settable: remaining #2289 till settings (ut-docs#2473)

## What shipped

The till-side mirror of ut-cloud's `AllowedTillSettingKeys` —
`allowedRemoteTillSettingKeys` and `cloudSetTillSetting`
(`internal/pages/cloudsync_wire.go`) — accepted only 6 remote-directive
keys. This adds the three remaining ut-docs#2289 settings, all pre-existing
LOCAL till settings (only the remote-directive path is new):

- `data.OrderTypePromptModeKey` (`sale.order_type_prompt`, top |
  before_item | at_pay) — mirrors `POST /api/settings/order-type-prompt`.
- `data.SellScreenCategoriesTabKey` (boolean, canonical wire value "1"/"0")
  — mirrors `POST /api/settings/categories-tab`.
- `data.SaleDisplayNoSchemeKey` (`sale.display_no_scheme`,
  trading_period_reset | lifetime_no_reset) — mirrors
  `POST /api/settings/order-no-scheme`.

Each new `case` in `cloudSetTillSetting`'s switch validates the value
exactly as its local form handler does, fails closed on anything else (no
write, no re-derive), and writes only once validated. The order-type-prompt
case additionally makes the same `httpx.InitOrderTypePromptMode(value)`
live-republish call the dedicated local handler makes — this setting lives
OUTSIDE `common.RuntimeState`/the generic `rederive` callback (the same
`ut-docs#2121`-class gap `display.mode`'s own re-derive documents), so
without it a remote directive would leave the sale screen showing the OLD
placement until the till restarts. The other two keys need no such call:
`ButtonStore.CategoriesTabEnabled` and `POSRepo.NextDisplayNo` both read
their setting fresh from the DB on every call, confirmed by the independent
reviewer to be the only two readers of either key in `internal/`.

Companion ut-cloud PR (same branch name) adds the matching
`AllowedTillSettingKeys`/`TillSettingFields` entries and locale strings —
land together; until this build ships, a cloud directive naming these keys
is refused there (visibly, in the directive's result), not silently.

## Independent review (Opus, isolated worktree)

Verdict: **pass, no blockers.**

- Deleted the `httpx.InitOrderTypePromptMode(value)` call and re-ran
  `TestCloudSetTillSetting_OrderTypePromptModeLiveRepublishes` — failed
  with "cloud directive did not live-republish the prompt mode"; restored,
  passes. Confirms the live-republish gap is genuinely closed, not just
  documented.
- Confirmed neither `CategoriesTabEnabled` nor `NextDisplayNo` caches —
  both are the only readers of their key and both read fresh every call.
- Confirmed value validation matches each local form exactly, the boolean
  case's `strconv.ParseBool` normalizes every accepted spelling to
  canonical "1"/"0" before writing, and the `default:` fail-closed branch
  and the existing non-whitelisted-key rejection are both untouched by the
  new cases (Go switch has no fallthrough).
- Confirmed the stale "concurrent, unmerged work" doc comment was updated.
- One nit, fixed: `TestCloudSetTillSetting_WhitelistedKeysWrite`'s table
  only exercised the boolean's "true"/"False" spellings. Added cases for
  the till's own canonical "1"/"0" wire values and one more
  `strconv.ParseBool` spelling ("t"), so the table can't silently pass a
  hand-written parser that only handled the two spellings it happened to
  be given.
- Two pre-existing patterns noted as out of scope, not regressions: a
  remote `set_till_setting` write isn't `settingsAudit`-logged the way a
  local change is (true of all 6 existing keys too), and the heartbeat
  reports "" rather than the effective default before a key is ever set
  (the documented, existing design).

## Gate

`gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean,
`guard-data-access.sh` and `guard-i18n.sh` green (no SQL added, no new
user-facing strings — the new validation errors ride the same plaintext
directive-result channel the existing 6 cases already use, not till-side
UI), full `go test ./...` green across every package (`internal/pages`,
`internal/data`, `internal/pos`, `internal/httpx`, `internal/cloudsync`,
e2e seed tooling, etc.) — no e2e or ut-cloud coverage referenced
`set_till_setting`/`AllowedTillSettingKeys` before this change, so nothing
pre-existing needed updating for the new keys.

## Verdict

**Safe to merge**, alongside the companion ut-cloud PR.
