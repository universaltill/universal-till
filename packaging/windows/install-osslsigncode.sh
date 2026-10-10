#!/usr/bin/env bash
# Build and install the osslsigncode that verifies our Windows signatures
# (packaging/windows/sign-exe.sh), from a pinned upstream commit
# (ut-docs#3867). Run by release.yml's signing jobs, in a step before
# `azure/login`, and by ci.yml's Windows code-signing regression test, so
# both use the same verifier. Callers run `sudo apt-get update` first.
#
# Why not `apt-get install osslsigncode`: Ubuntu 24.04 ships 2.8, which
# requires a PKCS#9 signingTime attribute inside the RFC 3161 timestamp
# token. That attribute is optional, and Microsoft's timestamp service does
# not always add it. 2.8 then drops the token and prints "Timestamp is not
# available", so sign-exe.sh refuses a correctly timestamped exe. Upstream
# fixed this in 2.9 (commit 71a046a, "Ignore missing PKCS#9 signing time
# field").
#
# The commit is pinned by its full hash and checked after the clone, the
# same trust model as SHA-pinned actions. A tag alone can be moved.
set -euo pipefail

OSSLSIGNCODE_VERSION="2.14"
OSSLSIGNCODE_COMMIT=beec94e308d1a1e03ca17b05fe089d93c6303e90

: "${RUNNER_TEMP:?install-osslsigncode.sh runs in GitHub Actions}"
src="$RUNNER_TEMP/osslsigncode-src"
rm -rf "$src"

# No libcurl: with OpenSSL 3 upstream fetches CRLs and -ts URLs through
# OpenSSL's own HTTP client (its CMakeLists only uses curl below 3.0).
# ut-docs#4059: retry and time-limit the network calls (a hang held a release).
sudo apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=30 install -y cmake libssl-dev zlib1g-dev

timeout 300 git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$OSSLSIGNCODE_VERSION" \
  https://github.com/mtrojnar/osslsigncode.git "$src" \
  || { echo "::error::osslsigncode clone failed or timed out after 300 s (ut-docs#4059)" >&2; exit 1; }
got="$(git -C "$src" rev-parse HEAD)"
if [ "$got" != "$OSSLSIGNCODE_COMMIT" ]; then
  echo "::error::osslsigncode $OSSLSIGNCODE_VERSION is $got, expected $OSSLSIGNCODE_COMMIT — refusing to build it" >&2
  exit 1
fi

cmake -S "$src" -B "$src/build" -DCMAKE_BUILD_TYPE=Release >/dev/null
cmake --build "$src/build" -j"$(nproc)" >/dev/null
sudo cmake --install "$src/build" --prefix /usr/local >/dev/null
hash -r
if [ "$(command -v osslsigncode)" != /usr/local/bin/osslsigncode ]; then
  echo "::error::osslsigncode on PATH is $(command -v osslsigncode), not the /usr/local/bin build" >&2
  exit 1
fi
osslsigncode --version
