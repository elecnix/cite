# Configuration

Cite is configurable and optional about it. A repository that never writes a
configuration file gets sensible behaviour forever: default model selection from
the ambient key, a comment budget, the standard skip list, `gate: comment`, and
the current compatibility profile.

When you do want to change something, the file is `.github/cite.yml`. This page
is the complete v1 surface.

## The seven keys

```yaml
# .github/cite.yml — optional. This is the complete v1 surface.
model: openai/gpt-5-mini      # one string; a role map is available below
max_comments: 10              # hard-capped at 20 by the schema
paths_ignore: ["**/*.gen.go", "vendor/**"]
nits: false                   # style and test-gap findings, default off
gate: comment                 # comment | block
compat_profile: "2026-08"     # which snapshot of the instruction formats to honour
```

| Key | Default | Meaning |
| -- | -- | -- |
| `model` | inferred from the available key | One string identifying the model for the review pass. See [Roles](#roles) when one string is not enough. |
| `max_comments` | `10` | Upper bound on comments per review. **Hard-capped at 20 by the schema**; values above 20 are rejected by [`cite validate`](#validation), not clamped silently. The per-run budget formula in [noise.md](noise.md#the-budget) can only lower this number, never raise it. |
| `paths_ignore` | `[]` | Extra glob patterns added to the built-in skip list (generated files, lockfiles, vendored trees, minified output, binaries). See the [glob dialect](instructions.md#glob-dialect). A skipped file is never a passed file — every skip appears on the check summary with its reason. |
| `nits` | `false` | Enables `convention` and `error-swallow` findings. Off by default, and they consume no comment budget unless enabled. `convention` findings can never block a merge in any configuration. |
| `gate` | `comment` | `comment` posts findings as a non-blocking review plus a check run that always concludes `success` or `neutral`. `block` makes the check run conclude `failure` when a finding blocks. Turn blocking on after a month of shadowing the tool's output. |
| `compat_profile` | `"2026-08"` | Which dated snapshot of instruction-file behaviour Cite honours. Never auto-updates. See [CONFORMANCE.md](../CONFORMANCE.md). |
| `require_parameters` | `false` | Ask OpenRouter-style routers to route only to endpoints that support every request parameter. See [Require parameters](#require-parameters). |

Seven keys. If v1 ships with more than ten it has already lost.

## Providers

The seven keys cover almost everyone. When they do not — a private gateway, a
local model, a provider that needs extra headers — the `providers` block exists.
It lives on this page rather than the front page because most repositories never
open it.

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

The endpoint Cite calls. Read from the base ref, never the pull request head —
a config file on a pull request branch cannot redirect your source code and your
key to an attacker's endpoint. See [security.md](security.md).

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

The key travels as an HTTP header only. It never enters a prompt, a log, a run
artifact, or an error message. See [security.md](security.md).

### `headers`

Extra HTTP headers sent with every call to this provider. Static values only;
this is not a credential channel.

### `models`

Each entry has exactly **one required field, `id`**. Everything else defaults:

- `context_window` — input token limit
- `max_tokens` — default output cap for calls using this model. Overrides the
  built-in default in either direction; see
  [the output cap](#the-output-cap).
- `cost` — per-million-token rates: `input`, `output`, `cache_read`,
  `cache_write`

Cost as first-class configuration means cost reporting works for a model Cite
has never heard of.

## Roles

One model string is enough for small teams. A reviewer actually runs three
distinct jobs, and they have different tolerances:

```yaml
roles:
  review:   { model: gateway/vendor/model-x, timeout: 90s, max_output_tokens: 8192, concurrency: 8 }
  triage:   { model: gateway/cheap-model,    timeout: 30s }
  assemble: { model: gateway/cheap-model,    timeout: 60s }
```

- **`review`** — the expensive pass over flagged files. Longest timeout, largest
  output budget.
- **`triage`** — the cheap whole-diff pass that decides which files deserve the
  expensive pass. Short timeout; if triage fails, the run fails closed.
- **`assemble`** — the final capping pass that enforces the comment budget. See
  [noise.md](noise.md#the-assembly-call-is-a-capper).

Timeouts and concurrency are **per role**, not global, because a slow local model
and a fast hosted one cannot share one number. Unspecified fields inherit sane
defaults; `concurrency` is capped at 16 regardless of configuration.

### The review timeout derives from the output cap

`review` has no fixed timeout default. When `roles.review.timeout` is unset,
the deadline is derived from the same resolved output cap the call is bounded
by (issue #28), floored at 15 minutes:

```
timeout = max(60s base + max_output_tokens ÷ 128 tok/s, 15 minutes)
```

The assumed generation rate (128 tokens per second) is deliberately
conservative — sized for a mid-tier hosted model, not a top-tier endpoint. A
faster provider simply finishes early; sizing on a fast rate would turn
slow-but-correct runs into deadline failures. The floor exists because a
wall-clock cap is a safety net for a hung call, not a tuning knob: realistic
caps finish well inside 15 minutes, and the floor only matters when a call is
genuinely stuck.

- the built-in **32768**-token cap derives ≈316s, which the floor raises to
  **15 minutes**;
- only caps above ≈107,000 tokens outgrow the floor;
- an explicit `roles.review.max_output_tokens` or a model entry's `max_tokens`
  moves the deadline with it.

An explicit `roles.review.timeout` always wins over the derivation. Triage and
assemble keep fixed defaults of **15 minutes** each; their output is bounded
regardless of file size. The defaults are generous because providers queue
requests and stall long before the first byte — openrouter-hosted models
routinely sit in queue for minutes — and because a deadline expiry is now
terminal (see below), the patience is free: a slow-but-correct call finishes,
and only a genuinely hung one is cut off.

When a call does hit its deadline, the run does NOT retry it: the first
attempt already burned its full wall-clock budget and the provider's tokens,
and a re-issue pays twice for the same wait. The failure names the knobs
instead — raise `roles.<role>.timeout`, or lower the output cap that drives
the derived review deadline. The failed run's record (per-call timings, token
usage) is archived by the GitHub Action for forensics.

### The output cap

The output cap resolves most-specific-first, in three layers:

1. `roles.<role>.max_output_tokens` — an explicit instruction, and it wins
   outright. Cite will not silently shrink a number you wrote down.
2. the `max_tokens` of the model the role resolves to — what the model says it
   can emit. This both raises the cap on a roomy model and lowers it on a narrow
   one, and it is the only way Cite can know a ceiling it cannot query.
3. the built-in default: **32768** for `review`, 8192 for `triage`.

The review default is sized for the schema's worst case — `max_comments`'s hard
cap of 20 findings, each with a title, body, impact, quoted evidence and an
optional fix, plus whatever reasoning tokens the provider bills against the same
budget.

**If the cap is too small, the file errors; it is never quietly shortened.** A
response cut off at the cap comes back as `finish_reason=length`, which is a
deterministic failure: the file is recorded as errored, and coverage is
incomplete, so the gate reports `COULD_NOT_EVALUATE` rather than a clean pass on
a review that stopped halfway. If your model advertises a smaller ceiling than
the default, declare its `max_tokens` under the provider's `models` entry.

## Fallback

```yaml
fallback: [gateway/vendor/model-x, other/backup-model]
```

An ordered list. If the primary provider is unavailable, the next leg serves the
run. For a merge gate, provider outage is the first operational failure you will
hit, so the chain is first-class configuration rather than an afterthought.

Two properties:

- **The chain is exercised by a canary.** A scheduled job calls every leg of the
  chain, because an untested fallback is not a fallback but a second outage that
  begins at the same moment as the first.
- **A failover is disclosed.** The run artifact records which leg served each
  call, so a quality change after a failover is diagnosable rather than
  mysterious.

## Require parameters

```yaml
require_parameters: true   # default false
```

Cite sends `response_format: json_schema` (strict) on review and triage calls.
When the base URL is a router such as OpenRouter, routing may pick an endpoint
that does not support structured outputs; OpenRouter then drops the parameter
or returns an empty body, which surfaces as an unparsable model response at a
non-zero cost.

Setting `require_parameters: true` adds a top-level `provider:
{require_parameters: true}` field to chat completion requests, so the router
only chooses endpoints that support **all** request parameters. Use it when you
call OpenRouter with models whose endpoints inconsistently support structured
outputs.

The trade-off: if no endpoint for your model supports your parameter set, the
call fails with an explicit routing error instead of silently degraded output.
That failure is the feature — a clean error costs nothing and names its cause,
while a dropped schema produces garbage that looks like a parse bug.

## Validation

```
cite validate
```

Schema-checks `.github/cite.yml`: unknown keys, bad enum values, `max_comments`
above the hard cap, malformed globs, unresolvable credential expressions. It
exits non-zero on any problem and prints what it found.

Cite ships a JSON Schema for its own config and validates against it from day
one. A typo in a configuration key is rejected loudly; it is never silently
ignored, because a silently ignored suppression or gate setting is a merge gate
that is not doing what its owner believes.

## Where the config file comes from

The action fetches the file named by `config_path` from the pull request's
**base ref** before the review step — never from the pull request head or
the merge ref, so a pull request cannot rewrite its own review settings or
redirect `base_url` to an attacker endpoint
([security.md](security.md#i3--everything-controlling-the-model-call-or-verdict-is-read-from-the-base-ref)).
The base ref is authoritative whenever a pull request context exists: a
workflow that also runs `actions/checkout` materializes the merge ref, so a
config file already on the runner can be authored by the pull request
itself, and the action fetches over it (a local file is only consulted
when there is no base ref at all to fetch from — e.g. a workflow that
checks out and runs on push). Each run logs exactly one line naming the
source used:

- `cite: config source: local file` — no pull request context, so the file
  already on the runner cannot be head-controlled.
- `cite: config <path> fetched from base ref <ref>` — fetched via the
  GitHub contents API with the ambient token (`contents: read`).
- `cite: no <path> on base ref; running with defaults` (or, when a
  checkout left a file behind, `… ignoring the local file from the checkout
  (pull request context) and running with defaults`) — the base ref has no
  config file. This is not an error; the run continues with the defaults
  on this page. Before this fetch ran, that case was silent: every tuned
  key was dropped without a trace in the logs.

A fetch or decode failure other than "no such file" is a hard error and fails
the step, rather than quietly reviewing with the wrong settings.
Retries cover transient HTTP failures. A 403 rate limit, a 429, or a 5xx is
retried up to 3 attempts total with a 2-second pause between attempts (log
line: `cite: transient failure fetching <path> (attempt n/3) ...`). A 404
and a permanent failure such as a 401 are terminal on the first attempt.
Exhausted retries still fail the step; a failed fetch is never treated as
"no config".

## What is deliberately absent

- No severity thresholds. There is no severity scale; blocking is computed in
  code from verifiable fields ([noise.md](noise.md)).
- No prompt overrides. Prompts are versioned files in this repository, changed
  only with a benchmark delta ([CONTRIBUTING.md](../CONTRIBUTING.md)).
- No suppressions file in v1. A file that does not exist cannot rot; dismissals
  live in a ledger governed by the dispute protocol
  ([security.md](security.md#the-dispute-protocol)).
- No `with:` inputs beyond the API key. The happy path needs none.
