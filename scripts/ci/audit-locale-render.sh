#!/usr/bin/env bash
#
# German (de) render audit (ut-docs#2300): drives the real Playwright e2e
# harness against a real till with German loaded, and fails on visible
# English text on any admin/help-topic page route. Replaces a manual crawl.
#
# `de` is NOT a core-shipped locale (web/locales/ only has en/ar/fa/tr); its
# strings live in ut-translations (Weblate's git backing, ADR-0160). This
# script expects a checkout of universaltill/ut-translations as a SIBLING of
# this repo; UT_TRANSLATIONS_DIR overrides that with an explicit path, absolute
# or relative to this repo's root. CI needs it because the clone lives inside
# the workspace.
#
# The actual check lives in e2e/tests-i18n-audit/audit-locale-render.spec.ts
# (playwright.locale-audit.config.ts); this script builds the German overlay
# (scripts/ci/build-locale-overlay.sh) into a temp dir, wires it in via
# UT_TEST_I18N_OVERLAY_DIR (internal/pages/init.go's test-only overlay loader),
# and runs Playwright.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# Relative paths resolve against ROOT_DIR (cd'd to above), so CI can pass
# the workspace-relative clone path.
TRANSLATIONS_DIR="${UT_TRANSLATIONS_DIR:-${ROOT_DIR}/../ut-translations}"

if [[ ! -f "${TRANSLATIONS_DIR}/meta/areas.json" ]]; then
  if [[ "${UT_LOCALE_AUDIT_STRICT:-}" == "1" ]]; then
    # The dedicated CI workflow (locale-render-audit.yml) always clones
    # ut-translations first, so a missing checkout here is a real
    # precondition failure, not something to skip past.
    echo "audit-locale-render: ${TRANSLATIONS_DIR} is not a ut-translations checkout, and UT_LOCALE_AUDIT_STRICT=1 -- it must be cloned before this script runs" >&2
    exit 1
  fi
  echo "audit-locale-render: ${TRANSLATIONS_DIR} is not a ut-translations checkout -- skipping (dev-machine-friendly local run)."
  echo "audit-locale-render: clone https://github.com/universaltill/ut-translations as a sibling of this repo (or point UT_TRANSLATIONS_DIR at an existing checkout) to run this check locally, or set UT_LOCALE_AUDIT_STRICT=1 to make its absence a hard failure."
  exit 0
fi

OVERLAY_DIR="$(mktemp -d)"
trap 'rm -rf "${OVERLAY_DIR}"' EXIT
bash "${ROOT_DIR}/scripts/ci/build-locale-overlay.sh" --translations "${TRANSLATIONS_DIR}" --locale de --out "${OVERLAY_DIR}"
export UT_TEST_I18N_OVERLAY_DIR="${OVERLAY_DIR}"

cd "${ROOT_DIR}/e2e"
# Not exec: the EXIT trap must run to remove the overlay dir.
npx playwright test --config=playwright.locale-audit.config.ts
