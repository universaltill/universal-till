# Review — store card describes `net:validation:<host>` in words (ut-docs#3515)

**Change:** a `net:validation:<host>` permission (ut-docs#3226: plain,
unencrypted HTTP to one public host, larger response cap, for PAdES
validation data) looked identical to an ordinary https-only `net:<host>`
badge on a plugin store card. `permissionDescKey` now gives a well-formed
`net:validation:` grant (`plugins.ParseValidationPermission`, the same
parser the installer uses) the ADR-0121 description key
`plugins.permissions.desc.net_validation`, so the store card lists it as
visible text under the badges, like the other described permissions. New
key in en/ar/fa/tr; the plugins help topic (en/de/ar/fa/tr) gains one
sentence; docs-shots regenerated (pixel changes are font rendering only —
the plugins screen itself is unchanged).

**Out of scope:** the plugin settings page's Permissions table still shows
no descriptions for any permission — ut-docs#3553 covers that and will pick
this key up with the rest.

**Review:** independent Sonnet review (author: Opus 5.5; easy card).
- *should-fix, fixed:* the first copy said the data "is signed, so it cannot
  be altered unnoticed". The host verifies no signature
  (`wasm_egress.go`/`wasm_hostfns.go` only relax the scheme and cap and keep
  the public-address check), so a plugin that skips verification would make
  that false. Copy now says anyone on the network path can read or change
  the connection and that the plugin is expected to check signatures
  itself; the examples became "such as" (intended use, not a host limit).
  Same fix in all 4 core locales, 5 help files and the 3 pack follow-ups.
- *nit, no change:* malformed `net:validation:` strings (empty, wildcard,
  port, scheme) get no description — covered by the test; no panic path.
- *nit, no change:* translations faithful; Persian slightly stiff.
- Second Sonnet pass on the de/es/pt pack strings: *should-fix, fixed* — de
  said the plugin "muss" check signatures (a fact the host doesn't
  enforce); now "vom Plugin wird erwartet", here in `web/help/de/plugins.md`
  and in the de pack. es/pt faithful (pt keeps the pack's "encriptado").

**Verified:**
- TDD re-verified: with the fix reverted, `TestDescribePermission_ADR0121DescKeys`
  and `TestPluginStoreRendersADR0121PermissionDescriptionsAsVisibleText` fail
  (empty DescKey; 2 description lines instead of 3); restored, both pass.
- `gofmt`, `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0
  issues), full `go test ./...` (`internal/plugins` run with a 30m timeout
  as CI does), every `ci.yml` build-job guard except
  `guard-shellcheck-version.sh` (no shellcheck binary in this container; no
  shell script changed).
- Rendered the real `plugins_store.html` with a `net:validation:` plugin and
  looked at it: en and fa at 1024×600, en/tr/ar at 360 px — description is
  visible text, wraps inside the card, no horizontal scroll, RTL correct
  (the raw name stays an LTR `<code>`). Not checked on a physical till.
- Packs de/es/pt: `scripts/validate.sh` and `check-key-drift.sh` against
  this branch's `en.json` pass (3198/3198, 0 drift).

**Verdict:** safe to merge.
