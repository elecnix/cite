#!/usr/bin/env bash
# cite-review: review one pull request or one local branch with Cite, and keep
# every run it produces.
#
#   cite-review.sh owner/repo#123
#   cite-review.sh owner/repo#123 --model-id openai/gpt-5-mini \
#                                 --model-base-url https://openrouter.ai/api/v1 \
#                                 --model-api-key '$OPENROUTER_API_KEY'
#   cite-review.sh --local                  # the working tree against its upstream
#   cite-review.sh --local --base origin/main
#
# Cite writes the file it is given, so one path per run would keep only the last
# review. This script gives every run its own directory:
#
#   ${CITE_HISTORY_DIR:-$HOME/.local/share/cite/reviews}/owner/repo/pr-123/
#     20261023T141502Z-openai-gpt-5-mini/
#       report.md        the readable review
#       report.json      the same review as JSON (run record + inline comments)
#       record.json      the run record at top level (pr mode: even on failure)
#       run.log          stdout and stderr, in one place
#       meta.json        what ran, when, against which endpoint, and its exit code
#       wire/            every model request and response (cite's wire capture)
#       truncated.json   a response the output cap cut off, when one happened
#
#   A local run lands under .../local/REPO/branch-NAME/ instead, and its
#   record.json is copied from report.json, so a failed local run has none.
#
# Exit code is the verdict, read from record.json because cite's own exit code
# cannot tell a gate verdict from a run error:
#
#   0  PASS
#   1  FOUND or COULD_NOT_EVALUATE (a real verdict, not a failure of this script)
#   2  this script could not start: bad arguments, no cite, no token, empty diff
#   3  cite ran and recorded no verdict; read run.log
#
# Flags this script owns: --pr, --local, --base (with --local only), --diff,
# --report, --out, --record-out, --capture-dir, --truncated-out. Every other
# `cite review` flag passes through unchanged, after the target; anything after
# `--` passes through verbatim. A literal model key on the command line reaches
# meta.json as REDACTED, and MODEL_API_KEY is never printed.
#
# Set CITE_BIN to use a binary that is not on PATH.
#
# --- end of usage text ---

set -uo pipefail

CITE=${CITE_BIN:-cite}
ROOT=${CITE_HISTORY_DIR:-$HOME/.local/share/cite/reviews}
SELF=${BASH_SOURCE[0]}

usage() {
  # The usage text is this file's comment header: line 2 down to the marker.
  sed -n '2,/^# --- end of usage text ---$/p' "$SELF" | sed '$d' | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

die() {
  printf 'cite-review: %s\n' "$1" >&2
  exit 2
}

# --- arguments -------------------------------------------------------------

PR=""
LOCAL=0
BASE=""
have_target=0
after_flag=0
PASSTHROUGH=()

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage 0 ;;
    --local)
      [ "$have_target" -eq 0 ] || die "--local takes no pull request"
      LOCAL=1
      have_target=1
      after_flag=0
      shift
      ;;
    --base)
      [ $# -ge 2 ] || die "--base needs a ref"
      BASE=$2
      after_flag=0
      shift 2
      ;;
    --base=*) BASE=${1#*=}; after_flag=0; shift ;;
    --) shift; PASSTHROUGH+=("$@"); break ;;
    -*)
      # A value that follows a pass-through flag arrives here as a positional
      # argument below and is appended in the same order, so `--model-id x`
      # reaches cite as the pair it was written as.
      PASSTHROUGH+=("$1")
      after_flag=1
      shift
      ;;
    *)
      # The value of the pass-through flag just read. It stays in order, and it
      # is never mistaken for the pull request or for an extra argument.
      if [ "$after_flag" -eq 1 ]; then
        PASSTHROUGH+=("$1")
        after_flag=0
        shift
        continue
      fi
      if [ "$LOCAL" -eq 1 ]; then
        die "unexpected argument: $1 (--local takes no pull request)"
      fi
      if [ "$have_target" -eq 0 ]; then
        PR=$1
        have_target=1
      else
        PASSTHROUGH+=("$1")
      fi
      shift
      ;;
  esac
done

[ "$have_target" -eq 1 ] || usage 2
if [ "$LOCAL" -eq 1 ] && [ -n "$PR" ]; then
  die "--local takes no pull request"
fi
if [ "$LOCAL" -eq 0 ] && [ -n "$BASE" ]; then
  die "--base only applies to --local: a pull request brings its own merge base"
fi

