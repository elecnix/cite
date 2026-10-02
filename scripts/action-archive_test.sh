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
# It also holds the forensics archive's file list to the code that writes
# those files. Each entry under $RUNNER_TEMP is produced either by Cite,
# through a CITE_*_OUT variable the review step exports, or by the `tee`
# that keeps the step log. Those three lists drifted apart once: the action
# exported CITE_TRUNCATED_OUT at $RUNNER_TEMP while the only writer still
# fell back to a hardcoded /tmp path, so the archive advertised a file that
# no run wrote.
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
  if ! printf '%s' "$retention" | grep -qE '^[0-9]+$'; then
    fail "$action: the wire capture retention-days is not a number: '${retention:-none}'"
  elif [ "$retention" -gt 7 ]; then
    fail "$action: the wire capture is kept for $retention days, want at most 7"
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

# upload_paths <file>: every non-blank path an upload step lists. Used to
# notice a path this script cannot read, rather than skipping it silently.
upload_paths() {
  awk '
    /^    - name:/ { step = $0; inupload = 0 }
    /uses: actions\/upload-artifact/ { inupload = 1 }
    inupload && /path:/ { inpath = 1; sub(/^[[:space:]]*path:[[:space:]]*/, "")
      # `path: |` and `path: >` are block scalars: the paths are on the lines
      # that follow, so the indicator itself is not one.
      if (length($0) && $0 != "|" && $0 != ">") print
      next }
    inpath {
      if ($0 ~ /^[[:space:]]*$/) { next }
      if ($0 ~ /^        [a-zA-Z-]+:/) { inpath = 0; next }
      if ($0 ~ /^    /) { inpath = 0; next }
      print
    }
  ' "$1"
}

# upload_names <file>: the basenames every upload step lists under
# runner.temp, sorted and unique, from both a single path and a block list.
upload_names() {
  grep -oE '\$\{\{[[:space:]]*runner\.temp[[:space:]]*\}\}/[^[:space:]]+' "$1" |
    sed 's#.*/##' | sort -u
}

# captures <file>: the CITE_* variables a step points at $RUNNER_TEMP, as
# "VAR path" pairs. The path may sit in a subdirectory; the archive list
# names the file, so both sides are reduced to a basename before they are
# compared. Comparing the raw pair made a capture under $RUNNER_TEMP/sub
# look unarchived, because the archive side had already dropped the prefix.
# shellcheck disable=SC2016 # $RUNNER_TEMP is matched literally, not expanded
captures() {
  sed -nE 's/^[[:space:]]*(CITE_[A-Z_]+)="\$RUNNER_TEMP\/([^"]+)"$/\1 \2/p' "$1"
}

# capture_names <file>: the file names alone, as basenames.
capture_names() {
  captures "$1" | awk '{ n = $2; sub(/.*\//, "", n); print n }'
}

# tee_pattern <VAR>: the ERE matching a tee that writes that capture, with or
# without flags such as -a. Matching only `tee "` counted one writer in
# `tee -a "$X" | tee "$X"` and let a capture be written twice unnoticed.
tee_pattern() {
  printf 'tee[[:space:]]+(-[a-zA-Z]+[[:space:]]+)*"\\$%s"' "$1"
}

