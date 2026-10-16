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
sandbox() {
  dir="$(mktemp -d)"
  sandboxes+=("$dir")
  git init -q --bare "$dir/origin.git"
  git clone -q "$dir/origin.git" "$dir/work" 2>/dev/null
  cd "$dir/work" || exit 1
  git checkout -q -b main
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
[ "$(git rev-parse v1.0.1^{commit})" = "$(git rev-parse origin/main)" ] ||
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
