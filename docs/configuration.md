# Configuration

Cite is configurable and optional about it. A repository that never writes a
configuration file gets sensible behaviour forever: the default model comes from the
ambient key, and a comment budget plus the standard skip list apply. The
current compatibility profile sets the instruction formats.

When you do want to change something, the file is `.github/cite.yml`. This page
is the complete v1 surface. A machine-level file accepts the same keys, and a
run reads it after the repository one ([Operator settings](#operator-settings)).

## Configuration keys

```yaml
# .github/cite.yml (optional). This is the complete v1 surface.
model: openai/gpt-5-mini      # one string; a role map is available below
max_comments: 10              # hard-capped at 20 by the schema
paths_ignore: ["**/*.gen.go", "vendor/**"]
nits: false                   # style and test-gap findings, default off
gate: comment                 # accepted but reserved (issue #86); selects nothing
compat_profile: "2026-08"     # which snapshot of the instruction formats to honour
```

| Key | Default | Meaning |
| -- | -- | -- |
| `model` | inferred from the available key | One string identifying the model for the review pass. See [Roles](#roles) when one string is not enough. |
| `max_comments` | `10` | Upper bound on comments per review. **Hard-capped at 20 by the schema**; values above 20 are rejected by [`cite validate`](#validation), not clamped silently. The per-run budget formula in [noise.md](noise.md#the-budget) can only lower this number, never raise it. |
| `paths_ignore` | `[]` | Extra glob patterns added to the built-in skip list (generated files, lockfiles, vendored trees, minified output, binaries). See the [glob dialect](instructions.md#glob-dialect). It uses the same dialect instruction `applyTo` uses and the same matcher (`internal/glob`), so a pattern cannot behave differently here. A skipped file is never a passed file. Every skip appears on the check summary with its reason. |
| `nits` | `false` | Enables `convention` and `error-swallow` findings. Off by default, and they do not consume a comment budget unless enabled. `convention` findings can never block a merge in any configuration. |
| `gate` | *(none)* | Accepted and validated, but reserved on the decision path (issue #86): `FOUND` concludes `failure` under every accepted value, so there is no shadow mode and no mode switch. The only conclusion knob is the action's `tool_failure_blocks` input, which governs `COULD_NOT_EVALUATE`. A real finding always blocks. The key exists so a config can store the value without silently dropping a future decision input. |
| `compat_profile` | `"2026-08"` | Which dated snapshot of instruction-file behaviour Cite honours. Never auto-updates. See [CONFORMANCE.md](../CONFORMANCE.md). |
| `require_parameters` | `false` | Ask OpenRouter-style routers to route only to endpoints that support every request parameter. See [Require parameters](#require-parameters). |

Seven keys. A v1 with more than ten has already lost.

The keys above say how a review reads code. The action's inputs live in the
workflow instead, and two of them say what a run archives: `archive_on_failure`
uploads the forensics archive when the review fails, and `capture_wire` uploads
the wire capture on every run, whether it passed or failed.

`capture_wire` is now also a key, under [Operator settings](#operator-settings).
This list left it out on purpose while a config file was the only place a
setting could live: a switch an operator flips while diagnosing one failure
should not need a commit. A machine-level file and a flag live outside the
repository too, so that reason expired, and a repository can set the switch in
its own file if it wants to.

## Operator settings

A repository file holds policy. The same schema also describes the settings that
say how a run reaches its model and what it writes, and each of them has a flag
and an environment variable as well. One ladder resolves them, strongest
first:

1. the command-line flag;
2. an environment variable;
3. configuration files, in the order given, later overriding earlier;
4. the built-in default.

Environment sits above the files so a one-off `MODEL_ID=… cite review` behaves
the same way on a machine that has a configuration file.

| Flag | Variable | Key |
| -- | -- | -- |
| `--model-id` | `MODEL_ID` | `model_id` |
| `--model-base-url` | `MODEL_BASE_URL` | `model_base_url` |
| `--model-api-key` | `MODEL_API_KEY` | `model_api_key` |
| `--fallback-models` | `CITE_FALLBACK_MODELS` | `fallback_model_ids` |
| `--debug` / `--no-debug` | `CITE_DEBUG` | `debug` |
| `--truncated-out` | `CITE_TRUNCATED_OUT` | `truncated_out` |
| `--capture-wire` / `--no-capture-wire` | `CITE_CAPTURE_WIRE` | `capture_wire` |
| `--capture-dir` | `CITE_CAPTURE_DIR` | `capture_dir` |
| `--capture-max-bytes` | `CITE_CAPTURE_MAX_BYTES` | `capture_max_bytes` |
| `--verify` / `--no-verify` | `CITE_VERIFY` | `verify` |
| `--related-code` / `--no-related-code` | `CITE_RELATED_CODE` | `related_code` |
| `--record-out` | `CITE_RECORD_OUT` | `record_out` |
| `--structured-output` | `CITE_STRUCTURED_OUTPUT` | `structured_output` |
| `--reasoning-effort` | `CITE_REASONING_EFFORT` | `reasoning_effort` |
| `--require-parameters` | `CITE_REQUIRE_PARAMETERS` | `require_parameters` |

`fallback_model_ids` is not the `fallback` key. Both are lists of model ids to
try after the primary, in that order, and they differ in the references they accept: an
entry in `fallback_model_ids` is always a model id at the primary's own endpoint,
while an entry in `fallback` may also be a `provider/modelid` reference to a
provider you declared (see [Fallback](#fallback)). The flag and the action's `fallback_model_ids` input set
this list, and the client tries it before the `fallback` chain.

The boolean settings take two flags, because a file may set a value that the
command line wants to overturn. A boolean variable counts as on unless it is
empty or one of `0`, `false`, `off` or `no`, which is how the variables behaved
before the flags existed. `CITE_REQUIRE_PARAMETERS` is the exception: only `1`,
`true`, `TRUE` and `True` turn it on, and it combines with the file's
`require_parameters` key rather than replacing it, so a repository that requires
the field keeps that requirement when the action passes its own default-off
input.

`--model-id` sets the id that the client sends. It is not the `model:` key: that
key says which model a role resolves to for the capability table, while
`--model-id` says what crosses the wire. A run that sets one and not the other
can disagree with the file, and the GitHub Action's `model_id` input sets this
flag's variable.

When a run compares models that differ in the capabilities it reads, it has to
move both. The flag sets the id that crosses the wire, and the file sets the
capability row: its output cap, its cost rates and its role models.

```yaml
# .github/cite.yml
model: gateway/vendor/model-x   # the capability row the run reads
providers:
  gateway:
    base_url: https://gw.example/v1
    api: openai-completions
    models:
      - id: vendor/model-x
        max_tokens: 65536
        cost: { input: 1, output: 4 }
```

```console
cite review --pr owner/repo#42 --model-id vendor/model-x
```

A flag alone sends the new id's requests while the run keeps the old row's cap
and rates, and the cost report follows the row rather than the response.

### The file list

`--config` takes a file, and the flag may appear more than once:

```
cite review --pr owner/repo#42 --config .github/cite.yml --config ~/.config/cite/config.yml
```

`CITE_CONFIG` supplies the same list, separated by the platform's path
separator, and a `--config` flag takes precedence over it. With neither, a run
reads `.github/cite.yml` and then `~/.config/cite/config.yml` when that file
exists. Passing `--config` at all replaces the default list, so a wrapper reads
exactly the files it names and nothing arrives from the home directory behind
its back. In CI the action passes its own `--config`, which is the file it
fetched from the base ref, so a runner never reads a home directory.

Cite skips a file that is not on disk, which is the same "no configuration"
case as before.

### Merge rules

- A scalar in a later file replaces the earlier value.
- A mapping merges per entry and per field within an entry. A later file that
  names one provider leaves the other providers alone.
- A sequence replaces the earlier one wholesale. A file that cannot shorten a
  list cannot undo an earlier one.

### Validation

Cite checks each file against the schema on its own. An unknown key, a duplicate
key and a type error fail with the file's name and the line that holds the
offending key. Cite validates the merged result once more, because a
combination can be invalid while each part is fine. A `fallback` entry whose provider only the other file
declares is valid, and a `max_comments` of 30 is not valid whichever file set it.

### Provenance

Layering has one cost: a value can come from a file nobody remembers writing.
`--effective` and one log line make that cost visible.

```
cite validate --config .github/cite.yml --config ~/.config/cite/config.yml --effective
```

prints every key of the merged result with the file it came from, one key per
line. Where no file set a value, the line says `built-in default`. Where a flag
or a variable set one, the line names that source instead. Cite never prints a
credential: an `api_key` line shows the reference, or says the value is a
literal, and a header value appears as `[masked]`.

A run also prints one line whenever a file other than the first one changes a
key that decides the verdict:

```
config override: max_comments: 4 -> 8 (from /home/you/.config/cite/config.yml; ...)
```

Cite applies the value, because the file is the operator's own. A local run
that judges a diff differently from CI has to say so out loud.

### Secrets

`model_api_key` accepts a pointer to a secret and nothing else. An operator
file is often a dotfile, and a dotfiles repository publishes the contents of
every file it tracks:

```yaml
model_api_key: $OPENROUTER_API_KEY   # $VAR, ${VAR} or !command
```

Validation rejects a literal in `model_api_key`. The
`providers.<name>.api_key` key keeps every expression form, literal included,
because provider blocks predate this rule.

## Providers

The configuration keys above cover almost everyone; the `providers` block
exists for a private gateway or a local model, and for providers that need
extra headers. It lives on this page rather than the front page
because most repositories never open it.

```yaml
providers:
  gateway:
    base_url: https://gateway.example.invalid/v1
    api: openai-completions          # openai-completions | openai-responses | anthropic-messages
    api_key: $MODEL_API_KEY          # literal | $VAR | ${VAR} | !shell-command
    headers:
      x-tenant: acme
    models:
      - id: vendor/model-x           # the only required field
        context_window: 262144
        max_tokens: 8192
        cost: { input: 5, output: 30, cache_read: 0.5, cache_write: 6.25 }
```

### `base_url`

The endpoint that Cite calls is read from the base ref, never the pull
request head. A config file that a pull request can modify cannot redirect your source
code and your key to an attacker's endpoint. See [security.md](security.md).

### `api`

One of three wire protocols:

- `openai-completions`
- `openai-responses`
- `anthropic-messages`

Without a `providers` block, the provider is inferred from which environment key
is present.

### `api_key`

A credential **expression**, never a literal secret you have to paste:

| Form | Example | Meaning |
| -- | -- | -- |
| literal | `api_key: sk-not-a-real-key` | Used as-is. Works, but there is rarely a reason. |
| `$VAR` | `api_key: $MODEL_API_KEY` | Read from the environment. |
| `${VAR}` | `api_key: ${MODEL_API_KEY}` | Same, braced. |
| shell command | `api_key: !op read op://vault/item/key` | Output of the command. Lets the file hold a vault reference instead of a secret. |

The key is present only as an HTTP header. It never enters a prompt, a log, a run
artifact, or an error message. See [security.md](security.md).

### `headers`

Extra HTTP headers sent with every call to this provider. Static values only;
this is not a credential channel.

### `models`

Each entry has exactly **one required field, `id`**. Everything else defaults:

- `context_window`: input token limit
- `max_tokens`: default output cap for calls using this model. Overrides the
  built-in default in either direction; see
  [the output cap](#the-output-cap).
- `cost`: per-million-token rates: `input`, `output`, `cache_read`,
  `cache_write`

When the provider reports a cost for each call, as OpenRouter does in
`usage.cost`, Cite reports that sum and ignores the declared rates. You don't
need a `cost` block for a model you reach through OpenRouter. Declared rates
give a cost for a provider that reports only token counts, including a model
Cite has never heard of. With neither, the run reports `cost: unknown` and
writes a note to the log. It does not report $0. A provider that bills in
aggregate, such as Ollama Cloud, charges for those tokens whatever Cite can
see, so an unpriced run has an undetermined cost rather than a free one. A
run without a token count has nothing to price, and reports $0.

## Roles

One model string is enough for small teams. A reviewer actually runs three
distinct jobs, and they have different tolerances:

```yaml
roles:
  review:   { model: gateway/vendor/model-x, timeout: 90s, max_output_tokens: 8192, concurrency: 8 }
  triage:   { model: gateway/cheap-model,    timeout: 30s }
  assemble: { model: gateway/cheap-model,    timeout: 60s }
```

- **`review`**: the expensive pass over flagged files. Longest timeout, largest
  output budget.
- **`triage`**: the cheap whole-diff pass that decides which files deserve the
  expensive pass. Short timeout; if triage fails, the run fails closed.
- **`assemble`**: the final capping pass that enforces the comment budget. See
  [noise.md](noise.md#the-assembly-call-is-a-capper).

Timeouts and concurrency are **per role**, not global, because a slow local model
and a fast hosted one cannot share one number. Unspecified fields inherit sane
defaults. `concurrency` is capped at 16 regardless of configuration.

### The review timeout derives from the output cap

`review` has no fixed timeout default. When `roles.review.timeout` is unset,
the deadline is derived from the same resolved output cap the call is bounded
by (issue #28), floored at 15 minutes:

```
timeout = max(60s base + max_output_tokens ÷ 128 tok/s, 15 minutes)
```

The assumed generation rate (128 tokens per second) is conservative on purpose:
it sizes for a mid-tier hosted model. A faster provider simply finishes early;
sizing on a fast rate would turn slow-but-correct runs into deadline failures.
The floor exists because a wall-clock cap is a safety net for a hung call. The
built-in default finishes comfortably past it, and the floor only matters when
a call is stuck.

- the built-in **131072**-token cap derives 60s + 1024s ≈ 1084s ≈ **18
  minutes**, already past the floor;
- only caps above ≈107,000 tokens outgrow the floor;
- an explicit `roles.review.max_output_tokens` or a model entry's `max_tokens`
  moves the deadline with it.

An explicit `roles.review.timeout` always wins over the derivation. Triage and
assemble keep fixed defaults of **15 minutes** each; their output is bounded
regardless of file size. The defaults are loose because providers queue
requests and can stall long before the first byte: openrouter-hosted models
routinely sit in queue for minutes. Since a deadline expiry is now terminal
(see below), the patience is free: a slow-but-correct call finishes, and only
a hung one is cut off.

When a call does hit its deadline, the run does NOT retry it: the first
attempt already burned its full wall-clock budget and the provider's tokens,
and a re-issue pays twice for the same wait. The failure message states the two
adjustments instead: raise `roles.<role>.timeout`, or lower the output cap
that drives the derived review deadline. The GitHub Action archives the run
record (per-call timings, token usage, cache counters) on every run, and the
full forensics archive on a failed one.

### The output cap

The output cap resolves most-specific-first, in three layers:

1. `roles.<role>.max_output_tokens`: an explicit instruction, and it wins
   outright. Cite will not silently shrink a number you wrote down.
2. the `max_tokens` of the model that the role resolves to: what the model says
   it can emit. This both raises the cap on a roomy model and lowers it on a
   narrow one, and it is the only way Cite can know a ceiling it cannot query.
3. the built-in default: **131072** for `review`, 8192 for `triage`.

The review default is sized for the schema's worst case: `max_comments`'s hard
cap of 20 findings, each with a title, body, impact, quoted evidence and an
optional fix, plus whatever reasoning tokens the provider bills against the same
budget. It was raised from 32768 to 131072 because big single-file diffs and
large briefing-style inputs truncated identically at the old default
(`finish_reason=length`, a deterministic failure). Cite's own self-review pinned
65536 on top of it, and a roughly 100 KB briefing file still ran out of room.
131072 gives the worst case four times the room it needs.

**The cap is an ANSWER budget, and on a reasoning model it is not only that.** Providers that bill reasoning tokens against `max_tokens` spend them out of the same number, so the surplus above the answer's worst case is not spare room. It is thinking budget handed to the model. Give that surplus to a model that will not stop and it takes everything, then hands back no answer. One budget covers both halves of the job, so raising the cap enlarges that failure. Cite tells this case apart from a real overflow and spends one bounded retry on it (see
[the model never stops](troubleshooting.md#the-model-never-stops-and-i-get-could_not_evaluate))
rather than losing the file to a terminal deterministic failure.

**If the cap is too small, the file errors and is never quietly shortened.** A
response cut off at the cap comes back as `finish_reason=length`, which is a
deterministic failure: coverage is incomplete because the file is recorded as
errored, so the gate reports `COULD_NOT_EVALUATE` rather than a clean pass on
a review that stopped halfway. If your model advertises a smaller ceiling than
the default, declare its `max_tokens` under the provider's `models` entry.

## Fallback

```yaml
fallback: [gateway/vendor/model-x, other/backup-model]
```

An ordered list. An entry here is a `provider/modelid` reference to a provider
you declared, or a bare model id at the primary endpoint. The separate
`fallback_model_ids` key ([Operator settings](#operator-settings)) holds bare
ids only, and the client tries that list first. When the primary model fails a review call, the call moves to
the next leg. A runaway generation moves it, and so does a model the endpoint no
longer serves. An output overflow, a provider outage or rate limit, and an
unreachable endpoint move it too.
A deadline does not move, because the next leg would start with the same
expired clock. The action's `fallback_model_ids` input adds models at the
primary's own endpoint ahead of this list, which is the short form when one
provider serves several models:

```yaml
- uses: elecnix/cite@db01467aae47c33f16ec6b404d3cdbd13ec94f12  # v0.15.1
  with:
    model_id: deepseek-v4.1-flash
    fallback_model_ids: glm-5.2,kimi-k2.6
```

A leg with another leg behind it gets at most 32768 output tokens, and the last
leg gets the full cap. A runaway spends its whole budget before it can fail over,
so the smaller first budget ends it four times sooner. The rare review that
needs more overflows and fails over as well.

For a merge gate, provider outage is the first operational failure you will
hit, so the chain is first-class configuration rather than an afterthought.

- **The chain is exercised by a canary.** A scheduled job calls every leg of the
  chain, because an untested fallback is not a fallback but a second outage that
  begins at the same moment as the first.
- **A failover is disclosed.** The run log records the model that failed and
  the leg that served the call, and the run record's per-call log carries the
  serving model, so a quality change after a failover is diagnosable rather
  than mysterious.

## Require parameters

```yaml
require_parameters: true   # default false
```

Cite sends `response_format: json_schema` (strict) on review and triage calls.
When the base URL is a router such as OpenRouter, routing may pick an endpoint
that does not support structured outputs; OpenRouter then drops the parameter
or returns an empty body, which comes back as an unparsable model response at
a non-zero cost.

Setting `require_parameters: true` adds a top-level `provider:
{require_parameters: true}` field to chat completion requests, so the router
only chooses endpoints that support **all** request parameters. Use it when you
call OpenRouter with models whose endpoints inconsistently support structured
outputs.

If no endpoint for your model supports your parameter set, the call fails with
an explicit routing error instead of silently degraded output. That failure is
the point: a clean error is cheap and names its cause, while a dropped schema
produces garbage that looks like a parse bug.

## Validation

```
cite validate
cite validate .github/cite.yml ~/.config/cite/config.yml
```

Schema-checks each file and then the merged result: unknown keys, bad enum
values, `max_comments` above the hard cap, unresolvable credential expressions.
It exits non-zero on any problem and prints what it found, with the file and the
line. `--effective` prints the merged configuration with its provenance
([above](#provenance)).

It does **not** check `paths_ignore` globs. A glob is not a schema error, and
whether a pattern matches anything depends on the pull request rather than on
the config file. `cite doctor` answers that instead, printing the canonical
form of each pattern and which paths it matched.

Cite provides a JSON Schema for its own config and validates against it from
day one. A typo in a configuration key is rejected loudly; it is never silently
ignored, because a silently ignored suppression or gate setting is a merge gate
that is not doing what its owner believes.

## Fetching the config file

The action fetches the file named by `config_path` from the pull request's
**base ref** before the review step (never from the pull request head or
the merge ref), so a pull request cannot rewrite its own review settings or
redirect `base_url` to an attacker endpoint
([security.md](security.md#i3--everything-controlling-the-model-call-or-verdict-is-read-from-the-base-ref)).
The base ref is authoritative whenever a pull request context exists: a
workflow that also runs `actions/checkout` materializes the merge ref, so a
config file already on the runner can be authored by the pull request
itself, and the action fetches over it (a local file is only consulted
when there is no base ref at all to fetch from, e.g. a workflow that
checks out and runs on push). Each run logs exactly one line that
identifies the source used:

- `cite: config source: local file` (no pull request context, so the file
  already on the runner cannot be head-controlled).
- `cite: config <path> fetched from base ref <ref>` (fetched via the
  GitHub contents API with the ambient token (`contents: read`)).
- `cite: no <path> on base ref; running with defaults` (or, when a
  checkout left a file behind, `… ignoring the local file from the checkout
  (pull request context) and running with defaults`). The base ref has no
  config file. This is not an error; the run continues with the defaults
  on this page. Before this fetch ran, that case was silent: every tuned
  key was dropped without a trace in the logs.

A fetch or decode failure other than "no such file" is a hard error and fails
the step, so the wrong settings are never silently applied. Retries cover
transient HTTP failures. A 403 rate limit, a 429, or a 5xx is retried up to 3
attempts total with a 2-second pause between attempts (log line: `cite:
transient failure fetching <path> (attempt n/3) ...`). A 404 and a permanent
failure such as a 401 are terminal on the first attempt. Exhausted retries
still fail the step. A failed fetch is never treated as "no config".

## What is deliberately absent

- No severity thresholds. There is no severity scale; blocking is computed in
  code from verifiable fields ([noise.md](noise.md)).
- No prompt overrides. Prompts are versioned files in this repository, changed
  only with a benchmark delta ([CONTRIBUTING.md](../CONTRIBUTING.md)).
- No suppressions file in v1. A file that does not exist cannot rot; dismissals
  live in a ledger governed by the dispute protocol
  ([security.md](security.md#the-dispute-protocol)).
- No new action input for the operator settings. The happy path runs without any
  input. The action already has inputs for the ones that predate this page:
  `model_id`, `model_base_url`, `model_api_key`, `fallback_model_ids` and
  `capture_wire`. The rest are flags or variables.
