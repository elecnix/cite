// Command cite is the single static binary behind the Cite GitHub Action.
//
// Subcommands: review, doctor, validate, soak, reaper, canary.
package main

import (
	"fmt"
	"os"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "review":
		err = runReview(os.Args[2:])
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "validate":
		err = runValidate(os.Args[2:])
	case "soak":
		err = runSoak(os.Args[2:])
	case "reaper":
		err = fmt.Errorf("reaper: not implemented yet")
	case "canary":
		err = fmt.Errorf("canary: not implemented yet")
	case "version", "--version", "-v":
		fmt.Println("cite " + version)
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cite:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `cite %s — an open code reviewer for GitHub

Usage:
  cite review  [--diff FILE | --pr owner/repo#N] [--dry-run]
  cite doctor  [path...]
  cite validate [config-file]
  cite soak    <cases-dir>
`, version)
}
