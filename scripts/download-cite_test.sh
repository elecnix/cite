#!/usr/bin/env bash
# Tests for scripts/download-cite.sh and scripts/version-check.sh.
#
# Usage: bash scripts/download-cite_test.sh
#
# Both scripts exist because of issue #94: the action's `version` input
# defaulted to `latest`, so `uses: elecnix/cite@<full-sha>` — the pin
# docs/release.md asks consumers for — pinned the action and floated the
# binary. These are the assertions that keep the default where it belongs and
# keep the version pins agreeing, and they need no network: curl is stubbed on
# PATH (scripts/download-cite.sh takes CURL for exactly this) and version-check
# reads a fixture root.
#
# Exits nonzero on the first failure.

set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
download="$here/download-cite.sh"
version_check="$here/version-check.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work:?}"' EXIT
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

assert_eq() { # assert_eq <what> <expected> <actual>
  if [ "$2" != "$3" ]; then
    fail "$1: expected '$2', got '$3'"
  fi
}

assert_contains() { # assert_contains <what> <needle> <haystack>
  case "$3" in
    *"$2"*) ;;
    *) fail "$1: '$2' not in: $3" ;;
  esac
}

# The stub stands in for curl: it records the URL it was handed (last
# argument) and its full command line, then creates the file named after -o, so
# a run can be asserted without reaching the network. CURL_FAIL makes it fail
# like curl does on an HTTP error: a partial file, a message, exit 22.
stub="$work/bin/curl"
mkdir -p "$work/bin"
cat > "$stub" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "${!#}" >> "$CURL_LOG"
printf '%s\n' "$*" >> "$CURL_ARGS_LOG"
outfile=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) outfile="${2:-}" ;;
  esac
  shift
done
if [ "${CURL_FAIL:-0}" = "1" ]; then
  [ -n "$outfile" ] && printf 'partial' > "$outfile"
  printf 'curl: (22) The requested URL returned error: 504\n' >&2
  exit 22
fi
[ -n "$outfile" ] && printf 'stub binary\n' > "$outfile"
exit 0
EOF
chmod +x "$stub"

urls="$work/urls.log"
args="$work/args.log"
pathdir="$work/github-path"

# action_path <version-file-content> — a fixture action checkout holding one
# VERSION file, which is all download-cite.sh reads from the action.
action_path() {
  local dir="$work/action-$RANDOM$RANDOM"
  mkdir -p "$dir"
  if [ "$1" != "-" ]; then
    printf '%s' "$1" > "$dir/VERSION"
  fi
  printf '%s' "$dir"
}

# run <version input, '-' for unset> [arch] [action path]
# Prints the script's combined output and exits with the script's status, so a
# caller captures both: out="$(run -)"; status=$?
run() {
  local v="$1" arch="${2:-X64}" ap="${3:-$own_action}"
  : > "$urls"
  : > "$args"
  : > "$pathdir"
  rm -rf "$work/tmp"
  mkdir -p "$work/tmp"
  if [ "$v" = "-" ]; then
    unset CITE_VERSION
  else
    export CITE_VERSION="$v"
  fi
  CURL_LOG="$urls" CURL_ARGS_LOG="$args" GITHUB_ACTION_PATH="$ap" RUNNER_ARCH="$arch" \
    RUNNER_TEMP="$work/tmp" GITHUB_PATH="$pathdir" CURL="$stub" \
    bash "$download" 2>&1
}

own_action="$(action_path "0.8.0")"
tagged="https://github.com/elecnix/cite/releases/download"

# --- the default: this action's own release, never `latest` -----------------
# The regression #94 reports: a full-SHA pin on the action used to download
# whatever release shipped last.
out="$(run -)"; status=$?
assert_eq "unset version exit status" 0 "$status"
assert_eq "unset version downloads the action's own release" \
  "$tagged/v0.8.0/cite-linux-amd64" "$(cat "$urls")"
case "$(cat "$urls")" in
  *releases/latest*)
    fail "unset version floated to releases/latest: $(cat "$urls")"
    ;;
