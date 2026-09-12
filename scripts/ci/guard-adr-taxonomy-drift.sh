#!/usr/bin/env bash
#
# Guard: a plugin-taxonomy COUNT restated in prose, in an ADR other than
# ADR-0002 itself, can drift out of sync with the real count independently
# of ADR-0002's own text (ut-docs#2159, split out of the ADR doc-vs-code
# drift audit in ut-docs#2150).
#
# guard-adr-plugin-taxonomy.sh (this directory) already pins ADR-0002's OWN
# taxonomy line + title against `CanonicalTypes` — that guard is untouched
# and this one deliberately does not re-check ADR-0002's own file, to avoid
# reporting the same drift twice under two different guards. What no guard
# caught before this one: every OTHER ADR that separately says "N-type
# taxonomy" or "(N canonical types)" in its own prose, as a cross-reference
# to ADR-0002, rather than pointing at ADR-0002 without a number. Two real,
# independent instances of exactly this drift already happened — ADR-0002's
# own text (ut-docs#2134) and ADR-0050 (ut-docs#2150) — and a live sweep for
# this card found four more, unfixed until this same change: ADR-0014,
# ADR-0023, ADR-0031, ADR-0091 all said "20-type taxonomy" after ADR-0010
# (which added `language`, the 21st type) and ADR-0088 Decision B (which
# added `layout`, the 22nd) had already moved the real count on.
#
# The trap in the obvious version of this guard: a blind "N-type taxonomy"
# scan also matches ADR-0010 and ADR-0050 themselves — both correctly
# narrate a HISTORICAL count as part of explaining their own decision
# ("the 20-type taxonomy... had no slot for them" is the state ADR-0010's
# own decision then changed; ADR-0050 explicitly frames its "21 types" as
# "at the time this ADR was written... before ADR-0088... took it to 22").
# Flagging either would be a false positive on correct prose, and the guard
# would need disabling or ignoring the moment someone hit it — exactly the
# failure this guard exists to prevent, just relocated. So: a line is only
# checked if it does NOT carry an inline `<!-- taxonomy-count:historical -->`
# marker — same reviewed-exception convention as this repo's own
# `i18n:ignore` (guard-i18n.sh) and `compliance-claim:allow`
# (guard-compliance-claims.sh). ADR-0010 and ADR-0050 both carry that marker
# on the exact line their historical count appears on, added in the same
# change that added this guard — an ADR added later that wants to narrate a
# historical count legitimately uses the same marker.
#
# DOCS_DIR resolution, skip-vs-fail semantics, and the "adr-taxonomy-guard"
# CI job that provides ut-docs via DOCS_READ_TOKEN: identical to
# guard-adr-plugin-taxonomy.sh — see that script's header for the full
# reasoning. Summary: DOCS_DIR explicitly set (or an explicit dir arg, see
# below) but pointing at nothing is a hard FAILURE; only the implicit
# local-dev default (no DOCS_DIR, no sibling checkout, no explicit arg)
# skips.
#
# Explicit-args form for fixture-based testing (see
# guard-adr-taxonomy-drift_test.sh): args are the Go source file and the
# ADR directory to scan, both as direct paths.
#
# Detection is line-based regex on three known current-fact phrasings this
# repo actually uses (confirmed by a live grep across every adr/*.md file
# while building this guard: "N-type (plugin )?taxonomy", "(N canonical
# types)", and "N types (ADR-0002" — the last one scoped to a same-line
# ADR-0002 co-mention specifically so it doesn't fire on an unrelated "N
# types of X" sentence elsewhere) — not a markdown parser, and not
# exhaustive against a phrasing nobody has used yet. Widen the patterns
# below if a future stale mention uses different wording and slips past
# this guard.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO_FILE="${ROOT_DIR}/internal/plugins/manifest_verifier.go"

