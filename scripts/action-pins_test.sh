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
# The collector is deliberately loose: it matches `uses:` anywhere on a line,
# quoted or not, in .md, .yml and .yaml alike. A pin this script fails to match
# is not reported as anything — it is simply absent, and the gate goes green
# while shipping a bad pin. Missing a real pin is the expensive failure, so
# `<full-sha>` is the single exemption and it is named explicitly below rather
# than falling out of a narrow pattern.

set -uo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)" || exit 1
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

# Every line naming this Action after a `uses:`, as "<file>:<line>:<text>".
# Prose that names a version without a `uses:` is not a pin: examples/bypass.yml
# explains why v0.1.4 cannot be used, and must keep saying v0.1.4.
pins="$(git grep -nE "uses:[[:space:]]*['\"]?elecnix/cite@" -- '*.md' '*.yml' '*.yaml' || true)"

if [ -z "$pins" ]; then
  fail "no 'uses: elecnix/cite@' pins found — the grep or the layout changed"
  exit 1
fi

# resolve_tag <tag> — prints the commit a tag points at.
#
# Asks origin first and reads the answer out of FETCH_HEAD, so a stale or
# hand-made local tag cannot answer for it and no local ref is rewritten.
# Falls back to a local tag only when origin is unreachable, so this still
# runs offline. CI checks out without tags, which makes the fetch the normal
# path rather than the exception.
resolve_tag() {
  local tag="$1"
  if git fetch --quiet origin "refs/tags/$tag" 2>/dev/null; then
    git rev-parse -q --verify 'FETCH_HEAD^{commit}' 2>/dev/null && return 0
  fi
  git rev-parse -q --verify "refs/tags/$tag^{commit}" 2>/dev/null
}

versions=""
placeholders=0
while IFS= read -r pin; do
  [ -n "$pin" ] || continue
  where="$(printf '%s' "$pin" | cut -d: -f1-2)"
  text="$(printf '%s' "$pin" | cut -d: -f3-)"

  # Everything after the `@`, up to whatever ends the YAML scalar or the
  # surrounding Markdown: whitespace, a quote, a backtick, a brace, a comma.
  ref="$(printf '%s' "$text" | sed -E "s|.*elecnix/cite@||; s|[][[:space:]'\"\`{}(),].*||")"
  version="$(printf '%s' "$text" | sed -nE 's|.*#[[:space:]]*(v[0-9]+\.[0-9]+\.[0-9]+).*|\1|p')"

  # docs/release.md teaches the rule with a placeholder rather than a real pin.
  if [ "$ref" = "<full-sha>" ]; then
    placeholders=$((placeholders + 1))
    continue
  fi

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
  fail "snippets disagree on the version:$(printf '%s' "$versions")"
fi

if [ "$fails" -gt 0 ]; then
  printf '\n%s pin problem(s).\n' "$fails" >&2
  exit 1
fi

pinned="$(printf '%s' "$versions" | tr -d ' ')"
printf 'ok — every elecnix/cite pin is a full SHA for %s (%s placeholder(s) skipped)\n' \
  "$pinned" "$placeholders"

# Advisory only. Cutting a release is a tag push (docs/release.md), so the
# pins necessarily lag the newest tag until a refresh PR lands; failing here
# would redden main on every release for a gap nobody can close in advance.
newest="$(git ls-remote --tags --refs origin 'v*' 2>/dev/null |
  sed 's|.*refs/tags/||' | sort -V | tail -1)"
if [ -n "$newest" ] && [ -n "$pinned" ] && [ "$newest" != "$pinned" ]; then
  printf 'note: newest release is %s; the snippets pin %s. Refresh them.\n' "$newest" "$pinned"
fi
