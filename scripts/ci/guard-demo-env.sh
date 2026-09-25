#!/usr/bin/env bash
#
# ADR-0113 §1.1 (ut-docs#2687): demo mode has ONE switch, read ONCE.
# UT_DEMO and UT_DEMO_TOKEN are read by internal/config.Init into
# Config.Demo / Config.DemoToken, and nothing else in the Go code may read
# them — a second, independent read is how a "demo-only" behaviour ends up
# switchable without the start gate (internal/app's checkDemoGate: env +
# token + marker file + database flag) ever seeing it.
#
# Rule: outside internal/config/config.go, no non-test Go file may contain
# the string literal "UT_DEMO" or "UT_DEMO_TOKEN" (double-quoted or
# backquoted) on a code line. Matching the bare name rather than just
# os.Getenv(...) also catches os.LookupEnv, a local getenv helper, and the
# name parked in a const first. Deliberately NOT matched:
#   - comment-only lines (prose may name the variable);
#   - _test.go files (tests set the env for config.Init via t.Setenv, and
#     the packaging test searches for the name; tests never ship);
#   - "UT_DEMO=1"-style literals — a child process's environment being
#     WRITTEN (the demo broker, ADR-0113 §2), not the variable being read —
#     UNLESS the same file calls os.Environ: there a "UT_DEMO=" /
#     "UT_DEMO_TOKEN=" literal is how an Environ + HasPrefix read looks, so
#     it is flagged (mark a genuine child-env write with the allow marker).
# Reviewed exception: same-line "demo-env-guard:allow <reason>".
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

ALLOWED_FILE="internal/config/config.go"

if ! grep -qE '"UT_DEMO"' "${ALLOWED_FILE}"; then
  echo "❌ demo-env guard: ${ALLOWED_FILE} no longer reads UT_DEMO —" >&2
  echo "   the one sanctioned read moved; update this guard rather than let it go blind" >&2
  exit 1
fi

violations=""
# Tracked plus untracked-but-not-ignored files: git ls-files skips build
# output and node_modules, and still sees a new file before it is added.
while IFS= read -r -d '' f; do
  [[ "${f}" == "${ALLOWED_FILE}" ]] && continue
  [[ "${f}" == *_test.go ]] && continue
  [[ "${f}" == .claude/* ]] && continue
  [[ -f "${f}" ]] || continue
  # The backquote is a literal Go raw-string delimiter, not a command substitution.
  # shellcheck disable=SC2016
  hits="$(grep -nE '["`]UT_DEMO(_TOKEN)?["`]' "${f}" \
    | grep -vE '^[0-9]+:[[:space:]]*//' \
    | grep -v 'demo-env-guard:allow' || true)"
  # Reading via os.Environ + a prefix match ("UT_DEMO=" / "UT_DEMO_TOKEN=")
  # names the variable only with its "=": in a file that calls os.Environ,
  # that literal counts as a read too.
  if grep -qE '\bos\.Environ\(' "${f}"; then
    environ_hits="$(grep -nE '"UT_DEMO(_TOKEN)?=' "${f}" \
      | grep -vE '^[0-9]+:[[:space:]]*//' \
      | grep -v 'demo-env-guard:allow' || true)"
    if [[ -n "${environ_hits}" ]]; then
      hits+="${hits:+$'\n'}${environ_hits}"
    fi
  fi
  if [[ -n "${hits}" ]]; then
    violations+=$'\n'"${f}:"$'\n'"${hits}"$'\n'
  fi
done < <(git ls-files -z --cached --others --exclude-standard -- '*.go')

if [[ -n "${violations}" ]]; then
  echo "❌ demo-env guard: UT_DEMO / UT_DEMO_TOKEN read outside ${ALLOWED_FILE}" >&2
  echo "   Use cfg.Demo / cfg.DemoToken (internal/config) instead — ADR-0113 §1.1." >&2
  echo "   A reviewed exception carries a same-line '// demo-env-guard:allow <reason>'." >&2
  echo "${violations}" >&2
  exit 1
fi

echo "✓ demo-env guard: UT_DEMO / UT_DEMO_TOKEN are read only in ${ALLOWED_FILE}"