DOCS_DIR_EXPLICIT="${DOCS_DIR:-}"
if [ -n "${DOCS_DIR_EXPLICIT}" ]; then
  case "${DOCS_DIR_EXPLICIT}" in
    /*) DOCS_DIR_RESOLVED="${DOCS_DIR_EXPLICIT}" ;;
    *) DOCS_DIR_RESOLVED="${ROOT_DIR}/${DOCS_DIR_EXPLICIT}" ;;
  esac
else
  DOCS_DIR_RESOLVED="${ROOT_DIR}/../ut-docs"
fi
ADR_DIR="${DOCS_DIR_RESOLVED}/adr"

EXPLICIT_ARGS=0
if [ "$#" -ge 1 ]; then GO_FILE="$1"; fi
if [ "$#" -ge 2 ]; then ADR_DIR="$2"; EXPLICIT_ARGS=1; fi

if [ ! -f "${GO_FILE}" ]; then
  echo "❌ adr-taxonomy-drift guard: ${GO_FILE} does not exist" >&2
  exit 1
fi

if [ ! -d "${ADR_DIR}" ]; then
  if [ -n "${DOCS_DIR_EXPLICIT}" ] || [ "${EXPLICIT_ARGS}" -eq 1 ]; then
    echo "❌ adr-taxonomy-drift guard: ${ADR_DIR} does not exist (DOCS_DIR/explicit arg was set — this is a real failure, not a missing local checkout)" >&2
    exit 1
  fi
  echo "adr-taxonomy-drift guard: skipping — no ut-docs checkout found at ${ADR_DIR} and DOCS_DIR is not set"
  exit 0
fi

# Live count, same extraction as guard-adr-plugin-taxonomy.sh (comments
# stripped first so a quoted string inside a `//` comment isn't counted).
GO_BLOCK="$(sed -n '/var CanonicalTypes = \[\]string{/,/^}/p' "${GO_FILE}" | sed 's#//.*##')"
if [ -z "${GO_BLOCK}" ]; then
  echo "❌ adr-taxonomy-drift guard: could not find a CanonicalTypes = []string{...} block in ${GO_FILE} — has it moved or been renamed?" >&2
  exit 1
fi
LIVE_COUNT="$(printf '%s' "${GO_BLOCK}" | grep -oE '"[^"]*"' | wc -l | tr -d ' ')"
if [ "${LIVE_COUNT}" -eq 0 ]; then
  echo "❌ adr-taxonomy-drift guard: found a CanonicalTypes block in ${GO_FILE} but no quoted values inside it" >&2
  exit 1
fi

FAIL_COUNT=0

while IFS= read -r -d '' f; do
  # ADR-0002's own file is guard-adr-plugin-taxonomy.sh's exclusive
  # territory — skip it here so drift there is only ever reported once.
  case "$(basename "${f}")" in
    0002-*) continue ;;
  esac

  # Two known current-fact phrasings: "<N>-type (plugin )?taxonomy" and
  # "(<N> canonical types)". A line carrying the historical-exception
  # marker is skipped outright, regardless of what number it names.
  while IFS=: read -r lineno line; do
    case "${line}" in
      *'taxonomy-count:historical'*) continue ;;
    esac
    n="$(printf '%s' "${line}" | grep -oE '[0-9]+-type( plugin)? taxonomy|\([0-9]+ canonical types\)|[0-9]+ types? \(ADR-0002' | head -1 | grep -oE '[0-9]+' | head -1)"
    if [ -n "${n}" ] && [ "${n}" -ne "${LIVE_COUNT}" ]; then
      echo "❌ adr-taxonomy-drift guard: ${f#"${ADR_DIR}"/}:${lineno} says ${n} but the live count is ${LIVE_COUNT} (${GO_FILE}'s CanonicalTypes):" >&2
      echo "    ${line}" >&2
      echo "  Either update the number, reword to name ADR-0002 without hardcoding a count, or — if this line is deliberately narrating a HISTORICAL count as part of explaining that ADR's own decision (see ADR-0010/ADR-0050) — add an inline <!-- taxonomy-count:historical --> marker." >&2
      FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
  done < <(grep -noE '.*(([0-9]+-type( plugin)? taxonomy)|(\([0-9]+ canonical types\))|([0-9]+ types? \(ADR-0002)).*' "${f}" || true)
done < <(find "${ADR_DIR}" -maxdepth 1 -name '*.md' -print0)

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "❌ ${FAIL_COUNT} adr-taxonomy-drift guard finding(s)" >&2
  exit 1
fi
echo "✓ adr-taxonomy-drift guard: no stale cross-ADR taxonomy-count mentions found (live count: ${LIVE_COUNT})"
