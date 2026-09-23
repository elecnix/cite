# Review instructions

Cite reads this file from the base branch when it reviews a pull request in
this repository. Each rule below records a fact about the code that a
reviewer can't see in one diff.

## Review

Report a finding only when the code is wrong. If your analysis ends with the
conclusion that the changed code is correct, post nothing for it.

Each `cite` command builds one `Reviewer` with `reviewer.New` and calls `Run`
on it once (`cmd/cite/review.go` and `cmd/cite/rereview.go`). Run-scoped
fields in `internal/reviewer`, such as the usage totals, the call log and the
retry budget, start at zero on every run because every run gets a new
instance. Don't report a missing reset on those fields unless the diff calls
`Run` twice on one `Reviewer`.

In `scripts/*_test.sh`, the `fixture` helper writes a temporary directory with
its own `VERSION` and `cmd/cite/main.go`. A version literal passed to
`fixture`, or compared against a fixture directory, is test data. It doesn't
depend on the repository's `VERSION` file, and a version bump doesn't change
what it checks. Only a check that reads `$root` reads the real repository.
