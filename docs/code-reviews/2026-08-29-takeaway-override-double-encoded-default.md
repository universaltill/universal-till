# Review: takeaway_rate_overrides self-heals a double-encoded manifest default (ut-docs#1255)

**Date:** 2026-08-29
**Card:** ut-docs#1255 — "differing takeaway tax rates could not be saved" / live-confirmed on a real till that dine-in and takeaway tax never actually differ on a sale.
**Complexity:** hard (cross-repo, fiscal-correctness-adjacent).
**Reviewer model:** Opus, independent subagent, isolated worktree.

## What shipped

Two repos:

- **`universal-till`** (`internal/data/plugin_repo.go`, `MergeAdditiveJSONMapSetting`):
  when the existing stored value fails to unmarshal as the target map, try
  unwrapping it as a JSON string first and unmarshal *that* — self-healing a
  specific, distinctive corruption shape instead of refusing forever. Tests
  added in `plugin_repo_merge_setting_test.go`.
- **`ut-plugin-tax-de`** (`manifest.json`): `takeaway_rate_overrides`'
  `default_value` changed from the JSON string `"{}"` to the real JSON object
  `{}`, version bumped 0.5.0 → 0.5.1 — stops any *future* fresh install from
  seeding the broken shape again.

## Root cause (traced live, on a real Android till, 2026-08-28/29)

`internal/plugins/manifest.go`'s install-seeding does
`json.Marshal(s.DefaultValue)` where `s.DefaultValue` is `interface{}` parsed
straight from the plugin's manifest JSON. A manifest field written as the
JSON **string** `"{}"` therefore seeds the stored `value_json` as the JSON
string `"{}"` (4 bytes) — not the raw object `{}` (2 bytes)
`MergeAdditiveJSONMapSetting` expects. Every fresh install of the plugin has
therefore always failed its very first merge attempt with "existing value is
not valid JSON," permanently, with no way to recover short of a hand DB edit
— confirmed as the live, reproducible cause of dine-in/takeaway VAT never
actually differing on a real sale.

