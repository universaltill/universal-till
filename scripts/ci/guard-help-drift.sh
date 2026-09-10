#!/usr/bin/env bash
#
# Help-topic translation-drift guard (ut-docs#1962): guard-help-topics.sh
# already guarantees every shipped locale has *a* file for every topic
# English has, but only checks the file exists — never that its content
# still says what English says. A translated topic can silently rot: an
# English topic grows a section, the translations don't, and
# guard-help-topics.sh stays green throughout (real example: German's
# catalog.md described a UI two releases gone, undetected, until this card).
#
# This calls the Go half (scripts/ci/checkhelpdrift), which compares each
# translated topic against its English original on cheap, language-
# independent structural counts (headings, numbered steps, indented
# bullets, top-level bullets, bold-lead-in list items) and fails on any
# mismatch not recorded in scripts/ci/i18n-baseline/help-drift-baseline.json
# — that baseline file is where already-known drift is tracked and burned
# down over time, mirroring the ut-plugin-language-*/i18n-baseline/
# convention for already-known untranslated keys.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

go run ./scripts/ci/checkhelpdrift
