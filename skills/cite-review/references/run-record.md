# The run record

`record.json` is Cite's `model.RunRecord`. The gate decides from it, and it
answers "why did you say that" for every finding. `report.json` holds the same
object under `run`, next to `review` (the body and the inline comments).

Every field below exists in the record. Read a field rather than inferring its
meaning from a report sentence.

## Identity

| Field | Answers |
| --- | --- |
| `schema_version` | which record shape this is |
| `repository`, `pr_number` | what Cite reviewed; `pr_number` is absent in local mode |
| `head_sha`, `base_ref`, `base_sha`, `merge_base_sha` | which commits the diff came from |
| `input_hash` | the identity of the reviewed input, so you can compare two runs |
| `model`, `temperature`, `seed` | the model that answered, and the sampling it ran with |

## The verdict

| Field | Answers |
| --- | --- |
| `verdict` | `PASS`, `FOUND` or `COULD_NOT_EVALUATE`; only `PASS` is a pass |
| `verdict_reason` | one line, always present for `COULD_NOT_EVALUATE` |
| `samples` | how many samples a green rests on; Cite runs one |

`COULD_NOT_EVALUATE` is fail-closed: if Cite could not judge one file, the run
refuses to call the change good.

## Coverage

Cite computes `coverage` in code rather than reading it from the model:

| Field | Answers |
| --- | --- |
| `api_files` | how many files the pull request changed |
| `reviewed` | how many Cite reviewed |
| `approved_skip` | how many a named rule skipped |
| `errored` | how many failed |
| `complete` | reviewed plus approved-skip equals `api_files` |

## `files`

One entry per changed file. Each file ends in exactly one terminal `state`.

| Field | Answers |
| --- | --- |
| `path`, `old_path` | the file, and its rename source |
| `status` | `A`, `M`, `D`, or `R###` |
| `state` | `reviewed`, `skipped` or `error` |
| `reason` | why Cite skipped the file, such as `generated` |
| `detail` | the first line of the error that ended an errored file, such as `rate_limited: HTTP 429: monthly usage limit` |
| `findings` | how many findings point at this file |
| `reasks` | bounded re-asks spent here; absent when zero |
| `blob_sha` | the content identity of the reviewed blob |

A `skipped` file is not a clean file. Say which files Cite skipped and why.


## `findings`

Each entry is a `ValidatedFinding`: the model's claim, after Cite's checks.

| Field | Answers |
| --- | --- |
| `id`, `fingerprint` | identity; the fingerprint hashes category, quoted span and title, so a rebase leaves it unchanged |
| `path`, `occurrence` | where it is; `occurrence` separates two identical findings in one file |
| `category` | `secret-exposure`, `injection`, `auth-bypass`, `destructive-operation`, `crash`, `logic-inversion`, `resource-leak`, `concurrency`, `error-swallow`, `api-contract-break` or `convention` |
| `anchor.start_line`, `anchor.end_line` | the line range it points at |
| `title`, `body`, `impact` | the claim, its explanation, and what breaks |
| `evidence[]` | `{line, quote}` pairs: the characters Cite checks against the diff |
| `evidence_kind` | `present`, or `absent` with `missing_assertion` naming what is not there |
| `evidence_level` | how far Cite normalised the quote to match: `exact`, `normalized`, `elided`, `token`, or `fail` |
| `introduced_by` | `added_line`, or `existing_line_made_wrong` |
| `confidence` | `certain`, `likely` or `question` |
| `external_claims[]` | registry and API facts, checked separately |
| `fix` | the suggested change, when the model offered one |
| `blocks` | computed in code; true when this finding fails the gate |
| `verifier_result` | `supported`, `unsupported`, `needs-context-not-provided`, or absent when the verifier did not run |

Quote `evidence[].quote` verbatim in any summary. Cite checked the claim against
that exact line, so a paraphrase removes the proof and leaves the assertion.

## `drops`

A drop is a finding Cite removed, with the reason it went. Its absence from
`findings` is the answer to "why wasn't this reported".

`reason` is one of:

- Evidence rails: `evidence_mismatch`, `ambiguous_quote`, `anchor_invalid`,
  `anchor_out_of_range`, `anchor_not_added_line`.
- Claim rails: `external_claim_unverified`, `external_claim_rejected_type`,
  `self_negating`, `absence_claim_on_partial_context`,
  `negative_claim_falsified`.
- Verifier and severity: `verifier_unsupported`,
  `category_never_blocks_below_notes`.
- Volume rails: `comment_budget`, `per_file_budget`, `assembly_cut`.
  `suppressed` covers a finding already dismissed on the pull request.
- `parse_failure` means Cite could not parse the model's answer.

## Cost and calls

| Field | Answers |
| --- | --- |
| `usage.input_tokens`, `usage.output_tokens` | the run total |
| `usage.cache_read_tokens`, `usage.cache_write_tokens` | what the prompt cache did |
| `usage.reasoning_tokens` | how much of the output was thinking; zero can mean unreported |
| `cost_usd` | provider-reported cost, else derived from the configured rates, else 0. Cite never guesses it |
| `cache_ceiling` | the best hit rate this run's prompts allowed |
| `calls[]` | one entry per model call: `unit` (`triage`, `review`, `verify`), `attempt`, `start_s`, `duration_s`, `outcome`, `error`, tokens, `cost_usd`, `provider`, `model`, `prefix_id` |

`calls[]` keeps every entry a killed run recorded before it stopped.

## Review inputs and limits

| Field | Answers |
| --- | --- |
| `instructions_used[]` | per instruction file: `total_sections`, `used_sections`, `authoring_sections` |
| `risk_ranked`, `risk_ranked_note` | whether risk ranking capped the reviewed set, and what it left out |
| `reasks_spent` | bounded re-asks the run spent before Cite declared a file unmeasurable |
| `echo_corrections` | schema-valid answers relabelled in code because they named another path |

## `cite`'s own exit code

`cite` exits 0 for a pass, and 1 for a finding or for an error, so the code
alone cannot tell a finding from a crash. The `--tool-failure-blocks=false`
flag, which the Action exposes as the `tool_failure_blocks` input, makes
`gateExit` return 0 for a `COULD_NOT_EVALUATE` as well, because the check run
already concluded neutral. Read `verdict` from the record either way.
