package main

// Glue for doctor and soak subcommands.

import (
	"fmt"

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
	return nil
}
