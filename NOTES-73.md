# NOTES — issue #73: bounded retry for parse_failure / deadline_exceeded

Worktree: `~/Source/cite/retry-73`, branch `fix/73-bounded-retry`, from `origin/main` (540f422).
Append findings here as you go — this file is what survives a dead process.

## Reading of the issue (what I chose and why)

The four bullets are directions, not prescriptions. My weighing:

1. **Echo guard (doing it, but as relabel, not re-ask).** The issue says a wrong-path
   echo is retriable "without spending a model round-trip". Two readings exist:
   (a) detect deterministically, re-ask from a budget; (b) don't re-ask at all — patch the
   echo in code. I chose (b): the payload contains exactly one file, so whatever the model
   reviewed, it reviewed the bytes we sent; the `path` field is a label, not a claim. A
   relabel costs zero model calls and cannot loop. Safety is preserved fail-closed: any
   findings in the response still face the full evidence cascade against the real
   post-image, so a response that hallucinated content for a different file still drops
   every finding it carried (covered by a test). This makes the old wrong-path re-ask
   (previously drawn from the run-global bucket) free, which matters because the measured
   CI failures were predominantly exactly this shape (`"unknown"`, `""`, `"README.md"`).
2. **Per-file parse budget (doing it, fully separate).** Output-contract failures
   (blank body, syntax garbage, schema violation) draw from a fresh per-file budget
   (default 2) instead of the run-global 3-token bucket. Not a hybrid: any tie-back to the
   run-global bucket reintroduces the starvation the issue measures (attempt 2 lost four
   files because earlier files had drained the shared bucket). Worst case per file is
   1 + 2 calls — a fixed multiplier, so an unmeasurable file still fails fast, now with
   the budget spent recorded on its outcome. Transient provider errors keep drawing from
   the run-global bucket: those are provider-level, not file-level, and sharing them is
   correct.
3. **Deadline retry (doing it, exactly one, fresh budget).** Today terminal by design —
   the rationale ("a timeout retry pays twice for the same wait") is real but the measured
   cost of *not* retrying is worse: attempt 1 of PR #71 lost README.md to a derived-deadline
   expiry on a docs-only diff. One fresh-budget re-ask per file (default 1), distinct from
   the parse budget, then terminal `deadline_exceeded` naming the mechanism and budget
   spent. Bounded: worst case 2 calls per file.
4. **Surfacing (doing it via the run record + gate reason).** New fields:
   `FileOutcome.Reasks` (budget spent on that file), `RunRecord.ReasksSpent` (run-wide),
   `RunRecord.EchoCorrections` (yield of the echo guard). The gate's COULD_NOT_EVALUATE
   reason now reads "N file(s) never evaluated: path (mechanism, k re-asks spent); m
   bounded re-asks spent run-wide" — this flows into the check summary and the sticky
   comment verbatim through the existing VerdictReason plumbing.

Sizing: an unmeasurable diff fails fast — per file at most 1 initial + 2 parse re-asks +
1 deadline re-ask = 4 calls, with the mechanism (`parse_failure` vs `deadline_exceeded`)
and the budget spent on the file outcome and in the gate reason. Not configurable on
purpose: a knob invites raising it until a fast red becomes a slow red.

## Process log

- [x] Read issue #73; read `internal/reviewer/reviewer.go`, `internal/model/{model,parse}.go`,
      `internal/gate/gate.go`, existing reviewer tests (the #59 retry shape).
- [x] Echo guard: `TestEchoGuardRelabelsWrongPathWithoutReask` (3 echoes: "unknown", "",
      "README.md" — one review call each, findings validated),
      `TestEchoGuardKeepsFindingsFailClosed` (relabeled response with a fabricated quote
      still drops every finding via the evidence cascade — relabel fixes the label, never
      the content), `TestWrongPathEchoRelabeledWithoutReask` (rewritten from the #59
      re-ask test: 2 calls → 1).
- [x] Per-file parse budget: `TestPerFileParseBudgetSparesOtherFiles` (a.go always blank
      burns its own budget and dies parse_failure with Reasks=2; b.go blank twice then
      good still reviews clean — under the old run-global bucket a.go drained all 3 tokens
      and b.go went terminal), `TestBlankBodiesUntilPerFileBudgetExhaustedRecordsParseFailure`
      and the schema/syntax variants (calls = 1+2, outcome carries the budget spent).
- [x] Deadline retry: `TestDeadlineExceededRetriedOnceWithFreshBudget` (2 calls, reviewed),
      `TestDeadlineRetryExhaustedStaysTerminal` (2 calls max, errored(deadline_exceeded),
      Reasks=1, remedy names roles.review.timeout), TestDeadlineErrorNamesTheTimeoutKnob
      updated (1 → 1+defaultPerFileDeadlineRetries calls; log wording now "fresh-budget").
      Run-record fields ReasksSpent/EchoCorrections asserted.
- [x] Gate surfacing (red → green): `TestErroredReasonNamesMechanismAndBudgetSpent` — red
      run showed the old reason `"file errored during review: bad.go, slow.go"`; green now
      reads `"2 file(s) never evaluated: bad.go (parse_failure, 2 re-ask(s) spent), slow.go
      (deadline_exceeded, 1 re-ask(s) spent); 3 bounded re-ask(s) spent"`.
      `TestErroredWithoutMechanismStillNamesPaths` pins the legacy shape (no reason/re-asks
      → path still named). Reason flows verbatim into check summary + sticky comment via
      the existing VerdictReason plumbing.
- [x] `go build ./...` + `go test ./...`: 11/11 packages ok.
- [x] Gate surfacing: red test → green.
- [x] `go build ./...` + `go test ./...` green; push after every commit.
- [ ] Draft PR linking #73 (never ready, never merge — operator's call).
