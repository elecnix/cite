#!/usr/bin/env bash
# Checks the guard rails around cutting a release.
#
# Usage: bash scripts/release-tag_test.sh
#
# `scripts/release-tag.sh` exists because of the v0.15.0 tag move: the GitHub
# release form creates the tag itself, from whatever the default branch points
# at, in the same click that publishes. The script's whole value is therefore in
# what it refuses -- a dirty tree, the default branch as the working branch, a
# branch that is not level with the remote, a tag that already exists -- and each
# of those refusals is a situation that already cost a release, or would.
#
# The sandbox is a real repository with a real remote: a bare repository beside
# the clone, so the guards that read the remote (its default branch, its tags,
# whether the branch is level with it) are exercised rather than simulated. A
# test against the working tree alone would confirm none of them.
#
# The tag phase is run twice on purpose: once before the bump is merged, where it
# has to refuse, and once after, where it has to tag the *merged* commit and not
# the branch, because a tag on a branch commit is the failure this script is for.

set -uo pipefail

cd "$(cd "$(dirname "$0")/.." && pwd)" || exit 1
script="$PWD/scripts/release-tag.sh"
version_check="$PWD/scripts/version-check.sh"
sandboxes=()
fails=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  fails=$((fails + 1))
}

cleanup() {
  for dir in "${sandboxes[@]}"; do rm -rf "$dir"; done
}
trap cleanup EXIT

# A fresh remote and a clone of it: VERSION and cmd/cite/main.go both at 1.0.0,
# with the real version-check.sh, on a release branch off main.
#
# `--initial-branch=main` rather than whatever the ambient configuration says: the
# guards read the remote's default branch, so the sandbox has to name it instead of
# inheriting it. A runner has no init.defaultBranch, which made the bare repository
# point HEAD at `master` while the test pushed `main`, and every guard then answered
# "cannot determine the default branch of origin". Local runs passed for that one
# reason, and only running the test with an empty global config reproduced it.
sandbox() {
  dir="$(mktemp -d)"
  sandboxes+=("$dir")
  git init -q --bare --initial-branch=main "$dir/origin.git"
  git clone -q "$dir/origin.git" "$dir/work" 2>/dev/null
  cd "$dir/work" || exit 1
  git checkout -q -b main 2>/dev/null || git checkout -q main
  # The script commits as whoever runs it, so the sandbox has to be somebody: without
  # a repository-local identity the commit is the thing that fails when the ambient
  # configuration is empty, which is what a runner starts with.
  git config user.email t@example.invalid
  git config user.name "release-tag test"
  printf '1.0.0\n' >VERSION
  mkdir -p cmd/cite scripts
  printf 'package main\n\nconst version = "1.0.0"\n' >cmd/cite/main.go
  cp "$version_check" scripts/version-check.sh
  git add -A
  git -c user.email=t@t -c user.name=t commit -qm init
  git push -q -u origin main
  git remote set-head origin -a >/dev/null 2>&1
  git checkout -q -b release/1.0.1
}

# The script under test acts on `--root`, so every call names the sandbox: without
# it, a test would exercise the checkout it is running from, which is both the wrong
# repository and the reason the refusals below would look like successes.
rt() {
  bash "$script" "$@" --root "$PWD"
}

contains() {
  case "$1" in
    *"$2"*) return 0 ;;
    *) return 1 ;;
  esac
}

# --- a dry run writes nothing and pushes nothing ---------------------------
sandbox
out="$(rt 1.0.1 --dry-run 2>&1)"
contains "$out" "would write VERSION: 1.0.1" || fail "dry run did not say what it would write: $out"
[ "$(cat VERSION)" = "1.0.0" ] || fail "dry run wrote VERSION"
contains "$(cat cmd/cite/main.go)" 'const version = "1.0.0"' || fail "dry run rewrote main.go"
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || fail "dry run created a commit"
[ -z "$(git ls-remote --heads origin release/1.0.1)" ] || fail "dry run pushed the branch"

