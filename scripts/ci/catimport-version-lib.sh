#!/usr/bin/env bash
#
# Sourced (never executed) by check-catimport-pin.sh,
# guard-catimport-version.sh and catimport-tag.sh (ut-docs#5882): the one
# definition of "catimport changed", of a valid catimport/VERSION, and of
# the latest catimport/vX.Y.Z tag, so the pin check, the PR guard and the
# tagging workflow can never disagree.
#
# No `set` here: the sourcing script owns its shell options.

# Only what a consumer compiles counts: Go source and the module files.
# README/other .md and catimport/VERSION itself deliberately do not.
CATIMPORT_SOURCE_PATHSPEC=(':(glob)catimport/**/*.go' catimport/go.mod catimport/go.sum)

# catimport_changed <repo> <from-rev> <to-rev>
# Prints the changed source paths, one per line. Non-zero if git fails (a git
# error is never "no change").
catimport_changed() {
  git -C "$1" diff --name-only "$2" "$3" -- "${CATIMPORT_SOURCE_PATHSPEC[@]}"
}

# catimport_read_version <repo> <rev>
# Prints X.Y.Z from catimport/VERSION as committed at <rev>. Exit 3 when the
# file is absent at that rev, 1 (with a message) when malformed or unreadable.
catimport_read_version() {
  local repo="$1" rev="$2" raw
  if ! git -C "${repo}" cat-file -e "${rev}:catimport/VERSION" 2>/dev/null; then
    return 3
  fi
  # $(...) strips every trailing newline, so count lines and CRs on the raw
  # bytes first: exactly one line, no CR.
  local nl
  raw="$(git -C "${repo}" show "${rev}:catimport/VERSION"; printf x)" || return 1
  raw="${raw%x}"
  nl="${raw//[^$'\n']/}"
  if [[ "${raw}" == *$'\r'* || ${#nl} -gt 1 || ( ${#nl} -eq 1 && "${raw}" != *$'\n' ) ]]; then
    echo "catimport/VERSION must be a single line X.Y.Z (no CR, no second line)" >&2
    return 1
  fi
  raw="${raw%$'\n'}"
  if [[ ! "${raw}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "catimport/VERSION '${raw}' is not a bare X.Y.Z (no v prefix, no pre-release suffix)" >&2
    return 1
  fi
  printf '%s\n' "${raw}"
}

# semver_cmp <a> <b> -> -1, 0 or 1 (numeric per field; both strict X.Y.Z).
semver_cmp() {
  local -a a b
  IFS=. read -r -a a <<<"$1"
  IFS=. read -r -a b <<<"$2"
  local i
  for i in 0 1 2; do
    if ((10#${a[i]} < 10#${b[i]})); then
      echo -1
      return 0
    elif ((10#${a[i]} > 10#${b[i]})); then
      echo 1
      return 0
    fi
  done
  echo 0
}

# catimport_latest_tag <repo>
# Prints the highest X.Y.Z among local tags catimport/vX.Y.Z (non-semver tags
# are ignored with a warning). Exit 1 when there is none: v0.2.0+ exist, so
# zero means a tagless/misconfigured checkout and the caller must fail closed.
catimport_latest_tag() {
  local repo="$1" tags t v best=""
  tags="$(git -C "${repo}" tag -l 'catimport/v*')" || return 1
  while IFS= read -r t; do
    [[ -n "${t}" ]] || continue
    v="${t#catimport/v}"
    if [[ ! "${v}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
      echo "::warning::ignoring non-semver tag ${t}" >&2
      continue
    fi
    if [[ -z "${best}" || "$(semver_cmp "${v}" "${best}")" == 1 ]]; then
      best="${v}"
    fi
  done <<<"${tags}"
  if [[ -z "${best}" ]]; then
    echo "no catimport/v* tags — were tags fetched?" >&2
    return 1
  fi
  printf '%s\n' "${best}"
}
