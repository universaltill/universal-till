# Review — setting-bound egress grants (ut-docs#2899)

**Change:** new permission forms `net:@setting:<urlKey>` and
`tcp:@setting:<hostKey>:<portKey>`: the address an admin configured in that
plugin setting counts as an **exact** egress grant, resolved at dial time by
the #2891 policy (LAN/loopback reachable only at that address; till port
never; redirects re-checked; per-read/write TCP re-check so a changed setting
cuts an open handle). Host normalisation (case, trailing dot, IDNA, `[::1]`).
Manifest parse refuses malformed forms and keys not declared in `settings`.
Permission UI shows the binding and the **currently resolved address** (or
"not set"). Audit `plugin_settings_saved` records the changed keys (never
values). tax-tr 0.1.1 declares `tcp:@setting:okc.host:okc.port` (restores ÖKC
after #2891). Also: ut-plugin-integration-webhook 1.1.0
(`net:@setting:endpoint_url`), ut-cloud marketplace accepts the forms with a
"may be on the shop LAN" reviewer flag, ut-docs host-function docs.
Author: Opus 5.5. Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | "A plugin cannot write its own settings" holds for wasm only — a plugin's page is same-origin HTML with no CSP, so a page script can POST settings with a manager's session (pre-existing; same script could self-grant `net:*`) | caveat in code + docs; hole fixed by **#2892** (raised to p1, in progress) |
| 2 | minor | Default value becomes the grant; UI showed only setting names | UI shows the resolved current address |
| 3 | minor | New keys missing from de/es packs | pack PRs right after core merges (new-key rule) |
| 4 | minor | Help sentence only in en/de | ar/fa/tr added (plus their missing Permissions paragraph) |
| 5 | minor | Audit recorded only a count | changed keys recorded, never values |
| 6 | minor | per-call settings read on tcp read/write | accepted (≤4 handles) |

**Checked, no issue (reviewer):** every writer of `plugin_settings` is a
human-authorised path (manager settings page, install defaults, main→replica
admin bundle); wasm exports `settings_get` only, `storage_set` is a separate
table; URL userinfo/port tricks resolve to the host the dialer connects to;
empty/unset setting grants nothing; multi-IP names still refuse the till
port; till and cloud share one grammar for the forms; tax-tr/webhook versions
and READMEs accurate.

**Verification:** `go build ./...`, `go vet`, gofmt; `go test -timeout 40m
./internal/plugins/... ./plugins/...` + race on egress/TCP/setting/OKC;
pages tests; guards i18n, data-access, core-neutral, help-topics, help-drift;
`make docs-shots` on the rebased tree.

**Verdict:** safe to merge; language packs follow in the same cycle.
