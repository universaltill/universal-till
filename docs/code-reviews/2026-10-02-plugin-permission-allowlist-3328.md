# Code review — plugin manifest permission allow-list, `endpoint` setting type, consent descriptions (ut-docs#3328)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3328 (ADR-0121 §2 build card).
- **Branch:** `feat/3328-plugin-permission-allowlist` (WIP snapshot `a262304`,
  review fixes `27125da`, this record on top).
- **Author:** this cycle's build model (not the reviewer).
- **Reviewer:** independent pass, Fable, a different model from the author,
  with no visibility into the implementation reasoning. Everything below was
  re-derived from the diff, the ADR and the running tests, not taken from
  the Dev/Tester summaries.
- **Verdict: SAFE TO MERGE** after the fixes in finding 1–3 (committed on
  top of the WIP snapshot, history untouched).

## What shipped

- `internal/plugins/permission_allowlist.go` (new): `isKnownPermission` /
  `validatePermissions`. Until this, nothing validated `Manifest.Permissions`
  — any string was persisted by `PersistManifest` and offered to an
  operator for granting. Accepted now: the exact names core checks
  (`storage`, `events:receive`, `payments:reconciliation`, `tcp:*`,
  `devices:printer`), `net:<host>` / `net:*`, `tcp:<host>:<port>`, the
  setting-bound `net:@setting:` / `tcp:@setting:` forms (already validated
  by `validateSettingBoundPermissions`, reused), `<entity>:read|write`
  (open shape by design — entity names are plugin-defined), the ten
  ADR-0121 §2 names, and a grandfathered legacy set (`storage.local.<N>KB|MB|GB`,
  `ui.locale`, `ui.theme`, `ui.page`, `ai.configure`, `pos.tender`).
- `internal/plugins/manifest.go`: `ParseManifest` calls
  `validatePermissions` after the setting-bound check (so a malformed
  `net:@…` still gets its specific error). `manifest_verifier.go`:
  `VerifyManifest` (the marketplace install path, which never goes through
  `ParseManifest`) appends the same error — both install paths refuse.
- `internal/plugins/permission_setting.go`: `splitTCPGrantAddr` extracted so
  the allow-list reads a `tcp:` grant exactly as `tcpGrantMatch` does.
- `internal/plugins/secret_settings.go`: `SettingTypeEndpoint`,
  `isValidSettingType` extended, `ValidEndpointURL`, nil-safe
  `Manifest.SettingDeclaredEndpoint`.
