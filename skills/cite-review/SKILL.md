---
name: cite-review
description: Review a pull request or a local branch diff with the Cite reviewer, and triage what it returns. Use when asked to review a PR with Cite, or asked what a Cite run found and why. Report mode publishes nothing to GitHub, and each run writes the readable report, the JSON report, the run record and the wire capture into its own directory.
license: Apache-2.0
compatibility: Requires the cite CLI on PATH, git, and jq. Pull request mode needs a GitHub token. The script borrows one from the gh CLI. Cite builds with Go 1.22 or newer.
metadata:
  homepage: https://github.com/elecnix/cite
allowed-tools: Bash(scripts/cite-review.sh:*) Bash(cite:*) Bash(git:*) Bash(jq:*) Read Grep
---

# Cite review

Review with Cite in report mode. Cite runs the same reviewer as the GitHub
Action and writes the outcome to files, so nothing on GitHub changes: it creates
no check run, and it posts no comment or thread resolution. Each run gets a
directory of its own, so a later run never overwrites an earlier review.

The script `scripts/cite-review.sh` does the bookkeeping. Run it from this
skill's directory, or give it the full path.

## Before the first run

1. Check the binary: `cite version`. If the command is missing, install it with
   `go install github.com/elecnix/cite/cmd/cite@latest`.
2. Choose the model endpoint. With nothing set, Cite sends the model calls to
   GitHub Models with the ambient `GITHUB_TOKEN`, which is rate-limited. The
   serious path is a key and an endpoint in `~/.config/cite/config.yml`, or on
   the command line:
   - `--model-api-key '$OPENROUTER_API_KEY'`
   - `--model-base-url https://openrouter.ai/api/v1`
   - `--model-id openai/gpt-5-mini`
3. See what a run will use before it spends anything: `cite validate --effective`
   prints every setting with the file it came from, and `cite doctor` prints
   which instruction files reach which paths.

## Run

```console
# a pull request, against the repository's own configuration
scripts/cite-review.sh owner/repo#123

# the same pull request on a chosen endpoint and model
scripts/cite-review.sh owner/repo#123 \
  --model-api-key '$OPENROUTER_API_KEY' \
  --model-base-url https://openrouter.ai/api/v1 \
  --model-id openai/gpt-5-mini

# the local branch, uncommitted changes included, against its upstream
scripts/cite-review.sh --local

# the local branch against a named base
scripts/cite-review.sh --local --base origin/main
```

With no upstream, `--local` falls back to the first of `origin/main`,
`origin/master`, `main`, then `master` that the repository has.

Keep the quotes around a key reference, as in `'$OPENROUTER_API_KEY'`. Cite
expands `$VAR` itself. A value the shell expands first shows up in clear in the
process arguments, where `ps` reports it, but the script writes the command to
`meta.json` with any key masked as `REDACTED`.

The script sets the pull request, the diff, the report paths, the record path
and the capture paths, because one path per run is what keeps the history.
Every other `cite review` flag passes through to Cite unchanged, such as
`--verify`, `--no-related-code`, `--structured-output tools`,
`--reasoning-effort low` or `--debug`.

Cite makes one model call per reviewed file, and another for each finding the
verifier checks. A large pull request takes minutes, and it costs money.


## The run directory

```
${CITE_HISTORY_DIR:-$HOME/.local/share/cite/reviews}/<owner>/<repo>/pr-123/<UTC stamp>-<model>/
  report.md        the readable review
  report.json      the same review as JSON: the run record, then the inline comments
  record.json      the run record at top level; a pull request run writes it even when the run fails
  run.log          stdout and stderr of the run
  meta.json        target, model, endpoint, window, cite version, exit status, command
  wire/            one masked document per model call
  truncated.json   a response the output cap cut off, when one happened
```

A local run lands under `local/<repo>/branch-NAME/` instead. Cite offers no
`--record-out` there, so the script copies `record.json` out of `report.json`.
A local run that stops before writing the report has nothing to copy. Read
`run.log` instead.

The directories are 0700 and the files are 0600. The wire capture stores the
prompt, and that prompt contains the reviewed diff.

The owner, the repository and the model id become directory names, so the script
rejects a `..` in any of them. A run cannot write outside the history directory.

## Read the verdict from the record, not from the exit code

`cite` exits 1 for a gate verdict and for a run error alike, so the exit code
alone cannot tell a finding from a missing token. The script reads `verdict`
from `record.json` and exits:

| Exit | Meaning |
| --: | -- |
| 0 | `PASS` |
| 1 | `FOUND` or `COULD_NOT_EVALUATE`: a real verdict |
| 2 | the script stopped before Cite ran: read its message |
| 3 | Cite ran and `record.json` has no `verdict`: read `run.log` |

With `<dir>` the run directory:

```console
jq -r '"\(.verdict) \(.verdict_reason // "")", (.coverage | tostring), "cost \(.cost_usd)"' "$dir/record.json"
jq -r '.findings[] | select(.blocks) | "\(.path):\(.anchor.start_line) [\(.category)] \(.title)"' "$dir/record.json"
jq -r '.files[] | select(.state != "reviewed") | "\(.state) \(.path) \(.reason // "") \(.detail // "")"' "$dir/record.json"
jq -r '.drops[] | .reason' "$dir/record.json" | sort | uniq -c
```

## Triage

- **`PASS`**: report the verdict with the coverage and the cost, and the path
  to the run directory. There is nothing to list per file.
- **`FOUND`**: list the blocking findings first, those with `blocks` true. For
  each one give the path, the line, the category, the title, and the evidence
  quote exactly as the record holds it. Quote, never paraphrase: that quote is
  the proof a finding rests on. Then list the notes, those with `blocks`
  false. To reject a finding, quote the code that refutes it and say why the
  recorded span does not support the claim.
- **`COULD_NOT_EVALUATE`**: this is not a pass. Read `verdict_reason`, then the
  files whose `state` is `error`, and report the cause from the first line of
  `detail`. Name the coverage: `coverage.reviewed` of `coverage.api_files`.
- **Drops**: `drops[].reason` says what the safety rails removed. Mention a
  large `comment_budget` count when the user asks why a known problem never
  reached the review.

Report mode leaves GitHub untouched. Never re-post a verdict from here: the
comment already on the pull request is the one the Action published.

Every field in `record.json`, and what each one answers, is in
[references/run-record.md](references/run-record.md).

## When something fails

- A `GITHUB_TOKEN` error on `--pr`: export a token, or log in to `gh`, which the
  script reads with `gh auth token`.
- `no model key found`: set `MODEL_API_KEY`, or pass `--model-api-key`.
- A provider error: `--debug` writes request and response dumps to `/tmp`, and
  `truncated.json` holds a response the output cap cut off.
- The cap ran out before the model wrote an answer: every character the provider
  returned is reasoning, so `record.json` has no `verdict` and the run exits 3.
  Read the closing line of `run.log`. A larger cap makes this response larger,
  not smaller, so lower `--reasoning-effort` or review fewer files.
- A model id on a router needs the prefix that router sells:
  `openai/gpt-5-mini`, not `gpt-5-mini`.
