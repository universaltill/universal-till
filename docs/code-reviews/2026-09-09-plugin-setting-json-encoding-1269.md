# Unify plugin-setting JSON encoding (ut-docs#1269)

## What shipped

`plugin_settings.value_json` had no single canonical on-disk shape for a
map-typed setting: `internal/pages/plugin_settings_page.go`'s
`writeTaxOverrides` deliberately JSON-string-wrapped on every save
(matching the generic scalar-settings save path's own `json.Marshal(val)`
convention), while `internal/data/plugin_repo.go`'s
`MergeAdditiveJSONMapSetting` wrote the raw unwrapped object directly. Both
readers already tolerated either shape, so nothing was broken in
production — but the actual bytes on disk silently depended on which write
path touched a setting last, which is exactly the class of inconsistency
that produced ut-docs#1255 in the first place.

This change extracts one shared encode/decode seam
(`internal/data/plugin_setting_value.go`: `EncodeMapSettingValue` /
`DecodeMapSettingValue`) and routes both write paths through it:

- `MergeAdditiveJSONMapSetting`'s write side now calls
  `EncodeMapSettingValue` instead of a bare `json.Marshal` — **this is the
  actual behavior change**: it now stores the string-wrapped shape, same
  as `writeTaxOverrides` always did.
- `MergeAdditiveJSONMapSetting`'s read side now calls
  `DecodeMapSettingValue`, replacing a hand-rolled self-heal block
  (ut-docs#1255) with a call to the shared decode helper — same behavior,
  less duplicated logic.
- `writeTaxOverrides` now calls `EncodeMapSettingValue` instead of its own
  inline double-`json.Marshal`.
- `unwrapSettingValue` now delegates its body to `DecodeMapSettingValue`.

`DecodeMapSettingValue` checks for a leading `"` before attempting to
unwrap, rather than just trying `json.Unmarshal` into a string and using
the result — a bare JSON `null` unmarshaled into a non-pointer `string`
target is a documented no-op (nil error, value left at its zero value),
which would be indistinguishable from "successfully unwrapped to an empty
string" and would turn a stored bare `null` into `""`, breaking the
caller's own null-handling. The leading-quote check avoids this.

## Independent review (Opus, isolated worktree)

Verdict: **safe to merge, no blocking defects.**

**TDD claim independently re-verified**, not taken on trust: reverted just
the write-side behavior hunk, confirmed
`TestMergeAdditiveJSONMapSetting_WritesStringWrappedShape` and
`TestMergeAdditiveJSONMapSetting_AgreesWithSharedDecodeHelper` fail with
the exact claimed shape-mismatch error, restored, confirmed all 9
`TestMergeAdditiveJSONMapSetting_*` tests pass. Noted (correctly) that
`TestMergeAdditiveJSONMapSetting_ReadsPreExistingRawObjectRow` is a
backward-compat lock, not a red-first regression test — only 2 of the 3
new tests are genuinely red-first; the third is a legitimate
never-was-broken compatibility guarantee.

**The `DecodeMapSettingValue` doc comment's claim was independently
verified true**, not trusted on the comment's word — the reviewer wrote
its own throwaway Go snippet confirming: unmarshaling bare `null` into a
non-pointer `string` is a silent no-op; its result is genuinely
indistinguishable from unwrapping `""`; the caller's follow-on unmarshal of
`""` fails with "unexpected end of JSON input"; and today's
bare-`null`-into-map behavior (nil map, handled by the pre-existing
`existing == nil` guard) is what would have regressed without the
leading-quote check.

Also independently confirmed the read-side rewrite is a faithful
replacement for the deleted self-heal block by enumerating input classes
(quote-leading vs. not) — both old and new code reach the identical
outcome for every class.

Ran the full gate plus every other CI-blocking guard this diff could
plausibly trip (including `guard-plugin-settings-bump.sh`, merged to
`main` only hours before this branch and specifically policing
`MergeAdditiveJSONMapSetting` call sites) — all clean:
`gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test ./internal/data/... ./internal/pages/... -count=1`,
`golangci-lint run ./internal/data/... ./internal/pages/...`,
`guard-data-access.sh`, `guard-plugin-settings-bump.sh`,
`guard-price-history-sync.sh`, `guard-plugin-menu-read.sh`,
`guard-page-http-error.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh`,
`guard-compliance-claims.sh`, `guard-help-topics.sh`.

### Findings

- **F1 (fixed here)** — `unwrapSettingValue`'s doc comment claimed it does
  "the same unwrap `hostSettingsGet` performs," which the change made
  false: `hostSettingsGet` (deliberately left untouched, per this card's
  own non-goals) unwraps unconditionally, so a stored bare `null` yields
  `""` there vs. `"null"` from `DecodeMapSettingValue`. Traced the one
  production caller (the typed tax-override read at
  `plugin_settings_page.go:~360`) and confirmed this divergence is
  harmless today — the result is only ever ranged over, never assigned
  into, so a nil map and an empty map behave identically there — but left
  undocumented it would mislead the next reader (ut-docs#1270). Fixed by
  correcting the comment to state the divergence and why it's safe, rather
  than leaving a false parity claim in place.
- **F2 (deferred, not blocking)** — a fourth, independent inline
  JSON-string unwrap survives in this same file's GET handler
  (`plugin_settings_page.go:~287`), disagreeing with the shared decode
  seam on the same `null` edge case. Still correct for both shapes today.
  Filed as ut-docs#1945.
- **F3 (deferred, not blocking)** — `EncodeMapSettingValue`'s doc comment
  overstates universality: `internal/plugins/manifest.go`'s install-time
  default seeding still writes the raw-object shape for a manifest-declared
  object default, so a freshly-installed plugin's row is non-canonical
  until the first merge/save rewrites it. Harmless (every reader tolerates
  both shapes; the new backward-compat test locks that in) but worth
  narrowing the claim or extending the seam. Filed as ut-docs#1946.

### Verified beyond automated tests

- Grepped every non-test reference to `plugin_settings.value_json`
  repo-wide (not just the files this diff touches) — no remaining
  read/write shape disagreement among production call sites.
- Confirmed the WASM plugin boundary is compatible: `hostSettingsGet`'s
  unconditional one-level unwrap decodes the new wrapped shape to the same
  content a plugin would have seen from the old raw-object shape.
- Confirmed no template/locale/help content changed
  (`git show --stat -- 'web/' '*.html' '*.json'` empty) — nothing
  user-facing, so the UX-guidelines checklist and user-manual-update
  requirement correctly don't apply.
- Confirmed no real client/shop name and no secret-shaped literal in the
  diff.
- Confirmed neither recurring bug class (missing `os.MkdirAll`, a
  cwd-relative path instead of `paths.Data(...)`) applies — this diff
  writes no files.

## Safe to merge

Yes. F1 fixed in this branch before merge; F2/F3 deferred to backlog
(ut-docs#1945, ut-docs#1946) per the reviewer's own recommendation —
neither changes behavior, both are consistency/doc-accuracy follow-ups
naturally scoped alongside ut-docs#1270.
