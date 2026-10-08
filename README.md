# Cite

An open code reviewer for GitHub.

Cite reviews your pull requests, with the model you choose, and can block the
merge. It posts a real pull request review with comments anchored to the lines
they are about, and publishes a check run a repository may require before
merge. Every finding quotes the source line it is about, and the quote is
verified against the file before anyone sees it.

> **Status: under construction.** This repository is being implemented from
> [PLAN.md](PLAN.md).

## Install

```yaml
name: review
on: pull_request_target
permissions:
  contents: read
  pull-requests: write
  checks: write
jobs:
  review:
    # Forks are not reviewed by default: the job is skipped for them, so a
    # fork pull request cannot spend the model key. Delete this line to opt
    # in — and read docs/security.md first: the two-workflow split there is
    # the pattern for reviewing fork pull requests.
    # Comparing the head repository by name, rather than testing
    # head.repo.fork, also holds when this repository is itself a fork.
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    steps:
      - uses: elecnix/cite@825c70e4b4a87b48bafd0684ee6402c8cffe4018  # v0.13.0
        env:
          MODEL_API_KEY: ${{ secrets.MODEL_API_KEY }}
```

GitHub takes the workflow definition from the default branch when a workflow
listens for `pull_request_target`, so that trigger, rather than `pull_request`,
keeps a pull request from running a stale copy of this file. Neither trigger
makes the job check anything out: the job holds the key and calls the GitHub
API, and that is what keeps pull-request-head code away from the trusted
context ([docs/security.md](docs/security.md), I1). The action reads the pull
request number from the event payload, so this trigger needs the next release
(v0.11.4 or later). Earlier pins resolve the number from `refs/pull/N/merge`
and skip the review silently under `pull_request_target`.

Configuration is optional, and the workflow above is the whole of it. Cite
reads the provider from whichever key is present, so a `with:` block can be
omitted and `actions/checkout` is unnecessary. With `models: read` permission
and no key at all, Cite runs on GitHub's models endpoint with the ambient
token, which is rate-limited and meant for the first review before you have
decided anything ([examples/zero-secret.yml](examples/zero-secret.yml)).
Bring-your-own-key is the upgrade.

Already have instruction files (`.github/copilot-instructions.md`,
`AGENTS.md`, `*.instructions.md`)? Cite reads them from the base ref and tells
you what it did with them.

## Diagnostics

Every run publishes a check run, and every finding links the run record that
produced it. A failed run uploads a forensics artifact (the step log and the
run record, kept 14 to 30 days), which answers what the run concluded and what
it spent. It cannot answer what your provider actually returned, because a
response Cite's schema rejected is never written down anywhere. When you need
those bytes, opt into the wire capture:

```yaml
      - uses: elecnix/cite@825c70e4b4a87b48bafd0684ee6402c8cffe4018  # v0.13.0
        with:
          capture_wire: true
        env:
          MODEL_API_KEY: ${{ secrets.MODEL_API_KEY }}
```

That uploads a `cite-wire-<run id>-…` artifact, kept 7 days, with one masked
document per model call. Each one has the request body exactly as sent, the
response body exactly as it arrived before any parsing, the HTTP status and
headers, and an outcome of `ok`, `parse-failure` or `http-error`. Masking is
allowlist-first, so a request header keeps its value only if Cite set it, and
your provider key is replaced wherever it appears, including inside a
provider's own error page.

Read the privacy cost before turning it on: a capture contains the prompt, and
the prompt contains the diff under review. A capture of a private repository is
private review material, and the masking covers credentials only. The input is
off by default for that reason. [docs/troubleshooting.md](docs/troubleshooting.md)
has the field list, the masking rules, and the two environment settings that
go with it (`CITE_CAPTURE_DIR`, `CITE_CAPTURE_MAX_BYTES`).

## The noise contract

- At most 10 comments per review, at most 2 per file. Small pull requests get
  at most 3.
- No style comments. Your formatter owns that.
- A push reviews only the files whose content changed since the last completed
  review. Cite keeps its earlier findings on the other files as they were, so
  code nobody touched cannot grow new comments.
