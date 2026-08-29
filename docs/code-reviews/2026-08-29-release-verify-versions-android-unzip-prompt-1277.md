# Code review: release.yml `verify-versions` unzip interactive-prompt fix (ut-docs#1277)

**Date:** 2026-08-29
**Card:** [ut-docs#1277](https://github.com/universaltill/ut-docs/issues/1277)
**Diff:** `.github/workflows/release.yml` only (`verify-versions` job)
**Complexity:** easy — reviewed by a fresh-context Sonnet subagent (no prior
involvement in the change), per the pipeline's model-routing rule.

## What changed

The `v0.6.17` release run's `verify-versions` job "Android APK's embedded
.so libraries must be built with buildinfo.Version stamped" step failed
with:

```
replace extracted/res/9n.9.png? [y]es, [n]o, [A]ll, [N]one, [r]ename:  NULL
(EOF or read error, treating as "[N]one" ...)
##[error]Process completed with exit code 1.
```

`unzip -q "unitill-pos_${VERSION}_android.apk" -d extracted` prompted
interactively for a colliding zip entry (plausibly two APK entries that
differ only by case, e.g. a 9-patch PNG resource, colliding on the
`macos-14` runner's case-insensitive filesystem) and, with no TTY to
answer, aborted the step under `set -euo pipefail` *before*
`checkAndroidLib` ever ran. So the v0.6.17 Android build's version-stamp
check was never actually executed — not evidence the libraries shipped
unstamped, just an unverified gap.

Fix: `unzip -q` → `unzip -qo` (force-overwrite, no prompt) for both unzip
calls inside this job — the Android-APK extraction (the one that actually
failed) and the sibling Windows-zip extraction a few lines above it, which
carries the identical unprotected-prompt shape. Both extractions target a
directory freshly `rm -rf`'d and `mkdir`'d immediately beforehand, so a
forced overwrite can only ever clobber another entry from the *same*
archive — never something from a prior step.

## Review

Independent review (fresh-context Sonnet subagent, `git diff` + full read
of the `verify-versions` job) — **PASS, no blocking findings**:

1. **Correctness of `-qo`.** Valid, standard combinable `unzip` flags.
   Does not mask a real regression: the actual gates (`checkAndroidLib`,
   `check`) run *after* extraction and independently assert presence +
   exact version-stamp content of the files that matter
   (`lib/*/libgojni.so`, `unitill-pos.exe`, …), which were never the
   colliding resource file. A genuinely missing or mis-stamped binary
   still fails loudly.
2. **Completeness vs. acceptance criteria.** Root cause is plausible and
   matches the symptom; both unzip calls in the job are now
   non-interactive/deterministic.
3. **Swept the rest of the job** (lines ~505-710) for the same risk: the
   two `tar xzf` calls (linux/darwin) are unaffected — `tar` overwrites by
   default, no prompt. `hdiutil attach` is unrelated. No other unzip call
   in this job was missed.
4. **Scope/risk.** One file changed, entirely inside `verify-versions`'s
   two `run:` blocks. Pure workflow YAML — no Go/template code touched, so
   none of this repo's mechanically-enforced guards (data-access,
   kiosk-engine, i18n, compliance-wording, …) apply.
5. **YAML/shell.** `python3 -c "import yaml; yaml.safe_load(...)"` parses
   clean. Confirmed `unzip -qo` exits 0 on a forced overwrite (not a
   warning condition that would still trip `set -euo pipefail`).

**Non-blocking notes** (no action taken, both informational):
- A third, unpatched `unzip -q` remains in the separate `windows-installer`
  job (`release.yml:257`, `runs-on: ubuntu-latest`), extracting into a
  fresh `mktemp -d`. Out of scope for this card — different job, different
  (case-sensitive) runner, so the failure mode that caused #1277 can't
  occur there; flagged for awareness only.
- The other job's inline comments state when a fix was "verified
  empirically against the real … build"; this fix's comment doesn't make
  that claim, since reproducing the actual macOS-runner case-insensitive
  collision isn't feasible from a Linux dev/CI sandbox. Root cause is
  reasoned from the job log, not reproduced locally — noted for
  transparency, not a correctness concern.

## Verification

| Check | Result |
|---|---|
| `python3 -c "import yaml; yaml.safe_load(...)"` | parses clean |
| `git diff --stat` | 1 file, `.github/workflows/release.yml`, +10/-2, entirely inside `verify-versions` |
| Manual grep sweep of `verify-versions` for other unprotected `unzip`/`tar` calls | none found |
| Independent fresh-context review | PASS, no blockers |

No Go code, templates, or locale files touched — `gofmt`/`go build`/
`go test`/the product's data-access/i18n/compliance guards are not
applicable to this diff. The change itself can only be fully confirmed by
the next real release run exercising `verify-versions` on a fresh
`macos-14` runner, per the card's own acceptance criteria ("re-run or wait
for the next release").
