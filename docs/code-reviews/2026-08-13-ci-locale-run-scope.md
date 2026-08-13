# Review — scope the OS-locale CI rerun to internal/pages (ut-docs#674)

## Summary

`ut-docs#662` (universal-till#330, merged 20:52 UTC same day) widened a
locale-set test rerun in `.github/workflows/ci.yml`'s `build` job from
nothing to a full second `go test ./...`. The very next merge to `main`
failed CI: `internal/plugins` hung for 600s (Go's default per-package test
timeout), goroutines blocked on `internal/logging`'s output mutex and
`database/sql`'s connection opener, inside
`TestPublish_NeverPanicsRacingManagerReload` /
`TestHandleEvent_AskResultRedactedInRealLog` — neither of which touches
locale code, and neither of which was touched by #330's diff. `main` had
15 consecutive green `ci` runs immediately before the full-suite-doubling
commit landed.

Fix: scope the locale-set rerun to `go test ./internal/pages/...` — the
one package that actually owns every OS-locale touchpoint in the repo
(`setup_detect.go`) — instead of re-running the whole tree. Keeps
ut-docs#662's AC#3 intent (catch the "hidden by CI's empty LANG" bug
class) without the full-suite duplication that appears to have triggered
the resource contention. Root cause of the contention itself is **not**
claimed as proven here — filed separately as ut-docs#674 to investigate
properly.

## Independent review

Fresh-context general-purpose subagent (this is a `complexity:easy`,
mechanical CI-scope fix — a fresh-context review, not a model-tier
escalation, per the reviewer skill's easy-card exception).

**First pass caught a real, blocking bug in the first draft of this
fix**: the edit added the new `run: go test ./internal/pages/...` line
but never removed the pre-existing `run: go test ./...` line, leaving two
`run:` keys in the same YAML step mapping. Depending on the parser, that
either fails the whole workflow to parse (breaking CI harder than before)
or silently resolves to the last key — i.e. still running the full suite,
making the fix a no-op that would reproduce the exact hang it claims to
fix. The reviewer verified this two ways: reading the raw file at the
branch tip, and loading it through both a permissive (`yaml.safe_load`,
last-key-wins) and a strict duplicate-key-rejecting YAML loader. This is
exactly why the "ci.yml re-parses as valid YAML" claim in the earlier
verification note was misleading — a bare parse doesn't catch a duplicate
mapping key when the loader used silently resolves it.

**Fixed**: removed the stray `run: go test ./...` line so the step
contains exactly one `run: go test ./internal/pages/...`. Re-verified
with a strict duplicate-key YAML loader (not just `yaml.safe_load`) that
no duplicate keys remain anywhere in the file.

**What the reviewer verified independently, beyond the blocking finding**:
- `go build`/`go vet` clean.
- `go test ./internal/pages/...` green both with `LANG`/`LC_ALL` unset and
  set to `en_GB.UTF-8`.
- `guard-data-access.sh`/`guard-i18n.sh` green.
- Grepped the whole repo for every OS-locale-env-var touchpoint
  (`LANG`, `LC_ALL`, `os.Getenv("LANG"`, `os.Getenv("LC_ALL"`,
  `osLocaleEnv`, `osTimezoneName`, `detectLanguage`, `detectCountry`) and
  confirmed `internal/pages` is genuinely the only package with any —
  narrowing the rerun there does not silently drop coverage of a
  locale-dependent bug living elsewhere. (One unrelated hit,
  `packaging/windows/installer.nsi`'s `MUI_LANGUAGE "English"` NSIS
  installer-UI macro, confirmed as a false positive — nothing to do with
  OS env-var locale detection.)
- Confirmed neither hanging test in `internal/plugins` references
  locale/`LANG`/`LC_ALL` anywhere, consistent with the causal story that
  the contention is about running the suite twice, not about the locale
  env vars themselves.
- Judged the causal story (full-suite duplication → resource contention →
  unrelated-package hang) as appropriately hedged rather than overclaimed,
  and the mitigation (narrow the rerun to the one package that needs it)
  as reasonable and low-risk regardless of the exact root cause.
- Confirmed no scope creep: the diff touches exactly one file
  (`.github/workflows/ci.yml`), one step.

## Verified beyond automated tests

Not applicable — CI-workflow-only change, no runtime/UI surface.

## Safe-to-merge verdict

**Safe to merge** after the blocking duplicate-`run:`-key fix above.
Verified: `go build`/`go vet` clean; `go test ./internal/pages/...` green
with LANG set and unset; `guard-data-access.sh`/`guard-i18n.sh` green;
`ci.yml` re-parses with zero duplicate mapping keys under a strict
loader (not just a permissive one).
