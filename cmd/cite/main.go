package main

import (
	"fmt"
	"os"
	"sort"
)

const version = "0.1.4"

// commandRegistry lets each subcommand live in its own file, registering
// itself via init(), so stacked feature branches never collide on a central
// switch statement.
var commands = map[string]func([]string) error{}

// registerCommand adds a subcommand; panics on duplicate names because that
// is a programming error, not a runtime condition.
func registerCommand(name string, fn func([]string) error) {
	if _, dup := commands[name]; dup {
		panic("duplicate subcommand: " + name)
	}
	commands[name] = fn
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	arg := os.Args[1]
	if arg == "version" || arg == "--version" || arg == "-v" {
		fmt.Println("cite " + version)
		return
	}
	fn, ok := commands[arg]
	if !ok {
		usage()
		os.Exit(2)
	}
	if err := fn(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "cite:", err)
		os.Exit(1)
	}
}

func usage() {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(os.Stderr, `cite %s — an open code reviewer for GitHub

Usage:
`, version)
	for _, n := range names {
		switch n {
		case "review":
			fmt.Fprintln(os.Stderr, "  cite review  [--diff FILE | --pr owner/repo#N] [--dry-run]")
		case "doctor":
			fmt.Fprintln(os.Stderr, "  cite doctor  [path...]")
		case "validate":
			fmt.Fprintln(os.Stderr, "  cite validate [config-file]")
		case "soak":
			fmt.Fprintln(os.Stderr, "  cite soak    <cases-dir>")
		default:
			fmt.Fprintf(os.Stderr, "  cite %-8s (see --help)\n", n)
		}
	}
}
