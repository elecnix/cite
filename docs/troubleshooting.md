# Troubleshooting

When a review looks wrong, work down this chain. Each step narrows the question
from "the review was bad" to the specific mechanism that produced it.

## The diagnostic flow

```
bad review
  → open the run artifact (linked from the review body)
    → read the drop log inside it
      → cite doctor   (instruction-file questions)
        → cite soak    (does it reproduce on the benchmark?)
```

**1. The run artifact.** Every review body links the run artifact for its run.
Per finding it records: the model that produced it, the files the reviewer saw,
which instruction sections were used, token counts, the evidence-match level,
and the verdict of the verification pass. This answers "why did you say that".

**2. The drop log.** The same artifact answers "why didn't you say that". Every
finding killed by the evidence gate, the anchor check, an unverified external
claim, the comment budget, or a suppression is recorded there with its reason.
"The model found your bug and the evidence gate killed it" and "the model missed
it" are different failures with different fixes, and the drop log distinguishes
them.

**3. `cite doctor`.** For anything about instruction files — what was read, what
was ignored, what was classified as authoring rather than reviewable — run:

```
cite doctor
```

It prints, for a given path, which instruction files matched, in what order,
which sections survived triage, and which were classified as authoring. It also
warns when [CONFORMANCE.md](../CONFORMANCE.md) is over 90 days old.

**4. `cite soak`.** If a behaviour looks like a pipeline regression — schema
violations, anchors landing outside the diff, fingerprint churn, carry-forward
losses — reproduce it on the benchmark corpus:

```
cite soak bench/cases
```

`soak` is a regression harness, not a quality score: schema validity, anchor
placement, fingerprint stability across a reformat, incremental carry-forward,
latency and cost budgets, and a stability number across repeats. A case that
reproduces your bad review is a benchmark case — see
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Common cases

### Why was my finding dropped?

Open the run artifact and find the finding in the drop log. The usual reasons,
in frequency order:

- **The quote did not match the file** at any evidence level. The quote is the
  grounding check; a paraphrase fails it. This is by design — see
  [noise.md](noise.md).
- **The quote matched but did not intersect an added or modified line.** The
  reviewer comments on the change, not the file.
- **An external claim did not verify** — a path or symbol that does not exist,
  or a claim about version behaviour, which is rejected outright because it
  cannot be checked without network access.
- **The budget.** `N = clamp(3, 3 + floor(changed_lines / 250), 10)`, at most 2
  per file. The assembly call's cut reason is in the drop log.
- **The category is off.** `convention` and `error-swallow` are off unless
  `nits: true`.

### Why did nothing get posted?

- **Silence is a valid review.** Nothing to say means nothing posted — no
  comment, no "LGTM". The check run still concludes, with a one-line summary.
  Check the check run before assuming the workflow did not run.
- **Everything was skipped.** Generated files, lockfiles, vendored trees,
  minified output, and binaries are skipped with a named reason; `paths_ignore`
  adds to the list. Every skip appears on the check summary.
- **The pull request was already reviewed and nothing changed.** Incremental
  re-review re-reviews only files whose content changed, comparing content
  hashes rather than commits. Findings on untouched files carry forward; they do
  not re-post.

### The check run failed with a coverage error

Coverage failure means `COULD_NOT_EVALUATE`, which is fail-closed on purpose: a
review that did not read everything must not look like a clean pass. The check
summary names the cause:

- **A file errored or the provider was unavailable.** Retry the run; if it
  recurs, check the fallback configuration in
  [configuration.md](configuration.md#fallback).
- **`parse_failure`.** The model's response could not be decoded. Blank bodies
  and responses that fail strict JSON decoding (truncated output, single-quoted
  keys) are retried from the run-global bucket before giving up; a semantic
  schema violation or a wrong-path echo is not retried, because re-asking
  invites a matching quote for the same wrong claim.
- **`output truncated at token cap (finish_reason=length)`.** The review of that
  file did not fit in the output budget. This is deterministic, so it is not
  retried — it would truncate identically. Raise
  `roles.review.max_output_tokens`, or declare the model's real `max_tokens`
  under its provider entry; see
  [the output cap](configuration.md#the-output-cap). A truncated review is
  always reported as an error, never accepted as a short clean one.
- **Zero in-scope files.** A pull request that changed files but resolved to an
  empty in-scope set is treated as a possible path-filter bypass, never as a
  pass.
- **An unexpected skip.** A file skipped without one of the named reasons fails
  the gate. "Skipped" must never collapse into "clean".

A red coverage failure is recoverable by design. An absent check is not — the
gate job creates the check run first thing and always concludes it, even when
the review job dies.

### Rate limits

- **Model provider 429s.** Default concurrency is 6–8, capped at 16; lower the
  per-role `concurrency` in `.github/cite.yml`. A run-global retry token bucket
  absorbs transient acceleration limits; deterministic failures are not retried.
- **GitHub API limits.** Findings are posted as one review with a comments
  array — one content-generating request regardless of finding count — so the
  per-hour budget is rarely the constraint. If you share the token with other
  workflows on the same repository, the shared 1,000 requests/hour ceiling is;
  stagger them.

### Cache misses

Cost per review far above the expected range usually means the prompt cache is
missing. Causes, in order of likelihood:

- **Concurrency above the cache ceiling.** Above roughly 15 requests per minute
  per cache key, one provider's cache silently stops hitting; a naive high
  concurrency defeats its own caching.
- **Below the minimum prefix length.** A cached prefix below the provider's
  minimum (512–6,144 tokens depending on model) is skipped with no error.
- **Volatile content in the cached prefix.** Nothing run-specific — timestamps,
  run ids, nonces — belongs in the shared prefix; if it appears there, every run
  cold-misses.

The run artifact records cache counters; compare them between a cheap run and an
expensive one. Caching failures are silent by nature, which is why the counters,
not the bill, are the diagnostic.

### The bypass label

Anyone can apply the break-glass label to unblock a merge when the gate is wrong
or the provider is down. It is self-service, loud, and enumerable:

- The check concludes `success` with `BYPASSED — <state> — @author — <run url>`
  appended to a bypass log.
- The bypass buys time, not amnesty: a scheduled job re-reviews bypassed merge
  commits on the default branch afterwards and files an issue per finding.
- "Every pull request merged unreviewed on this date" is a one-line query over
  the log.

If bypasses cluster, that is signal: a category that is usually wrong, or a
provider that is usually down. Both are cheaper to fix than to bypass weekly.

## Still stuck

Open an issue with the run artifact attached (secrets never appear in it), plus
`cite doctor` output if instruction files are involved, and the `cite soak`
result if you can reproduce it.
