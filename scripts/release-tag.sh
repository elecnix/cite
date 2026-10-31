#!/usr/bin/env bash
# Cut a release: bump the two version strings on a branch, and tag the commit
# that lands on the default branch.
#
# Usage:
#   bash scripts/release-tag.sh <version>            # prepare the bump
#   bash scripts/release-tag.sh <version> --tag      # tag the merged bump
#
# Options:
#   --tag        create and push the tag, after the bump is on the default branch
#   --dry-run    print what would happen; write nothing, push nothing
#   --root DIR   repository to act on (default: this script's parent)
#   --remote R   remote to push to and read (default: origin)
#
# WHY this exists
#
# The GitHub "new release" form creates the tag itself, from whatever the default
# branch happens to point at in that moment, and publishes the release in the same
# click. Nobody can check what commit the tag names before it exists, and the only
# way back from a wrong tag is to move it -- which is what happened to v0.15.0: the
# tag was created on one commit, the release was built from another, and the tag was
# moved afterwards to line the two up. A moved tag is not a repair: every consumer
# that resolved the tag in between has a binary whose provenance names a commit the
# tag no longer points at.
#
# So the order here is: commit, push, and only then tag -- and the tag is created on
# the commit that is already on the default branch, never on a local one.
#
# WHY two phases
#
# The bump lands by pull request, which is what the repository already does (every
# release commit is a merged `chore(release): bump the version string to X.Y.Z`) and
# what the default-branch rule requires: nothing pushes to `main` directly, including
# this script. Tagging then happens against the *merged* commit, which is why the
# tag phase runs after the merge and not in the same invocation.
#
# What it refuses, and why each refusal exists
#
#   * a dirty tree -- the bump must be the only change in the commit;
#   * the default branch as the current branch -- the bump goes through a pull
#     request, so a release commit cannot skip review by accident;
#   * a local branch that is not level with the remote -- a bump built on a stale
#     base is a bump whose commit is not the one that will be tagged;
#   * a version string that is not X.Y.Z (or X.Y.Z-prerelease);
#   * a version the two files already hold -- there would be nothing to commit, and
#     a silent no-op release commit is how a tag ends up on the previous version;
#   * a tag that already exists, locally or on the remote -- a tag is moved
#     deliberately or not at all, never as a side effect of a bump;
#   * in the tag phase, a default branch whose two files disagree with the tag --
#     scripts/version-check.sh reads them out of the commit being tagged, not out of
#     the working tree, so the check is about the commit rather than about this
#     machine's checkout.
#
# The tag itself is annotated, and its message names the version: `git tag -a`,
# never a lightweight tag, so the release carries an object with an author and a
# date rather than only a ref.

set -euo pipefail

version=""
do_tag=0
dry_run=0
root=""
remote="origin"

while [ $# -gt 0 ]; do
  case "$1" in
    --tag) do_tag=1 ;;
    --dry-run) dry_run=1 ;;
    --root) root="${2:-}"; shift ;;
    --remote) remote="${2:-}"; shift ;;
    -h | --help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    -*) printf 'error: unknown option %s\n' "$1" >&2; exit 2 ;;
    *)
      if [ -n "$version" ]; then
        printf 'error: unexpected argument %s\n' "$1" >&2
        exit 2
      fi
      version="$1"
      ;;
  esac
  shift
done

root="${root:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$root"

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

say() { printf '%s\n' "$1"; }

run() {
  if [ "$dry_run" -eq 1 ]; then
    printf '  would run: %s\n' "$*"
    return 0
  fi
  "$@"
}

version_re='^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$'
version="${version#v}"

[ -n "$version" ] || fail "usage: bash scripts/release-tag.sh <version> [--tag]"
printf '%s' "$version" | grep -qE "$version_re" ||
  fail "version '$version' is not X.Y.Z (or X.Y.Z-prerelease)"

tag="v$version"
git rev-parse --git-dir >/dev/null 2>&1 || fail "$root is not a git repository"

