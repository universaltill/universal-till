# Code review — plugin-settings-bump CI guard (ut-docs#1357)

**Card:** universaltill/ut-docs#1357 — "CI guard: any plugin-settings
writer must also bump the tax-ask/plugin-hook cache generation".
**PR:** universaltill#TBD (opened after this record).
**Complexity:** medium. **Dev:** Sonnet (inline). **Review:** Opus
(independent subagent, isolated worktree).

## What shipped

A new CI guard, `scripts/ci/guard-plugin-settings-bump.sh`, plus its
regression test `scripts/ci/guard-plugin-settings-bump_test.sh`, wired
into `.github/workflows/ci.yml`'s `build` job right after the
plugin-menu-read guard.

`pluginTaxRateAsker` and similar `.ask`-hook askers memoize answers per
event-bus generation (ut-docs#222). That's only correct if every writer
of a setting such an asker reads also bumps
`plugins.SharedBus(db).BumpGeneration()` after writing — an invariant
enforced only by convention until now, and already broken twice for
real: ut-docs#222 (the plugin-settings editor) and ut-docs#1351 (catalog
import's `mergeTakeawayOverrides`, which shipped a live VAT
over-collection bug in the Germany café pilot). The bump can't be pushed
down into the writers themselves
(`UpsertPluginSetting`/`UpsertPluginSettingScoped`/
`MergeAdditiveJSONMapSetting`, `internal/data/plugin_repo.go`) because
`internal/plugins` imports `internal/data`, not the reverse — that would
be an import cycle. So the fix is a grep-based CI guard, in the shape of
this repo's existing ones: any production `.go` file with a line calling
one of these writers must also reference `BumpGeneration()` somewhere in
that same file, or that exact line must carry an inline
`// plugin-settings-bump:allow <reason>` escape hatch.

Verified before building: every current production call site
(`internal/pages/import_page.go`, `internal/pages/plugin_settings_page.go`)
already references `BumpGeneration` — both known instances were already
fixed by ut-docs#222/#1351 — so this guard ships with zero live findings
and is purely forward-looking, matching the card's own framing
("non-blocking — this is a hardening/process card, not a live defect").

## Independent review (Opus, isolated worktree)

Full independent pass: read the diff, compared it against sibling guards
(`guard-price-history-sync.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`)
for convention, ran build/vet/gofmt/YAML-parse/the guard/its test, and
did a hands-on TDD re-verification (removed the guard script → test
failed loudly with 4 cases; restored it, neutered its core check →
the `MissingBump` case correctly reported FAIL; restored again → clean).

**Blocking finding (fixed):** the first draft's `plugin-settings-bump:allow`
escape hatch was file-scoped (`grep -q "${ALLOW_COMMENT}" "${file}"`), not
same-line like every sibling guard's escape hatch
(`guard-i18n.sh`'s `i18n:ignore`, `guard-kiosk-engine.sh`'s
`kiosk-engine-guard:allow`). The reviewer reproduced it concretely: a
file with two writer-call lines, only one carrying the allow comment,
still passed clean — the second, genuinely-unbumped call was silently
disarmed by the first line's exception. Real risk in the exact file this
card is about (`plugin_settings_page.go` already has two writer call
sites; a reviewed exception on one would have silently unguarded the
other, and any future one added to that file, forever).

*Fix:* reworked the check to per-line (`grep -nE "${WRITER_RE}" "${file}"
| grep -v "${ALLOW_COMMENT}"`) — an allow comment only silences the line
it's actually on, matching `guard-kiosk-engine.sh`'s exact shape. Added a
regression case (`PartialAllowInSameFile`) reproducing the review's own
probe: two writer-call lines, one allowed, one not — the guard must
still flag the unmarked one. Confirmed failing before the fix, passing
after.

**Non-blocking findings, all fixed in the same pass** (none were
money/tax/data-loss/security-class, so per the pipeline's process-depth
rule this didn't earn a second full review round — fixed, re-verified,
and re-ran the gate instead):

