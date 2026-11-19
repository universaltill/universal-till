#!/usr/bin/env bash
#
# Fail unless a built artifact carries the expected install channel
# (ADR-0194, ut-docs#6405):
#
#   check-artifact-channel.sh <channel> <binary>...
#
# Release builds use -trimpath, so `go version -m` records no -ldflags; the
# stamp is found as the literal `ut-channel:<channel>` (buildinfo.ChannelStamp).
# A binary with no stamp, a different channel, or two different stamps fails.
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <sideload|play|appstore|direct> <binary>..." >&2
  exit 2
fi
want=$1
shift
case "$want" in
  sideload | play | appstore | direct) ;;
  *) echo "::error::check-artifact-channel: unknown channel '${want}'" >&2; exit 2 ;;
esac

fail=0
for f in "$@"; do
  if [ ! -f "$f" ]; then
    echo "::error::check-artifact-channel: no such file: $f" >&2
    fail=1
    continue
  fi
  # Capture first, then match (strings | grep -q breaks under pipefail).
  stamps="$(LC_ALL=C grep -aoE 'ut-channel:(sideload|play|appstore|direct)' "$f" | sort -u || true)"
  if [ -z "$stamps" ]; then
    echo "::error::$f carries no channel stamp: built without an explicit buildinfo.Channel (ADR-0194)" >&2
    fail=1
  elif [ "$stamps" != "ut-channel:${want}" ]; then
    echo "::error::$f is stamped $(tr '\n' ' ' <<<"$stamps")but this artifact must be '${want}' (ADR-0194)" >&2
    fail=1
  else
    echo "ok: $f is stamped channel ${want}"
  fi
done
exit "$fail"