**Correction to the original ticket's framing (found by review, not
blocking):** this manifest value is not actually inconsistent with this
codebase's own convention — `plugin_settings_page.go`'s `writeTaxOverrides`
deliberately double-encodes on every UI save (its own comment: "using the
exact same JSON-string encoding the generic settings path uses"), and
`hostSettingsGet`/`unwrapSettingValue` both unwrap one level before use. So
`plugin_settings.value_json` canonically holds a JSON-encoded *string* of the
value fleet-wide; `MergeAdditiveJSONMapSetting` reading/writing the raw
column with no wrap/unwrap is the actual outlier. This doesn't change
correctness of the fix (both readers already tolerate a raw-object value, so
the merge's write shape was already fine) — it only changes which piece is
"weird." Filed as a follow-up (below), not fixed here.

## Independent review findings

**F1 (blocking, fixed) — nil-map panic newly reachable.** `json.Unmarshal`
of a JSON `null` into a map succeeds and sets it to `nil`. The self-heal's
unwrap path treats a string-wrapped `"null"` as a successful heal and falls
through to `existing[id] = v`, which panics on a nil map.
`plugin_settings_page.go`'s plain-text settings form does `json.Marshal(val)`
on operator input, so typing literally `null` into that field stores exactly
`"null"` — reachable by an operator, not just a contrived value. (A **bare**
`null` already panicked the same way on `main`, pre-existing and unchanged in
scope — the string-wrapped case is what this diff newly makes reachable.)

Fix: after the existing-value switch, `if existing == nil { existing =
map[string]int{} }` before the merge loop. Verified by the reviewer with the
fix applied, and independently re-verified here: reverting just the guard
(tests left in place) reproduces the exact panic
(`plugin_repo.go:813`, `assignment to entry in nil map`) on both the bare and
string-wrapped shapes; restoring it passes cleanly. Regression test
`TestMergeAdditiveJSONMapSetting_NullExistingValueDoesNotPanic` (both
subtests) added and stays.

**Everything else came back clean:**
- Double-unmarshal correctness: `existing` starts as a non-nil empty map;
  the two unmarshal attempts are mutually exclusive (the heal path only runs
  when the first attempt is a top-level type mismatch, which populates
  nothing), and `||` short-circuits so a failed unwrap never attempts the
  second unmarshal. Reviewer probed partial-population directly with a
  mixed-valid/invalid payload in both the wrapped and unwrapped positions —
  no partial write in either case.
- Transaction/concurrency: unchanged, still one `BeginTx`/`defer
  tx.Rollback()` read-modify-write; `_ConcurrentCallersBothLand` passes.
- Scope (`ut-docs#668`): untouched — `scope = 'global'` only, on both the
  read and the write; `_IgnoresRegisterScopedRow` passes.
- `os.MkdirAll` / `paths.Data(...)` classes: checked, don't apply — no file
  writes or paths anywhere in this diff.
- Secrets / real client-shop names: none.
- Field devices already bitten by this: confirmed `ReconcilePluginSettings`
  preserves an existing setting value across a plugin upgrade, so simply
  shipping tax-de 0.5.1 would **not** repair an already-broken install — the
  read-time self-heal in `universal-till` is the only actual in-field
  remedy, which is exactly why it's the shipped fix rather than a
  manifest-only patch.

## Verified

- `gofmt -l` clean, `go build ./...`, `go vet ./internal/data/...` clean.
- `go test ./internal/data/... ./internal/pages/... -count=1` — all green,
  no regressions.
- `bash scripts/ci/guard-data-access.sh` — clean.
- `ut-plugin-tax-de`: `go test ./...`, `scripts/build.sh`, `scripts/validate.sh`
  all green with the manifest change.
- TDD claim re-verified twice, independently: once by the reviewer subagent
  (isolated worktree, wrong-repo environment issue noted below but worked
  around with a full manual copy), once again here after applying F1's fix
  (revert-run-restore on the null-guard specifically).

## Environment note for the pipeline (not a code finding)

The review subagent's `isolation: "worktree"` created a worktree of the
**wrong repo** (`ut-plugin-tax-de` instead of `universal-till`), leaving it
unable to reach the actual fix branch via git from that worktree. It worked
around this by copying the real repo state to a scratch directory and
running everything for real there — the verification is trustworthy — but
the isolation mechanism itself picked the wrong repo when multiple repos are
in play in one session. Worth a look if this recurs.

## Deferred (filed as Backlog cards, not this card's scope)

1. **ut-docs#1269** — unify plugin-setting JSON encoding: one shared
   encode/decode helper between `unwrapSettingValue`/`writeTaxOverrides`
   and `MergeAdditiveJSONMapSetting`, so `value_json`'s shape stops
   depending on which code path wrote it last.
2. **ut-docs#1270** — normalize the four other `"default_value": "{}"`
   string-typed manifests still in the fleet (`datev_konten_by_method`,
   `datev_erloeskonten`, `datev_bu_schluessel` in `ut-plugin-tax-de`;
   `eatin_standard_rate_by_tax_code` in `ut-plugin-tax-uk`) once #1269
   settles the canonical shape, plus a CI check that a map-typed setting's
   default is declared as an object.
3. **ut-docs#1271** — add HTTP panic-recovery middleware to
   `internal/pages` — today a handler panic just drops the connection
   with no localized error to the operator.
4. Noted on ut-docs#1269 rather than filed separately: the self-heal only
   persists when `added > 0` — a merge call that adds nothing new (e.g.
   re-syncing an already-fully-overridden catalog) leaves the
   double-encoded value in place rather than rewriting it clean. Every
   reader already tolerates both shapes, so this is cosmetic, not a
   correctness gap.

## Verdict

**Safe to merge**, with F1's fix included (it is, in this diff).