- `internal/pages/plugin_settings_page.go`: `secretSettingCheck` also
  returns `declaredEndpoint`; `POST /api/plugins/{id}/settings` validates
  every endpoint-typed row **before the first write** (same placement as
  the takeaway-overrides check, ut-docs#190) and refuses the whole save
  with a localized 400. Blank clears the setting.
- `internal/pages/plugin_permission_view.go`: `permissionBadge.DescKey`,
  `permissionDescKey`, `storeItem.PermissionDescriptions`.
- `web/ui/pages/plugins_store.html`: a badge with a description loses its
  `title=` tooltip and gets a visible `<li>` line (`<code dir="ltr">name</code> — description`)
  under the badge row; every other permission renders byte-for-byte as before.
- `web/locales/{en,ar,fa,tr}.json`: 10 `plugins.permissions.desc.*` keys +
  `plugins.settings.invalid_endpoint` (`%s` in all four).
- `web/help/{en,ar,de,fa,tr}/plugins.md`, `docs/plugin_guidelines.md`.
- Tests: `permission_allowlist_test.go` (5), `endpoint_setting_test.go` (2),
  `plugin_settings_endpoint_test.go` (1), `plugin_permission_view_test.go` (2).

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | **Medium (fixed, `27125da`)** | `net:<host>` accepted any whitespace-free string and `tcp:<host>:<port>` any non-empty host: `net:*.example.com`, `net:**`, `net:https://x`, `net:host:443`, `net:a/b`, `tcp:*:9100` all passed (probed directly). None of those can ever match in `netGrantMatch`/`tcpGrantMatch` (`normGrantHost` of a wildcard pattern never equals a real host), so the operator would be shown — and could grant — a badge that grants nothing, and a plugin author's typo surfaced only as a runtime denial. The card's whole point is refusing shapes core does not grant. | `validGrantHost`: after `normGrantHost` the host must be an IP literal (`netip.ParseAddr`, bracketed IPv6 included) or a DNS-shaped name (`grantHostnameRe`, LDH labels, `_` tolerated for LAN hosts). IDN hosts pass through IDNA like the runtime does (`net:münchen.example` accepted). All 12 published-repo lists and both shipped manifests still parse. |
| 2 | **Medium (fixed, `27125da`)** | `ui:slot:<slot>` accepted any non-empty suffix; ADR-0121 §7 defines a closed set of six content slots and `validateABI3Fields` already refuses an entry `slot` outside it. The tests' own example, `ui:slot:checkout.sidebar`, is a slot the ADR says explicitly does not exist (the sale screen has no content slot). Likewise `view:<name>` accepted `view:foo bar` / `view:a:b`; ADR-0121 §5 makes it a class identifier (`view:sales`, `view:inventory`, `view:audit`). | `ui:slot:` reuses `isContentSlot`; `view:` is `^view:[a-z][a-z0-9_]*$`. Test examples moved to `ui:slot:reports.panels` / `view:sales`; the bad shapes are now in the `unknown` list. `docs/plugin_guidelines.md` says what both may be. |
| 3 | **Low (fixed, `27125da`)** | `ValidEndpointURL("http://erp.lan:8080:80/")` returned true: `url.Parse` splits the port at the **last** colon, so host = `erp.lan:8080`, port = `80`. Not exploitable (the dial fails), but it is exactly the free-text-in-a-host case the validator exists to refuse, and `settingBoundNetHost` would derive a nonsense grant host from it. `http://[v1.fe80::1]/` (IPvFuture) also passed. | A colon inside `u.Hostname()` must parse as an IPv6 literal. Both cases added to `TestValidEndpointURL`'s invalid list. |
| 4 | Low (accepted) | The `<entity>:read|write` shape also admits `http:read`, `db:write`, `secret:read`, `cloud:read`, `ui:read`, `schedule:read`, `storage:read`, `events:write`. They grant nothing (no code checks them) and the shape is deliberately open because entity names are plugin-defined (`import_dispatch.go` builds `e+":write"` dynamically), so closing it would need an entity registry — a separate decision, not this card. | Noted; no change. |
| 5 | Low (accepted) | `ValidEndpointURL` accepts loopback, RFC 1918, `169.254.169.254`, `0x7f.1`, `127.1`, `localhost`, a space in the path, `/../` segments. Per the card, SSRF/cloud-metadata blocking is runtime policy (ADR-0121 §3 `http:lan`, out of scope here); an operator typing a LAN address is the feature. `javascript:`, `file:`, `data:`, `ftp:`, `ws:`, userinfo, query, fragment, `http:` opaque, scheme-relative, bare host, surrounding whitespace, control characters, port 0/65536 are all refused (verified by probe, not just by the shipped test list). | No change. |
| 6 | Low (deferred → backlog) | Only `POST /api/plugins/{id}/settings` enforces `ValidEndpointURL`. A manifest `default_value`, a cloud `plugin.settings` directive and LAN sync from the main till write the same rows without it (the main till's own POST validated already, so sync carries a validated value; directives are ADR-0121 §9's card). The code comment on `SettingTypeEndpoint` states the `default_value` gap honestly. | Note for the §9 directive card: validate `type: endpoint` there too. |
| 7 | Low (deferred → backlog) | The plain-language description is on the **store card** only, as the card asked. The place an operator actually taps **Grant** is the settings page's Permissions table (`plugin_settings.html`), which still shows the raw name only; `permissionView` already embeds `permissionBadge`, so `DescKey` is available there for free. Also the settings form renders an endpoint-typed setting as a plain `type="text"` input (no `inputmode="url"`, no hint that a URL is expected until the 400). | Out of the card's stated scope; cheap follow-up. |
| 8 | Info (accepted) | An already-installed plugin whose stored manifest carries a now-unknown permission would fail `InstalledManifest → ParseManifest`, which the settings POST treats as "manifest unresolved" and fails closed with a 500 (ADR-0082). That is the right failure mode but it is a user-visible regression if the grandfathered list is incomplete. | Re-derived the list myself (next section): complete for every public `ut-plugin-*` repo and both shipped manifests. |
| 9 | Info | New `en.json` keys need follow-up PRs in `ut-plugin-language-{de,es,pt}` (CLAUDE.md); `lang-pack-drift.yml` will warn on the PR. ut-docs `reference/plugin-manifest.md` cites #3328 but has no table for the new permissions or `type: endpoint` yet. | For the Scrum Master. |

Nothing found for: the two recurring bug classes (no file writes, no
cwd-relative paths — the diff is validation/view-only; the pages test uses
`paths.Init(t.TempDir())` + `os.MkdirAll` correctly); ReDoS (all regexes are
anchored, single-pass, Go RE2 — linear); i18n (every new key is in all four
locales, `%s` matches, `guard-i18n.sh` green; `{{ T .DescKey }}` is a
dynamic key, which the guard cannot see, so
`TestDescribePermission_ADR0121DescKeys` resolves every key in every locale
at test time instead); half-saves (the endpoint loop runs before any upsert,
and the test asserts the sibling setting is untouched on refusal); the
marketplace install path (`VerifyManifest` is what `installer_marketplace.go`
and `importer.go` call, both covered).

## Grandfathered legacy list — re-derived, not trusted

Fetched `manifest.json` from every public `universaltill/ut-plugin-*` repo
(`list_repos` shows 17; theme-midnight / theme-screen-top /
theme-buttons-left / archived `ut-plugin-themes` have no root
`manifest.json` on `main`, so `ui.theme` rests on the author's claim and the
test fixture only):

| Repo | permissions | settings `type` |
|---|---|---|
| tax-de | `events:receive, sales:read, fiscal_register_de:read, net:kassensichv-middleware.fiskaly.com, storage` | none |
| tax-uk | `events:receive, sales:read, net:test-api.service.hmrc.gov.uk, storage` | none |
| language-de, language-es | `ui.locale` | — |
| faq | `ui.page` | — |
| integration-ai | `ai.configure` | `api_key: secret` |
| integration-webhook | `events:receive, net:*, net:@setting:endpoint_url, storage` | none |
| payment-stripe, payment-sumup | `pos.tender, events:receive, net:api.<psp>.com, storage` | secrets |
| payment-qrpay | `pos.tender, events:receive` | — |
| payment-demo | `pos.tender, events:receive, storage` | — |
| button-nosale | `events:receive` | — |
| in-repo `plugins/tax-tr` | `tcp:@setting:okc.host:okc.port, storage.local.1MB` | — |
| `testdata/marketplace_signed_manifest.json` | `storage.local.10MB` | — |

Matches `TestPublishedPluginRepoPermissionsStillParse` exactly. Core checks
none of the dotted names (`grep` of `"ui.locale"|"ui.theme"|"ui.page"|"ai.configure"|"pos.tender"`
in non-test Go hits only the allow-list itself).

## Verified beyond the automated run

**TDD re-verification (my own mutations, chosen independently of Dev/Tester):**

| Mutation | Test | Observed failure |
|---|---|---|
| A: `validatePermissions` call removed from `VerifyManifest` | `TestVerifyManifest_RefusesUnknownPermission` | `permission_allowlist_test.go:166: VerifyManifest should refuse exec:*, got <nil>` |
| B: endpoint check short-circuited (`false &&`) in the POST handler | `TestPluginSettingsAPI_POST_EndpointSettingValidated` | `plugin_settings_endpoint_test.go:70: "ftp://x": code 200, want 400 (body "<span>✓ Settings saved — applied immediately (2)</span>")` |
| C: template renders the description as `title=` on the `<li>` instead of visible text | `TestPluginStoreRendersADR0121PermissionDescriptionsAsVisibleText` | 16 errors, e.g. `tr: plugins.permissions.desc.http_lan must render as visible element text "Mağazanızın yerel ağındaki …"` and `… must not be a tooltip`, for every locale and both keys |
| D: `devices:printer` flipped to `false` in `exactPermissions` | `TestIsKnownPermission` | `permission_allowlist_test.go:43: isKnownPermission("devices:printer") = false, want true` |

Each mutation was reverted and the test re-run green before the next.
Findings 1–3 were first demonstrated with a throwaway probe test (deleted,
never committed) that logged `isKnownPermission` / `ValidEndpointURL` for
~65 edge inputs; the new assertions in the committed tests fail against the
WIP code and pass against `27125da`.

**UX checklist (`reference/ux-guidelines.md`), template diff only:** no new
colors (the list is `.muted`, the badge keeps `.tag`); logical properties
only (`margin-block`, `padding-inline-start`); `<code dir="ltr">` keeps the
raw permission name readable inside an RTL `<li>` (rendered and asserted in
ar/fa); the description is a wrapping list item, so the longest locale
cannot truncate; the tooltip is removed only where visible text replaces it
— hover is no longer load-bearing for the ten new names, and the old
badge+tooltip path is asserted unchanged for `storage`. No modal, no
checkout-path surface.

**Gates, run by me after the fixes:** `gofmt -l .` empty; `go build ./...`
OK; `go vet ./internal/plugins/... ./internal/pages/...` OK;
`golangci-lint run` on both packages: 0 issues; `go test -count=1
./internal/plugins/...` (269 s) and `./internal/pages/...` all green;
guards `guard-i18n.sh`, `guard-help-drift.sh`, `guard-help-topics.sh`,
`guard-compliance-claims.sh`, `guard-competitor-naming.sh`,
`guard-core-neutral.sh`, `guard-data-access.sh`, `guard-page-http-error.sh`,
`guard-plugin-settings-bump.sh` all pass.

**Not checked:** a real driven run of the store page on hardware (the
Tester's lane; the template change is asserted at the HTML level in four
locales), and the private `ut-plugin-theme-*` manifests (no root
`manifest.json` reachable).

## Verdict

Safe to merge with `27125da`. Findings 6, 7 and 9 go to the backlog.
