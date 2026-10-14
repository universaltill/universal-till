#!/usr/bin/env bash
#
# Sourced by scripts/ci/guard-i18n_*_test.sh (ut-docs#4258). Those regression
# tests plant fixtures and rewrite locale files to prove guard-i18n.sh fails
# when it should. Doing that in the REAL tree races with anything else reading
# it (e.g. a concurrent `go test ./internal/pages` loading a half-written
# web/locales/ar.json, or compiling a planted internal/pages/*.go), so each
# test instead runs inside a throwaway copy of exactly what guard-i18n.sh reads:
#   scripts/ci/guard-i18n.sh, web/ui, web/public, web/locales, internal/**/*.go
# guard-i18n.sh derives its root from its own location, so the sandbox copy
# only ever sees the sandbox.
#
# Usage (from a test, after `set -euo pipefail`):
#   source "$(dirname "${BASH_SOURCE[0]}")/i18n-guard-sandbox.sh"
#   enter_i18n_guard_sandbox        # copies the tree and cd's into the copy
#   trap ... EXIT                   # cleanup must call remove_i18n_guard_sandbox
# scripts/ci/guard-i18n-tests-sandboxed.sh enforces this for every test.

I18N_GUARD_SANDBOX=""

enter_i18n_guard_sandbox() {
  local real_root
  real_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  I18N_GUARD_SANDBOX="$(mktemp -d)"
  mkdir -p "${I18N_GUARD_SANDBOX}/scripts/ci"
  cp "${real_root}/scripts/ci/guard-i18n.sh" "${I18N_GUARD_SANDBOX}/scripts/ci/"
  # A failed copy removes the half-built sandbox itself: the caller's EXIT
  # trap may not be installed yet.
  if ! (
    cd "${real_root}" || exit 1
    # web/public/vendor is excluded by the guard, but copy everything: the
    # tree is small and a partial copy would change what "unmodified" means.
    find web/ui web/public web/locales internal -type f \
      \( -path 'internal/*' -name '*.go' -o -path 'web/*' \) -print0 |
      tar --null -T - -cf -
  ) | tar -xf - -C "${I18N_GUARD_SANDBOX}"; then
    remove_i18n_guard_sandbox
    return 1
  fi
  cd "${I18N_GUARD_SANDBOX}" || return 1
}

# Removes only the mktemp dir created above. Safe to call twice or when
# enter_i18n_guard_sandbox never ran.
remove_i18n_guard_sandbox() {
  if [[ -n "${I18N_GUARD_SANDBOX:-}" && -d "${I18N_GUARD_SANDBOX}" ]]; then
    cd /
    rm -rf "${I18N_GUARD_SANDBOX}"
  fi
  I18N_GUARD_SANDBOX=""
}