# The default branch, from the remote rather than from a hardcoded name: this
# repository's default branch is `main`, and a copy of it that renames that would
# otherwise have this script bump on the wrong branch.
git fetch --quiet "$remote" || fail "cannot reach $remote"
default_ref="$(git symbolic-ref -q --short "refs/remotes/$remote/HEAD" 2>/dev/null || true)"
default_branch="${default_ref#"$remote"/}"
if [ -z "$default_branch" ]; then
  # A bare clone without the symbolic ref: fall back to asking the remote. Both
  # answers come from the remote rather than from this machine's configuration,
  # because a default branch guessed locally is a branch the bump might be pushed to
  # without anyone asking for it.
  default_branch="$(git ls-remote --symref "$remote" HEAD 2>/dev/null |
    sed -n 's|^ref: refs/heads/\([^[:space:]]*\).*|\1|p' | head -1)"
fi
[ -n "$default_branch" ] ||
  fail "cannot determine the default branch of $remote: neither $remote/HEAD nor its advertised HEAD names one (is its HEAD unborn, or pointing at a branch it does not have?)"

tag_exists() {
  git rev-parse -q --verify "refs/tags/$tag" >/dev/null 2>&1 && return 0
  [ -n "$(git ls-remote --tags "$remote" "refs/tags/$tag" 2>/dev/null)" ] && return 0
  return 1
}

# ---------------------------------------------------------------------------
# Phase 2: tag the commit that landed
# ---------------------------------------------------------------------------
if [ "$do_tag" -eq 1 ]; then
  head="$(git rev-parse "$remote/$default_branch")"
  say "tagging $tag at $remote/$default_branch ($(git rev-parse --short "$head"))"

  tag_exists && fail "tag $tag already exists (locally or on $remote) -- a tag is moved deliberately, never by a bump"

  # Read the two version files out of the commit being tagged, not out of the
  # working tree: the tag names that commit, so that is the tree to check.
  staging="$(mktemp -d)"
  trap 'rm -rf "$staging"' EXIT
  mkdir -p "$staging/cmd/cite"
  git show "$head:VERSION" >"$staging/VERSION" ||
    fail "$head has no VERSION file"
  git show "$head:cmd/cite/main.go" >"$staging/cmd/cite/main.go" ||
    fail "$head has no cmd/cite/main.go"

  say "  checking the tagged commit with scripts/version-check.sh"
  bash "$root/scripts/version-check.sh" "$tag" "$staging" ||
    fail "the commit on $default_branch does not name $tag -- merge the bump first, then tag"

  run git tag -a "$tag" -m "$tag" "$head"
  run git push "$remote" "refs/tags/$tag"
  if [ "$dry_run" -eq 1 ]; then
    say "dry run: nothing tagged, nothing pushed"
  else
    say ""
    say "tag $tag pushed. The release workflow builds and attests the three binaries from it:"
    say "  gh run list -R $(git remote get-url "$remote" | sed -E 's#\.git$##; s#.*[:/]([^/]+/[^/]+)$#\1#') --workflow release --limit 1"
    say ""
    say "Then tick the Marketplace box on the release: the API does not expose it (docs/release.md)."
  fi
  exit 0
fi

# ---------------------------------------------------------------------------
# Phase 1: prepare the bump
# ---------------------------------------------------------------------------
current_branch="$(git symbolic-ref -q --short HEAD || true)"
# A detached HEAD has no branch name to compare with the default branch, so the
# refusal below would quietly not apply and the commit would land nowhere.
[ -n "$current_branch" ] ||
  fail "HEAD is detached: check out a branch, so the release commit has one to land on"
[ "$current_branch" = "$default_branch" ] &&
  fail "refusing to bump on $default_branch: the release commit lands by pull request"

[ -z "$(git status --porcelain)" ] ||
  fail "working tree is dirty: the bump has to be the only change in the commit"

git rev-parse -q --verify "refs/remotes/$remote/$default_branch" >/dev/null ||
  fail "$remote/$default_branch is unknown after fetching $remote"
