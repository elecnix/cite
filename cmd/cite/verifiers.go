package main

// External-claims verifiers (§8). path_exists and symbol_exists get
// mechanical checks; everything else is note-only. "Verify after, do not
// inform before" — a claimed path costs one lookup, never a file tree.

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/elecnix/cite/internal/githubclient"
	"github.com/elecnix/cite/internal/reviewer"
)

// gitVerifier answers claims against a local git checkout (local --diff
// mode). It runs no pull-request-head code: ls-tree and grep read objects,
// they do not execute anything.
type gitVerifier struct {
	dir string
}

var _ reviewer.Verifier = (*gitVerifier)(nil)

func (v *gitVerifier) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = v.dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (v *gitVerifier) PathExists(path string) bool {
	_, err := v.git("cat-file", "-e", "HEAD:"+path, "--")
	return err == nil
}

func (v *gitVerifier) SymbolExists(symbol string) bool {
	// A definition-shaped pattern: the symbol followed by an opening
	// bracket, paren or assignment — zero hits repo-wide means the symbol
	// was invented.
	out, err := v.git("grep", "-l", "-E", symbol+`[[:space:]]*[(:={]`, "HEAD", "--")
	return err == nil && strings.TrimSpace(out) != ""
}

// apiVerifier answers claims against the base ref over the GitHub API —
// the Actions job has no checkout (§12, I1).
type apiVerifier struct {
	c                *githubclient.Client
	owner, repo, ref string
	tree             *githubclient.APITree
}

var _ reviewer.Verifier = (*apiVerifier)(nil)

func (v *apiVerifier) PathExists(path string) bool {
	_, found, err := v.c.GetFileContent(context.Background(), v.ref, path)
	if err != nil {
		return false // unverifiable ⇒ the finding drops (fail-closed)
	}
	return found
}

func (v *apiVerifier) SymbolExists(symbol string) bool {
	// Code search over the base repository. On API failure the answer is
	// "unverified", which drops the finding — never blocks on a guess.
	q := fmt.Sprintf("repo:%s/%s %s", v.owner, v.repo, symbol)
	var out struct {
		TotalCount int `json:"total_count"`
	}
	err := v.c.SearchCode(context.Background(), q, &out)
	return err == nil && out.TotalCount > 0
}
