#!/usr/bin/env bash
# Tests for scripts/fetch-config.sh, with a stubbed `gh` on PATH.
#
# Usage: bash scripts/fetch-config_test.sh
# Exits nonzero on the first failure (set -e through the assert helper).

set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="$here/fetch-config.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work:?}"' EXIT
fails=0

build_flaky_stub() {
  local nfail="$1" err="$2" out="$3"
  mkdir -p "$work/bin"
  cat > "$work/bin/gh" <<EOF
#!/usr/bin/env bash
# The first $nfail calls fail with the stub's exit code and message;
# later calls succeed with the stub payload.
printf '%s\n' "\$*" >> "$work/gh-calls.log"
count="\$(cat "$work/call-count" 2>/dev/null || echo 0)"
count="\$((count+1))"
printf '%s' "\$count" > "$work/call-count"
if [ "\$count" -le $nfail ]; then
  printf '%s' '$err' >&2
  exit 1
fi
printf '%s' '$out'
exit 0
EOF
  chmod +x "$work/bin/gh"
}

# build_stub <exit-code> <stderr-text> <stdout-text> — creates $work/bin/gh
build_stub() {
  local code="$1" err="$2" out="$3"
  mkdir -p "$work/bin"
  cat > "$work/bin/gh" <<EOF
#!/usr/bin/env bash
# Records the invoked URL for assertion, then emulates the given outcome.
printf '%s\n' "\$*" >> "$work/gh-calls.log"
printf '%s' '$err' >&2
printf '%s' '$out'
exit $code
EOF
  chmod +x "$work/bin/gh"
}

# run_case <name> <expected-exit> [setup-command] — runs the script in a
# scratch dir; the optional setup runs inside the scratch dir beforehand.
run_case() {
  local name="$1" want="$2" setup="${3:-}"
  local dir; dir="$(mktemp -d "$work/case.XXXXXX")"
  (
    export CONFIG_PATH=".github/cite.yml"
    export GITHUB_REPOSITORY="elecnix/cite"
    export GITHUB_BASE_REF="main"
    export GH="$work/bin/gh"
    export PATH="$work/bin:$PATH"
    cd "$dir" || exit 1
    [ -n "$setup" ] && eval "$setup"
    bash "$script"
  ) >"$dir/.stdout" 2>"$dir/.stderr"
  local got=$?
  if [ "$got" -ne "$want" ]; then
    echo "FAIL $name: exit $got, want $want"; cat "$dir/.stderr"; fails=$((fails+1))
  fi
  LAST_CASE_DIR="$dir"
}

expect_file() { # expect_file <present-under-last-case: yes|no>
  local f="$LAST_CASE_DIR/.github/cite.yml"
  if [ "$1" = yes ] && [ ! -s "$f" ]; then
    echo "FAIL: expected non-empty config at $f"; fails=$((fails+1))
  elif [ "$1" = no ] && [ -e "$f" ]; then
    echo "FAIL: expected no config file at $f"; fails=$((fails+1))
  fi
}

expect_log() { # expect_log <substring>
  if ! grep -qF "$1" "$LAST_CASE_DIR/.stdout" "$LAST_CASE_DIR/.stderr" 2>/dev/null; then
    echo "FAIL: log line not found: $1"; fails=$((fails+1))
  fi
}

expect_no_tmp() {
  local f="$LAST_CASE_DIR/.github/cite.yml.tmp"
  if [ -e "$f" ]; then echo "FAIL: leftover temp file $f"; fails=$((fails+1)); fi
}

# 1. File present locally WITH a base ref: still fetched from the base
# ref — on pull_request a checkout step gives the merge ref, so a local
# .github/cite.yml can be head-controlled and must not be trusted. The
# base-ref content wins and overwrites it.
build_stub 0 '' "$(printf 'model: from-base\\n' | base64)"
run_case "local file never overrides base ref" 0 "mkdir -p .github && printf 'model: attacker\\n' > .github/cite.yml"
if ! grep -q 'model: from-base' "$LAST_CASE_DIR/.github/cite.yml" 2>/dev/null; then
  echo "FAIL: local head-controlled config was not replaced by base-ref content"
  fails=$((fails+1))
fi
expect_log "cite: config .github/cite.yml fetched from base ref main"

# 1b. Local file present but base ref has none (404): the local file is
# still not trusted in a pull_request context — it is removed and the run
# continues with defaults, so a PR cannot supply its own config via
# checkout.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "${work:?}"/case.*
build_stub 1 'gh: Not Found (HTTP 404)' ''
run_case "404 removes untrusted local file" 0 "mkdir -p .github && printf 'model: attacker\\n' > .github/cite.yml"
expect_file no
expect_no_tmp
expect_log "cite: no .github/cite.yml on base ref; ignoring the local file from the checkout (pull request context) and running with defaults"

# 1c. File present locally with NO base ref (no pull_request context at
# all — e.g. a workflow that checks out and runs on push): there is
# nothing to fetch from and nothing head-controlled about the file, so it
# is left alone.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "${work:?}"/case.*
build_stub 1 'gh: should not be called' ''
run_case "no base ref means defaults" 0 "unset GITHUB_BASE_REF"
run_case "no base ref, local file present" 0 "unset GITHUB_BASE_REF; mkdir -p .github && printf 'model: gpt\\n' > .github/cite.yml"
if [ -e "$LAST_CASE_DIR/.github/cite.yml.tmp" ]; then
  echo "FAIL: local-present case left a temp file"; fails=$((fails+1))
