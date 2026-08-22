# Contributing

## The one rule

**A prompt change is only mergeable with a benchmark delta.**

Every file under `prompts/` is a versioned artifact that shapes what the
reviewer says on every pull request it runs on. Without measurement, prompt
contribution is an argument about taste and the maintainer becomes the
bottleneck on their own project.

So the workflow for any change to `prompts/` is:

1. Run the harness before your change:

   ```
   ./bench/run.sh bench/cases
   ```

2. Make your prompt change.
3. Run it again.
4. Include both outputs (and the delta) in your pull request.

A change with no delta is rejected as noise. A change with a regression anywhere
— schema validity, anchor placement, fingerprint stability, detection of planted
defects — needs a reason in the pull request, and "it reads better" is not one.

## Benchmark cases

`bench/cases/` holds the public split of the corpus. Each case is:

- a base tree,
- a patch,
- a manifest of planted defects, each with a one-sentence detection rubric.

The corpus targets 40% positive (a planted defect), 40% clean, and 20%
near-miss — code matching a defect pattern that is actually correct. The near
misses are non-negotiable: without them you measure pattern-matching rather than
reasoning, and false-positive-heavy models score well.

See [bench/README.md](bench/README.md) for the case format.

## Development setup

- Go 1.22 or newer.
- Build: `go build ./...`
- Test: `go test ./...`
- The reviewer never calls GitHub; the publisher never calls a model. Both are
  testable offline against fixtures. Keep it that way.

```
git clone <your fork>
cd cite
go test ./...
```

## Commit style

Plain commits. Imperative subject line, no scope prefixes required, body only
when the why is not obvious from the diff:

```
Fix anchor validation on renamed files

Rename sources have no post-change lines to anchor to; they were
falling through to the whole-file anchor path.
```

## What contributions are wanted

In order:

1. **Benchmark cases** (`bench/cases/`) — especially near-misses you have seen
   fool reviewers in the wild.
2. **Prompt changes** — with the benchmark delta above.
3. **Documentation fixes** — this project treats its documentation as part of
   the product; a divergence that is not written down is a bug.
4. **Code**, matching the existing structure: `scope/`, `reviewer/`,
   `publisher/`, `gate/`, `config/`, `instructions/`.

Please do not add configuration surface. The v1 config has seven keys by design;
new keys need a case that the seven cannot express.

## License

Apache-2.0. Contributions are made under the same license.
