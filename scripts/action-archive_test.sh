#!/usr/bin/env bash
# Checks how action.yml names and gates its artifact uploads.
#
# Usage: bash scripts/action-archive_test.sh
#
# upload-artifact v4 refuses a second artifact with a name the run already
# has. A workflow that runs Cite in two jobs of one run (a matrix over
# directories, say) would fail its second job after a green review if the
# names were only run-scoped. Each archive name therefore ends in the per-job
# suffix that the "Name the archives" step writes. The run record keeps its
# `cite-run-record-` prefix and 14-day retention, so recent records can be
# listed by name.
#
# Each check reads only its own block: an input's lines up to the next input,
# a step's lines up to the next step. A check that scanned on would accept a
# later input's default or a later step's field as this one's.

set -uo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)" || exit 1
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

# shellcheck disable=SC2016 # the literal GitHub expression, not a shell expansion
suffix='${{ steps.archive.outputs.suffix }}'

# input_block <file> <name>: the lines of one input, up to the next input or
# the next top-level key.
input_block() {
  awk -v key="  $2:" '
    $0 == key { f = 1; next }
    f && (/^  [^ #]/ || /^[^ #]/) { exit }
    f { print }
  ' "$1"
}

# step_block <file> <step name>: the lines of one step, up to the next step
# or the next top-level key.
step_block() {
  awk -v head="    - name: $2" '
    $0 == head { f = 1; next }
    f && (/^    - / || /^[^ #]/) { exit }
    f { print }
  ' "$1"
}

# check_action <file>: every rule this script holds action.yml to. Returns
# the number of problems it reported.
check_action() {
  local action="$1" before="$fails" first_step namer names line

  first_step="$(grep -n '^    - name:' "$action" | head -1 | cut -d: -f1)"
  namer="$(grep -n '^    - name: Name the archives$' "$action" | cut -d: -f1)"
  if [ -z "$namer" ]; then
    fail "$action: no 'Name the archives' step writes the artifact suffix"
  elif [ "$namer" != "$first_step" ]; then
    fail "$action: 'Name the archives' must be the first step, so a failure in any later step still has a suffix to name its archive"
  fi
  step_block "$action" "Name the archives" | grep -q '^      id: archive$' ||
    fail "$action: the 'Name the archives' step has no id: archive"

  names="$(grep -E '^        name: cite-' "$action")"
  [ -n "$names" ] || fail "$action: no cite- artifact names found"
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    case "$line" in
      *"$suffix") ;;
      *) fail "$action: artifact name is not unique per job: ${line#"${line%%[![:space:]]*}"}" ;;
    esac
  done <<<"$names"

  step_block "$action" "Archive the run record" | grep -q '^        name: cite-run-record-' ||
    fail "$action: the run record artifact lost its cite-run-record- prefix"

  input_block "$action" archive_run_record | grep -q "^    default: 'true'$" ||
    fail "$action: archive_run_record does not default to 'true'"
  step_block "$action" "Archive the run record" | grep -q "^      if: success() && inputs.archive_run_record == 'true'$" ||
    fail "$action: the run record upload ignores archive_run_record"
  step_block "$action" "Archive the run record" | grep -q '^        retention-days: 14$' ||
    fail "$action: the run record is not kept for 14 days"

  return $((fails - before))
}

# The checks must reject an action whose fields sit in the wrong block.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cat > "$work/misplaced.yml" <<'EOF'
inputs:
  archive_run_record:
    description: no default of its own
    required: false
  later_input:
    required: false
    default: 'true'
runs:
  using: composite
  steps:
    - name: Name the archives
      shell: bash
      run: echo suffix=x >> "$GITHUB_OUTPUT"
    - name: Archive the run record
      uses: actions/upload-artifact@v4
      with:
        name: cite-run-record-${{ steps.archive.outputs.suffix }}
    - name: Something else
      id: archive
      if: success() && inputs.archive_run_record == 'true'
      with:
        retention-days: 14
EOF
real_fails="$fails"
check_action "$work/misplaced.yml" 2>/dev/null
misplaced=$?
fails="$real_fails"
# The sample misplaces 4 fields: the default, the id, the if and the retention.
if [ "$misplaced" -ne 4 ]; then
  fail "the checks found $misplaced of the 4 misplaced fields in the sample action"
fi

check_action action.yml

if [ "$fails" -gt 0 ]; then
  printf '\n%d archive problem(s).\n' "$fails" >&2
  exit 1
fi
echo "ok — archive names are unique per job, and the run record upload is on by default for 14 days"