# --- the default branch is refused ----------------------------------------
sandbox
git checkout -q main
out="$(rt 1.0.1 2>&1)"
contains "$out" "refusing to bump on main" || fail "bumping on main was not refused: $out"
[ "$(cat VERSION)" = "1.0.0" ] || fail "VERSION was written on a refusal"

# --- the bump itself ------------------------------------------------------
sandbox
out="$(rt 1.0.1 2>&1)" || fail "the bump failed: $out"
[ "$(cat VERSION)" = "1.0.1" ] || fail "VERSION is $(cat VERSION), expected 1.0.1"
contains "$(cat cmd/cite/main.go)" 'const version = "1.0.1"' || fail "main.go was not bumped"
[ "$(git log -1 --format=%s)" = "chore(release): bump the version string to 1.0.1" ] ||
  fail "unexpected commit subject: $(git log -1 --format=%s)"
[ "$(git rev-parse origin/release/1.0.1)" = "$(git rev-parse HEAD)" ] || fail "the bump was not pushed"
[ "$(git rev-parse origin/main)" = "$(git rev-parse main)" ] ||
  fail "the bump reached the default branch, which nothing may push to"

# --- refusals that keep the release commit honest -------------------------
sandbox
printf 'x\n' >NOTES.md
out="$(rt 1.0.1 2>&1)"
contains "$out" "working tree is dirty" || fail "a dirty tree was not refused: $out"
rm -f NOTES.md

# A detached HEAD has no branch name, so the default-branch refusal would silently
# not apply: the guard has to be on "is this a branch" and not only on its name.
sandbox
git checkout -q --detach origin/main
out="$(rt 1.0.1 2>&1)"
contains "$out" "HEAD is detached" || fail "a detached HEAD was not refused: $out"
[ "$(cat VERSION)" = "1.0.0" ] || fail "VERSION was written on a detached HEAD"

# Two version lines: `sed` rewrites both, version-check.sh reads the first, and a
# count capped at one could not see the difference.
sandbox
git checkout -q main
printf 'package main\n\nconst version = "1.0.0"\nconst version = "1.0.0"\n' >cmd/cite/main.go
git -c user.email=t@t -c user.name=t commit -qam duplicate
git push -q origin main
git checkout -q -b release/1.0.3
out="$(rt 1.0.3 2>&1)"
contains "$out" "expected exactly one" || fail "a duplicated version line was not refused: $out"

sandbox
out="$(rt 1.0.0 2>&1)"
contains "$out" "already at 1.0.0" || fail "a no-op bump was not refused: $out"

sandbox
out="$(rt 1.1 2>&1)"
contains "$out" "is not X.Y.Z" || fail "a malformed version was not refused: $out"

sandbox
# Single-fault on purpose: the inconsistency is committed and pushed, so the branch
# is level with origin/main and the version check is the only thing that can object.
git checkout -q main
sed -i 's|const version = "1.0.0"|const version = "1.0.1"|' cmd/cite/main.go
git -c user.email=t@t -c user.name=t commit -qam inconsistent
git push -q origin main
git checkout -q -b release/1.0.2
out="$(rt 1.0.2 2>&1)"
contains "$out" "fix that first" || fail "disagreeing version strings were not refused: $out"

sandbox
git tag -a v1.0.1 -m v1.0.1
out="$(rt 1.0.1 2>&1)"
contains "$out" "already exists" || fail "an existing local tag was not refused: $out"

sandbox
git tag -a v1.0.1 -m v1.0.1
git push -q origin v1.0.1
git tag -d v1.0.1 >/dev/null
out="$(rt 1.0.1 2>&1)"
contains "$out" "already exists" || fail "an existing remote tag was not refused: $out"

sandbox
printf 'ahead\n' >AHEAD.md
git add AHEAD.md
git -c user.email=t@t -c user.name=t commit -qm ahead
out="$(rt 1.0.1 2>&1)"
contains "$out" "not level with origin/main" || fail "a stale base was not refused: $out"

