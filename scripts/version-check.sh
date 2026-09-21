#!/usr/bin/env bash
# Check that the three places naming a release agree.
#
# Usage: bash scripts/version-check.sh [expected-tag] [root]
#
#   expected-tag  the tag being pushed (vX.Y.Z), checked when release.yml runs
#   root          repository root to read (default: this script's parent)
#
# At tag time one release is named in three places, and nothing used to check
# they agreed:
#
#   * the tag being pushed                      vX.Y.Z
#   * VERSION, beside action.yml                X.Y.Z
#     (the composite action reads it to pick the binary — issue #94)
#   * const version in cmd/cite/main.go         X.Y.Z
#     (compiled into the binary, printed by `cite version`)
#
# Keeping them in lockstep by hand is exactly the manual-synchronisation
# failure #94 describes for the action pin and the binary: two values in two
# syntaxes, nothing verifying they match. This is the check, so a release can
# only ship a tag whose binary and action agree on what that tag is.
#
# Exits nonzero if any of them disagree, naming each disagreement.

set -uo pipefail

expected="${1:-}"
root="${2:-$(cd "$(dirname "$0")/.." && pwd)}"
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

version_re='^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$'

# 1. VERSION exists, holds a bare version, and carries no `v` prefix. The
# prefix is the tag's, not the version's: a file holding `v0.8.0` would build
# the URL for the tag `vv0.8.0`.
file_version=""
if [ ! -f "$root/VERSION" ]; then
  fail "$root/VERSION is missing — the composite action reads it to pick the binary"
else
  file_version="$(tr -d '[:space:]' < "$root/VERSION")"
  case "$file_version" in
    v*) fail "VERSION holds '$file_version' — the bare version (${file_version#v}) is what goes in the file; the tag carries the v" ;;
  esac
  if ! printf '%s' "$file_version" | grep -qE "$version_re"; then
    fail "VERSION holds '$file_version', which is not X.Y.Z"
    file_version=""
  fi
fi

# 2. cmd/cite/main.go names the same version. A release whose binary reports
# one version while its action downloads another is the bug this prevents.
const_version=""
if [ ! -f "$root/cmd/cite/main.go" ]; then
  fail "$root/cmd/cite/main.go is missing"
else
  const_version="$(grep -m1 -oE '^const version = "[^"]+"' "$root/cmd/cite/main.go" |
    sed -E 's|^const version = "([^"]+)"$|\1|')"
  if [ -z "$const_version" ]; then
    fail "no 'const version = \"...\"' line in cmd/cite/main.go"
  elif [ -n "$file_version" ] && [ "$const_version" != "$file_version" ]; then
    fail "cmd/cite/main.go says $const_version, VERSION says $file_version — a release commit bumps both"
  fi
fi

# 3. The tag being released names the same version.
if [ -n "$expected" ]; then
  if [ -z "$file_version" ]; then
    fail "cannot compare tag $expected against a VERSION file that did not parse"
  elif [ "$expected" != "v$file_version" ]; then
    fail "tag $expected does not match VERSION v$file_version"
  fi
fi

if [ "$fails" -gt 0 ]; then
  printf '\n%s version disagreement(s).\n' "$fails" >&2
  exit 1
fi

if [ -n "$expected" ]; then
  printf 'ok — tag %s, VERSION, and cmd/cite/main.go all name %s\n' "$expected" "$file_version"
else
  printf 'ok — VERSION and cmd/cite/main.go both name %s\n' "$file_version"
fi
