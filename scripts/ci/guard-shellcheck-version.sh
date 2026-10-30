#!/usr/bin/env bash
#
# ut-docs#1955 (follow-up to ut-docs#1943's shellcheck CI guard): the
# "Shellcheck (scripts/ci/*.sh)" step in .github/workflows/ci.yml relies on
# whatever `shellcheck` binary ubuntu-latest happens to ship preinstalled —
# no version pin at all, unlike the adjacent golangci-lint step (`version:
# v2.5.0`), where a schema/CLI mismatch across versions is a documented
# failure mode (ut-docs#1565: an unpinned action version would have made
# that gate unable to run at all). A future ubuntu-latest image bump could
# silently change which findings shellcheck reports (new checks added,
# wording/severity changes) and turn main red with no code change here.
#
# Rather than pin an exact shellcheck build — vendoring or downloading a
# binary is a new supply-chain dependency this ecosystem's security-first
# default weighs against when a cheaper option exists (ECOSYSTEM.md's
# "Security first") — this guard fails loudly the moment the *installed*
# `shellcheck --version` drifts from the known-good versions below, so a
# drift is caught and triaged deliberately instead of discovered as an
# unexplained red build.
#
# To change the accepted versions deliberately (a real shellcheck upgrade,
# verified to still pass scripts/ci/*.sh cleanly): update ACCEPTED_VERSIONS
# below (and the test's accepted versions) and say so in the commit/PR.
set -euo pipefail

# 0.9.0 = the Ubuntu 24 runner image, 0.11.0 = Ubuntu 26. 0.9.0 stays only
# while actions/runner-images#14748's ubuntu-latest migration is mixed — drop
# it once every ubuntu-latest job reports 0.11.0 (ut-docs#5207).
ACCEPTED_VERSIONS=("0.9.0" "0.11.0")

if ! command -v shellcheck >/dev/null 2>&1; then
  echo "❌ guard-shellcheck-version: no 'shellcheck' binary found on PATH — expected one of: ${ACCEPTED_VERSIONS[*]} (ubuntu-latest preinstalled)" >&2
  exit 1
fi

version_output="$(shellcheck --version 2>/dev/null || true)"
# The trailing `|| true` matters: under `set -eo pipefail`, a `shellcheck
# --version` whose output has no "version:" line (an unexpected output
# format, or a broken binary that prints nothing) makes `grep` exit 1,
# which pipefail propagates through the pipe and kills the script right
# here — before the "could not parse a version" diagnostic below ever
# gets to run (independent review, ut-docs#1955).
actual_version="$(grep '^version:' <<<"${version_output}" | awk '{print $2}' || true)"

if [[ -z "${actual_version}" ]]; then
  echo "❌ guard-shellcheck-version: could not parse a version out of 'shellcheck --version' output — its output format may have changed" >&2
  echo "${version_output}" >&2
  exit 1
fi

accepted=0
for v in "${ACCEPTED_VERSIONS[@]}"; do
  if [[ "${actual_version}" == "${v}" ]]; then
    accepted=1
    break
  fi
done

if [[ "${accepted}" -ne 1 ]]; then
  echo "❌ guard-shellcheck-version: installed shellcheck is ${actual_version}, expected one of: ${ACCEPTED_VERSIONS[*]} (ut-docs#1955, ut-docs#5207)" >&2
  echo "   This usually means the ubuntu-latest runner image shipped a new shellcheck build." >&2
  echo "   If ${actual_version}'s findings on scripts/ci/*.sh have been reviewed and are clean, add it to ACCEPTED_VERSIONS in this script deliberately." >&2
  exit 1
fi

echo "✓ guard-shellcheck-version: shellcheck ${actual_version} is one of the accepted versions (${ACCEPTED_VERSIONS[*]})"
