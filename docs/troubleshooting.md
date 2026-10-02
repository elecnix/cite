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
Per finding it records the model that produced it, the files that the
reviewer saw, which instruction sections were used, token counts, the
evidence-match level, and the verdict of the verification pass. This answers
"why did you say that".

**2. The drop log.** The same artifact answers "why didn't you say that". Every
finding killed by the evidence gate, the anchor check, an unverified external
claim, the comment budget, or a suppression is recorded there with its reason.
"The model found your bug and the evidence gate killed it" and "the model missed
it" are different failures with different fixes, and the drop log distinguishes
them.

**2b. The forensics archive.** When a review FAILS, the GitHub Action uploads a
`cite-forensics-<run id>-…` artifact (download it from the run's summary page):
the full step log plus `cite-run-record.json`, which carries the per-attempt
call log. It records when each model call started, how long it ran, how it ended
(`ok`, `deadline_exceeded`, `truncated`, …) and what it cost in tokens. This
answers "why did the run take so long / what did it burn" without the raw log,
which is truncated and unavailable for in-progress runs. When the failure was a
token-cap truncation, the archive also includes
`cite-truncated-response.json`: the raw response body the model was emitting
when it hit the cap. The review of that file is recorded as errored rather
than parsed, so this capture is the only place that content is kept. Set
`archive_on_failure: false` to disable. Note that a run killed mid-flight
still archives its partial record.

**2c. The wire capture (opt-in).** The forensics archive answers what a run
concluded and what it spent. It cannot answer what was actually exchanged with
the provider, because a response the review schema rejected leaves nothing
behind: the run log carries the error, the run record carries the call, and the
bytes that earned the error are gone. That is the whole diagnosis for a run
that ends in `response violated the review schema (schema: version 0, want 1)`,
which a consumer repository has seen: nothing in the artifact can tell whether
the provider ignored `response_format`, answered with a different schema, or ran
out of room mid-document.

Set the action's `capture_wire: true` input and the action uploads a
`cite-wire-<run id>-…` artifact, kept 7 days, on every run rather than only on
failure. It holds one document per model call plus a `manifest.json`:

| Field | What it is |
| -- | -- |
| `request.body` | The exact JSON body sent: the system prompt, the user payload with the diff under review, the `response_format` or `tools` payload, `temperature`, `max_tokens`. |
| `request_headers` | Every request header by name. Values are kept for an allowlist of five (`content-type`, `content-length`, `accept`, `user-agent`, `x-session-id`) and replaced everywhere else. |
| `response_headers` | Every response header, with credential-bearing names replaced and every value scrubbed. |
| `response.body` | The exact raw bytes that came back, before any decoding or parsing. |
| `response_status`, `response_status_text` | The HTTP status line. |
| `outcome` | `ok`, `parse-failure`, or `http-error`. A response the reviewer rejected carries `rejected_reason` with the parse error. |
| `model`, `provider_host`, `request_path` | Which model, which host, which path. Never a query string, never a userinfo. |
| `request.sha256`, `response.sha256` | Hashes of the exact bytes, so two runs of the same input can be compared without reading the prompt. |
| `usage` | The provider's token counters. |
| `redaction` | Which header names are masked, and how many values were masked in this document. |

`manifest.json` carries one row per call with the call index, model, provider
host, input hash, token counts and outcome, so a directory can be triaged
without opening a single document. A response the review schema rejected is
captured with outcome `parse-failure` and the bytes that were refused, which is
the case the feature exists for. On a token-cap truncation the same raw bytes
appear with outcome `http-error` and a note naming the cap, so this artifact
supersedes `cite-truncated-response.json` for diagnosis; the file is still
written, because a run that has not opted in depends on it.

**A capture contains the prompt, and the prompt contains the diff.** That is
the trade-off, stated plainly: the capture is the reviewer's whole input, so a
capture of a private repository is private review material. Credentials are
masked so that the directory is safe to share inside the repository, and
nothing else is. Keep the retention short, download what you need, and treat
the artifact as you would treat the pull request itself. The input is off by
default because of that, not because the feature is unfinished.

Masking is the one guarantee a capture makes about content. It is
allowlist-first, because a list of credential header names is a guess about
what a provider will send and providers invent headers:

- **request headers, by allowlist.** Cite chooses every header it sends, so
  the safe set is knowable: `content-type`, `content-length`, `accept`,
  `user-agent` and `x-session-id` keep their values, and every other header
  keeps its name and loses its value. A header name this repository has never
  seen is masked by the rule rather than by luck. Each document records the
  allowlist it was built with in `redaction.request_header_allowlist`;
- **response headers, by name and then by value.** The provider writes these,
  and they are the diagnosis (request ids, rate-limit counters, the upstream
  model), so they are not dropped by an allowlist. A name that reads like a
  credential is replaced (`Authorization`, `Proxy-Authorization`, `Cookie`,
  `Set-Cookie`, `api-key`, `x-api-key`, and anything containing `token`,
  `secret`, `password`, `credential` or `authenticate`), and every remaining
  value is scrubbed for the key and for inline credentials;
- **the value of your provider key is replaced everywhere**, in a body, a
  header or a URL, because a gateway that reflects a request header into its
  own error page is how a key would reach a file meant to be shareable. The
  key is a header and never enters a body ([security.md](security.md), I4), so
  this closes the reflection path rather than the ordinary one;
- **an inline `Bearer`, `Basic` or `token` credential inside a body is
  replaced** too, for the same reason;
- **the URL is recorded as host and path.** The query string is dropped whole,
  because a parameter name is not a reliable place to look for a credential;
- **a value shorter than 8 bytes is not searched for in bodies**, because
  masking a three-character key would corrupt the reviewed source it sits in.
  Each document records how many such values this run held in
  `redaction.secret_values_too_short_to_mask`.

A capture is read from the files the artifact carries. Cite never echoes one
into a job log, and the manifest holds sizes, hashes and counters only.

The capture is a passive observer. It never changes the verdict, the findings,
the coverage, the bounded per-file retry budget, or the fail-closed
`tool_failure_blocks` behaviour; a run with the capture on and a run with it off
reach the same conclusion.

Two knobs travel with it. `CITE_CAPTURE_DIR` sets where the documents go (the
action points it at `$RUNNER_TEMP/cite-wire`), and `CITE_CAPTURE_MAX_BYTES`
sets the per-body ceiling, 256 KiB by default. A body over the ceiling is cut
with `truncated`, `original_bytes` and a note naming the setting, and the
manifest row carries `request_omitted_bytes` and `response_omitted_bytes`, so
a cut is never silent. Set it in the workflow's own `env:`; neither knob needs
an input.

Outside CI, `CITE_CAPTURE_WIRE=1 cite review --diff patch.diff` writes the
same directory under the system temp directory, which is the quickest way to
see what your provider actually answered.

A successful run uploads `cite-run-record-<run id>-…` instead, kept for 14
days: the run record JSON alone. Set `archive_run_record: false` to skip it.
Both names end in the attempt, the job id and a random part, so two jobs of
one run never collide. Each entry in its `calls` array has the call's
`cache_read_tokens`, `cache_write_tokens`, the upstream `provider` that served
it, and the `prefix_bytes` and `prompt_bytes` that `cache_ceiling` comes from.
When the printed cache-hit rate is below 80% of the ceiling, look there first.
Several providers in one run mean the calls read several caches.

**3. `cite doctor`.** For anything about instruction files (what was read, what
was ignored, what was classified as authoring rather than reviewable), run:

```
cite doctor
```

It prints, for any path, which instruction files matched, in what order,
which sections survived triage, and which were classified as authoring. It also
warns when [CONFORMANCE.md](../CONFORMANCE.md) is over 90 days old.

**4. `cite soak`.** If a behaviour looks like a pipeline regression (schema
violations, anchors landing outside the diff, fingerprint churn, carry-forward
losses), reproduce it on the benchmark corpus:

```
cite soak bench/cases
```

`soak` is a regression harness. It reports schema validity, anchor placement,
fingerprint stability across a reformat, incremental carry-forward,
latency and cost budgets, and a stability number across repeats. A case that
reproduces your bad review is a benchmark case. See
[CONTRIBUTING.md](../CONTRIBUTING.md).

## Common cases

### Why was my finding dropped?

Open the run artifact and find the finding in the drop log. The usual reasons,
in frequency order:

- **The quote did not match the file** at any evidence level. The quote is the
  grounding check; a paraphrase fails it. This is by design: see
  [noise.md](noise.md).
- **The quote matched but did not intersect an added or modified line.** The
  reviewer comments on the change, not the file.
- **An external claim did not verify.** A path or symbol that does not exist,
  or a claim about version behaviour, is rejected outright because it cannot be
  checked without network access.
- **The budget.** `N = clamp(3, 3 + floor(changed_lines / 250), 10)`, at most 2
  per file. The assembly call's cut reason is in the drop log.
- **The category is off.** `convention` and `error-swallow` are off unless
  `nits: true`.

### Why did nothing get posted?

- **Silence is a valid review.** Nothing to say means nothing posted. There is
  no comment and no "LGTM". The check run still concludes, with a one-line
  summary. Check the check run before assuming the workflow did not run.
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
  retried: it would truncate identically. Raise
  `roles.review.max_output_tokens`, or declare the model's real `max_tokens`
  under its provider entry; see
  [the output cap](configuration.md#the-output-cap). A truncated review is
  always reported as an error, never accepted as a short clean one. The partial
  output is always captured to a file: cite writes the raw response body to
  `/tmp/cite-truncated-response.json` by default, overridable with
  `CITE_TRUNCATED_OUT`, and stderr always names the path and prints the
  partial content itself (the GitHub Action points the capture at
  `$RUNNER_TEMP` so the forensics archive uploads it). With the
  [`capture_wire` input](#2c-the-wire-capture-opt-in) on, the same bytes are in
  the wire capture next to the request that provoked them. With `CITE_DEBUG=1`
  the full raw response body is also dumped to
  `/tmp/cite-last-response.json`.
- **Zero in-scope files.** A pull request that changed files but resolved to an
  empty in-scope set is treated as a possible path-filter bypass, never as a
  pass.
- **An unexpected skip.** A file skipped without a listed reason fails
  the gate. "Skipped" must never collapse into "clean".

A red coverage failure is recoverable by design, while an absent check is not:
the gate job creates the check run first thing and always concludes it, even
when the review job fails.

### The provider ignores response_format

Some providers accept `response_format: {type: json_schema}` and then answer in
prose anyway. Ollama Cloud does exactly that, and it fails silently. The API
returns a successful response. The schema does not reach the model. The symptom
is `parse_failure` on every file, from a provider whose models otherwise look
capable. Set the action's `structured_output` input to `tools`:

```yaml
      - uses: elecnix/cite@<full-sha>
        with:
          structured_output: tools
          model_api_key: ${{ secrets.OLLAMA_API_KEY }}
          model_base_url: https://ollama.com/v1
          model_id: deepseek-v4.1-flash
```

In `tools` mode, Cite hands the model one function whose parameters are the
finding schema itself, and the model must call it. Cite reads the answer from
the call's argument. A rejection gets one follow-up turn. That turn contains the
rejected call and the validation error, and the next user message asks for a
proper call. When the model still refuses, the file ends as `parse_failure`,
like any other unusable answer. `response_format` stays the default. Both modes
share one schema and one validation pipeline, so findings do not change with
the mode.

Ollama also counts reasoning tokens against `max_tokens`, so a heavy reasoner
can spend the whole output budget thinking and never call the tool; Cite then
reports `output truncated at token cap`. The `reasoning_effort` input defaults
to `none` for that reason. An empty value omits the field, and `low`, `medium`
or `high` pass through to a provider that accepts them.

### My tool failures block the merge and I want them not to

By default a `COULD_NOT_EVALUATE` check run concludes `failure`, so a
repository that requires the check blocks the merge until the tool failure is
resolved. Some repositories prefer to block only on real findings: a
transient provider outage should not block every pull request. Set the
action input `tool_failure_blocks: false` in the workflow's `with:` block:

```yaml
with:
  model_api_key: ${{ secrets.MODEL_API_KEY }}
  tool_failure_blocks: false
```

With that set, a tool failure still publishes `COULD_NOT_EVALUATE` on the
check run (the title and summary say exactly what failed), but the check-run
conclusion is `neutral`, which GitHub counts as a satisfied required check,
so the pull request is not blocked. A real finding (`FOUND`) always concludes
`failure` and blocks, whether or not the repository opted out: the opt-out
covers "the tool could not read its own output", never "the tool found
something". The default remains `true` (red), so repositories that do
nothing keep fail-closed behaviour.

### Rate limits

- **Model provider 429s.** Default concurrency is 6 to 8, capped at 16; lower
  the per-role `concurrency` in `.github/cite.yml`. A run-global retry token
  bucket absorbs transient acceleration limits. Deterministic failures are not
  retried.
- **GitHub API limits.** Findings are posted as one review with a comments
  array (one content-generating request regardless of finding count), so the
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
  minimum (512 to 6,144 tokens depending on model) is skipped with no error.
- **Volatile content in the cached prefix.** Nothing run-specific (timestamps,
  run ids, nonces) belongs in the shared prefix; if it appears there, every run
  cold-misses.

The run artifact includes cache counters; compare them between a cheap run and
an expensive one. Caching failures are silent by nature, so the counters,
not the bill, are the diagnostic.

### The bypass label

Anyone can apply the break-glass label to unblock a merge when the gate is wrong
or the provider is down. It is self-service, loud, and enumerable:

- The check concludes `success` with `BYPASSED — <state> — @author — <run url>`
  appended to a bypass log.
- The bypass unblocks the current merge, and a scheduled job re-reviews bypassed
  merge commits on the default branch afterwards and files an issue per finding.
- "Every pull request merged unreviewed on this date" is a one-line query over
  the log.

If bypasses cluster, that is signal: a category that is usually wrong, or a
provider that is usually down. Both are cheaper to fix than to bypass weekly.

## Still stuck

Open an issue with the run artifact attached (secrets never appear in it), plus
`cite doctor` output if instruction files are involved, and the `cite soak`
result if you can reproduce it.
