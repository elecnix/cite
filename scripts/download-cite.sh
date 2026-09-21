#!/usr/bin/env bash
# Download the Cite release binary the composite action should run.
#
# This lives in a script rather than inline in action.yml because which
# version a consumer gets is a contract, and a contract belongs somewhere it
# can be tested: scripts/download-cite_test.sh exercises every branch below
# with a stubbed curl, while a YAML step is only reachable from a runner.
#
# Version resolution (issue #94):
#   version: (unset)   the release this action belongs to. VERSION sits next to
#                      action.yml and is checked out with it, so
#                      `uses: elecnix/cite@<full-sha>` pins the binary as well
#                      as the action. That is the pair docs/release.md already
#                      asks consumers to pin, and before this the binary
#                      floated to whatever shipped last.
#   version: latest    the newest published release, deliberately floating.
#                      An upgrade policy a consumer opts into, not a default.
#   version: vX.Y.Z    that release, exactly.
#
# The URL for an explicit version is the explicit-tag form, which needs no
# server-side "which release is newest" resolution and no redirect hop; the
# floating form is the only one that uses the releases/latest redirect.
#
# Inputs (environment):
#   CITE_VERSION        the `version` input: a release tag, `latest`, or empty
#   GITHUB_ACTION_PATH  where the action was checked out (holds VERSION)
#   RUNNER_ARCH         X64 or ARM64
#   RUNNER_TEMP         writable directory for the binary
#   GITHUB_PATH         file the install directory is appended to
#   CURL                curl command to use (default: curl; overridable for
#                       tests, which stub it rather than reach the network)
#   CITE_DOWNLOAD_ATTEMPTS, CITE_DOWNLOAD_BACKOFF, CITE_DOWNLOAD_MAX_TIME,
#                       CITE_DOWNLOAD_CONNECT_TIMEOUT — the retry window below

set -euo pipefail

ACTION_PATH="${GITHUB_ACTION_PATH:?GITHUB_ACTION_PATH must be set}"
CITE_VERSION="${CITE_VERSION:-}"
RUNNER_ARCH="${RUNNER_ARCH:?RUNNER_ARCH must be set}"
RUNNER_TEMP="${RUNNER_TEMP:?RUNNER_TEMP must be set}"
GITHUB_PATH="${GITHUB_PATH:?GITHUB_PATH must be set}"
CURL="${CURL:-curl}"

# The version file holds the bare version, the way cmd/cite/main.go does:
# tags carry the `v`, the version itself does not. One format, and
# scripts/version-check.sh enforces it at release time so a consumer never
# downloads a tag that was assembled from two different conventions.
# The version pattern is the tag pattern without its `^v`, so the two cannot
# drift apart.
tag_re='^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$'
version_re="^${tag_re#^v}"

case "$CITE_VERSION" in
  '')
    if [ ! -r "$ACTION_PATH/VERSION" ]; then
      echo "cite: $ACTION_PATH/VERSION is missing; cannot tell which release this action is. Pass version: vX.Y.Z or version: latest." >&2
      exit 1
    fi
    own_version="$(tr -d '[:space:]' < "$ACTION_PATH/VERSION")"
    if ! printf '%s' "$own_version" | grep -qE "$version_re"; then
      echo "cite: $ACTION_PATH/VERSION holds '$own_version', which is not X.Y.Z. Pass version: vX.Y.Z or version: latest." >&2
      exit 1
    fi
    resolved="v$own_version"
    ;;
  latest)
    resolved="latest"
    ;;
  *)
    if ! printf '%s' "$CITE_VERSION" | grep -qE "$tag_re"; then
      echo "cite: version input '$CITE_VERSION' is not a release tag. Use vX.Y.Z, or latest to track the newest release." >&2
      exit 1
    fi
    resolved="$CITE_VERSION"
    ;;
esac

case "$RUNNER_ARCH" in
  X64) arch=amd64 ;;
  ARM64) arch=arm64 ;;
  *) echo "unsupported runner arch: $RUNNER_ARCH" >&2; exit 1 ;;
esac

if [ "$resolved" = "latest" ]; then
  url="https://github.com/elecnix/cite/releases/latest/download/cite-linux-${arch}"
else
  url="https://github.com/elecnix/cite/releases/download/${resolved}/cite-linux-${arch}"
fi

# Install into a per-run writable directory instead of a system path:
# /usr/local/bin is root-owned on self-hosted runners (e.g. ARC images run the
# runner as non-root), and installing a CLI must never require root.
# Exporting the directory onto GITHUB_PATH makes `cite` resolvable in every
# later step, exactly as installing into /usr/local/bin did.
cite_bin="$RUNNER_TEMP/cite-bin"
mkdir -p "$cite_bin"

# The download is retried for minutes, not seconds. Measured on 2026-09-21: the
# release-asset endpoint answered four fast 504s inside about seven seconds,
# which is the whole window `--retry 3` covers, then served the same URL
# normally, so the job died over a blip a longer window would have ridden out
# (elecnix/gridkeeper run 35618280194). Defaults: six retries fifteen seconds
# apart, at most three minutes total, ten seconds to connect. Each knob is
# overridable, because this is wall-clock time every consumer's job pays.
#
# `--retry-all-errors` is deliberately absent: it would also retry a 404,
# which is what a mistyped `version:` input looks like, for the whole window.
# curl already retries the transient statuses without it: 408, 429, 500, 502,
# 503, 504.
attempts="${CITE_DOWNLOAD_ATTEMPTS:-6}"
backoff="${CITE_DOWNLOAD_BACKOFF:-15}"
max_time="${CITE_DOWNLOAD_MAX_TIME:-180}"
connect_timeout="${CITE_DOWNLOAD_CONNECT_TIMEOUT:-10}"

# The version and URL are printed before the download so a failed fetch is
# diagnosable from the log alone: which version, which platform, which URL.
echo "cite: downloading $resolved ($url)"
if ! "$CURL" -fsSL \
  --retry "$attempts" \
  --retry-delay "$backoff" \
  --retry-max-time "$max_time" \
  --connect-timeout "$connect_timeout" \
  -o "$cite_bin/cite" "$url"; then
  echo "cite: download failed for $resolved after ${attempts} retries over up to ${max_time}s: $url" >&2
  echo "cite: a 404 above means release $resolved has no such asset; a 5xx above means retry the job" >&2
  # curl -o leaves whatever it wrote before it gave up. Remove it rather than
  # leave a truncated binary where a later step would run it.
  rm -f "$cite_bin/cite"
  exit 1
fi
chmod +x "$cite_bin/cite"
echo "$cite_bin" >> "$GITHUB_PATH"
