#!/usr/bin/env bash
# Checks scripts/marketplace-metadata.sh against this repository and against
# the shapes that must not pass.
#
# Usage: bash scripts/marketplace-metadata_test.sh
#
# The check exists because GitHub's Marketplace validation has no CI job: it
# runs in the release form, so a name or a description it refuses is found by
# whoever submits that form. A check that only ever sees a good action.yml
# proves nothing, so every rule is exercised against a fixture that breaks it,
# and the description's boundary is exercised on both sides.
#
# No network: the check reads action.yml (and README.md) and nothing else.

set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
check="$here/marketplace-metadata.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work:?}"' EXIT
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

# fixture <name>: a directory holding a copy of the real action.yml and
# README.md, printed on stdout for the caller to mutate.
fixture() {
  local dir="$work/$1"
  mkdir -p "$dir"
  cp "$root/action.yml" "$dir/action.yml"
  cp "$root/README.md" "$dir/README.md"
  printf '%s\n' "$dir"
}

# run_check <root>: sets out to the check's output and rc to its status.
run_check() {
  out="$(bash "$check" "$1" 2>&1)"
  rc=$?
}

# expect_pass <what> <root>
expect_pass() {
  run_check "$2"
  if [ "$rc" -ne 0 ]; then
    fail "$1: the check failed: $out"
  fi
}

# expect_fail <what> <root> <needle>: the check fails, and one of its lines
# carries the needle, so a failing check for an unrelated reason is a failure
# of this test too.
expect_fail() {
  run_check "$2"
  if [ "$rc" -eq 0 ]; then
    fail "$1: the check passed, want a failure"
    return
  fi
  case "$out" in
    *"$3"*) ;;
    *) fail "$1: '$3' not in: $out" ;;
  esac
}

# The shapes GitHub refuses, one fixture each.

dir="$(fixture name-user)"
sed -i "s/^name: .*/name: 'Cite'/" "$dir/action.yml"
expect_fail "a name a GitHub user holds" "$dir" 'github.com/cite is a user'

dir="$(fixture name-case)"
sed -i "s/^name: .*/name: 'CITE'/" "$dir/action.yml"
expect_fail "a name a GitHub user holds, in another case" "$dir" 'github.com/cite is a user'

dir="$(fixture name-folded)"
sed -i "s/^name: .*/name: >/" "$dir/action.yml"
expect_fail "a name in a folded block" "$dir" "name is not a one-line 'quoted' scalar"

dir="$(fixture name-absent)"
sed -i "/^name: /d" "$dir/action.yml"
expect_fail "no name at all" "$dir" "name is not a one-line 'quoted' scalar"

dir="$(fixture desc-125)"
long="$(printf 'a%.0s' $(seq 1 125))"
sed -i "s/^description: .*/description: '$long'/" "$dir/action.yml"
expect_fail "a 125-character description" "$dir" 'description is 125 characters'

dir="$(fixture desc-folded)"
sed -i "s/^description: .*/description: >/" "$dir/action.yml"
expect_fail "a folded description" "$dir" "description is not a one-line 'quoted' scalar"

dir="$(fixture readme-absent)"
rm "$dir/README.md"
expect_fail "no README to render" "$dir" 'README.md is missing'

dir="$(fixture action-absent)"
rm "$dir/action.yml"
expect_fail "no action.yml to read" "$dir" 'action.yml is missing'

# The boundary GitHub draws is "less than 125": 124 characters pass, and the
# fixture's own name, which this repository's action.yml also carries, passes.
dir="$(fixture desc-124)"
long="$(printf 'a%.0s' $(seq 1 124))"
sed -i "s/^description: .*/description: '$long'/" "$dir/action.yml"
expect_pass "a 124-character description" "$dir"

# The real thing: this repository's own action.yml, which is the file the next
# release form will submit.
expect_pass 'action.yml at the repository root' "$root"

if [ "$fails" -gt 0 ]; then
  printf '\n%s marketplace metadata problem(s).\n' "$fails" >&2
  exit 1
fi
echo "ok — the name clears the users GitHub holds, the description is under 125 characters, and every refusal the release form makes has a fixture here"
