#!/usr/bin/env bash
#
# Guard: ADR-0002 claims to be "pinned by" a test that in fact pinned the
# code against a second hardcoded copy of itself (ut-docs#2134).
# `TestCanonicalTypesMatchTaxonomy` diffed `CanonicalTypes` against a
# literal string inside its own test file — not against ADR-0002 at all —
# so it stayed green through two real additions (`language` via ADR-0010,
# `layout` via ADR-0088 Decision B) while ADR-0002's own text kept saying
# "20 canonical types" and listing only the original 20. This guard is the
# check that test could never be: it reads ADR-0002's actual taxonomy line
# and CanonicalTypes for real and fails if they disagree.
#
# Needs the ut-docs repo checked out — via DOCS_DIR (a path relative to this
# repo's root, or absolute), or the sibling-checkout default (../ut-docs)
# already used elsewhere in this ecosystem (e.g. ut-cloud's
# internal/signing/manifest_contract_test.go POS_DIR convention). The
# "adr-taxonomy-guard" CI job (.github/workflows/ci.yml) checks ut-docs out
# at DOCS_DIR=ut-docs specifically so this runs for real on every push/PR.
#
# DOCS_DIR explicitly set (or an explicit ADR-file arg, see below) but
# pointing at nothing is a hard FAILURE, not a skip — a skip there would
# make the CI job silently green forever on exactly the drift this guard
# exists to catch. Only the implicit local-dev default (no DOCS_DIR, no
# sibling checkout, no explicit args) skips, for a bare `bash
# scripts/ci/*.sh` run with no ut-docs anywhere nearby.
#
# Explicit-args form for fixture-based testing (see
# guard-adr-plugin-taxonomy_test.sh): args are the Go source file and the
# ADR markdown file, both as direct file paths — this guard compares
# exactly one file to exactly one file, not directory trees.
#
# Known detection gaps, accepted (independent review, ut-docs#2134's
# finding N8) — this is a line-based regex match on ADR-0002's specific,
# current formatting, not a markdown parser, so it fails LOUDLY (never
# silently) rather than correctly on: the taxonomy line being re-wrapped
# across multiple lines, re-indented, or given a trailing character after
# the closing backtick; or a second backtick-quoted pipe-list appearing
# anywhere else in the file. None of these are a false pass — worth fixing
# only if ADR-0002's own formatting is deliberately changed, at which
# point this guard's extraction needs updating anyway.
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
ADR_FILE="${DOCS_DIR_RESOLVED}/adr/0002-plugin-type-taxonomy.md"

EXPLICIT_ARGS=0
if [ "$#" -ge 1 ]; then GO_FILE="$1"; fi
if [ "$#" -ge 2 ]; then ADR_FILE="$2"; EXPLICIT_ARGS=1; fi

if [ ! -f "${GO_FILE}" ]; then
  echo "❌ adr-plugin-taxonomy guard: ${GO_FILE} does not exist" >&2
  exit 1
fi

if [ ! -f "${ADR_FILE}" ]; then
  if [ -n "${DOCS_DIR_EXPLICIT}" ] || [ "${EXPLICIT_ARGS}" -eq 1 ]; then
    echo "❌ adr-plugin-taxonomy guard: ${ADR_FILE} does not exist (DOCS_DIR/explicit arg was set — this is a real failure, not a missing local checkout)" >&2
    exit 1
  fi
  echo "adr-plugin-taxonomy guard: skipping — no ut-docs checkout found at ${ADR_FILE} and DOCS_DIR is not set"
  exit 0
fi

# Extract CanonicalTypes from the Go var block. Independent review
# (ut-docs#2134) found a real silent false-pass here: an earlier version
# grep'd directly for `"[a-z_]+"`, so any quoted value NOT matching that
# character class (a digit, an uppercase letter — e.g. a hypothetical
# `oauth2` type) was silently DROPPED from the comparison instead of
# tripping an error, which is exactly the failure class this guard exists
# to prevent. Fixed by extracting every quoted string in the block first,
# then validating each one explicitly — anything that doesn't look like a
# type name is a hard failure, never a silent drop. Line comments are
# stripped first so a quoted string inside a `//` comment (e.g. `//
# "widget" was rejected`) isn't mistaken for a real entry.
GO_BLOCK="$(sed -n '/var CanonicalTypes = \[\]string{/,/^}/p' "${GO_FILE}" | sed 's#//.*##')"

if [ -z "${GO_BLOCK}" ]; then
  echo "❌ adr-plugin-taxonomy guard: could not find a CanonicalTypes = []string{...} block in ${GO_FILE} — has it moved or been renamed?" >&2
  exit 1
