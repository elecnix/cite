"""Build a throwaway git repository from a fixture case directory.

    <case>/case.json   labels, description, config (same fields as a pinned case)
    <case>/base/       the tree before the change
    <case>/steps/NN/   files to add or overwrite in the NNth commit; an optional
                       `.delete` file lists paths to remove, one per line

The identity and dates are fixed and git's own configuration is ignored, so the
same fixture yields the same commit SHAs on every machine.
"""

import os
import shutil
import subprocess
from pathlib import Path

IDENTITY = {"NAME": "Cite Eval", "EMAIL": "eval@cite.invalid"}
EPOCH = 946684800  # 2000-01-01T00:00:00Z; commit N is N minutes later


def _git(repo, *args, env):
    r = subprocess.run(["git", *args], cwd=repo, env=env, text=True,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)}: {r.stderr.strip()[:500]}")
    return r.stdout.strip()


def _env(n):
    when = f"{EPOCH + 60 * n} +0000"
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    env.update({
        "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_AUTHOR_NAME": IDENTITY["NAME"], "GIT_AUTHOR_EMAIL": IDENTITY["EMAIL"],
        "GIT_COMMITTER_NAME": IDENTITY["NAME"], "GIT_COMMITTER_EMAIL": IDENTITY["EMAIL"],
        "GIT_AUTHOR_DATE": when, "GIT_COMMITTER_DATE": when,
    })
    return env


def _overlay(src: Path, dst: Path):
    for p in sorted(src.rglob("*")):
        rel = p.relative_to(src)
        if p.is_dir():
            (dst / rel).mkdir(parents=True, exist_ok=True)
        elif rel.name != ".delete" or rel.parent != Path("."):
            (dst / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(p, dst / rel)


def _commit(repo, message, n):
    env = _env(n)
    # Rebuild the index so same-size, same-second edits are never taken for
    # unchanged files by git's stat cache.
    (repo / ".git" / "index").unlink(missing_ok=True)
    _git(repo, "add", "-A", env=env)
    _git(repo, "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", message, env=env)
    return _git(repo, "rev-parse", "HEAD", env=env)


def build_repo(case_dir, dest):
    """Create the repository at dest. Returns (path, base_sha, head_sha)."""
    case_dir, dest = Path(case_dir), Path(dest)
    steps = sorted(p for p in (case_dir / "steps").glob("*") if p.is_dir())
    if not steps:
        raise ValueError(f"{case_dir}: no steps/NN directory")
    dest.mkdir(parents=True)
    _git(dest, "init", "--quiet", "--initial-branch=main", env=_env(0))
    base_dir = case_dir / "base"
    if base_dir.is_dir():
        _overlay(base_dir, dest)
    base = _commit(dest, "base", 0)
    head = base
    for n, step in enumerate(steps, start=1):
        marker = step / ".delete"
        if marker.exists():
            for rel in (l for l in marker.read_text().splitlines() if l.strip()):
                target = (dest / rel).resolve()
                if dest.resolve() not in target.parents:
                    raise ValueError(f"{step.name}: .delete names {rel}, which is outside the repository")
                if not target.is_file():
                    raise ValueError(f"{step.name}: .delete names {rel}, which is not a file")
                target.unlink()
        _overlay(step, dest)
        head = _commit(dest, f"step {step.name}", n)
    return dest, base, head
