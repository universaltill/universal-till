#!/usr/bin/env bash
#
# Regression test for guard-readme-local-links.sh (ut-docs#2611): proves the
# guard fails on a missing image and passes on present files and on
# external links, so it can't be silently passing because its grep stopped
# matching.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-readme-local-links.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAIL=0

mkdir -p "${TMP}/docs/images"
touch "${TMP}/docs/images/ok.webp" "${TMP}/CONTRIBUTING.md"

# The backticks are literal markdown code markers in the fixture, not expansions.
# shellcheck disable=SC2016
printf '![a](docs/images/ok.webp)\n[c](CONTRIBUTING.md#top)\n[w](https://example.com/x.png)\n[s](#section)\nUse `![x](docs/not-here.png)` like this.\n```md\n![y](docs/also-not-here.png)\n```\n' > "${TMP}/good.md"
printf '![a](docs/images/ok.webp)\n![b](docs/images/gone.webp "title")\n' > "${TMP}/bad.md"
printf '<a href="CONTRIBUTING.md">ok</a> <img src="docs/images/missing.png">\n' > "${TMP}/bad-html.md"

if bash "${GUARD}" "${TMP}/good.md" >/dev/null; then echo "PASS: present files, external links and code examples accepted"; else echo "FAIL: guard rejected a valid file"; FAIL=1; fi
if bash "${GUARD}" "${TMP}/bad.md" >/dev/null 2>&1; then echo "FAIL: guard accepted a missing image"; FAIL=1; else echo "PASS: missing image rejected"; fi
if bash "${GUARD}" "${TMP}/bad-html.md" >/dev/null 2>&1; then echo "FAIL: guard accepted a missing <img src>"; FAIL=1; else echo "PASS: missing <img src> rejected"; fi
if bash "${GUARD}" >/dev/null; then echo "PASS: the real README.md/CONTRIBUTING.md have no broken local links"; else echo "FAIL: the real README.md/CONTRIBUTING.md have broken local links"; FAIL=1; fi

exit "${FAIL}"
