#!/usr/bin/env bash
#
# German (de) render audit (ut-docs#2300): drives the real Playwright e2e
# harness against a real till with the ut-plugin-language-de pack loaded,
# and fails on visible English text on any admin/help-topic page route.
# Today this repeatable check replaces a manual crawl.
#
# `de` is NOT a core-shipped locale (web/locales/ only has en/ar/fa/tr) — it
# lives in the external ut-plugin-language-de repo. By default this script
# expects that repo cloned as a SIBLING directory next to this one, same
# relative-path convention scripts/audit-nav-i18n-parity.sh already uses for
# the same pack.
#
# UT_DE_PACK_DIR overrides that: an explicit path to the pack checkout,
# absolute or relative to this repo's root. CI needs it because
# actions/checkout refuses a `path:` that resolves outside $GITHUB_WORKSPACE,
# so the pack cannot be a real sibling there — same convention ci.yml's
# taxonomy guards already use for their own ut-docs checkout (DOCS_DIR).
#
# The actual check lives in e2e/tests-i18n-audit/audit-locale-render.spec.ts
# (playwright.locale-audit.config.ts); this script only resolves the pack
# path, wires it in via UT_TEST_I18N_OVERLAY_DIR (internal/pages/init.go's
# test-only overlay loader), and runs Playwright.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# Relative paths resolve against ROOT_DIR (cd'd to above), so CI can pass
# the workspace-relative checkout path actions/checkout produced.
DE_PACK_LOCALE="${UT_DE_PACK_DIR:-${ROOT_DIR}/../ut-plugin-language-de}/locales/de.json"

if [[ ! -f "${DE_PACK_LOCALE}" ]]; then
  if [[ "${UT_LOCALE_AUDIT_STRICT:-}" == "1" ]]; then
    # The dedicated CI workflow (locale-render-audit.yml) always checks the
    # pack repo out first, so a missing pack here is a real precondition
    # failure, not something to skip past.
    echo "audit-locale-render: ${DE_PACK_LOCALE} not found, and UT_LOCALE_AUDIT_STRICT=1 -- the pack repo must be checked out before this script runs" >&2
    exit 1
  fi
  echo "audit-locale-render: ${DE_PACK_LOCALE} not found -- skipping (dev-machine-friendly local run)."
  echo "audit-locale-render: clone https://github.com/universaltill/ut-plugin-language-de as a sibling of this repo (or point UT_DE_PACK_DIR at an existing checkout) to run this check locally, or set UT_LOCALE_AUDIT_STRICT=1 to make its absence a hard failure."
  exit 0
fi

DE_PACK_DIR="$(cd "$(dirname "${DE_PACK_LOCALE}")" && pwd)"
echo "audit-locale-render: using German pack locales at ${DE_PACK_DIR}"
export UT_TEST_I18N_OVERLAY_DIR="${DE_PACK_DIR}"

cd "${ROOT_DIR}/e2e"
exec npx playwright test --config=playwright.locale-audit.config.ts
