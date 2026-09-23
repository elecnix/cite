#!/usr/bin/env bash
# Report a mapping key defined twice in the same YAML mapping.
#
# Usage: bash scripts/yaml-duplicate-keys.sh <file>...
#
# GitHub refuses to load an action.yml with a duplicate key ("'required' is
# already defined"), so every workflow that pins the release fails at "Set up
# job". Python's and Ruby's YAML loaders accept the duplicate without a
# warning, and no CI job loads this repository's action.yml through `uses:`
# before a release, so v0.9.0 was published with one. This check reads the
# block-style YAML that action.yml uses: nested mappings, `- ` sequence items
# and folded or literal block scalars. It doesn't parse flow style.
#
# Exits nonzero if any file has a duplicate key, naming the file, the line and
# the key.

set -uo pipefail

if [ "$#" -eq 0 ]; then
  printf 'usage: %s <file>...\n' "$0" >&2
  exit 2
fi

status=0
for f in "$@"; do
  awk -v file="$f" '
    function forget_deeper(indent,   k, parts) {
      for (k in seen) {
        split(k, parts, SUBSEP)
        if (parts[1] + 0 >= indent) delete seen[k]
      }
    }
    {
      line = $0
      sub(/\r$/, "", line)
      # Inside a block scalar, every blank or more-indented line is text.
      if (block >= 0) {
        if (line ~ /^[ \t]*$/) next
        match(line, /^ */)
        if (RLENGTH > block) next
        block = -1
      }
      # A document separator starts a new document with its own keys.
      if (line ~ /^---([ \t]|$)/) { for (k in seen) delete seen[k]; next }
      if (line ~ /^[ \t]*(#|$)/) next
      match(line, /^ */)
      indent = RLENGTH
      rest = substr(line, indent + 1)
      # A sequence item starts a new mapping: forget the keys of the item
      # before it, then read the key after the dash at its own column.
      while (rest ~ /^- /) {
        forget_deeper(indent + 1)
        rest = substr(rest, 3)
        indent += 2
        match(rest, /^ */)
        indent += RLENGTH
        rest = substr(rest, RLENGTH + 1)
      }
      if (!match(rest, /^("[^"]*"|\047[^\047]*\047|[^:#"\047 ][^:#]*):( |$)/)) next
      key = substr(rest, 1, RLENGTH)
      sub(/:[ ]?$/, "", key)
      sub(/[ \t]+$/, "", key)
      # A key closes every mapping nested deeper than it.
      forget_deeper(indent + 1)
      id = indent SUBSEP key
      if (id in seen) {
        printf "FAIL: %s:%d: key \"%s\" is already defined on line %d\n", file, NR, key, seen[id] > "/dev/stderr"
        dup = 1
      } else {
        seen[id] = NR
      }
      value = substr(rest, RLENGTH + 1)
      if (value ~ /^[ \t]*[>|][-+0-9]*[ \t]*(#.*)?$/) block = indent
    }
    BEGIN { block = -1; dup = 0 }
    END { exit dup }
  ' "$f" || status=1
done
exit "$status"
