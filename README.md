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
on: pull_request
permissions:
  contents: read
  pull-requests: write
  checks: write
jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: elecnix/cite@7bbe8f051e9df569744c18e0fa8285f4129c1c79  # v0.10.0
        env:
          MODEL_API_KEY: ${{ secrets.MODEL_API_KEY }}
```

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

## The noise contract

- At most 10 comments per review, at most 2 per file. Small pull requests get
  at most 3.
- No style comments. Your formatter owns that.
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
- [docs/troubleshooting.md](docs/troubleshooting.md)
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
