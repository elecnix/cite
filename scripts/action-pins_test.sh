#!/usr/bin/env bash
# Checks every documented `uses: elecnix/cite@...` pin in this repository.
#
# Usage: bash scripts/action-pins_test.sh
#
# docs/release.md ("Why SHA pinning matters") tells consumers to pin the Action
# to a release's full commit SHA, because a mutable tag is one compromised
# release away from code execution in every consumer's CI. The snippets we ship
# are the ones consumers copy, so they have to obey that rule themselves.
#
# Three ways a pin goes wrong, one assertion each:
#   1. it names a tag or a short SHA instead of a full one;
#   2. its `# vX.Y.Z` comment names a version the SHA does not belong to;
#   3. the snippets disagree, so a reader copies two different versions.
#
# Resolving the comment to a commit needs the tag locally. CI checks out without
# tags, so fetch the one tag we need when it is missing.

set -uo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)" || exit 1
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

# Every `uses:` line naming this Action, as "<file>:<line>:<text>". Prose that
# mentions a version without a `uses:` (examples/bypass.yml explains why v0.1.4
# is unsuitable) is deliberately not a pin and is not matched.
pins="$(git grep -nE '^[[:space:]]*(- )?uses:[[:space:]]*elecnix/cite@' -- '*.md' '*.yml' || true)"

if [ -z "$pins" ]; then
  fail "no 'uses: elecnix/cite@' pins found — the grep or the layout changed"
  exit 1
fi

# resolve_tag <tag> — prints the commit a tag points at, fetching it if needed.
resolve_tag() {
  local tag="$1"
  if ! git rev-parse -q --verify "refs/tags/$tag^{commit}" 2>/dev/null; then
    git fetch --quiet origin "refs/tags/$tag:refs/tags/$tag" 2>/dev/null || return 1
    git rev-parse -q --verify "refs/tags/$tag^{commit}" 2>/dev/null || return 1
  fi
}

versions=""
while IFS= read -r pin; do
  [ -n "$pin" ] || continue
  where="${pin%%:*}:$(printf '%s' "$pin" | cut -d: -f2)"
  text="$(printf '%s' "$pin" | cut -d: -f3-)"

  ref="$(printf '%s' "$text" | sed -E 's|.*elecnix/cite@([^[:space:]]+).*|\1|')"
  version="$(printf '%s' "$text" | sed -nE 's|.*#[[:space:]]*(v[0-9]+\.[0-9]+\.[0-9]+).*|\1|p')"

  # 1. a full 40-hex SHA, never a tag
  if ! printf '%s' "$ref" | grep -qE '^[0-9a-f]{40}$'; then
    fail "$where pins '$ref' — docs/release.md requires a full 40-character commit SHA"
    continue
  fi

  # 2. the trailing comment has to say which release that SHA is
  if [ -z "$version" ]; then
    fail "$where pins a SHA with no '# vX.Y.Z' comment saying which release it is"
    continue
  fi

  if ! commit="$(resolve_tag "$version")"; then
    fail "$where names $version, which does not resolve to a commit"
    continue
  fi

  if [ "$commit" != "$ref" ]; then
    fail "$where says $version but pins $ref; $version is $commit"
    continue
  fi

  case " $versions " in
    *" $version "*) ;;
    *) versions="$versions $version" ;;
  esac
done <<EOF
$pins
EOF

# 3. one version across every snippet a consumer might copy
count="$(printf '%s' "$versions" | wc -w | tr -d ' ')"
if [ "$count" -gt 1 ]; then
  fail "snippets disagree on the version: $(printf '%s' "$versions" | tr -s ' ')"
fi

if [ "$fails" -gt 0 ]; then
  printf '\n%s pin problem(s).\n' "$fails" >&2
  exit 1
fi

printf 'ok — every elecnix/cite pin is a full SHA for%s\n' "$versions"
