package main

// Subcommand entry points wiring internal packages together.
//
// review  — local diff mode (--diff FILE) or pull-request mode (--pr owner/repo#N)
// doctor  — which instruction files reached which paths
// validate— schema-check .github/cite.yml
// soak    — pipeline regression harness over bench cases

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/elecnix/cite/internal/config"
)

func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	path := fs.String("config", ".github/cite.yml", "config file to check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		*path = fs.Arg(0)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			for _, p := range ve.Problems {
				fmt.Printf("error: %s\n", p)
			}
			return fmt.Errorf("%s is not valid (%d problem(s))", *path, len(ve.Problems))
		}
		return err
	}
	fmt.Printf("%s: OK (gate=%s max_comments=%d nits=%t compat_profile=%s)\n",
		*path, cfg.Gate, cfg.MaxComments, cfg.Nits, cfg.CompatProfile)
	return nil
}

func runSoak(args []string) error {
	fs := flag.NewFlagSet("soak", flag.ContinueOnError)
	dir := "."
	if fs.Parse(args) == nil && fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	rep, pass, err := runSoakDir(dir)
	if err != nil {
		return err
	}
	fmt.Println(soakHelpLine())
	fmt.Print(rep)
	if !pass {
		return fmt.Errorf("soak: failures present")
	}
	return nil
}

func runDoctor(args []string) error {
	root := "."
	paths := args
	if len(paths) > 0 && isDir(paths[0]) {
		root = paths[0]
		paths = paths[1:]
	}
	return doctorTree(root, paths)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// parsePRSpec parses "owner/repo#123".
func parsePRSpec(s string) (owner, repo string, num int, err error) {
	s = strings.TrimPrefix(s, "https://github.com/")
	i := strings.Index(s, "#")
	if i < 0 {
		parts := strings.Split(s, "/")
		if len(parts) == 3 {
			s = parts[0] + "/" + parts[2]
			i = strings.Index(s, "#")
		}
	}
	if i < 0 {
		return "", "", 0, fmt.Errorf("expected owner/repo#N, got %q", s)
	}
	repoPart := s[:i]
	n, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return "", "", 0, fmt.Errorf("bad PR number in %q", s)
	}
	parts := strings.Split(repoPart, "/")
	if len(parts) != 2 {
		return "", "", 0, fmt.Errorf("expected owner/repo#N, got %q", s)
	}
	return parts[0], parts[1], n, nil
}
