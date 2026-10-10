#!/usr/bin/env bash
# Check the action.yml fields GitHub Marketplace validates at publish time.
#
# Usage: bash scripts/marketplace-metadata.sh [root]
#
# Root defaults to this script's parent directory. Exits nonzero when a field
# disagrees, naming the field and the value it read.
#
# GitHub validates these when a release form is submitted and nowhere else: a
# violation fails no workflow, it appears as a red line under "Release Action"
# the day someone goes to publish, which is the day a release is due. The rules
# (docs.github.com, "Publishing actions in GitHub Marketplace"):
#
#   * the name is unique. It cannot match an action already published on the
#     Marketplace, a user or an organization on GitHub, or a Marketplace
#     category. "Cite" was rejected because github.com/cite is a user, so the
#     name now names the thing rather than the project.
#   * the description is under 125 characters.
#   * branding.icon names a Feather icon and branding.color one of the eight
#     colors GitHub accepts.
#
# Two of those need something a script here does not have: the live Marketplace
# (asking it is the release form) and the Feather icon list (long, versioned,
# and not published as data). What is left is the half the form complained
# about on this repository: the name against a user GitHub already has, the
# description's length, and the README the listing renders. A rename onto a
# name this script has never heard of still has to be checked in the form.
#
# Both fields are read as one-line 'quoted' scalars, which is the form
# action.yml uses. A folded block (`description: >`) is reported rather than
# measured: measuring its first line would pass a description that is four
# lines long in the form.

set -uo pipefail

root="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
file="$root/action.yml"
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

# Names GitHub already holds, and why each is here. The Marketplace refuses a
# name a user or organization has; these are the ones this repository has been
# told about. Append rather than guess: the form is the only authority.
occupied='cite'

# quoted_scalar KEY: the value of a top-level `KEY: '...'` line. Prints nothing
# and returns nonzero when the key is absent or is not that shape, so the
# caller names the field it could not read instead of measuring a fragment.
quoted_scalar() {
  local key="$1" line value
  line="$(grep -m1 -E "^${key}:" "$file" 2>/dev/null || true)"
  [ -n "$line" ] || return 1
  value="$(printf '%s\n' "$line" |
    sed -nE "s/^${key}:[[:space:]]*'([^']*)'[[:space:]]*(#.*)?$/\1/p")"
  [ -n "$value" ] || return 1
  printf '%s\n' "$value"
}

if [ ! -f "$file" ]; then
  printf 'FAIL: %s is missing; the release form reads its name and description\n' "$file" >&2
  exit 1
fi

name=''
if ! name="$(quoted_scalar name)"; then
  fail "action.yml's name is not a one-line 'quoted' scalar; the release form needs a name, and its length and its uniqueness are not guesses"
fi

if [ -n "$name" ]; then
  lower="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
  for taken in $occupied; do
    if [ "$lower" = "$taken" ]; then
      fail "action.yml names the action \"$name\"; github.com/$taken is a user, and the Marketplace refuses a name a user already has"
    fi
  done
fi

description=''
if ! description="$(quoted_scalar description)"; then
  fail "action.yml's description is not a one-line 'quoted' scalar; a folded block hides its length from every check here, and the release form counts all of it"
fi

if [ -n "$description" ]; then
  length="${#description}"
  if [ "$length" -ge 125 ]; then
    fail "action.yml's description is $length characters; GitHub Marketplace refuses 125 or more"
  fi
fi

if [ ! -f "$root/README.md" ]; then
  fail "$root/README.md is missing; the Marketplace listing renders it"
fi

if [ "$fails" -gt 0 ]; then
  printf '\n%s Marketplace metadata problem(s).\n' "$fails" >&2
  exit 1
fi

printf 'ok — name "%s" (%d characters), description %d characters, README present\n' \
  "$name" "${#name}" "${#description}"
