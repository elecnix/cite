# The noise contract

The single reason reviewers in this category get uninstalled is noise. Cite's
answer is a specification the prompts are written against, with numbers — not a
tuning parameter discovered later. This page is that specification.

## The numbers, up front

- At most **10 comments per review** under the default budget; `max_comments`
  in the config is hard-capped at 20 by the schema.
- At most **2 comments per file**.
- Small pull requests (40 changed lines) get at most **3** comments.
- No style comments. Your formatter owns that.
- Nothing to say means nothing posted.

## Rule 1: the evidence gate

Every finding must quote a span from the file, and the quoted span must match
the file *and* intersect added or modified lines. Matching alone would let the
reviewer comment on the whole file — which is how "helpful context" becomes
thirty comments on a twenty-line change.

Matching is a recorded cascade, and the level is recorded on every finding:

| Level | Rule | Publishes |
| -- | -- | -- |
| `exact` | byte substring of the post-image | yes |
| `normalized` | both sides through one documented normaliser: line-ending unification, Unicode normalisation, non-breaking spaces to spaces, zero-width characters stripped, tabs expanded to the file's dominant indent, trailing whitespace stripped, dedent | yes |
| `elided` | quote split on an ellipsis marker; every segment matches at `normalized`, in order, spanning at most 40 lines | yes |
| `token` | opt-in per language; whitespace- and comment-insensitive token subsequence | notes only |
| fail | — | dropped, and logged with its reason |

A quote must contain at least one non-whitespace, non-punctuation token and 12
bytes — otherwise a model quoting `}` matches everywhere. More than one match
site with no line hint drops as ambiguous. The model is never asked to re-quote
on failure: it has the file in context and will produce a matching quote for the
same wrong claim, converting a detectable failure into an undetectable one.

What this catches: fabricated code, wrong-file anchors, findings about the
pre-image. What it does not catch: a correct quote with an invented consequence.
The evidence gate is a grounding check, not a truth check — every published
comment still carries its origin tag ([downstream-contract.md](downstream-contract.md)).

Absence claims ("no validation", "nothing releases this") have no span to quote,
so they take a separate branch: the finding must quote an enclosing anchor span
that passes the same cascade plus an explicit missing-thing assertion, and when
the file was sent truncated, absence claims are unsayable and the harness
enforces it.

## Rule 2: a closed category vocabulary

Findings use eleven mechanism-named categories. There is no catch-all: no
`bug`, no `other`, no free-form tags. Roughly four-fifths of AI review output is
unfalsifiable prose — "consider extracting this into a helper" — which fits no
category here and therefore never reaches a human.

`security` is not a tag; it is derived from the category.

| Category | May block | Security-derived |
| -- | -- | -- |
| `secret-exposure`, `injection`, `auth-bypass`, `destructive-operation` | yes | yes |
| `crash`, `logic-inversion` | yes | no |
| `resource-leak`, `concurrency`, `error-swallow`, `api-contract-break` | no | no |
| `convention` | **never, in any configuration** | no |

Repository configuration may **shrink** the blocking set and may never grow it —
that column is a property of measured competence, not a preference. `convention`
findings are always rendered as questions.

## The budget

Not a threshold — a budget, computed per pull request:

```
N = clamp(3, 3 + floor(changed_lines / 250), 10)
```

A 40-line pull request gets at most 3 comments; a 2,000-line one gets at most
10. Never more than 10 under any configuration (`max_comments` can raise the
schema ceiling to 20, but the per-run formula caps at 10). And **at most 2
comments per file**: ten comments in one file is a rewrite request, and it goes
in the review body as one paragraph instead.

Above 40 flagged files, files are risk-ranked and the top N reviewed, disclosed
in one line of the review body. Never silently.

## Convention and error-swallow are off by default

`nits: false` (the default) disables the `convention` and `error-swallow`
categories entirely. Not ranked lower — off. They consume no comment budget
unless you enable them in `.github/cite.yml`.

## The assembly call is a capper

The final model call receives the candidate findings and the budget number N,
and must return at most N findings, **with a one-line reason for each cut**.
Forced ranking under a hard cap behaves differently from "rank these".

It is explicitly **not** asked to find cross-file issues — that would be a second
reviewer with a fresh failure mode and no evidence gate in front of it.

It is hierarchical for large pull requests, and **non-fatal**: if the assembly
call fails, the run publishes un-deduped with a note. A cosmetic step never fails
a run that already has its verdict. Cut reasons go to the drop log.

## Silence is valid

Nothing to say means nothing posted. No review event, no "LGTM", no "reviewed 12
files and found no issues". The check run goes green with a one-line summary;
that is what check runs are for. A reviewer that posts on every pull request
trains the team to skim past it — which costs you the one time it matters.

## The drop log

Every finding killed by the evidence gate, the anchor check, an unverified
external claim, the budget, or a suppression is written to the run record with
its reason. Without it there is no way to measure the recall cost of these rails,
and every one of them gets tuned by anecdote instead. "The model found your bug
and the evidence gate killed it" is a completely different failure from "the
model missed it" — the run artifact distinguishes them, and the review body links
it.
