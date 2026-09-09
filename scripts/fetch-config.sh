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
#   run continues with built-in defaults (source: defaults).
# - Any other fetch or decode failure is an error and exits nonzero.
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

if [ -f "$CONFIG_PATH" ]; then
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

if ! content="$("$GH" api "repos/$GITHUB_REPOSITORY/contents/$CONFIG_PATH?ref=$GITHUB_BASE_REF" --jq '.content' 2>"$errfile")"; then
  rm -f "$tmp"
  if grep -q 'HTTP 404' "$errfile"; then
    echo "cite: no $CONFIG_PATH on base ref; running with defaults"
    exit 0
  fi
  echo "cite: failed to fetch $CONFIG_PATH from base ref $GITHUB_BASE_REF" >&2
  exit 1
fi

# Decode into the temporary path; only a successful, non-empty decode is moved
# into place. The base64 content of a real file is never empty.
if [ -z "$content" ] || ! printf '%s' "$content" | base64 -d > "$tmp" 2>/dev/null || [ ! -s "$tmp" ]; then
  rm -f "$tmp"
  echo "cite: failed to decode $CONFIG_PATH fetched from base ref $GITHUB_BASE_REF" >&2
  exit 1
fi

mv "$tmp" "$CONFIG_PATH"
echo "cite: config $CONFIG_PATH fetched from base ref $GITHUB_BASE_REF"