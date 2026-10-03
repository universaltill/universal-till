# Review: plugin event namespace is the manifest `id` (ut-docs#3329)

ADR-0121 §2 named an event-namespace prefix `<plugin-key>` without ever
defining it — `plugins.Manifest` has no `key` field, only `id`. This closes
the gap: `<plugin-key>` **is** the manifest's existing `id` (reverse-DNS, no
new field). Full design: `ut-docs` `adr/0121-feature-code-ships-in-its-plugin-runtime-types.md`,
"## Amendment (2026-10-03)".

## What shipped

- `internal/plugins/manifest_abi3.go` (`validateABI3Fields`): `schedules[].event`
  must now start with the declaring plugin's own `id` + `.`, not merely "any
  non-core-root string" — closes the gap where plugin A could tick an event
  prefixed with plugin B's name. Check order: format → core-root → own-id
  prefix → `every_s` → `jitter_s`.
- `internal/plugins/plugin_id.go` (`validatePluginID`): a plugin `id` whose
  first dot-segment is a core event root (`sale`, `fiscal`, `ui`, `payment`,
  `tax`, `catalog`, …) is now refused at parse — closes the gap systematically
  for every event surface this ADR adds (schedules now; `event_publish` and
  `plugin.event` directives later, build cards 7/9), rather than re-checking
  it at each call site. Covers every install path (`ParseManifest`,
  `VerifyManifest` → marketplace installer, importer, `install.go`,
  `rollback.go`, `exporter.go`).
- `scheduleEventRe` widened to allow `-` (`^[a-z0-9_-]+(\.[a-z0-9_-]+)+$`):
  most real ids are hyphenated (`com.universaltill.tax-de`); without this the
  own-id-prefix rule would make most real plugins unable to declare a
  schedule at all.
- `ut-cloud/pkg/manifest` (separate PR, universaltill/ut-cloud#TBD): mirrors
  all of the above for upload-time validation, plus closes pre-existing
  cross-repo drift the marketplace's `id` charset had from the till's
  (missing `_`, no length cap, no Windows-reserved-name check) and adds a
  required-`runtime` check at upload.

## Findings

| Severity | Finding | Outcome |
|---|---|---|
| Nit | Till's format-refusal hint said `like <plugin>.<name>`; marketplace said `like <plugin-id>.<name>` — authors would see different wording for the same rule at upload vs. install. | Fixed (`1d97ff9`): till now says `<plugin-id>` too. |
| Advisory, not fixed (out of scope) | **Nested-id namespace ownership.** If plugins `a` and `a.b` both exist, `a` can legitimately declare event `a.b.tick`, which is also inside `a.b`'s own namespace — the ADR's `<id>.` design doesn't registry-check for a listing id that is a dot-prefix of another vendor's existing id. Needs a vendor to publish a parent-namespace id past marketplace review; not reachable by any shipped plugin today. | Filed as a new Backlog card (see below), not fixed here — it's a marketplace listing-uniqueness policy question, not a defect in this card's own scope. |
| Nit, not fixed | When `id` is empty, the own-id-prefix check still fires alongside the separate "id is required" error, so a manifest with no id and a schedule gets two errors instead of one. Cosmetic — the real "id is required" error is still present and correct. | Left as is. |
| Nit, not fixed | `ut-cloud/internal/signing/signer_test.go`'s sample manifest (`com.universaltill.abi3-example`, event `abi3.retry.tick`) would now fail `Validate()` — harmless, that test never calls `Validate()`, just a stale illustrative example. | Left as is; not this card's scope. |

## Verified beyond automated tests

- **Adversarial reasoning on the prefix boundary** (could a shorter id's
  prefix ever false-positive-match a longer sibling id's event?): no — the
  required prefix always ends in `.`, and an id can neither contain `..` nor
  end in `.` (`windowsUnsafe`), so `<id>.` always lands on a real segment
  boundary. Worked through with concrete id pairs (`a`/`ab`/`a.b`) by both
  the Dev and the independent Fable review; both reached the same proof.
- **Core-root check bypass hunt:** none found. The id pattern is
  lowercase-ASCII-only, so no case-folding or homoglyph bypass; RE2's
  `[a-z]` is ASCII, not Unicode. First-segment-only matching is intentional
  and correct for a prefix-based namespace (`com.sale.x` is rightly outside
  `sale.`'s namespace).
- **Cross-repo consistency, diffed programmatically, not by eye:**
  `coreEventRoots` — identical, 32 members in both repos. `scheduleEventRe`,
  the `id` charset pattern, `maxPluginIDLen=128`, the `..` check and
  `windowsUnsafe` — byte-identical mirrors. Check order and message wording
  identical (format hint now identical after the fix above). No manifest can
  pass marketplace upload and then fail till install (or vice versa) on any
  of these rules.
- **Signed-bytes safety:** `pkg/manifest.Manifest`'s new `Schedules`
  field is a separate, event-only struct from `ut-cloud/internal/signing`'s
  `CanonicalManifest`/`CanonicalSchedule` (which already carried the full
  ABI-3 fields since ut-docs#3155) — the new upload-lint field cannot affect
  signed bytes.
- **TDD re-verified independently** (revert the production line, confirm the
  specific failure, restore): own-id-prefix check, core-root id check, and
  the hyphen-regex widening in the till; runtime-required, own-id prefix, and
  the underscore-charset fix in ut-cloud. Each failed with the claimed error
  and passed again on restore.
- **Backward compatibility:** no shipped plugin id collides with a core
  event root and no shipped manifest is missing `runtime` — checked against
  `plugins/layout-salon` and `plugins/tax-tr` directly, and against all 17
  `ut-plugin-*` repos' current manifests (cloned read-only for the sweep).
  `ScanService.ValidateRelease` only runs once at upload (no periodic rescan
  job re-validates already-published releases — established prior behaviour,
  `docs/code-reviews/2026-09-30-permission-exact-spelling-3187.md`), so the
  new `runtime`-required check cannot retroactively fail a historical release.
- **Secrets/seed data:** the re-signed testdata fixture
  (`internal/plugins/testdata/marketplace_signed_manifest_abi3.json`) carries
  only a public signature and a synthetic id/name; new test fixtures use
  `merchant-1`/`secret-1`/`tok-1` placeholders. No real client/shop name.
- **No UI surface touched** (`git diff --stat -- web internal/pages` empty);
  no shop-owner-visible change, so no `web/help/` topic needed.
- Full gate (`go build`, `go vet`, `gofmt -l`, full `go test ./...`) green,
  re-run independently by both the Tester and the Reviewer, not just taken
  from the Dev's own report.

## Real-path tests (not just the unit-level validator)

- `TestMarketplaceInstallerEnforcesEventNamespace`: a real signed download
  through the marketplace installer harness into a migrated DB — own-id
  schedule installs; another plugin's prefix and a core-root id are refused
  with no row written.
- ut-cloud `TestUploadEnforcesEventNamespaceAndRuntime`: a real HTTP upload
  through the actual mux/handler — own-id prefix gets 201; another plugin's
  prefix, a core-root event, a core-root id and a missing `runtime` each get
  422 naming the reason.

## Deferred

- Nested-id namespace ownership (above) → new Backlog card, filed this cycle.
- `event_publish` / `plugin.event` directive namespace enforcement — ADR-0121
  build cards 7/9, already tracked, out of scope here.

## Models

Dev + Tester: Opus 5.5 (fresh subagents). Review: Fable (different model,
per `MODEL-ROUTING.md`'s medium-complexity routing). One review round; the
one fix found was wording-only and needed no second round.

**Safe to merge: yes.**