# The report paths belong to this script, so pass-through of them would write
# outside the per-run directory.
for arg in "${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}"; do
  case "$arg" in
    --pr|--pr=*|-pr|-pr=*|--local|--base|--base=*|\
    --diff|--diff=*|-diff|-diff=*|--report|--report=*|-report|-report=*|\
    --out|--out=*|-out|-out=*|--record-out|--record-out=*|-record-out|-record-out=*|\
    --capture-dir|--capture-dir=*|-capture-dir|-capture-dir=*|\
    --truncated-out|--truncated-out=*|-truncated-out|-truncated-out=*)
      die "$arg is owned by this script: every run keeps its own directory" ;;
  esac
done

command -v "$CITE" >/dev/null 2>&1 \
  || die "$CITE not found on PATH; install it with go install github.com/elecnix/cite/cmd/cite@latest, or set CITE_BIN"
command -v jq >/dev/null 2>&1 || die "jq not found on PATH"


# --- target ----------------------------------------------------------------

diff=""
if [ "$LOCAL" -eq 1 ]; then
  top=$(git rev-parse --show-toplevel 2>/dev/null) || die "--local needs a git work tree"
  cd "$top" || die "cannot enter $top"
  if [ -z "$BASE" ]; then
    BASE=$(git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null || true)
  fi
  # No upstream, so fall back to whatever this repo calls its mainline.
  if [ -z "$BASE" ]; then
    for candidate in origin/main origin/master main master; do
      if git rev-parse --verify --quiet "$candidate" >/dev/null 2>&1; then
        BASE=$candidate
        break
      fi
    done
  fi
  [ -n "$BASE" ] || die "no upstream branch and no mainline to diff against; name one with --base"
  diff=$(mktemp) || die "cannot create a temporary file"
  trap 'rm -f "$diff"' EXIT
  # Merge the base into HEAD by hand: `git diff --merge-base` needs git 2.42.
  base_sha=$(git merge-base "$BASE" HEAD 2>/dev/null) \
    || die "$BASE shares no history with HEAD"
  git diff --no-color "$base_sha" > "$diff" || die "git diff against $BASE failed"
  [ -s "$diff" ] || die "no changes against $BASE"
  owner=local
  repo_name=$(basename "$top")
  branch=$(git rev-parse --abbrev-ref HEAD)
  group=branch-$(printf '%s' "$branch" | tr -c 'A-Za-z0-9._-' '-' | sed 's/-\{2,\}/-/g; s/-$//')
