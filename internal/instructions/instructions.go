// Package instructions discovers, resolves and triages the instruction
// files a repository already carries (PLAN.md §5).
//
// Everything is read from the BASE REF, never the pull-request head: a pull
// request that edits AGENTS.md must not rewrite the reviewer's own
// instructions before the reviewer reads them (deliberate divergence §5).
//
// The paths in Discover are written verbatim because they are the
// compatibility contract — repositories carry these files under exactly
// these names, and a tool that reads them has to spell them the way they
// are on disk.
//
// Truncation is disclosed, never silent, in either direction: Cite reads
// whole files, and emits a warning entry when a file exceeds the 4,000
// character cap another tool used to apply, noting what that tool would
// have seen (deliberate divergence §5).
//
// Applicability triage is plumbing-only here: the Classifier interface and
// a NoopClassifier. The LLM-backed classifier arrives later; results are
// cached by the sha256 of the file content so a file is classified once no
// matter how many changed paths it applies to.
package instructions

// Tree reads instruction files from a git tree. Implementations must serve
// paths from the base ref.
type Tree interface {
	// List returns the paths of all regular files beneath dir, relative to
	// the tree root, sorted lexically. dir == "" lists the entire tree.
	List(dir string) ([]string, error)
	// Read returns the content of path. ok is false when the file does not
	// exist (a missing file is not an error).
	Read(path string) ([]byte, bool, error)
}

// Section is one markdown-heading-delimited slice of an instruction file.
// The preamble before the first heading has an empty Heading and is dropped
// when it has no body text.
type Section struct {
	Heading string
	Text    string
}

// Kind is the applicability-triage verdict for a section (PLAN.md §5:
// "Applicability triage, cached by file hash").
type Kind string

const (
	// KindReviewable marks a section as a checkable property of code. Only
	// reviewable sections enter the review.
	KindReviewable Kind = "reviewable"
	// KindAuthoring marks workflow, process or tooling guidance — written
	// for authors, not checkable from a diff.
	KindAuthoring Kind = "authoring"
	// KindIgnore marks a section as noise for the reviewer.
	KindIgnore Kind = "ignore"
)

// ClassifiedSection is a Section with its triage verdict.
type ClassifiedSection struct {
	Section
	Kind Kind
}

// Classifier assigns a triage verdict to each section of one instruction
// file. Implementations must not mutate the input.
type Classifier interface {
	Classify(file string, sections []Section) ([]ClassifiedSection, error)
}

// NoopClassifier marks every section reviewable. It is the default until
// the LLM-backed classifier lands, and what `cite doctor` reports against
// when no classifier is configured.
type NoopClassifier struct{}

// Classify implements Classifier.
func (NoopClassifier) Classify(_ string, sections []Section) ([]ClassifiedSection, error) {
	out := make([]ClassifiedSection, len(sections))
	for i, s := range sections {
		out[i] = ClassifiedSection{Section: s, Kind: KindReviewable}
	}
	return out, nil
}

// Warning is a disclosed, non-fatal observation about resolution — most
// importantly the truncation disclosure (§5 divergence 2).
type Warning struct {
	File    string
	Message string
}
