#!/usr/bin/env bash
# Checks that the release-verify step names every problem it finds.
#
# Usage: bash scripts/release-verify_test.sh
#
# The job exists to say what is wrong with a published release, so the thing worth
# holding is how much it says per run. A loop that exits on the first bad asset needs
# three releases to describe three problems, and `set -e` ends the step on a curl
# transport failure before the status it was written to report. Both were reported by
# the reviewer on the first version of this job, and both are about the diagnostic
# rather than the verdict: the step failed either way, which is why only a test on the
# output can tell the difference.
#
# The step is a `run:` block inside release.yml, so this runs that block itself, with
# `gh` and `curl` replaced by stubs on PATH. Cutting a release to find out would be
# the other option, and it is not one.

set -uo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)" || exit 1
workflow=".github/workflows/release.yml"
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

contains() {
  case "$1" in
    *"$2"*) return 0 ;;
    *) return 1 ;;
  esac
}

# The step's own shell: from `run: |` to the first line that leaves the block's
# indentation, with the ten spaces of YAML indentation removed.
step="$(awk '
  /- name: Every binary is attached and downloadable/ { in_step = 1 }
  in_step && /^        run: \|$/ { in_run = 1; next }
  in_run && /^          / { sub(/^          /, ""); print; next }
  in_run && !/^          / { exit }
' "$workflow")"

[ -n "$step" ] || {
  printf 'FAIL: could not read the verify step out of %s\n' "$workflow" >&2
  exit 1
}

contains "$step" "failed=0" || fail "the extracted step is not the one this test is for"

stubs="$(mktemp -d)"
trap 'rm -rf "$stubs"' EXIT

# `gh` answers the existence probe with an object, and each asset probe according to
# STUB_MODE. The asset name is read back out of the --jq expression, which is how the
# step asks for it.
cat >"$stubs/gh" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
for arg in "$@"; do
  [ "$arg" = "--jq" ] && { jq_seen=1; continue; }
  if [ -n "${jq_seen:-}" ]; then expression="$arg"; break; fi
done
if [ -z "${expression:-}" ]; then
  printf '{}\n'
  exit 0
fi
name="$(printf '%s' "$expression" | grep -oE 'cite-[a-z0-9-]+' | head -1)"
case "${STUB_MODE:-good}" in
  good | curl-fails)
    printf '{"url":"https://example.invalid/%s","size":4096,"state":"uploaded"}\n' "$name"
    ;;
  missing-two)
    case "$name" in
      cite-linux-amd64 | cite-darwin-arm64) exit 0 ;;
      *) printf '{"url":"https://example.invalid/%s","size":4096,"state":"uploaded"}\n' "$name" ;;
    esac
    ;;
  empty-file)
    printf '{"url":"https://example.invalid/%s","size":0,"state":"uploaded"}\n' "$name"
    ;;
esac
STUB

# `curl` prints the code on success, and nothing with a non-zero status on a transport
# failure, which is what the real one does.
cat >"$stubs/curl" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
[ "${STUB_MODE:-good}" = "curl-fails" ] && exit 7
printf '200'
STUB

chmod +x "$stubs/gh" "$stubs/curl"

run_step() {
  STUB_MODE="$1" PATH="$stubs:$PATH" GITHUB_REF_NAME=v1.2.3 \
    GITHUB_REPOSITORY=owner/repo GH_TOKEN=stub bash -c "$step" 2>&1
}

# --- a good release passes, and says so for each binary --------------------
out="$(STUB_MODE=good run_step good)"
status=$?
[ "$status" -eq 0 ] || fail "a good release failed the step: $out"
[ "$(printf '%s' "$out" | grep -c 'HTTP 200')" = "3" ] ||
  fail "a good release did not report three assets: $out"

# --- every missing asset is named in one run ------------------------------
out="$(STUB_MODE=missing-two run_step missing-two)"
status=$?
[ "$status" -ne 0 ] || fail "a release missing two binaries passed the step"
contains "$out" "cite-linux-amd64 is not attached" ||
  fail "the first missing asset was not named: $out"
contains "$out" "cite-darwin-arm64 is not attached" ||
  fail "the second missing asset was not named in the same run: $out"
contains "$out" "cite-linux-arm64: HTTP 200" ||
  fail "the asset that was fine was not reported: $out"

# --- an empty asset is reported, and the run continues --------------------
out="$(STUB_MODE=empty-file run_step empty-file)"
status=$?
[ "$status" -ne 0 ] || fail "an empty asset passed the step"
[ "$(printf '%s' "$out" | grep -c 'is empty')" = "3" ] ||
  fail "an empty asset was not named for every binary: $out"

# --- a transport failure keeps the diagnostic ----------------------------
out="$(STUB_MODE=curl-fails run_step curl-fails)"
status=$?
[ "$status" -ne 0 ] || fail "an unfetchable asset passed the step"
[ "$(printf '%s' "$out" | grep -c 'could not be fetched')" = "3" ] ||
  fail "a transport failure did not report every asset: $out"

if [ "$fails" -gt 0 ]; then
  printf '\n%s check(s) failed.\n' "$fails" >&2
  exit 1
fi

printf 'ok - the release verify step names every problem it finds\n'