fi
if [ -f "$work/gh-calls.log" ]; then
  echo "FAIL: gh was invoked although there was no base ref"; fails=$((fails+1))
fi
expect_log "cite: config source: local file"

# 2. Fetch succeeds: decoded content lands at CONFIG_PATH, tmp cleaned by mv.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log"
b64="$(printf 'model: openai/gpt-5-mini\nmax_comments: 3\n' | base64)"
build_stub 0 '' "$b64"
run_case "fetch success" 0
expect_file yes
expect_no_tmp
expect_log "cite: config .github/cite.yml fetched from base ref main"
if ! cmp -s <(printf '%s' "$b64" | base64 -d) "$LAST_CASE_DIR/.github/cite.yml"; then
  echo "FAIL: decoded content differs from stub payload"; fails=$((fails+1))
fi
if ! grep -q 'ref=main' "$work/gh-calls.log"; then
  echo "FAIL: fetch URL did not use the base ref"; fails=$((fails+1))
fi

# 3. 404 on base ref: defaults, clear log line, no file left behind.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "$work"/case.*
build_stub 1 'gh: Not Found (HTTP 404)' ''
run_case "404 on base ref" 0
expect_file no
expect_no_tmp
expect_log "cite: no .github/cite.yml on base ref; running with defaults"

# 4. Decode failure (invalid base64): no partial file, nonzero exit.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "$work"/case.*
build_stub 0 '' '!!!not-base64!!!'
run_case "decode failure" 1
expect_file no
expect_no_tmp

# 5. Non-404 API failure (e.g. 401): nonzero exit, no file left behind,
# and fail fast. A 401 is a permanent credential problem, not a
# transient one, so it must not be retried (exactly one gh call).
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "$work"/case.*
rm -f "$work/call-count"
build_stub 1 'gh: bad credentials (HTTP 401)' ''
run_case "non-404 api failure" 1
expect_file no
expect_no_tmp
if [ "$(wc -l < "$work/gh-calls.log")" -ne 1 ]; then
  echo "FAIL: permanent 401 failure was retried"; fails=$((fails+1))
fi

# 5b. Transient 403 (rate limit): retried, and a later attempt that
# succeeds still delivers the config with a zero exit.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "$work"/case.*
rm -f "$work/call-count"
build_flaky_stub 1 'gh: API rate limit exceeded (HTTP 403)' "$b64"
run_case "transient 403 retried to success" 0 "export CITE_FETCH_BACKOFF=0"
expect_file yes
expect_no_tmp
expect_log "cite: transient failure fetching .github/cite.yml (attempt 1/3)"
if [ "$(wc -l < "$work/gh-calls.log")" -ne 2 ]; then
  echo "FAIL: expected 2 gh calls for one retry, got $(wc -l < "$work/gh-calls.log")"
  fails=$((fails+1))
fi

# 5c. Persistent 403: every attempt is used, then the script exits
# nonzero. Exhausted retries must never be mistaken for "no config".
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "$work"/case.*
rm -f "$work/call-count"
build_stub 1 'gh: API rate limit exceeded (HTTP 403)' ''
run_case "persistent 403 exhausts retries" 1 "export CITE_FETCH_BACKOFF=0"
expect_file no
expect_no_tmp
expect_log "cite: failed to fetch .github/cite.yml from base ref main after 3 attempt(s)"
if [ "$(wc -l < "$work/gh-calls.log")" -ne 3 ]; then
  echo "FAIL: expected 3 gh calls (all attempts), got $(wc -l < "$work/gh-calls.log")"
  fails=$((fails+1))
fi

# 5d. Transient 429 (secondary rate limit): also retried.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "$work"/case.*
rm -f "$work/call-count"
build_flaky_stub 2 'gh: you have exceeded a secondary rate limit (HTTP 429)' "$(printf 'model: from-429\n' | base64)"
run_case "transient 429 retried to success" 0 "export CITE_FETCH_BACKOFF=0"
expect_file yes
expect_no_tmp
if [ "$(wc -l < "$work/gh-calls.log")" -ne 3 ]; then
  echo "FAIL: expected 3 gh calls for two 429 retries, got $(wc -l < "$work/gh-calls.log")"
  fails=$((fails+1))
fi

# 5e. A 404 is terminal "no config": it must never be retried (exactly
# one gh call), and the run continues with defaults.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "$work"/case.*
rm -f "$work/call-count"
build_stub 1 'gh: not found (HTTP 404)' ''
run_case "404 is terminal, never retried" 0
expect_file no
expect_no_tmp
if [ "$(wc -l < "$work/gh-calls.log")" -ne 1 ]; then
  echo "FAIL: 404 was retried"; fails=$((fails+1))
fi

# 6. Empty content (defensive): treated as decode failure, nothing written.
rm -rf "${work:?}/bin" "${work:?}/gh-calls.log" "$work"/case.*
build_stub 0 '' ''
run_case "empty content rejected" 1
expect_file no
expect_no_tmp

if [ "$fails" -gt 0 ]; then
  echo "== $fails failure(s) =="
  exit 1
fi
echo "== all fetch-config cases pass =="