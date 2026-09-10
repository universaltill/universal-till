#!/usr/bin/env bash
#
# ut-docs#1357: pluginTaxRateAsker and similar `.ask`-hook askers memoize
# answers per event-bus generation (ut-docs#222) — correct only if every
# writer of a setting such an asker reads also bumps
# plugins.SharedBus(db).BumpGeneration() after writing. This invariant is
# currently enforced only by convention across several call sites, and it
# has already been broken twice for real: ut-docs#222 caught a missing
# bump in the plugin-settings editor, and ut-docs#1351 found the catalog
# import path (`mergeTakeawayOverrides`) never bumped at all, causing a
# live VAT over-collection bug in the Germany café pilot — a
# merchant-configured takeaway tax override was silently ignored until an
# unrelated cache-busting event happened to occur.
#
# The bump can't be pushed down into the writers themselves
# (UpsertPluginSetting/UpsertPluginSettingScoped/MergeAdditiveJSONMapSetting,
# internal/data/plugin_repo.go) so every future caller gets it for free:
# `internal/plugins` imports `internal/data`, not the reverse, so a call
# from `internal/data` into `plugins.SharedBus(...)` would be an import
# cycle. So this guard enforces the convention by inspection instead: any
# production .go file with a LINE calling one of these writers must also
# reference `BumpGeneration()` somewhere in that SAME file, or that exact
# writer-call line must carry an explicit inline escape hatch for a
# reviewed exception where bumping genuinely isn't needed — same
# same-LINE convention as guard-kiosk-engine.sh's
# `kiosk-engine-guard:allow` (a file-scoped escape hatch would silence
# every OTHER writer call in the same file too, including ones added
# later with no review at all).
#
# ut-docs#1942: the scan covers internal/, cmd/, scripts/, and e2e/ — not
# just internal/. At the time this was widened, zero real call sites
# existed outside internal/, but nothing stops a future seed/smoke tool
# under cmd/, scripts/, or e2e/ from writing a plugin setting an
# .ask-hook asker reads, unguarded.
#
# A file under internal/data itself can never satisfy the BumpGeneration
# check (that import cycle is this guard's whole premise) — its only
# legitimate way to pass is the inline allow comment on the writer line.
#
# Known limitations (same shape as guard-price-history-sync.sh):
# - Same-file, direct-caller check via plain grep, not a call-graph
#   analysis. A writer call wrapped by a same-package helper, with the
#   actual BumpGeneration() call living in a different file than the
#   wrapper, is invisible to this guard. Keep the bump next to the writer
#   call it belongs to (the existing convention at every current call
#   site) and this holds; if that stops being true, tighten this guard
#   rather than trust its green check blindly.
# - The BumpGeneration() check is a same-file text search, not aware of
#   comments or string literals — a stray `// BumpGeneration()` mention
#   elsewhere in the file (or the token appearing inside an unrelated
#   string) satisfies it same as a real call. Keep that mention out of
#   files that don't actually call it.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# Overridable (same convention as guard-price-history-sync.sh's
# SYNC_CLASSIFICATION_FILE) so the regression test can exercise the
# "matched no production file" fail-closed path without needing a real
# rename of these methods.
WRITER_RE="${WRITER_RE:-\.(UpsertPluginSetting|UpsertPluginSettingScoped|MergeAdditiveJSONMapSetting)\(}"
DEFINITION_FILE="internal/data/plugin_repo.go"
ALLOW_COMMENT="plugin-settings-bump:allow"

# ut-docs#1942: internal/ is where every known call site lives today, but
# cmd/, scripts/, and e2e/ can just as easily grow a seed/smoke tool that
# writes a plugin setting — scan all four, same exclusions throughout.
SEARCH_DIRS=(internal cmd scripts e2e)

