#!/usr/bin/env bash
#
# Prints the value release builds stamp as internal/buildinfo.BuildDate: the
# HEAD commit's time in RFC 3339 (ADR-0160 §6, ut-docs#4369). Fails loudly
# rather than printing nothing: an empty stamp makes a release behave like a
# source build, letting every dated first-party language bundle — stale ones
# included — override its built-in strings.
# Usage: BUILD_DATE="$(bash scripts/ci/build-date.sh)"
set -euo pipefail

d="$(git show -s --format=%cI HEAD)"
if ! [[ "$d" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(Z|[+-][0-9]{2}:[0-9]{2})$ ]]; then
  echo "::error::build-date.sh: HEAD's commit time '${d}' is not RFC 3339 — refusing to stamp an empty or bad buildinfo.BuildDate" >&2
  exit 1
fi
printf '%s\n' "$d"