# check_captures <action file> <go root>: the one-writer rule. Every archived
# file has a producer, every producer reaches exactly one writer, every
# writer's path comes from the exported variable rather than a literal, and
# every export is archived. Returns the number of problems it reported.
check_captures() {
  local action="$1" goroot="$2" before="$fails" gofiles names var path name writers

  gofiles="$(find "$goroot" -name '*.go' ! -name '*_test.go' | sort)"
  names="$(upload_names "$action")"

  # An upload path this script cannot read is not the same as an upload path
  # that does not exist. A bare path, or one built from a step output, would
  # contribute no names and leave the archive side silently empty, so every
  # drift below would be compared against a list that cannot fail.
  local unreadable
  unreadable="$(upload_paths "$action" |
    grep -vE '\$\{\{[[:space:]]*runner\.temp[[:space:]]*\}\}' | sort -u)"
  if [ -n "$unreadable" ]; then
    fail "$action: an upload path is not under \${{ runner.temp }}, so this check cannot verify it: ${unreadable//$'\n'/; }"
  fi

  # A Go file that bakes the runner's temp directory into the binary has the
  # path in hand at runtime: either it reads the variable with os.Getenv, or
  # it builds a path from a literal that starts with it. That is the drift
  # the export exists to prevent.
  # Matching the bare word reported prose as a baked path — a comment, a doc
  # string, a constant that merely names the variable — and the failure
  # message then blamed the binary for a sentence in a comment.
  local hardcoded
  # shellcheck disable=SC2086 # $gofiles is a deliberate word list
  hardcoded="$(awk '
    /^[[:space:]]*(\/\/|\*)/ { next }
    /os\.Getenv\(["'"'"'`]RUNNER_TEMP["'"'"'`]\)/ { print FILENAME; next }
    /["'"'"'`][[:space:]]*\$\{?RUNNER_TEMP\}?[[:space:]]*\// { print FILENAME }
  ' $gofiles | sort -u)"
  if [ -n "$hardcoded" ]; then
    fail "${hardcoded//$'\n'/, }: bakes the runner temp directory into the binary; the capture path must arrive through the exported variable"
  fi

  while IFS=' ' read -r var path; do
    [ -z "$var" ] && continue
    # Both sides name the file, not the path it sits at.
    name="${path##*/}"
    if ! grep -qxF "$name" <<<"$names"; then
      fail "$action: $var writes $name but no upload step archives it"
    fi
    if ! grep -qE "^[[:space:]]*export ${var}_OUT=" "$action" &&
       ! grep -qE "$(tee_pattern "$var")" "$action"; then
      fail "$action: $var names $name but is neither exported nor tee'd, so nothing writes it"
      continue
    fi
    # Every producer must reach exactly one writer. An exported capture is
    # written by the Go site that reads it, so that site must be unique; a
    # tee'd capture is written by the tee in the step, so the tee must be.
    # Checking only the exported case let a capture that was tee'd and read
    # twice through Go pass without ever being counted.
    local writers
    if grep -qE "^[[:space:]]*export ${var}_OUT=" "$action"; then
      # shellcheck disable=SC2086 # $gofiles is a deliberate word list
      writers="$(grep -hoF "os.Getenv(\"${var}_OUT\")" $gofiles 2>/dev/null | wc -l | tr -d ' ')"
      if [ "$writers" -ne 1 ]; then
        fail "$action: ${var}_OUT is exported but $writers sites read it; exactly one may claim a capture"
      fi
    else
      # Count occurrences, not lines: `tee -a "$X" | tee "$X"` writes the
      # capture twice on one line, and grep -c called that one writer.
      writers="$(grep -hoE "$(tee_pattern "$var")" "$action" | wc -l | tr -d ' ')"
      if [ "$writers" -ne 1 ]; then
        fail "$action: $var is tee'd $writers times; exactly one may write $name"
      fi
    fi
  done < <(captures "$action")

  while IFS= read -r name; do
    [ -z "$name" ] && continue
    grep -qxF "$name" <(capture_names "$action") ||
      fail "$action: the archive lists $name, which no step writes"
  done <<<"$names"

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
        retention-days: 7d
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
# the gate, the name suffix, the upload path, and a retention of `7d` where an
# integer bound applies). Changing 9 here must fail this script, which is how
# the count stays honest as checks are added.
if [ "$misplaced" -ne 9 ]; then
  fail "the checks found $misplaced of the 9 misplaced fields in the sample action"
fi

# The capture checks must reject a workflow whose archive list, its exports
# and its writers have drifted apart.
mkdir -p "$work/drift/cmd/cite" "$work/drift/internal/model"
cat > "$work/drift.yml" <<'EOF'
runs:
  steps:
    - name: Run Cite review
      run: |
        CITE_LOG="$RUNNER_TEMP/cite-output.log"
        CITE_RECORD="$RUNNER_TEMP/cite-run-record.json"
        export CITE_RECORD_OUT="$CITE_RECORD"
        CITE_TRUNCATED="$RUNNER_TEMP/cite-truncated-response.json"
        export CITE_TRUNCATED_OUT="$CITE_TRUNCATED"
        CITE_SECRET="$RUNNER_TEMP/cite-secret.json"
        export CITE_SECRET_OUT="$CITE_SECRET"
        CITE_UNUSED="$RUNNER_TEMP/cite-unused.json"
        cite review $ARGS 2>&1 | tee "$CITE_LOG"
    - name: Archive forensics on failure
      with:
        path: |
          ${{ runner.temp }}/cite-output.log
          ${{ runner.temp }}/cite-run-record.json
          ${{ runner.temp }}/cite-truncated-response.json
          ${{ runner.temp }}/cite-ghost.json
EOF
cat > "$work/drift/cmd/cite/record.go" <<'EOF'
package cite

import "os"

func record() {
	_ = os.Getenv("CITE_RECORD_OUT")
	_ = os.Getenv("CITE_SECRET_OUT")
}
EOF
cat > "$work/drift/internal/model/capture.go" <<'EOF'
package model

import "os"

func capture() {
	_ = os.Getenv("CITE_TRUNCATED_OUT")
	_ = os.Getenv("CITE_TRUNCATED_OUT")
}

// The workflow's own directory, resolved inside the binary.
var baked = os.Getenv("RUNNER_TEMP") + "/cite-truncated-response.json"
EOF
real_fails="$fails"
check_captures "$work/drift.yml" "$work/drift" 2>/dev/null
drift=$?
fails="$real_fails"
# The sample drifts five ways and the checks must catch every one: an
# archived file nothing writes, a capture no upload step archives, a
# variable that is neither exported nor tee'd (unarchived as well, so that
# drift reports twice), two sites reading one exported path, and a capture
# path baked into the binary.
if [ "$drift" -ne 6 ]; then
  fail "the capture checks found $drift of the 6 reported drifts in the sample action"
fi

# Each rule added after the reviewer reported it gets a fixture, because a
# check that cannot fire is worse than no check at all.
mkdir -p "$work/shapes/cmd/cite" "$work/shapes/internal/model"

# A capture may sit in a subdirectory. The archive side has always named the
# file rather than the path, so comparing the raw pair reported a correctly
# archived capture as unarchived.
cat > "$work/subdir.yml" <<'EOF'
runs:
  steps:
    - name: Run Cite review
      run: |
        CITE_LOG="$RUNNER_TEMP/sub/cite-output.log"
        export CITE_LOG_OUT="$CITE_LOG"
    - name: Archive forensics
      uses: actions/upload-artifact@v4
      with:
        path: ${{ runner.temp }}/sub/cite-output.log
EOF
cat > "$work/shapes/cmd/cite/record.go" <<'EOF'
package cite

import "os"

func record() { _ = os.Getenv("CITE_LOG_OUT") }
EOF
real_fails="$fails"
check_captures "$work/subdir.yml" "$work/shapes" 2>/dev/null
subdir=$?
fails="$real_fails"
if [ "$subdir" -ne 0 ]; then
  fail "a capture under a subdirectory of the runner temp dir, archived at the same path, reported $subdir problem(s); want 0"
fi

# A name in a Go comment is not a baked path, and a line with no string on it
# is not either. Both used to be reported as a path compiled into the binary.
cat > "$work/prose.yml" <<'EOF'
runs:
  steps:
    - name: Run Cite review
      run: |
        CITE_LOG="$RUNNER_TEMP/cite-output.log"
        export CITE_LOG_OUT="$CITE_LOG"
    - name: Archive forensics
      uses: actions/upload-artifact@v4
      with:
        path: ${{ runner.temp }}/cite-output.log
EOF
cat > "$work/shapes/cmd/cite/prose.go" <<'EOF'
package cite

// RUNNER_TEMP is where the runner puts its temporary directory; the capture
// path must arrive through the exported variable rather than from here.
const note = "RUNNER_TEMP is documented by GitHub, not read by Cite"

// runnerTempDir names no path: the mention is in a comment, not a literal.
var runnerTempDir = "unused"
EOF
real_fails="$fails"
check_captures "$work/prose.yml" "$work/shapes" 2>/dev/null
prose=$?
fails="$real_fails"
if [ "$prose" -ne 0 ]; then
  fail "a Go file mentioning RUNNER_TEMP only in prose reported $prose problem(s); want 0"
fi

# An upload path this script cannot read is a hole, not a pass: it would leave
# the archive side empty and every comparison below it unable to fail.
cat > "$work/unreadable.yml" <<'EOF'
runs:
  steps:
    - name: Run Cite review
      run: |
        CITE_LOG="$RUNNER_TEMP/cite-output.log"
        export CITE_LOG_OUT="$CITE_LOG"
    - name: Archive the log
      uses: actions/upload-artifact@v4
      with:
        path: ${{ runner.temp }}/cite-output.log
    - name: Archive something else
      uses: actions/upload-artifact@v4
      with:
        path: ${{ steps.build.outputs.dir }}/forensics
EOF
real_fails="$fails"
check_captures "$work/unreadable.yml" "$work/shapes" 2>/dev/null
unreadable=$?
fails="$real_fails"
if [ "$unreadable" -ne 1 ]; then
  fail "an upload path outside the runner temp dir reported $unreadable problem(s); want exactly 1"
fi

# A tee'd capture is written by the tee, so the tee is what must be unique.
# Counting only exported captures let a tee'd one be written twice unnoticed.
cat > "$work/tee.yml" <<'EOF'
runs:
  steps:
    - name: Run Cite review
      run: |
        CITE_LOG="$RUNNER_TEMP/cite-output.log"
        cite review $ARGS 2>&1 | tee -a "$CITE_LOG" | tee "$CITE_LOG"
    - name: Archive forensics
      uses: actions/upload-artifact@v4
      with:
        path: ${{ runner.temp }}/cite-output.log
EOF
real_fails="$fails"
check_captures "$work/tee.yml" "$work/shapes" 2>/dev/null
tee2=$?
fails="$real_fails"
if [ "$tee2" -ne 1 ]; then
  fail "a capture tee'd twice reported $tee2 problem(s); want exactly 1"
fi

check_action action.yml
check_captures action.yml .

if [ "$fails" -gt 0 ]; then
  printf '\n%d archive problem(s).\n' "$fails" >&2
  exit 1
fi
echo "ok — archive names are unique per job, the run record upload is on by default for 14 days, every archived file has exactly one writer, and the wire capture is off by default, gated on its own input, and kept for 7 days or less"