else
  rest=${PR#*/}
  owner=${PR%%/*}
  repo_name=${rest%%#*}
  if [ "$rest" = "$PR" ] || [ -z "$owner" ] || [ -z "$repo_name" ] \
     || [ "$repo_name" = "$rest" ] || ! printf '%s' "${rest##*#}" | grep -Eq '^[0-9]+$'; then
    die "pull request must be owner/repo#N, got $PR"
  fi
  group=pr-${rest##*#}
fi

# --- credentials -----------------------------------------------------------

# --pr mode reads the pull request over the API, so it needs a token. Borrow
# the one gh already holds rather than making the caller export it.
if [ "$LOCAL" -eq 0 ] && [ -z "${GITHUB_TOKEN:-}" ] && command -v gh >/dev/null 2>&1; then
  GITHUB_TOKEN=$(gh auth token 2>/dev/null || true)
  [ -n "$GITHUB_TOKEN" ] && export GITHUB_TOKEN
fi
if [ "$LOCAL" -eq 0 ] && [ -z "${GITHUB_TOKEN:-}" ]; then
  die "no GITHUB_TOKEN, and gh could not supply one"
fi


# --- one directory per run -------------------------------------------------

# The model id names the directory, so two models against one pull request
# never share a path. It comes from the pass-through --model-id when there is
# one, else from the environment, else from cite's own default.
model_arg=""
base_url_arg=""
i=0
while [ "$i" -lt ${#PASSTHROUGH[@]} ]; do
  case "${PASSTHROUGH[$i]}" in
    -model-id|--model-id)
      j=$((i + 1))
      [ "$j" -lt ${#PASSTHROUGH[@]} ] && model_arg=${PASSTHROUGH[$j]}
      ;;
    -model-id=*|--model-id=*) model_arg=${PASSTHROUGH[$i]#*=} ;;
    -model-base-url|--model-base-url)
      j=$((i + 1))
      [ "$j" -lt ${#PASSTHROUGH[@]} ] && base_url_arg=${PASSTHROUGH[$j]}
      ;;
    -model-base-url=*|--model-base-url=*) base_url_arg=${PASSTHROUGH[$i]#*=} ;;
  esac
  i=$((i + 1))
done
model_slug=${model_arg:-${MODEL_ID:-default}}
slug=$(printf '%s' "$model_slug" | tr -c 'A-Za-z0-9._-' '-' | sed 's/-\{2,\}/-/g; s/-$//')

stamp=$(date -u +%Y%m%dT%H%M%SZ)
dir="$ROOT/$owner/$repo_name/$group/$stamp-$slug"
n=1
while [ -e "$dir" ]; do            # two runs inside one second never share a directory
  n=$((n + 1))
  dir="$ROOT/$owner/$repo_name/$group/$stamp-$slug-$n"
done

# The wire capture carries the prompt and therefore the reviewed diff, so the
# whole run directory is private: 0700 directories, 0600 files.
umask 077
mkdir -p "$dir/wire" || die "cannot create $dir"

report="$dir/report.md"
report_json="$dir/report.json"
record_json="$dir/record.json"

cmd=("$CITE" review)
if [ "$LOCAL" -eq 1 ]; then
  cmd+=(--diff "$diff")
else
  cmd+=(--pr "$PR")
fi
cmd+=("${PASSTHROUGH[@]+"${PASSTHROUGH[@]}"}")
cmd+=(--report "markdown=$report"
      --report "json=$report_json"
      --capture-wire --capture-dir "$dir/wire"
      --truncated-out "$dir/truncated.json")
if [ "$LOCAL" -eq 0 ]; then
  cmd+=(--record-out "$record_json")
fi

# meta.json records what a later reader of the archive cannot recover from the
# report or the record: the wall-clock window, the endpoint, and the command.
# A literal key passed on the command line is masked before it is written.
masked_cmd=""
prev=""
for arg in "${cmd[@]}"; do
  case "$prev" in
    -model-api-key|--model-api-key) arg='REDACTED' ;;
  esac
  case "$arg" in
    -model-api-key=*|--model-api-key=*) arg='--model-api-key=REDACTED' ;;
  esac
  masked_cmd="$masked_cmd$(printf '%q ' "$arg")"
  prev=$arg
done

started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
if [ "$LOCAL" -eq 1 ]; then
  target="$repo_name@$branch"
else
  target=$PR
fi
printf 'cite-review: %s -> %s\n' "$target" "$dir" >&2
printf 'cite-review: model=%s endpoint=%s\n' "${model_arg:-${MODEL_ID:-cite default}}" "${base_url_arg:-${MODEL_BASE_URL:-cite default}}" >&2

"${cmd[@]}" 2>&1 | tee "$dir/run.log"
status=${PIPESTATUS[0]}
finished=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# A local run has no --record-out, and a pull request run that died early may
# have written none: the JSON report carries the same record under "run".
if [ ! -s "$record_json" ] && [ -s "$report_json" ]; then
  jq '.run' "$report_json" > "$record_json" 2>/dev/null || rm -f "$record_json"
fi

jq -n \
  --arg target "$target" \
  --arg mode "$(if [ "$LOCAL" -eq 1 ]; then printf local; else printf pr; fi)" \
  --arg base "$BASE" \
  --arg model "${model_arg:-${MODEL_ID:-}}" \
  --arg base_url "${base_url_arg:-${MODEL_BASE_URL:-}}" \
  --arg reviewer "${CITE_REVIEWER_ID:-}" \
  --arg started "$started" \
  --arg finished "$finished" \
  --arg status "$status" \
  --arg cite "$("$CITE" version 2>/dev/null || echo unknown)" \
  --arg cmd "$masked_cmd" \
  '{target: $target, mode: $mode, base: $base, model: $model, base_url: $base_url,
    reviewer: $reviewer, started_utc: $started, finished_utc: $finished,
    exit_status: ($status | tonumber), cite: $cite, command: $cmd}' > "$dir/meta.json"

# --- the verdict, and what happened ---------------------------------------

verdict=""
if [ -s "$record_json" ]; then
  verdict=$(jq -r '.verdict // ""' "$record_json" 2>/dev/null || true)
fi

if [ -n "$verdict" ]; then
  printf 'cite-review: %s | %s finding(s), %s blocking | $%s | cite exited %s\n' \
    "$verdict" \
    "$(jq -r '.findings // [] | length' "$record_json" 2>/dev/null || echo '?')" \
    "$(jq -r '[.findings // [] | .[] | select(.blocks)] | length' "$record_json" 2>/dev/null || echo '?')" \
    "$(jq -r '.cost_usd // 0' "$record_json" 2>/dev/null || echo '?')" \
    "$status" >&2
else
  printf 'cite-review: no verdict recorded; cite exited %s, read %s\n' "$status" "$dir/run.log" >&2
fi
printf 'cite-review: report %s\n' "$report" >&2

case "$verdict" in
  PASS) exit 0 ;;
  FOUND|COULD_NOT_EVALUATE) exit 1 ;;
  *) exit 3 ;;
esac
