#!/usr/bin/env bash
# Fetch the trusted Cite configuration file from the pull request's BASE ref.
#
# The composite action runs with no actions/checkout, so the config file named
# by config_path is usually absent from the runner filesystem. This script
# fetches it from the base repository via the GitHub contents API using the
# ambient token (GITHUB_TOKEN for the API, GH_TOKEN for the gh CLI).
#
# Security invariant (docs/security.md I3): the config is fetched from the
# BASE ref only — never the pull request head or the merge ref. A config
# fetched from the head would let a pull request rewrite its own review
# settings and point model_base_url at an attacker-controlled endpoint.
#
# Behaviour:
# - If the file already exists locally, it is left alone (source: local file).
# - If the base ref has the file, it is decoded into place (source: base ref).
#   The content is written to a temporary path first and only moved into place
#   after a successful, non-empty decode, so an empty or partial file is never
#   left behind.
# - If the base ref has no config file (HTTP 404), that is not an error: the
#   run continues with built-in defaults (source: defaults). A 404 is
#   terminal and is never retried.
# - Transient HTTP failures (403 rate limit, 429, 5xx) are retried
#   CITE_FETCH_ATTEMPTS times (default 3) with a CITE_FETCH_BACKOFF second
#   pause (default 2) between attempts, so a secondary rate limit or a
#   blip on the API side does not turn the whole review job red.
# - Any other fetch or decode failure is an error and exits nonzero. A
#   permanent failure (e.g. 401) is not retried, and exhausted retries are
#   never mistaken for "no config": they exit nonzero, like before.
#
# Inputs (environment):
#   CONFIG_PATH       path of the config file, relative to the repository root
#   GITHUB_REPOSITORY owner/name of the base repository
#   GITHUB_BASE_REF   the pull request's base ref name (target branch)
#   GH                gh CLI command to use (default: gh; overridable for tests)

set -euo pipefail

CONFIG_PATH="${CONFIG_PATH:-.github/cite.yml}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY must be set}"
GITHUB_BASE_REF="${GITHUB_BASE_REF:-}"
GH="${GH:-gh}"

if [ -f "$CONFIG_PATH" ] && [ -z "$GITHUB_BASE_REF" ]; then
  # A local file is only trustworthy when there is no pull_request
  # context at all (e.g. a workflow that checks out and runs on push):
  # nothing can have planted a head-controlled file there. On
  # pull_request a checkout step materializes the MERGE ref, so any
  # local config could be authored by the pull request itself — in that
  # context the base ref below is authoritative and always wins.
  echo "cite: config source: local file ($CONFIG_PATH)"
  exit 0
fi

# Outside a pull_request context there is no base ref to fetch from (the
# review step itself also skips such events); defaults are the honest answer.
if [ -z "$GITHUB_BASE_REF" ]; then
  echo "cite: config source: defaults (no pull request context; nothing to fetch)"
  exit 0
fi

mkdir -p "$(dirname "$CONFIG_PATH")"
tmp="$CONFIG_PATH.tmp"

# The contents API returns the file content base64-encoded. A 404 means the
# base ref carries no config file, which is not an error: Cite then runs with
# its built-in defaults. Any other HTTP failure is fatal — it must not be
# mistaken for "no config", and it must not leave an empty or partial file.
errfile="$(mktemp)"
defer_rm_errfile() { rm -f "$errfile"; }
trap defer_rm_errfile EXIT

# Transient HTTP failures (403 rate limit, 429, 5xx) are retried; a 404
# (no config on the base ref) and a permanent failure (e.g. 401) are
# terminal on the first attempt. Exhausted retries still exit nonzero: a
# failed fetch must never be mistaken for "no config".
fetch_attempts="${CITE_FETCH_ATTEMPTS:-3}"
fetch_backoff="${CITE_FETCH_BACKOFF:-2}"
attempt=1
while :; do
  if content="$("$GH" api "repos/$GITHUB_REPOSITORY/contents/$CONFIG_PATH?ref=$GITHUB_BASE_REF" --jq '.content' 2>"$errfile")"; then
    break
  fi
  rm -f "$tmp"
  if grep -q 'HTTP 404' "$errfile"; then
    # No config on the base ref. In a pull_request context any local
    # file at CONFIG_PATH was put there by the checkout of the merge
    # ref (authored by the pull request itself) and must not be used.
    if [ -f "$CONFIG_PATH" ]; then
      rm -f "$CONFIG_PATH"
      echo "cite: no $CONFIG_PATH on base ref; ignoring the local file from the checkout (pull request context) and running with defaults"
    else
      echo "cite: no $CONFIG_PATH on base ref; running with defaults"
    fi
    exit 0
  fi
  if grep -Eq 'HTTP (403|429|5[0-9][0-9])' "$errfile" && [ "$attempt" -lt "$fetch_attempts" ]; then
    echo "cite: transient failure fetching $CONFIG_PATH (attempt $attempt/$fetch_attempts): $(cat "$errfile"); retrying in ${fetch_backoff}s" >&2
    sleep "$fetch_backoff"
    attempt=$((attempt+1))
    continue
  fi
  echo "cite: failed to fetch $CONFIG_PATH from base ref $GITHUB_BASE_REF after $attempt attempt(s)" >&2
  exit 1
done

# Decode into the temporary path; only a successful, non-empty decode is moved
# into place. The base64 content of a real file is never empty.
if [ -z "$content" ] || ! printf '%s' "$content" | base64 -d > "$tmp" 2>/dev/null || [ ! -s "$tmp" ]; then
  rm -f "$tmp"
  echo "cite: failed to decode $CONFIG_PATH fetched from base ref $GITHUB_BASE_REF" >&2
  exit 1
fi

mv "$tmp" "$CONFIG_PATH"
echo "cite: config $CONFIG_PATH fetched from base ref $GITHUB_BASE_REF"