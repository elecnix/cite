package instructions

import (
	"fmt"
	"strings"
)

// DoctorReport renders the resolved answer in plain text for `cite doctor`:
// which instruction sources matched each changed path, in what order, which
// sections survived triage, and which were classified authoring (PLAN.md §5:
// "`cite doctor` prints the resolved answer for any file").
func DoctorReport(r *ResolvedInstructions) string {
	var b strings.Builder
	b.WriteString("cite doctor — resolved instructions (base ref)\n\n")

	for _, p := range r.paths {
		fmt.Fprintf(&b, "%s\n", p)
		srcs := r.sources[p]
		if len(srcs) == 0 {
			b.WriteString("  (no instruction sources matched)\n\n")
			continue
		}
		for i, sr := range srcs {
			reason := sr.spec.reason
			if sr.matched != "" {
				reason = fmt.Sprintf("%s `%s` matched", reason, sr.matched)
			}
			fmt.Fprintf(&b, "  %d. %s (rank %d, %s)\n", i+1, sr.spec.path, sr.spec.rank, reason)
			var reviewable, authoring, ignored int
			for _, cs := range sr.sections {
				switch cs.Kind {
				case KindReviewable:
					reviewable++
				case KindAuthoring:
					authoring++
				default:
					ignored++
				}
				h := cs.Heading
				if h == "" {
					h = "(preamble)"
				}
				fmt.Fprintf(&b, "       · [%s] %s\n", cs.Kind, h)
			}
			if len(sr.sections) == 0 {
				b.WriteString("       · (no sections)\n")
			}
			fmt.Fprintf(&b, "     %d section(s): %d reviewable, %d authoring, %d ignore\n",
				len(sr.sections), reviewable, authoring, ignored)
		}
		fmt.Fprintf(&b, "  → %d section(s) entered the review for this path.\n\n", len(r.byPath[p]))
	}

	warns := r.Warnings()
	if len(warns) > 0 {
		b.WriteString("warnings:\n")
		for _, w := range warns {
			fmt.Fprintf(&b, "  · %s: %s\n", w.File, w.Message)
		}
		b.WriteString("\n")
	}

	usage := r.Usage()
	if len(usage) > 0 {
		b.WriteString("usage:\n")
		for _, u := range usage {
			fmt.Fprintf(&b, "  Using %d of %d sections from %s. %d were authoring.\n",
				u.UsedSections, u.TotalSections, u.File, u.AuthoringSections)
		}
	}
	return b.String()
}
