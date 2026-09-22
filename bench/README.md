# bench

The iteration harness is a versioned corpus of review cases and a runner.

```
./bench/run.sh bench/cases
```

which is a thin wrapper around:

```
go run ./cmd/cite soak bench/cases
```

## What soak is and is not

`soak` is a **pipeline regression harness**. It is not a quality A/B or an
evaluation. It measures:

- schema validity of model output,
- anchors landing inside the diff,
- fingerprint stability across a reformat,
- incremental carry-forward correctness,
- dismissal-ledger behaviour,
- latency and cost budgets,
- a stability number (Jaccard similarity of the fingerprint set across repeats).

There are no labels on historical pull requests and no between-arm quality
claims here. For prompt iteration, the benchmark delta below is the instrument.

## Case format

Each case under `cases/` is a directory containing:

| File | Contents |
| -- | -- |
| `base/` | the base tree: the repository state before the change |
| `patch.diff` | the patch applied on top of the base tree |
| `defects.yml` | the planted-defects manifest |

`defects.yml` lists each planted defect with a one-sentence detection rubric:

```yaml
defects:
  - id: unchecked-close
    path: internal/store/store.go
    lines: 40-44
    category: resource-leak
    rubric: "The file handle opened at line 40 is never closed on the error return."
```

A clean case has an empty (or absent) `defects.yml` and must produce zero
blocking findings.

## Corpus composition

The corpus targets, per release:

- **40% positive**: a planted defect the reviewer should catch.
- **40% clean**: a case with no planted defect and zero blocking findings.
- **20% near-miss**: code matching a defect pattern that is actually correct.

The near-miss share is non-negotiable. Without it you measure pattern-matching
rather than reasoning, and false-positive-heavy models score well.

## Case construction, in decreasing validity

1. **Mined from real fix commits**: revert the fix.
2. **Mutation under a defect grammar.**
3. **Hand-written**, for the classes that do not mutate.

Each mutant passes a liveness filter: it must change behaviour on some input,
and it must **not** be caught by the repository's existing tests, linters, or
type-checker. Skipping that filter benchmarks what CI already catches for free.

## Anti-memorisation

- A 30% private split is never published.
- Provenance is rewritten on mined cases.
- Date fencing: scores are reported separately for code published after each
  model's cutoff.
- 15% of cases rotate per release.

A widening gap between public and private splits is the memorisation alarm.

## Scores

Scores compare only within a major version. Escaped defects the tool saw and
missed become cases: the corpus grows with each miss.