- **Fail-open on a silently-broken pattern.** The guard previously had no
  assertion that `WRITER_RE` actually matched *anything* — a future
  rename of these methods would make the guard permanently green with
  nothing left to check. Added an explicit `matched_any_file` check
  (mirrors `guard-price-history-sync.sh`'s missing-classification-file
  fail-closed check) plus existence checks for `internal/` and
  `internal/data/plugin_repo.go`. `WRITER_RE` is now overridable via env
  (same convention as that sibling guard's `SYNC_CLASSIFICATION_FILE`),
  which let the regression test exercise this path with a simulated
  rename instead of mutating real source.
- **`BumpGeneration` (no parens) could match a comment or string
  literal**, not just a real call. Tightened to `BumpGeneration()`.
- **Error message didn't say internal/data's only way to pass is the
  allow comment** (it can never reference `BumpGeneration()` without
  creating the same import cycle this guard exists to work around).
  Added a line spelling that out.
- **`testdata/` fixture packages were in scan scope** (WASM guest test
  fixtures under `internal/plugins/testdata/...`) — no real false
  positive today, but excluded via `! -path '*/testdata/*'` since
  nothing in that scope is production code.
- **Punctuation drift** between the error message's escape-hatch
  instructions and the actual convention (`allow: <reason>` vs.
  `allow <reason>`) — fixed to match every sibling guard.
- **Test's `expect_fail` couldn't distinguish "guard correctly rejected
  this" from "guard script crashed/missing".** Added an optional
  must-contain-substring assertion (checks the offending filename
  actually appears in the guard's output), applied to every `expect_fail`
  case in the updated test.

## Verified beyond the guard's own test suite

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean (no Go source touched by this
  change; confirms nothing else broke).
- `golangci-lint run ./...` — 0 issues.
- `go test ./...` — full suite green (run once, before the review's
  findings — no Go source changed since, so not re-run a second time;
  the guard/test scripts and build/vet/lint were re-verified after every
  fix instead).
- `python3 -c "import yaml; yaml.safe_load(...)"` on `ci.yml` — parses;
  reviewer additionally confirmed the new steps land in the `build` job
  at the intended position with no duplicate step names.
- CI-blocking guard suite spot-run clean: `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-page-http-error.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-htmx-loaded.sh`, plus the new `guard-plugin-settings-bump.sh`.
- `guard-plugin-settings-bump_test.sh`'s 7 cases: missing-bump rejection
  (with filename assertion), same-file-bump acceptance, `_test.go`
  exemption, same-line allow acceptance, partial-allow-in-same-file
  rejection (the review's own finding), fail-closed on a
  simulated-rename `WRITER_RE`, and a clean-codebase baseline pass.

## Not findings (reviewer checked, no issue)

- No live violation hiding behind the original file-scoped check —
  traced both real call sites in `plugin_settings_page.go` and
  `import_page.go` to their actual `BumpGeneration()` calls a few lines
  downstream; both are genuinely covered.
- `WRITER_RE`'s anchoring is correct — doesn't false-match a method
  definition or `UpsertPluginSettingScoped` being a prefix collision
  with `UpsertPluginSetting`.
- `internal/data/plugin_repo.go`'s exemption is load-bearing and correct
  (it has zero `BumpGeneration()`/allow-comment references — it would
  fail on its own internal delegation without the exemption).
- No secrets, no real client/shop name anywhere in the diff (the only
  hits for "Germany café pilot" are a market descriptor in comments/error
  text, not a shop name).
- `CLAUDE.md`'s guard list wasn't updated — consistent with existing
  practice (several other guards, e.g. `guard-price-history-sync.sh`,
  are also absent from that list, which explicitly says it drifts).

## Deferred (new Backlog cards, out of scope here)

1. Close the layering hole structurally instead of by convention — an
   injected invalidation callback on `PluginRepo`, or hoisting the event
   bus above both `internal/data` and `internal/plugins`, would make the
   invariant unbreakable instead of grep-checked and let this guard be
   deleted. (Already scoped out once before, in
   `docs/code-reviews/2026-08-31-tax-takeaway-vat-cache-invalidation-1351.md`.)
2. Extend writer-call scanning beyond `internal/` if a `cmd/`/`scripts/`/
   `e2e/` caller of these writers ever appears (none exist today,
   verified by repo-wide grep).
3. Add `shellcheck` to CI for the 47+ scripts already running in the
   `build` job with no linting — would have caught the fail-open
   process-substitution gap mechanically.

## Safe-to-merge verdict

**Safe to merge.** The one blocking finding (file-scoped escape hatch)
is fixed and covered by a new regression case reproducing the exact
review probe; every non-blocking finding is fixed and re-verified; full
gate (build/vet/lint/test/guards) is green; no live production violation
exists today, so this ships as pure hardening with zero behavior change
to the running product.