for dir in "${SEARCH_DIRS[@]}"; do
  if [[ ! -d "${dir}" ]]; then
    echo "❌ plugin-settings-bump guard: ${dir}/ does not exist — can't scan for writer call sites" >&2
    echo "   (repo layout changed? fix this guard's SEARCH_DIRS rather than let it silently no-op)" >&2
    exit 1
  fi
done

if [[ ! -f "${DEFINITION_FILE}" ]]; then
  echo "❌ plugin-settings-bump guard: ${DEFINITION_FILE} does not exist (renamed or moved?)" >&2
  echo "   Update DEFINITION_FILE in this guard rather than let its exemption silently miss." >&2
  exit 1
fi

violations=()
matched_any_file=0
while IFS= read -r -d '' file; do
  [[ "${file}" == "${DEFINITION_FILE}" ]] && continue
  grep -qE "${WRITER_RE}" "${file}" || continue
  matched_any_file=1

  # Per-LINE: a writer-call line is fine only if it itself carries the
  # allow comment. Deliberately NOT gated on whether the file references
  # BumpGeneration at all — a file can legitimately have one bumped call
  # site and one genuinely-exempt one; each writer-call line stands on
  # its own for the allow check.
  offending_lines="$(grep -nE "${WRITER_RE}" "${file}" | grep -v "${ALLOW_COMMENT}" || true)"
  [[ -z "${offending_lines}" ]] && continue

  # A file with at least one offending line still needs a same-file
  # BumpGeneration() reference to be acceptable — this is the file-scoped
  # half of the check (the bump legitimately lives a few lines away from
  # the write, e.g. after an `if changed > 0` guard), whereas the allow
  # check above is deliberately per-line.
  grep -q 'BumpGeneration()' "${file}" && continue

  violations+=("${file}")
done < <(find "${SEARCH_DIRS[@]}" -name '*.go' ! -name '*_test.go' ! -path '*/testdata/*' -print0)

if [[ "${matched_any_file}" -eq 0 ]]; then
  echo "❌ plugin-settings-bump guard: found no production caller of UpsertPluginSetting/" >&2
  echo "   UpsertPluginSettingScoped/MergeAdditiveJSONMapSetting anywhere under" >&2
  echo "   ${SEARCH_DIRS[*]}." >&2
  echo "   That's suspicious — these are real, used writers. If they were renamed, update" >&2
  echo "   WRITER_RE in this guard rather than let it silently stop checking anything." >&2
  exit 1
fi

if [[ ${#violations[@]} -gt 0 ]]; then
  echo "❌ plugin-settings-bump guard: file(s) have a plugin-settings writer call line" >&2
  echo "   (UpsertPluginSetting/UpsertPluginSettingScoped/MergeAdditiveJSONMapSetting)" >&2
  echo "   with no same-file BumpGeneration() reference and no same-line allow comment" >&2
  echo "   (ut-docs#1357):" >&2
  printf '   %s\n' "${violations[@]}" >&2
  echo "" >&2
  echo "   Every writer of a plugin setting an .ask-hook asker reads must bump" >&2
  echo "   plugins.SharedBus(db).BumpGeneration() after writing, or the change is" >&2
  echo "   silently ignored until an unrelated cache-busting event occurs (this" >&2
  echo "   exact bug shipped once already — ut-docs#1351, a live VAT over-collection" >&2
  echo "   in the Germany café pilot)." >&2
  echo "   If this call site genuinely doesn't need a bump, add an inline" >&2
  echo "   '// ${ALLOW_COMMENT} <reason>' comment on the SAME line as the call." >&2
  echo "   Inside internal/data itself this is the ONLY way to pass — a file there" >&2
  echo "   can never reference plugins.BumpGeneration() (that import cycle is why" >&2
  echo "   this guard exists as a grep check instead of a compiler-enforced one)." >&2
  exit 1
fi

echo "✓ plugin-settings-bump guard: every plugin-settings writer call line references BumpGeneration() in its file or carries an inline allow comment"
