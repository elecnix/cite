# Instruction files

Cite reads the instruction files a repository already has. If a team has
configured an AI reviewer already, Cite works on the next pull request with no
new files and no migration. It invents none of these paths; it reads all of them
**from the base ref**.

## What it reads

These paths are written verbatim because they are the compatibility contract —
a repository already carries them under exactly these names, and a tool that
reads them has to spell them the way they are on disk.

| Rank | Path | Scope |
| --: | -- | -- |
| 1 | `.github/instructions/**/*.instructions.md` | files matching the `applyTo` globs |
| 2 | `.github/copilot-instructions.md` | repository-wide |
| 3 | `AGENTS.md`, nested, nearest file wins | directory subtree |
| 4 | `CLAUDE.md`, `.claude/CLAUDE.md`, `GEMINI.md`, `REVIEW.md` | repository-wide |
| 5 | `.claude/rules/*.md` (frontmatter `paths:`) | matching paths |
| 6 | `.github/skills/*/SKILL.md`, `.claude/skills/`, `.agents/skills/` | selected by description |
| 7 | `.vscode/settings.json` → `github.copilot.chat.reviewSelection.instructions` | review-specific, `{text}`/`{file}` entries |

Frontmatter honoured on `*.instructions.md`: `applyTo`, `description`, `name`,
and `excludeAgent: code-review` — which Cite reads as "not for me", because a
repository that excluded another tool from a file meant it. `chat.instructionsFilesLocations`
maps a path to a boolean; a `false` disables a location, and Cite honours the
boolean rather than just reading the keys.

Known dead, deliberately not implemented: repository-settings "coding guidelines"
configured only in a web UI (no file, no export), and `*.chatmode.md` /
`*.agent.md` rename churn is parsed under both extensions.

Read as signal, not as instruction:
`.github/workflows/copilot-setup-steps.yml` tells Cite the repository's real
build and test commands. That is useful evidence about the repository and never
a rule for the reviewer to follow or report on.

## Precedence answers

Some behaviour was never documented by any prior reader of these files. Guessing
silently would be the wrong move, so Cite specifies its answer here, and
`cite doctor` prints the resolved result for any file:

- **Ordering between two `*.instructions.md` files whose `applyTo` both match:**
  most specific glob first, then lexical path.
- **Whether `applyTo` matches the changed files or the whole tree:** the changed
  file.
- **`.github/AGENTS.md` versus root `AGENTS.md`:** root is repository-wide;
  `.github/AGENTS.md` is the nearest file only for paths under `.github/`.

## The two deliberate divergences

Compatibility is promised for inputs, not behaviour. Where copying an existing
tool's behaviour would make a merge gate less safe, Cite diverges — in writing,
with the reason.

### 1. Instructions come from the base ref, never the pull request head

Reading instruction files from the head branch lets authors iterate without
merging. For a tool that gates a merge, that convenience is a vulnerability: a
pull request that edits `AGENTS.md` rewrites the reviewer's own instructions
before the reviewer reads them, and on a fork pull request the author is a
stranger. Everything that controls the review — including your instruction
files — is read from the base ref; the head diff is data only
([security.md](security.md)).

When a pull request modifies an instruction file, the review body says so in one
line and names the version used.

### 2. Truncation is disclosed, never silent — in either direction

Never silently truncate an instruction file, and never silently un-truncate one.
If a length cap ever applies, Cite reads the whole file and warns in the
resolution table that other tools would have seen only the first N characters.
The point is telling a team their instruction file behaves ambiguously between
two readers of it, rather than letting each behave differently in silence.

## Instructions are evidence, not a basis

An instruction file is written in the imperative, for an author. A reviewer that
reads every imperative as a finding template produces nonsense: "Always write
tests" becomes a demand for a test on a README change; "Open every pull request
via the skill" is unverifiable from a diff and gets asserted anyway. The first
confidently wrong finding a team sees will be sourced from their own instruction
file, and they will conclude the file is hazardous.

Two mechanisms handle this, both still zero-config:

### Grounding filter

A finding must cite a span in the changed lines. "The instructions say to always
write tests" cites nothing, so it is dropped before ranking. This is a filter in
code, not a sentence in a prompt. See [noise.md](noise.md).

### Applicability triage

One cheap call, cached by file hash, classifies each section of each instruction
file as:

- `reviewable` — a checkable property of code; enters the review.
- `authoring` — workflow, process, tooling guidance; does not enter the review.
- `ignore`

Only `reviewable` sections reach the model. The first run reports it in the
footer:

> Using 6 of 41 sections from `AGENTS.md`. 35 were authoring or workflow
> instructions.

That turns an invisible behaviour into an inspectable one and hands you the fix
without a configuration file. `cite doctor` prints the same breakdown on demand.

### The `## Review` override

A `## Review` heading inside a file the team already maintains wins wholesale
and skips triage. A heading beats a new dotfile: it is greppable, it lives beside
its context, and it adds nothing to the repository root.

## Conformance tiers

Compatibility is pinned, not chased. `compat_profile: "2026-08"` in
`.github/cite.yml` selects a dated snapshot of behaviour; it defaults to the
current profile and never auto-updates. A new profile is a new minor release
shipping a diff of what changed.

The four tiers live in [CONFORMANCE.md](../CONFORMANCE.md), which carries that
date:

1. **Guaranteed** — the file paths, frontmatter keys, and precedence in the
   table above. Each has a test fixture; changing any of them is a breaking
   change.
2. **Best-effort** — behaviour no prior reader ever documented. Cite picks an
   answer, documents it, and prints it via `cite doctor`.
3. **Declared divergence** — the two cases above, plus anything future where
   copying would make the merge gate less safe. Each divergence lives on this
   page with its reason.
4. **Out of scope** — settings that exist only in a web UI with no file and no
   export. They cannot be read, so the promise does not cover them, and this is
   stated rather than left for you to discover.

Conformance is observed quarterly, by hand, against a fixed scenario set, and
the observation date is written down. Staleness is the signal: `cite doctor`
warns when [CONFORMANCE.md](../CONFORMANCE.md) is over 90 days old.

## Glob dialect

The glob syntax accepted in `applyTo` and `paths_ignore` was never specified by
any prior reader, so Cite picks one and writes it here:

- Patterns are **comma-separated globs in one string**: `applyTo: "**/*.ts,**/*.js"`.
- A **YAML array is accepted as an alias** for the comma-separated form.
- `**` crosses directory boundaries.
- **Brace expansion (`{a,b}`) is not supported.**
- Overlapping patterns are ordered **most-specific-first, then lexical path**.

`cite validate` checks patterns at parse time; `cite doctor` shows which files
each pattern matched.
