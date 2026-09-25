# Review — rederive cached settings after LAN admin sync (ut-docs#2790)

- **Change:** one `publishCachedSettings(ctx, store, st, authDisabled)` in
  `internal/pages/init.go` republishes every `httpx.Init*/Set*` process
  global (currency, theme, default locale, locale generation, UI
  scale/OSK, self-order/display mode, dine-in/takeaway prompt, idle-lock
  timer). Called by both boot (`Init`) and `newRederiveSettings`, so a LAN
  admin pull or cloud directive can no longer miss one the way the
  order-type prompt and default locale were missed before. Tests:
  `internal/pages/rederive_cached_settings_test.go` — an end-to-end real
  pull against a real primary server, plus an AST guard that fails if a new
  `httpx.Init*/Set*` global is added without landing in the helper.
- **Author:** Claude Opus 5.5. **Reviewer:** Claude Sonnet 5 (independent,
  different model).

## Findings and dispositions

1. **Info — guard scope is `init.go` only.** `TestCachedSettingsGlobals_AllPublishedByOneHelper`
   parses only `internal/pages/init.go`, so a direct `httpx.Init*` call
   added in some *other* file in the package would not be caught — e.g.
   `cloudsync_wire.go`'s existing explicit `InitOrderTypePromptMode` call
   right after its own `Settings.Set` (kept deliberately, so a nil
   `rederive` still takes effect). Not a false pass for the bug this card
   fixes (both historical drift sites — boot and the replica/cloud
   re-derive — live in `init.go`), but worth knowing the guard isn't
   package-wide. Accepted, not fixed.
2. **Checked clean, no fix needed:**
   - Boot ordering: `publishCachedSettings` moved to right after
     `state.UIScale`/`authDisabled` are resolved (both still computed
     before it, as before); `InitI18n` still runs before it; idle-lock's
     server-side `authSvc.SetIdleLockMinutes` stays unconditional and
     separate from the cosmetic client timer. No `authDisabled` double
     declaration.
   - Every republished global is `atomic.Value`/`atomic.Int64`-backed, so
     re-publishing concurrently with request handlers reading them is
     race-safe (confirmed with `-race`).
   - Cost: `newRederiveSettings`'s caller (`syncPullTick`) only invokes
     `refresh` when the admin bundle's fingerprint actually changed, not
     every 30s tick — two extra `Settings.Get` reads and a handful of
     atomic stores on an already-rare event, no reload/reallocation churn.
   - Per-till keys: `display.*` is in `data.PerTillSettingPrefixes`
     (`internal/data/sync_admin_repo.go`), so `ApplyAdmin` never overwrites
     a replica's own UI scale/OSK/display-mode from the primary's bundle;
     re-reading them from the local store on rederive is a no-op by
     design, matching the pre-existing boot behaviour.
   - Locale mid-session: `SetDefaultLocale` only moves the fallback a
     request with no `ut_lang`/cookie override resolves to; locale
     generation republish just copies the already-persisted counter into
     the live cache, so a rederive that changes nothing can't retire a
     browser override that a real generation bump didn't already retire.

## Verification

`go test -race -count=1 ./internal/httpx/`; `go test -count=1 -run
'Rederive|Cached|Sync' ./internal/pages/...` and the same two new tests
individually with `-race`; full `go test -count=1 -timeout 20m
./internal/pages/...` — all pass. TDD re-verified personally: stashed
`init.go`/`cloudsync_wire.go` (keeping the new test file), both new tests
failed with the exact claimed symptom (`order_type_prompt`/`locale`/
`locale_generation` stuck at boot-time values; guard reports no
`publishCachedSettings`), then restored. gofmt, go vet, golangci-lint,
guard-data-access: clean.

**Verdict: safe to merge.**
