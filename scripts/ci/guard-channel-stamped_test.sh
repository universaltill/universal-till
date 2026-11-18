#!/usr/bin/env bash
# Test for guard-channel-stamped.sh and check-artifact-channel.sh (ut-docs#6405).
# Fixture and `A && ok || fail` assertion style used throughout this test.
# shellcheck disable=SC2015
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-channel-stamped.sh"
CHECK="${ROOT_DIR}/scripts/ci/check-artifact-channel.sh"
WORK="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }
B=github.com/universaltill/universal-till/internal/buildinfo

mk() { # mk <goreleaser-windows-channel> <makefile-extra>
  R="$WORK/repo"; rm -rf -- "${R:?}"; mkdir -p "$R/.github/workflows"
  cat >"$R/.goreleaser.yaml" <<YML
builds:
  - id: linux
    ldflags: ["-X $B.Channel=sideload -X $B.ChannelStamp=ut-channel:sideload -X $B.Version=1 -X $B.BuildDate=x"]
  - id: windows
    ldflags: ["-X $B.Channel=$1 -X $B.ChannelStamp=ut-channel:$1 -X $B.Version=1 -X $B.BuildDate=x"]
YML
  printf 'LDFLAGS=-X %s.Channel=sideload -X %s.ChannelStamp=ut-channel:sideload -X %s.Version=1 -X %s.BuildDate=x\n%s\n' "$B" "$B" "$B" "$B" "${2:-}" >"$R/Makefile"
}
expect() { # expect <0|1> <name>
  local rc=0; bash "$GUARD" "$R" >"$WORK/out" 2>&1 || rc=$?
  if [ "$rc" = "$1" ]; then ok "$2"; else bad "$2 (exit $rc)"; cat "$WORK/out" >&2; fi
}

mk direct; expect 0 "windows=direct, linux=sideload passes"
mk sideload; expect 1 "windows build stamped sideload fails"
mk direct "X=-X $B.Version=2 -X $B.BuildDate=x"; expect 1 "a Version stamp with no Channel fails"
mk direct "X=-X $B.Channel=\$(CH) -X $B.ChannelStamp=ut-channel:\$(CH) -X $B.Version=2"; expect 1 "a variable channel fails"
mk direct "X=-X $B.Channel=bogus -X $B.ChannelStamp=ut-channel:bogus -X $B.Version=2"; expect 1 "an unknown channel fails"
mk direct "X=-X $B.Channel=play -X $B.Version=2"; expect 1 "channel without its ChannelStamp twin fails"
mk direct "# -X $B.Version=2"; expect 0 "a commented stamp is ignored"
bash "$GUARD" >"$WORK/out" 2>&1 && ok "guard passes on the real tree" || { bad "real tree"; cat "$WORK/out" >&2; }

# check-artifact-channel.sh
printf 'xx ut-channel:direct yy' >"$WORK/d.bin"; printf 'nothing' >"$WORK/n.bin"
printf 'ut-channel:direct ut-channel:play' >"$WORK/two.bin"
bash "$CHECK" direct "$WORK/d.bin" >/dev/null 2>&1 && ok "right channel passes" || bad "right channel"
! bash "$CHECK" sideload "$WORK/d.bin" >/dev/null 2>&1 && ok "wrong channel fails" || bad "wrong channel"
! bash "$CHECK" direct "$WORK/n.bin" >/dev/null 2>&1 && ok "unstamped artifact fails" || bad "unstamped"
! bash "$CHECK" direct "$WORK/two.bin" >/dev/null 2>&1 && ok "two stamps fail" || bad "two stamps"
! bash "$CHECK" direct "$WORK/missing" >/dev/null 2>&1 && ok "missing file fails" || bad "missing"
rc=0; bash "$CHECK" nonsense "$WORK/d.bin" >/dev/null 2>&1 || rc=$?; [ "$rc" = 2 ] && ok "unknown channel is a usage error" || bad "unknown channel"
# A real Go build with the real stamp.
if command -v go >/dev/null; then
  mkdir -p "$WORK/m" && printf 'package main\nimport _ "github.com/universaltill/universal-till/internal/buildinfo"\nfunc main(){}\n' >"$WORK/m/main.go"
  mkdir -p "$ROOT_DIR/.tmp-channel-test" && cp "$WORK/m/main.go" "$ROOT_DIR/.tmp-channel-test/main.go"
  if ( cd "$ROOT_DIR" && go build -trimpath -ldflags "-s -w -X $B.Channel=direct -X $B.ChannelStamp=ut-channel:direct" -o "$WORK/real" ./.tmp-channel-test ); then
    bash "$CHECK" direct "$WORK/real" >/dev/null 2>&1 && ok "a real trimpath Go build is recognised" || bad "real go build not recognised"
  else bad "go build"; fi
  rm -rf -- "${ROOT_DIR:?}/.tmp-channel-test"
fi
[ "$fails" = 0 ] || { echo "guard-channel-stamped_test: $fails failure(s)" >&2; exit 1; }
echo "✓ guard-channel-stamped_test: all cases passed"
