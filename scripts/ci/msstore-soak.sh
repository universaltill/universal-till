#!/usr/bin/env bash
#
# Soak check for the Store submission URL (ut-docs#6407): the Store fetches the
# installer by URL some time after submission, so the URL must keep serving
# the same bytes. Fetch it several times over a window and fail if the
# redirect target, the size or the sha256 ever differs.
#
#   msstore-soak.sh <url> <expected-sha256> <expected-size> [checks=3] [interval-seconds=60]
#
# Each check: HEAD (follow redirects; the signed query string of GitHub's
# object-store redirect changes every time, so only its host + path count),
# a ranged GET (bytes=0-1023) that must answer 206 with 1024 bytes, and a full
# download whose sha256 and size must match. Only https is followed; the
# test-only MSSTORE_SOAK_ALLOW_HTTP=1 needs UT_MSSTORE_TEST=1 (else exit 2).
set -euo pipefail

url=${1:?usage: msstore-soak.sh <url> <sha256> <size> [checks] [interval]}
want_sha=${2:?sha256}
want_size=${3:?size}
checks=${4:-3}
interval=${5:-60}
[[ "$want_sha" =~ ^[0-9a-f]{64}$ ]] || { echo "::error::msstore-soak: bad sha256" >&2; exit 2; }
[[ "$checks" =~ ^[0-9]+$ ]] && [ "$checks" -ge 1 ] || { echo "::error::msstore-soak: checks must be >= 1" >&2; exit 2; }
[[ "$interval" =~ ^[0-9]+$ ]] || { echo "::error::msstore-soak: bad interval" >&2; exit 2; }

proto='=https'
if [ -n "${MSSTORE_SOAK_ALLOW_HTTP+x}" ]; then
  # Test override: only with UT_MSSTORE_TEST=1 (ut-docs#6636); never ignored silently.
  if [ "${UT_MSSTORE_TEST:-}" != 1 ]; then
    echo "::error::msstore-soak: MSSTORE_SOAK_ALLOW_HTTP is a test override and is honoured only with UT_MSSTORE_TEST=1 (ut-docs#6636)" >&2
    exit 2
  fi
  [ "$MSSTORE_SOAK_ALLOW_HTTP" = 1 ] && proto='=http,https'
fi

tmp="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${tmp:?}"; }
trap cleanup EXIT

first_target=""
for i in $(seq 1 "$checks"); do
  [ "$i" -gt 1 ] && sleep "$interval"
  # Final URL after redirects, minus the query string.
  eff="$(curl -fsSL --proto "$proto" --proto-redir "$proto" -I -o "$tmp/head" -w '%{url_effective}' "$url")" \
    || { echo "::error::soak check ${i}: HEAD failed for ${url}" >&2; exit 1; }
  target="${eff%%\?*}"
  size="$(tr -d '\r' <"$tmp/head" | awk 'tolower($1)=="content-length:"{v=$2} END{print v}')"
  code="$(curl -sSL --proto "$proto" --proto-redir "$proto" -r 0-1023 -o "$tmp/range" -w '%{http_code}' "$url")" \
    || { echo "::error::soak check ${i}: ranged GET failed" >&2; exit 1; }
  rsize="$(wc -c <"$tmp/range" | tr -d ' ')"
  curl -fsSL --proto "$proto" --proto-redir "$proto" -o "$tmp/full" "$url" \
    || { echo "::error::soak check ${i}: download failed" >&2; exit 1; }
  sha="$(shasum -a 256 "$tmp/full" | awk '{print $1}')"
  fsize="$(wc -c <"$tmp/full" | tr -d ' ')"

  [ "$code" = 206 ] && [ "$rsize" = 1024 ] \
    || { echo "::error::soak check ${i}: ranged GET answered ${code} with ${rsize} bytes, want 206 with 1024 (the Store resumes downloads)" >&2; exit 1; }
  [ "$size" = "$want_size" ] \
    || { echo "::error::soak check ${i}: Content-Length ${size:-none} != ${want_size}" >&2; exit 1; }
  [ "$fsize" = "$want_size" ] \
    || { echo "::error::soak check ${i}: downloaded ${fsize} bytes != ${want_size}" >&2; exit 1; }
  [ "$sha" = "$want_sha" ] \
    || { echo "::error::soak check ${i}: sha256 ${sha} != ${want_sha}" >&2; exit 1; }
  if [ -z "$first_target" ]; then
    first_target=$target
  elif [ "$target" != "$first_target" ]; then
    echo "::error::soak check ${i}: redirect target changed (${first_target} -> ${target})" >&2
    exit 1
  fi
  echo "soak check ${i}/${checks}: ok (${size} bytes, ${sha})"
done
echo "soak: ${checks} check(s) stable"
