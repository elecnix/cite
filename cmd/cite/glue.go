package main

// Glue for doctor and soak subcommands.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/instructions"
	"github.com/elecnix/cite/internal/soak"
)

func soakHelpLine() string { return soak.HelpText }

func runSoakDir(dir string) (report string, pass bool, err error) {
	rep, err := soak.RunAll(dir)
	if err != nil {
		return "", false, err
	}
	return soak.RenderReport(rep), rep.Pass(), nil
}

// doctorTree resolves instruction files for the given changed paths against
// a filesystem tree and prints the doctor report (§5: the debugging surface
// that turns vendor-change bug reports from arguments into outputs).
func doctorTree(root string, paths []string) error {
	tree := githubclient.NewFSTree(root)
	if len(paths) == 0 {
		// Default: every regular file in the tree, capped to keep the
		// report readable.
		var err error
		paths, err = tree.List("")
		if err != nil {
			return err
		}
		if len(paths) > 500 {
			paths = paths[:500]
			fmt.Println("(report capped at 500 paths)")
		}
	}
	resolved, warnings, err := instructions.Resolve(tree, paths, nil)
	if err != nil {
		return err
	}
	fmt.Print(instructions.DoctorReport(resolved))
	for _, w := range warnings {
		fmt.Println("warning:", w.Message)
	}
	warnConformanceStale(root)
	return nil
}

// conformanceStaleWindow is how long a conformance observation stays
// trustworthy (PLAN.md §5: "warns when CONFORMANCE.md is over 90 days old").
const conformanceStaleWindow = 90 * 24 * time.Hour

// ConformanceDate extracts the profile date from CONFORMANCE.md content.
// The file carries a line like "**Profile date: 2026-08-21**"; to stay robust
// against rewording, it scans the first ~20 lines and returns the first date
// matching YYYY-MM-DD. It reports false when no parseable date is found.
func ConformanceDate(content []byte) (time.Time, bool) {
	lines := strings.SplitN(string(content), "\n", 21)
	for i := range lines {
		if i >= 20 {
			break
		}
		for _, field := range strings.Fields(lines[i]) {
			field = strings.Trim(field, "*_`[](){}<>,.:;!\"'")
			if t, err := time.Parse("2006-01-02", field); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// stalenessWarning returns the staleness warning for a conformance date,
// or the empty string when the observation is still within the window.
func stalenessWarning(date, now time.Time) string {
	days := int(now.Sub(date).Hours() / 24)
	if days <= int(conformanceStaleWindow.Hours()/24) {
		return ""
	}
	return fmt.Sprintf("WARNING: CONFORMANCE.md is %d days old (>90). Conformance observations expire; re-run the quarterly hand-check.", days)
}

// warnConformanceStale prints the staleness warning for <root>/CONFORMANCE.md,
// if present. A file with no parseable date counts as undated. An absent file
// is silent: doctor describes instruction resolution, not file inventory.
func warnConformanceStale(root string) {
	content, err := os.ReadFile(filepath.Join(root, "CONFORMANCE.md"))
	if err != nil {
		return
	}
	date, ok := ConformanceDate(content)
	if !ok {
		fmt.Println("WARNING: CONFORMANCE.md is undated. Conformance observations expire; re-run the quarterly hand-check and add the profile date.")
		return
	}
	if w := stalenessWarning(date, time.Now()); w != "" {
		fmt.Println(w)
	}
}