esac
assert_contains "unset version is logged with the version it resolved" "downloading v0.8.0" "$out"
assert_eq "the install directory is exported on GITHUB_PATH" "$work/tmp/cite-bin" "$(cat "$pathdir")"
if [ ! -x "$work/tmp/cite-bin/cite" ]; then
  fail "the downloaded binary is not executable"
fi

# arm64 is a different asset name, not a different version.
out="$(run - ARM64)"; status=$?
assert_eq "arm64 asset" "$tagged/v0.8.0/cite-linux-arm64" "$(cat "$urls")"

# --- an explicit version wins ----------------------------------------------
out="$(run v0.1.3)"; status=$?
assert_eq "explicit version" "$tagged/v0.1.3/cite-linux-amd64" "$(cat "$urls")"

# --- `latest` stays available as the deliberate float -----------------------
out="$(run latest)"; status=$?
assert_eq "latest is the documented float" \
  "https://github.com/elecnix/cite/releases/latest/download/cite-linux-amd64" "$(cat "$urls")"

# --- the retry window: minutes, not seconds --------------------------------
# The failure that motivated it was four fast 504s inside about seven seconds,
# which is the whole window `--retry 3` covered (elecnix/gridkeeper run
# 35618280194). These assertions are that window, so a later edit cannot shrink
# it back to seconds without a red test.
out="$(run -)"; status=$?
assert_eq "a stubbed download succeeds" 0 "$status"
assert_contains "the retry count" "--retry 6" "$(cat "$args")"
assert_contains "the retry spacing" "--retry-delay 15" "$(cat "$args")"
assert_contains "a total time budget" "--retry-max-time 180" "$(cat "$args")"
assert_contains "a connect timeout" "--connect-timeout 10" "$(cat "$args")"

# Each knob is overridable: this is wall-clock time every job pays.
export CITE_DOWNLOAD_ATTEMPTS=2 CITE_DOWNLOAD_BACKOFF=1 CITE_DOWNLOAD_MAX_TIME=30 CITE_DOWNLOAD_CONNECT_TIMEOUT=3
out="$(run -)"; status=$?
unset CITE_DOWNLOAD_ATTEMPTS CITE_DOWNLOAD_BACKOFF CITE_DOWNLOAD_MAX_TIME CITE_DOWNLOAD_CONNECT_TIMEOUT
assert_eq "an overridden window still downloads" 0 "$status"
assert_contains "an overridden retry count" "--retry 2" "$(cat "$args")"
assert_contains "an overridden retry spacing" "--retry-delay 1" "$(cat "$args")"
assert_contains "an overridden time budget" "--retry-max-time 30" "$(cat "$args")"
assert_contains "an overridden connect timeout" "--connect-timeout 3" "$(cat "$args")"

# --- a failed download: named, cleaned up, and off GITHUB_PATH --------------
# A failed fetch must say which release and URL failed, pass curl's own message
# through, leave no truncated binary for a later step to execute, and not put a
# directory on GITHUB_PATH as if it had installed something.
export CURL_FAIL=1
out="$(run -)"; status=$?
unset CURL_FAIL
if [ "$status" -eq 0 ]; then
  fail "a failing download was reported as success"
fi
assert_contains "a failure names the release and the window" \
  "download failed for v0.8.0 after 6 retries over up to 180s" "$out"
assert_contains "a failure names the URL" \
  "releases/download/v0.8.0/cite-linux-amd64" "$out"
assert_contains "a failure passes curl's message through" "error: 504" "$out"
if [ -e "$work/tmp/cite-bin/cite" ]; then
  fail "a failed download left a truncated binary behind"
fi
if [ -s "$pathdir" ]; then
  fail "a failed download still exported a directory on GITHUB_PATH"
fi

# --- refusals: loud, before any request, naming the accepted shapes ---------
# Each of these used to build a URL that 404s (or, worse, silently downloaded a
# different release), which surfaces as an opaque curl exit 22.
out="$(run 0.8.0)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "version '0.8.0' without the v was accepted"
fi
assert_contains "a version without the v says what to pass" "not a release tag" "$out"
if [ -s "$urls" ]; then
  fail "a rejected version still made a request: $(cat "$urls")"
