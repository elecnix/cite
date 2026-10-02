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

# check_capture <file>: every rule the opt-in wire capture is held to. It is
# off by default, and a default the action cannot see is a feature nobody
# opted into deliberately: the input's own default and the step's own gate are
# checked separately, so a gate that reads a different input, or a step that
# ignores the input, is caught rather than assumed consistent.
check_capture() {
  local action="$1" retention

  input_block "$action" capture_wire | grep -q "^    default: 'false'$" ||
    fail "$action: capture_wire does not default to 'false' (a capture holds the prompt, so it is opt-in)"
  step_block "$action" "Archive the wire capture" | grep -q "^      if: always() && inputs.capture_wire == 'true'$" ||
    fail "$action: the wire upload ignores capture_wire"
  step_block "$action" "Archive the wire capture" | grep -q '^        name: cite-wire-' ||
    fail "$action: the wire upload lost its cite-wire- prefix"
  step_block "$action" "Archive the wire capture" | grep -q '^        path: ${{ runner.temp }}/cite-wire/$' ||
    fail "$action: the wire upload does not point at the capture directory CITE_CAPTURE_DIR names"
  # A capture carries the prompt, so its retention is held below the 14 days
  # the run record keeps and well under the 30 of the failure archive.
  retention="$(step_block "$action" "Archive the wire capture" | sed -n 's/^        retention-days: //p')"
  # The bound is checked as a bound, not as a string: `[ x -gt 7 ]` on a
  # value that is not a number exits non-zero with a diagnostic on stderr,
  # which a silenced check would read as "compliant". An unparseable value
  # is its own failure, then the bound applies to a real number.
  # 0 is not a short retention: upload-artifact reads it as "use the
  # repository default", which is however many days the repository happens
  # to set. The bound is therefore 1 to 7 days, both ends checked.
  if ! printf '%s' "$retention" | grep -qE '^[0-9]+$'; then
    fail "$action: the wire capture retention-days is not a number: '${retention:-none}'"
  elif [ "$retention" -lt 1 ]; then
    fail "$action: the wire capture retention-days is $retention, which means the repository default rather than a short retention"
  elif [ "$retention" -gt 7 ]; then
    fail "$action: the wire capture is kept for $retention days, want between 1 and 7"
  fi
}

# check_action <file>: every rule this script holds action.yml to. It reports
# each problem through fail(), which owns the verdict, and it EXITS WITH THE
# NUMBER OF PROBLEMS it found, which is what the sample action below asserts
# on. The two are separate on purpose: the sample needs the count, and the
# script needs the counter.
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

  # check_capture adds to the same global counter the sample-action assertion
  # reads, so its problems reach the verdict without a return value of its
  # own to be dropped.
  check_capture "$action"

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
  capture_wire:
    description: the default sits in a later input
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
    - name: Archive the wire capture
      uses: actions/upload-artifact@v4
      with:
        name: cite-wire-x
        path: ${{ runner.temp }}/elsewhere/
        retention-days: 0
    - name: Something else
      id: archive
      if: success() && inputs.archive_run_record == 'true'
      with:
        retention-days: 14
EOF
real_fails="$fails"
# $? is check_action's exit status, which is its problem count.
check_action "$work/misplaced.yml" 2>/dev/null
misplaced=$?
fails="$real_fails"
# The sample misplaces 9 fields: four of the run record's (the default, the
# id, the if and the retention) and five of the capture's (the input default,
# the gate, the name suffix, the upload path, and a retention of 0 where the
# bound is 1 to 7 days). Changing 9 here must fail this script, which is how
# the count stays honest as checks are added.
if [ "$misplaced" -ne 9 ]; then
  fail "the checks found $misplaced of the 9 misplaced fields in the sample action"
fi

check_action action.yml

if [ "$fails" -gt 0 ]; then
  printf '\n%d archive problem(s).\n' "$fails" >&2
  exit 1
fi
echo "ok: archive names are unique per job, the run record upload is on by default for 14 days, the wire capture is off by default, gated on its own input, and kept for 7 days or less"