sandbox
out="$(rt 2>&1)"
contains "$out" "usage: bash scripts/release-tag.sh" || fail "a missing version was not refused"
out="$(rt 1.0.1 --nonsense 2>&1)"
contains "$out" "unknown option" || fail "an unknown option was not refused: $out"

# A remote whose HEAD names no branch it has: the script cannot say what the default
# branch is, so it refuses rather than guessing one. This is the shape the CI runner
# produced from the sandbox's own ambiguity, and it is worth keeping as a case: the
# refusal is the correct outcome, and guessing `master` or `main` from the local
# configuration would push the bump to a branch nobody asked about.
sandbox
git remote set-head origin -d 2>/dev/null
git --git-dir="$dir/origin.git" symbolic-ref HEAD refs/heads/does-not-exist
out="$(rt 1.0.1 2>&1)"
rc=$?
[ "$rc" != 0 ] || fail "the bump ran with no determinable default branch: $out"
[ "$(cat VERSION)" = "1.0.0" ] || fail "VERSION was written without a known default branch"
contains "$out" "default branch" || contains "$out" "unknown after fetching" ||
  fail "the refusal does not say why it stopped: $out"

# --- a version with build metadata is a version this script accepts -------
# `version_re` allows `1.0.1+build1`, so the assertions that check the two writes have
# to read it literally: interpolated into an ERE the `+` is a quantifier, and the count
# came back zero *after* both files were already written.
sandbox
out="$(rt 1.0.1+build1 2>&1)" || fail "a version with build metadata was refused: $out"
[ "$(cat VERSION)" = "1.0.1+build1" ] || fail "VERSION is $(cat VERSION)"
contains "$(cat cmd/cite/main.go)" 'const version = "1.0.1+build1"' ||
  fail "main.go was not written for a version with build metadata"

# --- the tag phase: refuses before the merge, tags the merged commit after --
sandbox
out="$(rt 1.0.1 --tag 2>&1)"
contains "$out" "does not name v1.0.1" ||
  fail "tagging before the bump is merged was not refused: $out"
[ -z "$(git ls-remote --tags origin refs/tags/v1.0.1)" ] || fail "a tag was pushed by a refusal"

rt 1.0.1 >/dev/null 2>&1 || fail "the bump failed in the tag-phase sandbox"
git checkout -q main
git merge -q --no-ff release/1.0.1 -m "merge the release bump"
git push -q origin main

# A dry run first: it has to pass the checks and still create nothing.
out="$(rt 1.0.1 --tag --dry-run 2>&1)"
contains "$out" "would run: git tag -a v1.0.1" || fail "the tag dry run did not report the tag: $out"
[ -z "$(git ls-remote --tags origin refs/tags/v1.0.1)" ] || fail "the tag dry run pushed a tag"

out="$(rt 1.0.1 --tag 2>&1)" || fail "tagging the merged bump failed: $out"
[ -n "$(git ls-remote --tags origin refs/tags/v1.0.1)" ] || fail "the tag was not pushed"
[ "$(git cat-file -t v1.0.1)" = "tag" ] || fail "the tag is not annotated"
# shellcheck disable=SC1083 # the ^{commit} peeling syntax is git's, not a shell brace
[ "$(git rev-parse "v1.0.1^{commit}")" = "$(git rev-parse origin/main)" ] ||
  fail "the tag does not point at the merged commit"
[ "$(git tag -l --format='%(contents:subject)' v1.0.1)" = "v1.0.1" ] ||
  fail "the tag message does not name the version"

out="$(rt 1.0.1 --tag 2>&1)"
contains "$out" "already exists" || fail "a second tag was not refused: $out"

if [ "$fails" -gt 0 ]; then
  printf '\n%s check(s) failed.\n' "$fails" >&2
  exit 1
fi

printf 'ok - the release-tag.sh guard rails held\n'