fi

out="$(run v0.8)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "version 'v0.8' was accepted"
fi

out="$(run - IA32)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "unsupported runner arch was accepted"
fi
assert_contains "unsupported arch is named" "unsupported runner arch: IA32" "$out"

# A missing or malformed VERSION must not fall back to `latest`: that would
# reintroduce the float in the one case where the action cannot tell which
# release it is.
out="$(run - X64 "$(action_path -)")"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a missing VERSION file was accepted"
fi
assert_contains "a missing VERSION file says so" "VERSION is missing" "$out"
if [ -s "$urls" ]; then
  fail "a missing VERSION still made a request: $(cat "$urls")"
fi

out="$(run - X64 "$(action_path "v0.8.0")")"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a v-prefixed VERSION file was accepted"
fi
assert_contains "a v-prefixed VERSION file says what it should hold" "not X.Y.Z" "$out"

out="$(run - X64 "$(action_path "0.8")")"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a malformed VERSION file was accepted"
fi

# --- version-check: the tag, VERSION and the binary's constant must agree ---
# fixture <version-file-content or '-'> <const version in main.go or '-'>
fixture() {
  local dir="$work/repo-$RANDOM$RANDOM"
  mkdir -p "$dir/cmd/cite"
  if [ "$1" != "-" ]; then
    printf '%s' "$1" > "$dir/VERSION"
  fi
  if [ "$2" != "-" ]; then
    printf 'package main\n\nconst version = "%s"\n' "$2" > "$dir/cmd/cite/main.go"
  fi
  printf '%s' "$dir"
}

# The repository's own pins name whatever release VERSION holds, so a
# version bump doesn't have to edit this test.
repo_version="$(tr -d '[:space:]' < "$root/VERSION")"

out="$(bash "$version_check" "v$repo_version" "$root" 2>&1)"; status=$?
assert_eq "this repository's pins agree" 0 "$status"
assert_contains "the agreement is reported" "all name $repo_version" "$out"

out="$(bash "$version_check" "" "$root" 2>&1)"; status=$?
assert_eq "no expected tag is not an error" 0 "$status"

out="$(bash "$version_check" v0.0.1 "$root" 2>&1)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a tag disagreeing with VERSION was accepted"
fi
assert_contains "a disagreeing tag names both values" "does not match VERSION v$repo_version" "$out"

out="$(bash "$version_check" v0.8.0 "$(fixture 0.9.0 0.9.0)" 2>&1)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a tree whose pins disagree with the tag was accepted"
fi
assert_contains "a disagreeing tag is compared against the tree" "does not match VERSION v0.9.0" "$out"

out="$(bash "$version_check" "" "$(fixture 0.8.0 0.7.0)" 2>&1)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a VERSION/main.go mismatch was accepted"
fi
assert_contains "a VERSION/main.go mismatch names both" "main.go says 0.7.0, VERSION says 0.8.0" "$out"

out="$(bash "$version_check" "" "$(fixture - 0.8.0)" 2>&1)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a missing VERSION file was accepted"
fi
assert_contains "a missing VERSION file is named" "VERSION is missing" "$out"

out="$(bash "$version_check" "" "$(fixture v0.8.0 0.8.0)" 2>&1)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a v-prefixed VERSION file was accepted"
fi
assert_contains "a v-prefixed VERSION file is named" "the tag carries the v" "$out"

out="$(bash "$version_check" "" "$(fixture abc 0.8.0)" 2>&1)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a non-version VERSION file was accepted"
fi

out="$(bash "$version_check" "" "$(fixture 0.8.0 -)" 2>&1)"; status=$?
if [ "$status" -eq 0 ]; then
  fail "a missing cmd/cite/main.go was accepted"
fi

if [ "$fails" -gt 0 ]; then
  printf '\n%s download/version problem(s).\n' "$fails" >&2
  exit 1
fi

printf 'ok — the version default, the refusals, the retry window, and the three version pins\n'
