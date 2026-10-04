# Conformance

**compat_profile: `2026-08`**
**Profile date: 2026-08-21**

This file is the compatibility contract for Cite's instruction-file reading. It
is a dated snapshot rather than a moving promise: behaviour upstream of these
files changes without version numbers or deprecation windows, so Cite pins a
profile (`compat_profile: "2026-08"` in `.github/cite.yml`, defaulted, never
auto-updating) and publishes a diff of what changed when a new profile is
released.

`cite doctor` warns when this file is more than **90 days** past its profile
date. Staleness is the signal.

## Tier 1: Guaranteed

The items below are covered by test fixtures in the repository. A change to any
of them is a breaking change to Cite and requires a major-version bump.

| Guarantee | Fixture |
| -- | -- |
| Reads `.github/instructions/**/*.instructions.md`, scoped by `applyTo` | `internal/instructions/testdata/rank1/` |
| Reads `.github/copilot-instructions.md` repository-wide | `internal/instructions/testdata/rank2/` |
| Reads nested `AGENTS.md`, nearest file wins | `internal/instructions/testdata/rank3/` |
| Reads `CLAUDE.md`, `.claude/CLAUDE.md`, `GEMINI.md`, `REVIEW.md` | `internal/instructions/testdata/rank4/` |
| Reads `.claude/rules/*.md` with frontmatter `paths:` | `internal/instructions/testdata/rank5/` |
| Reads `.github/skills/*/SKILL.md`, `.claude/skills/`, `.agents/skills/` | `internal/instructions/testdata/rank6/` |
| Reads `.vscode/settings.json` → `github.copilot.chat.reviewSelection.instructions`, honouring `{text}`/`{file}` entries | `internal/instructions/testdata/rank7/` |
| Frontmatter keys on `*.instructions.md`: `applyTo`, `description`, `name`, `excludeAgent: code-review` (honoured as "not for me") | `internal/instructions/testdata/frontmatter/` |
| `chat.instructionsFilesLocations`: a `false` disables a location | `internal/instructions/testdata/locations/` |
| Precedence: rank order as in [docs/instructions.md](docs/instructions.md), root `AGENTS.md` repository-wide vs `.github/AGENTS.md` nearest-only under `.github/` | `internal/instructions/testdata/precedence/` |

## Tier 2: Best-effort

Behaviour that no prior reader of these formats ever documented. Cite picks an
answer, documents it here, prints it via `cite doctor`, and treats a change to
its own answer as a documented behavioural change rather than a breakage.

### Glob dialect

Cite's chosen dialect for `applyTo` globs and `paths_ignore`. One package owns
it, `internal/glob`, whose package doc is the normative description.
`internal/scope` calls it for `paths_ignore` and `internal/instructions` calls
it for `applyTo` and `paths`. Neither keeps a matcher of its own, so a pattern
cannot mean one thing in `.github/cite.yml` and another in an instruction
file. `internal/glob/glob_test.go` pins the dialect.

- Patterns are **comma-separated globs in one string**:
  `applyTo: "**/*.ts,**/*.js"`.
- Cite accepts a **YAML array as an alias** for the comma-separated form, and
  both parse to the same pattern list. Cite canonicalises both at parse time,
  so `cite doctor` prints the normalised spelling rather than the one typed.
- Patterns are `/`-separated and match a whole repository-relative path.
- **Normalisation is cosmetic, not semantic.** Cite resolves a leading `/`, a
  leading `./`, repeated `//`, `.` and `..` elements and a trailing `/` before
  matching, so `/docs/**`, `./docs/**`, `docs//**` and `docs/` all mean
  `docs/**`.
- `**`, as a whole segment, **crosses directory boundaries and matches zero or
  more of them**, so `**/*.gen.go` matches `c.gen.go` as well as
  `a/b/c.gen.go`, and a standalone `**` matches every path.
- Within one segment `*`, `?`, `[...]` and `\` behave as in Go's `path.Match`,
  and none of them ever crosses a `/`, so `docs/*.md` matches `docs/a.md` but
  not `docs/x/a.md`. A malformed class matches no name rather than failing the
  run.
- Matching is **case-sensitive**.
- **Brace expansion (`{a,b}`) is NOT supported.** Cite matches a pattern
  containing braces literally, and no command reports it today: `cite validate`
  checks `.github/cite.yml` and never looks at instruction globs. A brace
  pattern shows up only in `cite doctor`, which prints the patterns that
  matched a path.
- **An empty or root-only pattern matches no path.** That covers `""`, `/`,
  `//`, `./` and `docs/..`, because a pattern that specifies no segment can't
  specify a file. `**` is the only spelling that means "every path", and a
  pattern of `.` must never silently widen an instruction file to the whole
  repository.
- Overlapping patterns are ordered **most-specific-first**, ties broken by
  **lexical path order**.

### Other best-effort answers

- Two `*.instructions.md` files whose `applyTo` both match apply in
  most-specific-glob-first order, then lexical path.
- `applyTo` matches against **the changed file** only.
- `.claude/rules/*.md` `paths:` frontmatter uses the same dialect as `applyTo`,
  through the same `internal/glob` matcher.

## Tier 3: Declared divergence

Cases where copying the established behaviour would make the merge gate less
safe. Each divergence is documented with its reason in
[docs/instructions.md](docs/instructions.md); a divergence that is not written
down is a bug.

| Divergence | Reason |
| -- | -- |
| Instruction files are read from the **base ref**, never the pull request head | A pull request that edits an instruction file would otherwise rewrite the reviewer's own rules before it reads them, on fork pull requests authored by a stranger. See docs/instructions.md and docs/security.md (I3). |
| **Truncation is disclosed, never silent, in either direction** | Never silently truncate an instruction file, and never silently un-truncate one. If a length cap applies, the resolution table says so and names how much another reader would have missed. |

## Tier 4: Out of scope

Settings that exist only in a web UI with no file representation or export.
They cannot be read from the repository, so no conformance promise covers them,
and this is stated rather than left for a user to discover:

- Repository-settings "coding guidelines" configured in the UI only.
- Content-exclusion settings configured via UI/API only.

If a setting has no file, it is out of scope by definition. Requests to support
it should start with the file's location.

## Quarterly hand-conformance check

Conformance against this profile is observed **quarterly, by hand**: one sandbox
repository with a fixed set of crafted scenarios, recording which guidance each
reader honoured. It is deliberately never automated into CI: it is a report
whose staleness is the signal.

| Observation date | Profile | Result | Notes |
| -- | -- | -- | -- |
| 2026-08-21 | 2026-08 | initial profile | Tiers fixed; glob dialect chosen; divergences declared |

The next observation is due by **2026-11-21**.
