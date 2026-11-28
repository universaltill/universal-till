#!/usr/bin/env bash
#
# Guard (ut-docs#6332 S3, ADR-0190): every core row in the four uislot tables
# (CoreMenu, CoreItems, CoreRail, CoreSettings) declares a visibility Class
# (`Class: ClassEssential|ClassCommon|ClassAdvanced`). A new row without one
# fails the build here, before the Go test (TestEveryCoreRowDeclaresValidClass,
# which also checks the value) ever has to be read. Row = a line starting with
# `{Key:` inside one of the four `var Core... = []Entry{` blocks.
#
# Arg (fixture testing): path to a slot.go. Default internal/uislot/slot.go.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SLOT="${1:-${ROOT_DIR}/internal/uislot/slot.go}"

[ -f "${SLOT}" ] || { echo "❌ visibility-class guard: ${SLOT} not found" >&2; exit 1; }

awk '
  /^var Core(Menu|Items|Rail|Settings) = \[\]Entry\{/ { table=$2; inblock=1; next }
  inblock && /^\}/ { inblock=0; next }
  inblock && /^[ \t]*\{Key:/ {
    rows++
    if ($0 !~ /Class: *Class(Essential|Common|Advanced)[,}]/) {
      printf "❌ visibility-class guard: %s row without a valid Class: %s\n", table, $0 > "/dev/stderr"
      bad++
    }
  }
  END {
    if (rows == 0) { print "❌ visibility-class guard: found no core rows (pattern drifted)" > "/dev/stderr"; exit 1 }
    if (bad > 0) { print "   declare Class: ClassEssential|ClassCommon|ClassAdvanced on every row (ADR-0190)" > "/dev/stderr"; exit 1 }
    printf "✓ visibility-class guard: %d core rows all declare a class\n", rows
  }
' "${SLOT}"
