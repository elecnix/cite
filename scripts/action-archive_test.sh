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

# upload_names <file>: the basenames every upload step lists under
# runner.temp, sorted and unique, from both a single path and a block list.
upload_names() {
  grep -oE '\$\{\{[[:space:]]*runner\.temp[[:space:]]*\}\}/[^[:space:]]+' "$1" |
    sed 's#.*/##' | sort -u
}

# captures <file>: the CITE_* variables a step points at $RUNNER_TEMP, as
# "VAR name" pairs.
# shellcheck disable=SC2016 # $RUNNER_TEMP is matched literally, not expanded
captures() {
  sed -nE 's/^[[:space:]]*(CITE_[A-Z_]+)="\$RUNNER_TEMP\/([^"]+)"$/\1 \2/p' "$1"
}

# capture_names <file>: the file names alone.
capture_names() {
  captures "$1" | awk '{ print $2 }'
}

# check_captures <action file> <go root>: the one-writer rule. Every archived
# file has a producer, every producer reaches exactly one writer, every
# writer's path comes from the exported variable rather than a literal, and
# every export is archived. Returns the number of problems it reported.
check_captures() {
  local action="$1" goroot="$2" before="$fails" gofiles names var name readers

  gofiles="$(find "$goroot" -name '*.go' ! -name '*_test.go' | sort)"
  names="$(upload_names "$action")"

  # A Go file that knows about the runner's temp directory has the path baked
  # into the binary: the workflow would no longer be able to say where a
  # capture goes. That is the drift the export exists to prevent.
  local hardcoded
  # shellcheck disable=SC2086 # $gofiles is a deliberate word list
  hardcoded="$(grep -lE 'RUNNER_TEMP|runner\.temp' $gofiles 2>/dev/null)"
  if [ -n "$hardcoded" ]; then
    fail "${hardcoded//$'\n'/, }: bakes the runner temp directory into the binary; the capture path must arrive through the exported variable"
  fi

  while IFS=' ' read -r var name; do
    [ -z "$var" ] && continue
    if ! grep -qxF "$name" <<<"$names"; then
      fail "$action: $var writes $name but no upload step archives it"
    fi
    if ! grep -qE "^[[:space:]]*export ${var}_OUT=" "$action" &&
       ! grep -qE "\\btee \"\\\$${var}\"" "$action"; then
      fail "$action: $var names $name but is neither exported nor tee'd, so nothing writes it"
      continue
    fi
    grep -qE "^[[:space:]]*export ${var}_OUT=" "$action" || continue
    # shellcheck disable=SC2086 # $gofiles is a deliberate word list
    readers="$(grep -hoF "os.Getenv(\"${var}_OUT\")" $gofiles 2>/dev/null | wc -l | tr -d ' ')"
    if [ "$readers" -ne 1 ]; then
      fail "$action: ${var}_OUT is exported but $readers sites read it; exactly one may claim a capture"
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

# The capture checks must reject a workflow whose archive list, its exports
# and its writers have drifted apart.
mkdir -p "$work/drift/cmd/cite" "$work/drift/internal/model"
cat > "$work/drift.yml" <<'EOF'
runs:
  using: composite
  steps:
    - name: Run Cite review
      shell: bash
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
      uses: actions/upload-artifact@v4
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

func record() { _ = os.Getenv("CITE_RECORD_OUT") }
EOF
cat > "$work/drift/internal/model/secret.go" <<'EOF'
package model

import "os"

func secret() { _ = os.Getenv("CITE_SECRET_OUT") }
EOF
cat > "$work/drift/internal/model/capture.go" <<'EOF'
package model

import "os"

func capture() { _ = os.Getenv("CITE_TRUNCATED_OUT") }

func second() { _ = os.Getenv("CITE_TRUNCATED_OUT") }
EOF
cat > "$work/drift/internal/model/baked.go" <<'EOF'
package model

import "os"

// The workflow's own directory, resolved inside the binary: the capture path
// can no longer be pointed somewhere the archive step looks.
var capture = os.Getenv("RUNNER_TEMP") + "/cite-truncated-response.json"
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

check_action action.yml
check_captures action.yml .

if [ "$fails" -gt 0 ]; then
  printf '\n%d archive problem(s).\n' "$fails" >&2
  exit 1
fi
echo "ok — archive names are unique per job, the run record upload is on by default for 14 days, and every archived file has exactly one writer"