fi

GO_QUOTED="$(printf '%s' "${GO_BLOCK}" | grep -oE '"[^"]*"' | tr -d '"')"

if [ -z "${GO_QUOTED}" ]; then
  echo "❌ adr-plugin-taxonomy guard: found a CanonicalTypes block in ${GO_FILE} but no quoted values inside it" >&2
  exit 1
fi

GO_BAD="$(printf '%s\n' "${GO_QUOTED}" | grep -vE '^[a-z0-9_]+$' || true)"
if [ -n "${GO_BAD}" ]; then
  echo "❌ adr-plugin-taxonomy guard: ${GO_FILE}'s CanonicalTypes block contains value(s) that don't look like a canonical type name (expected lowercase/digits/underscore only):" >&2
  printf '%s\n' "${GO_BAD}" | sed 's/^/    /' >&2
  echo "  Fix the value, or widen this guard's validation if it's a genuinely new naming convention." >&2
  exit 1
fi

GO_TYPES="${GO_QUOTED}"

# Extract the pipe-delimited taxonomy line from the ADR — the one and only
# line in the file that is a single backtick-quoted pipe list of lowercase
# identifiers, e.g. `page|button|...|layout`.
# shellcheck disable=SC2016 # literal backtick pattern for grep -E, no expansion intended
ADR_LINE="$(grep -E '^`[a-z_]+(\|[a-z_]+)+`$' "${ADR_FILE}" || true)"
ADR_LINE_COUNT=0
if [ -n "${ADR_LINE}" ]; then
  ADR_LINE_COUNT="$(printf '%s\n' "${ADR_LINE}" | grep -c '.')"
fi

if [ "${ADR_LINE_COUNT}" -ne 1 ]; then
  echo "❌ adr-plugin-taxonomy guard: expected exactly one pipe-list line matching \`type|type|...\` in ${ADR_FILE}, found ${ADR_LINE_COUNT} — has the ADR's format changed?" >&2
  exit 1
fi

ADR_TYPES="$(printf '%s' "${ADR_LINE}" | tr -d '`' | tr '|' '\n')"

GO_SORTED="$(printf '%s\n' "${GO_TYPES}" | sort)"
ADR_SORTED="$(printf '%s\n' "${ADR_TYPES}" | sort)"

if [ "${GO_SORTED}" != "${ADR_SORTED}" ]; then
  echo "❌ adr-plugin-taxonomy guard: ADR-0002's taxonomy and CanonicalTypes have drifted." >&2
  echo "  only in code (${GO_FILE}):" >&2
  comm -23 <(printf '%s\n' "${GO_SORTED}") <(printf '%s\n' "${ADR_SORTED}") | sed 's/^/    /' >&2
  echo "  only in ADR-0002 (${ADR_FILE}):" >&2
  comm -13 <(printf '%s\n' "${GO_SORTED}") <(printf '%s\n' "${ADR_SORTED}") | sed 's/^/    /' >&2
  echo "  Adding a type = new/superseding ADR + code + CHECK + docs (ADR-0002's own rule) — update ADR-0002's taxonomy line to match." >&2
  exit 1
fi

TYPE_COUNT="$(printf '%s\n' "${GO_SORTED}" | grep -c '.')"

# The taxonomy line agreeing with the code doesn't guarantee the ADR is
# internally consistent — its own title ("... (22 canonical types)") is a
# second, independently-editable place the count is stated, and nothing
# above would catch that drifting from the real count (independent review,
# ut-docs#2134, finding N4: a 23rd type could be added to the pipe line
# alone, passing the check above, while the title still said 22).
TITLE_COUNT="$(grep -oE '\([0-9]+ canonical types\)' "${ADR_FILE}" | head -1 | grep -oE '[0-9]+' || true)"
if [ -z "${TITLE_COUNT}" ]; then
  echo "❌ adr-plugin-taxonomy guard: could not find a \"(N canonical types)\" count in ${ADR_FILE}'s title — has it moved or been reworded?" >&2
  exit 1
fi
if [ "${TITLE_COUNT}" -ne "${TYPE_COUNT}" ]; then
  echo "❌ adr-plugin-taxonomy guard: ${ADR_FILE}'s title says ${TITLE_COUNT} canonical types, but its own taxonomy line (which agrees with CanonicalTypes) has ${TYPE_COUNT} — the title is stale." >&2
  exit 1
fi

echo "✓ adr-plugin-taxonomy guard: ADR-0002 and CanonicalTypes agree (${TYPE_COUNT} types)"