[ "$(git rev-parse HEAD)" = "$(git rev-parse "$remote/$default_branch")" ] ||
  fail "$current_branch is not level with $remote/$default_branch: rebase before bumping, or the tag will not name this commit"

tag_exists && fail "tag $tag already exists (locally or on $remote) -- a tag is moved deliberately, never by a bump"

have_file="$(tr -d '[:space:]' < VERSION 2>/dev/null || true)"
have_const="$(grep -m1 -oE '^const version = "[^"]+"' cmd/cite/main.go 2>/dev/null |
  sed -E 's|^const version = "([^"]+)"$|\1|' || true)"
[ -n "$have_file" ] || fail "VERSION is missing or empty"
[ -n "$have_const" ] || fail "no 'const version = \"...\"' line in cmd/cite/main.go"

baseline="$have_file"
[ "$have_const" = "$have_file" ] ||
  fail "VERSION says $have_file and cmd/cite/main.go says $have_const -- fix that first, so the bump commit changes exactly two strings"
[ "$baseline" != "$version" ] ||
  fail "already at $version: there would be nothing to commit, and a no-op release commit is how a tag lands on the previous version"

say "bumping $baseline -> $version on $current_branch"

# Both writes are asserted rather than assumed: a substitution that matched
# nothing would otherwise leave one of the two strings behind, and
# version-check.sh would then refuse the *tag* instead -- at the point where the
# mistake is expensive, rather than here.
if [ "$dry_run" -eq 1 ]; then
  say "  would write VERSION: $version"
  say "  would rewrite the version line in cmd/cite/main.go"
else
  printf '%s\n' "$version" >VERSION
  # Counted without -m1: a count capped at one cannot see a second version line,
  # which is the duplicate this assertion exists to catch -- `sed` rewrites every
  # matching line, so two of them would become two, and version-check.sh reads only
  # the first. Each write is asserted rather than assumed: a substitution that
  # matched nothing would leave one of the two strings behind, and version-check.sh
  # would then refuse the *tag* instead, where the mistake is expensive.
  before="$(grep -c -E '^const version = "[^"]+"$' cmd/cite/main.go || true)"
  [ "$before" = "1" ] ||
    fail "cmd/cite/main.go has $before 'const version' lines, expected exactly one"
  # No `sed -i`: BSD sed (macOS) reads the next argument as the backup suffix, so
  # `-i -E` wrote cmd/cite/main.go-E and left the version line unchanged.
  sed -E "s|^const version = \"[^\"]+\"$|const version = \"$version\"|" cmd/cite/main.go >cmd/cite/main.go.new &&
    mv cmd/cite/main.go.new cmd/cite/main.go
  # `-F -x` and not an ERE built from the version: `version_re` above allows
  # `1.0.0+build1`, and interpolated into a pattern the `+` is a quantifier, so the
  # assertion counted zero lines and failed *after* both files were written, leaving a
  # dirty tree for a version the script had just declared valid. Literal and whole-line
  # is what "did this line become the version" actually means.
  after="$(grep -c -F -x "const version = \"$version\"" cmd/cite/main.go || true)"
  [ "$after" = "1" ] ||
    fail "the version line in cmd/cite/main.go did not change exactly once ($after now)"
fi

say "  checking with scripts/version-check.sh"
run bash scripts/version-check.sh

run git add VERSION cmd/cite/main.go
run git commit -m "chore(release): bump the version string to $version"
run git push --set-upstream "$remote" "$current_branch"

if [ "$dry_run" -eq 1 ]; then
  say "dry run: nothing written, nothing pushed"
  exit 0
fi

say ""
say "bump pushed on $current_branch. Next:"
say "  1. open the pull request and merge it once CI is green"
say "  2. bash scripts/release-tag.sh $version --tag"
say ""
say "The tag happens in step 2, on the merged commit, which is why this script does"
say "not tag here: a tag on a branch is a tag on a commit the release cannot be built"
say "from, and moving it afterwards is the failure this script exists to prevent."
