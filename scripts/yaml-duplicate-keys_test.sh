#!/usr/bin/env bash
# Tests scripts/yaml-duplicate-keys.sh, then runs it on this repository's
# action.yml.
#
# Usage: bash scripts/yaml-duplicate-keys_test.sh

set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
check="$root/scripts/yaml-duplicate-keys.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

# expect <name> <want-status> <yaml>
expect() {
  local f="$work/case-$RANDOM$RANDOM.yml" out status
  printf '%s\n' "$3" > "$f"
  out="$(bash "$check" "$f" 2>&1)"; status=$?
  if [ "$2" -eq 0 ] && [ "$status" -ne 0 ]; then
    fail "$1: reported a duplicate in valid YAML: $out"
  elif [ "$2" -ne 0 ] && [ "$status" -eq 0 ]; then
    fail "$1: missed the duplicate key"
  fi
}

expect "the v0.9.0 regression" 1 'inputs:
  version:
    description: >
      Cite release tag to download.
      required: false is prose here, not a key
    required: false
    default: ""
    required: false
    default: ""
  model_id:
    required: false'

expect "a duplicate top-level key" 1 'name: a
runs:
  using: composite
name: b'

expect "a duplicate key inside a sequence item" 1 'steps:
  - name: one
    shell: bash
    name: again'

expect "sibling mappings reuse their keys" 0 'inputs:
  a:
    required: false
    default: ""
  b:
    required: false
    default: ""'

expect "sequence items reuse their keys" 0 'steps:
  - name: one
    shell: bash
  - name: two
    shell: bash
    env:
      name: nested'

expect "block scalar text that looks like a key" 0 'runs:
  steps:
    - run: |
        name: not a key
        name: still not a key
      shell: bash'

expect "a duplicate key that ends its line" 1 'permissions:
  contents: read
permissions:
  contents: write'

expect "a new document after ---" 0 'name: a
permissions:
  contents: read
---
name: b
permissions:
  contents: read'

expect "comments and quoted keys" 0 '# name: a comment
"on":
  push: {}
name: x'

if ! bash "$check" "$root/action.yml"; then
  fail "action.yml has a duplicate key, so GitHub refuses to load the action"
fi

if [ "$fails" -gt 0 ]; then
  printf '\n%d duplicate-key problem(s).\n' "$fails" >&2
  exit 1
fi
echo "ok — the duplicate-key check and this repository's action.yml"
