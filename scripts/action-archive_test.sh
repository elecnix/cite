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

set -uo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)" || exit 1
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

action="action.yml"
# shellcheck disable=SC2016 # the literal GitHub expression, not a shell expansion
suffix='${{ steps.archive.outputs.suffix }}'

# Line of the first `- name:` step, and of the step that writes the suffix.
first_step="$(grep -n '^    - name:' "$action" | head -1 | cut -d: -f1)"
namer="$(grep -n '^    - name: Name the archives$' "$action" | cut -d: -f1)"
if [ -z "$namer" ]; then
  fail "no 'Name the archives' step writes the artifact suffix"
elif [ "$namer" != "$first_step" ]; then
  fail "'Name the archives' must be the first step, so a failure in any later step still has a suffix to name its archive"
fi
grep -q '^      id: archive$' "$action" || fail "the 'Name the archives' step has no id: archive"

# Every artifact name ends in the per-job suffix.
names="$(grep -E '^        name: cite-' "$action")"
[ -n "$names" ] || fail "no cite- artifact names found"
while IFS= read -r line; do
  [ -z "$line" ] && continue
  case "$line" in
    *"$suffix") ;;
    *) fail "artifact name is not unique per job: ${line#"${line%%[![:space:]]*}"}" ;;
  esac
done <<<"$names"

grep -q "^        name: cite-run-record-" "$action" || fail "the run record artifact lost its cite-run-record- prefix"

# The run record upload is on by default, can be turned off, and keeps 14 days.
grep -q '^  archive_run_record:$' "$action" || fail "no archive_run_record input"
awk '/^  archive_run_record:$/{f=1} f&&/^    default:/{print; exit}' "$action" | grep -q "'true'" ||
  fail "archive_run_record does not default to 'true'"
grep -q "if: success() && inputs.archive_run_record == 'true'" "$action" ||
  fail "the run record upload ignores archive_run_record"
awk '/name: Archive the run record/{f=1} f&&/retention-days:/{print; exit}' "$action" | grep -q 'retention-days: 14' ||
  fail "the run record is not kept for 14 days"

if [ "$fails" -gt 0 ]; then
  printf '\n%d archive problem(s).\n' "$fails" >&2
  exit 1
fi
echo "ok — archive names are unique per job, and the run record upload is on by default for 14 days"
