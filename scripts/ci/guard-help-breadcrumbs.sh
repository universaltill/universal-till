#!/usr/bin/env bash
#
# Help-topic breadcrumb guard (ut-docs#4696): a help topic names a settings
# section as "Settings → <Section>". The translated topic must call that
# section what the till's own translated UI calls it (the web/locales
# settings.*.title / .label value, or one of the few other keys the Settings
# page shows as a section name -- extraSectionKeys), or the reader looks for a label that is
# not on screen. guard-help-drift.sh only compares structure, so it cannot
# see this.
#
# This calls the Go half (scripts/ci/checkhelpbreadcrumbs), which maps each
# English breadcrumb to its en.json key and checks the same numbered item of
# every translated topic for that key's value in the locale. A locale whose
# strings are not in web/locales (a language pack, e.g. de) is read from
# $UT_<LOC>_PACK_DIR or the sibling ut-plugin-language-<loc> checkout, and
# skipped with a notice when neither exists. Known mismatches live in
# scripts/ci/i18n-baseline/help-breadcrumb-baseline.txt (shrink-only: a stale
# entry fails). Quoted action/button labels are out of scope.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

go run ./scripts/ci/checkhelpbreadcrumbs -root "${ROOT_DIR}"
