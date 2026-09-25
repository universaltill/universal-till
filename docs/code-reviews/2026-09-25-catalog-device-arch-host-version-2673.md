# Review: catalog query sends device_arch + host_version (ut-docs#2673)

- **Branch:** `fix/2673-catalog-device-arch-host-version`
- **Author model:** Opus 5.5 · **Reviewer model:** Fable (independent subagent)
- **Verdict:** safe to merge

## What shipped

- `marketplace.Client.ListPlugins` sends `device_arch` in place of `arch`.
  grpc-gateway silently drops query keys it doesn't know, so until now the
  till's catalog was never filtered by arch.
- It also sends `host_version`: an explicit `ListPluginsRequest.HostVersion`
  is sent as given. Otherwise the till sends the running `buildinfo.Version`,
  but only when that is a plain dotted release number. A `dev` or
  pre-release build sends no `host_version` and filters nothing.
- `PluginSummary.MinHostVersion` is decoded from `minHostVersion` (the live
  protojson wire format) and from `min_host_version` (the snapshot/snake
  form). Before this change the value was decoded and then thrown away.
- Extra safety check in case the cloud is older: after decoding,
  `ListPlugins` drops listings whose `MinHostVersion` is newer than the host
  version. Each segment is compared by its leading number, and a leading `v`
  is ignored. An unparseable minimum is left to the cloud. Every caller
  (the catalog snapshot, setup base/tax/language pickers) now gets this
  filter.
- Fake cloud servers in tests (`server_test.go`, `plugins_page_test.go`,
  `catalog_repository*_test.go`) now read `device_arch`.
- Help `plugins` topic (en/de/ar/fa/tr): the store lists only plugins that
  run on this till. "Update status unknown" also covers an installed plugin
  whose newest version needs a newer till. Screenshots and manifest were
  regenerated with `make docs-shots`, and only the `plugins` images changed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `Makefile` `VERSION?=0.1.0` means `make build` dev tills send `host_version=0.1.0` | Accepted: latent, because every live listing is `0.1.0`. Follow-up ut-docs#2700 (don't flip to `dev`, which would re-open #369) |
| 2 | minor | An installed plugin whose listing now needs a newer till shows "Update status unknown", and the help text blamed only file imports | Fixed: help copy updated in all 5 locales |
| 3 | minor | The cloud's `compareVersions` falls back to string compare for `v`/`-rc` minimums and hides those listings | Accepted: cloud-side, and the cloud's stricter result wins. Follow-up ut-docs#2701 |
| 4 | nit | `releaseVersion` also swallowed an explicit `req.HostVersion` | Fixed: it now applies only to the `buildinfo` fallback |
| 5 | nit | This adds a fourth hand-written version comparator | Accepted: semantics match `updates.Newer` / `plugins.compareVersions` (numeric prefix per segment) |

Also checked with no problem found:
- **Arch format:** the till sends `GOOS/GOARCH`. The cloud schema says os/cpu, and cloud tests use `linux/amd64`.
- **Nothing hidden by arch today:** no production ut-cloud code writes `compatible_arches`. The live fixture has none, so `containsFold` of an empty list is true.
- **Pagination:** the cloud filters before paging. Every till caller loops on the next-page token, not on page length, so an empty filtered page is harmless.
- **Test global:** the `hostVersion` test override is safe because no test in the package calls `t.Parallel`.

## Verification

- TDD: the new tests in `client_compat_test.go` were first run against stub fields with no behaviour. The query-key, explicit-version and filter tests failed with the real assertion errors, then passed after the fix. The reviewer checked this again independently: with `client.go` reverted, the test package does not compile.
- `go build ./...`, `go vet` (touched packages), full `go test ./...`, and `golangci-lint run ./...` (0 issues) all pass.
- Every `scripts/ci/*.sh` step in `ci.yml`'s build job passes, including `guard-docs-shots`, `guard-help-drift`, `guard-help-topics` and `guard-i18n`. One exception: `guard-shellcheck-version.sh` fails identically on `main` in this container, because of the local shellcheck version, not this change.
- A real app run through the docs-shots Playwright harness (124 screenshots passed) drove `/plugins` in en/fa/ar/tr. I looked at `en/plugins.png`: the layout is unchanged and nothing visible on the page changed. The store's filtered result needs a live cloud with a newer-min listing, so it was not driven end to end. It is covered by the httptest client tests.