- Nothing to say means nothing posted. It never posts "LGTM".
- Every comment quotes the exact line it is about. If the quote does not match
  your file, the comment is dropped before you see it.
- Cost you can see: every run reports its token usage and USD cost. The cost
  is what your provider billed, when it reports one (OpenRouter does), or
  else a figure from the rates you declare.

## Local evaluation path

```
cite review --diff <(git diff main...)        # local branch, nothing pushed
cite review --pr owner/repo#123 --dry-run     # a real PR; prints, posts nothing
cite review --pr owner/repo#123 --report markdown --out report.md  # full run, local report
cite doctor                                   # which instruction files reached which paths
cite validate                                 # schema-check the config
cite soak bench/cases                         # pipeline regression harness
cite canary                                   # ping every provider/fallback leg
cite bypass --pr owner/repo#N                 # break-glass: conclude + log (§11)
cite listen --pr owner/repo#N --comment-id ID # handle an "@cite review" comment
cite signals --pr owner/repo#N                # ingest 👎 dismissals into the ledger
cite metrics fix-or-argue --pr owner/repo#N   # weekly actioned-rate proxy (§15)
cite signals --pr owner/repo#N                # ingest 👎 reactions into the ledger
cite re-review --repo owner/name              # re-review bypassed merges; one issue per finding
```

The `--report json|markdown` path runs the reviewer against the real pull request and writes the outcome to stdout or `-o FILE`. Nothing on GitHub changes, and the check and comment already published keep their last state. It reviews all manifest files fresh (no incremental carry-forward) so reports stay reproducible. This lets you evaluate Cite on any repository your token can read without deploying the action there.

## Documentation

- [docs/configuration.md](docs/configuration.md): every key
- [docs/instructions.md](docs/instructions.md): what it reads, precedence, triage, base-ref rule
- [docs/noise.md](docs/noise.md): the budget formula and category table
- [docs/security.md](docs/security.md) covers the invariants, the fork case, and the capabilities Cite works without.
- [docs/downstream-contract.md](docs/downstream-contract.md): what an agent consuming these comments must be told
- [docs/troubleshooting.md](docs/troubleshooting.md): the diagnostic chain, the forensics archive and the opt-in wire capture
- [CONFORMANCE.md](CONFORMANCE.md): the compatibility tiers, dated
- [CONTRIBUTING.md](CONTRIBUTING.md)

## Comparison with other reviewers

[Gito](https://github.com/Nayjest/Gito) is another open-source AI code
reviewer. The table compares the two, with Gito as of commit
[`f48498d`](https://github.com/Nayjest/Gito/tree/f48498d192ede9aa81808c2579c69cc5d7919e20).

| | Cite | Gito |
|---|---|---|
| Runs as | A GitHub Action and a Go CLI | A Python package (`gito.bot`) with a CLI and CI workflows for GitHub and GitLab |
| Finding fields | Category, and confidence `certain`, `likely` or `question`, without severity | Title, details, severity 1 to 5, confidence 1 to 4, tags, and line ranges with an optional fix |
| Categories | A closed list. Cite rejects a response with any other category. | The prompt suggests tags, and Gito accepts any string |
| Filtering | Each finding quotes its lines, and Cite drops it if the quote doesn't match the file. At most 10 per review and 2 per file. | A Python `post_process` snippet in the config. The default keeps confidence 1 with severity 3 or lower. |
| Output | A pull request review with inline comments, and a check run that can block the merge | One summary comment on GitHub. On GitLab, `--inline` posts a comment per issue and puts the rest in the overview. |
| Project settings | Reads `AGENTS.md`, `CLAUDE.md`, `REVIEW.md`, Copilot instructions and more from the base ref | `.gito/config.toml`, with prose requirements, `exclude_files` and `aux_files` |
| Models and tools | OpenAI and Anthropic APIs, OpenAI-compatible endpoints, and GitHub Models | OpenAI-compatible, Anthropic and Google APIs, local and embedded models, coding agent CLIs, and Jira and Linear issue lookup |

## License

Apache-2.0.
