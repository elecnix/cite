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
      - uses: elecnix/cite@v1
        env:
          MODEL_API_KEY: ${{ secrets.MODEL_API_KEY }}
```

No `actions/checkout`. No `with:` block. No configuration file. The provider is
inferred from which key is present — and with `models: read` permission and no
key at all, Cite runs on GitHub's models endpoint with the ambient token:
rate-limited, meant for the first review before you have decided anything
([examples/zero-secret.yml](examples/zero-secret.yml)). Bring-your-own-key is
the upgrade.

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
- Cost you can see: every run reports its token usage and USD cost from your
  provider's declared rates.

## Local evaluation path

```
cite review --diff <(git diff main...)        # local branch, nothing pushed
cite review --pr owner/repo#123 --dry-run     # a real PR; prints, posts nothing
cite doctor                                   # which instruction files reached which paths
cite validate                                 # schema-check the config
cite soak bench/cases                         # pipeline regression harness
cite canary                                   # ping every provider/fallback leg
cite bypass --pr owner/repo#N                 # break-glass: conclude + log (§11)
cite listen --pr owner/repo#N --comment-id ID # handle an "@cite review" comment
cite signals --pr owner/repo#N                # ingest 👎 reactions into the ledger
cite re-review --repo owner/name              # re-review bypassed merges; one issue per finding
```

## Documentation

- [docs/configuration.md](docs/configuration.md) — every key
- [docs/instructions.md](docs/instructions.md) — what it reads, precedence, triage, base-ref rule
- [docs/noise.md](docs/noise.md) — the budget formula and category table
- [docs/security.md](docs/security.md) — the invariants, fork PRs, what it never has
- [docs/downstream-contract.md](docs/downstream-contract.md) — what an agent consuming these comments must be told
- [docs/troubleshooting.md](docs/troubleshooting.md)
- [CONFORMANCE.md](CONFORMANCE.md) — the compatibility tiers, dated
- [CONTRIBUTING.md](CONTRIBUTING.md)

## License

Apache-2.0.
