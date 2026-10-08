#!/usr/bin/env python3
"""Quality evaluation of the reviewer against labelled historical pull requests.

`cite soak` checks the plumbing and says so. This harness measures what soak
does not: whether the reviewer flags the lines a human labelled as defects,
and whether it stays quiet on the lines a human labelled as correct. Each case
is a real pull request at a fixed base and head, so a run is reproducible up
to the model's own sampling, which is why --repeat exists.

    bench/eval/run.py --cite ./cite bench/eval/cases            # every case
    bench/eval/run.py --cite ./cite --repeat 3 bench/eval/cases/x.json

The model comes from the same environment the CLI reads (MODEL_BASE_URL,
MODEL_ID, MODEL_API_KEY, CITE_STRUCTURED_OUTPUT, CITE_REASONING_EFFORT).
Repositories are fetched into --cache once and reused.

Case format (one JSON file per case):

    {
      "id": "cite-171-similarity",
      "repo": "elecnix/cite",
      "base": "<sha>", "head": "<sha>",
      "description": "optional pull request body",
      "config": "optional .github/cite.yml content",
      "must_flag":     [{"path": "a.go", "lines": [10, 12], "rubric": "..."}],
      "must_not_flag": [{"path": "a.go", "lines": [40, 40], "why": "..."}]
    }

Lines are post-change line numbers at head. A finding hits a label when its
path matches and its anchor overlaps the label's range widened by --slack.
A case with neither list is a clean case: every finding on it is a false
positive.

Standard library only, like the Go module it measures.
"""

import argparse
import concurrent.futures
import json
import os
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path


def sh(args, cwd=None, check=True, capture=True, env=None):
    r = subprocess.run(args, cwd=cwd, env=env, text=True,
                       stdout=subprocess.PIPE if capture else None,
                       stderr=subprocess.PIPE if capture else None)
    if check and r.returncode != 0:
        raise RuntimeError(f"{' '.join(args)}: exit {r.returncode}: {r.stderr.strip()[:500]}")
    return r


_repo_locks = {}
_repo_locks_guard = threading.Lock()


def ensure_repo(cache: Path, repo: str, shas) -> Path:
    """A bare clone per repository, fetched once, with the case's commits.

    One lock per repository: cases run in parallel, and two jobs cloning
    into one directory corrupt it. A commit the clone lacks (a pull request
    branch since deleted) is fetched by the pull request refs, which GitHub
    keeps, and then by its full SHA. Cases name full SHAs: a fetch by an
    abbreviated one is refused."""
    with _repo_locks_guard:
        lock = _repo_locks.setdefault(repo, threading.Lock())
    with lock:
        bare = cache / (repo.replace("/", "__") + ".git")
        if not bare.exists():
            cache.mkdir(parents=True, exist_ok=True)
            sh(["git", "clone", "--bare", "--quiet", f"https://github.com/{repo}.git", str(bare)])

        def missing():
            return [s for s in shas if sh(["git", "cat-file", "-e", s + "^{commit}"], cwd=bare, check=False).returncode != 0]

        if missing():
            sh(["git", "fetch", "--quiet", "origin", "+refs/heads/*:refs/heads/*", "+refs/pull/*/head:refs/pull/*/head"], cwd=bare)
        for sha in missing():
            # A commit no ref reaches any more (a force-pushed branch) is
            # still served by its full SHA.
            sh(["git", "fetch", "--quiet", "origin", sha], cwd=bare, check=False)
        if missing():
            raise RuntimeError(f"{repo}: commits not found: {', '.join(missing())}")
        return bare


def overlaps(anchor, rng, slack):
    lo, hi = rng[0] - slack, rng[1] + slack
    return not (anchor[1] < lo or anchor[0] > hi)


def finding_anchor(f):
    a = f.get("anchor") or {}
    s = a.get("start_line") or 0
    e = a.get("end_line") or s
    return (s, max(s, e))


def score(case, record, slack):
    findings = record.get("findings") or []
    must_flag = case.get("must_flag") or []
    must_not = case.get("must_not_flag") or []
    clean = not must_flag and not must_not

    def hits(label):
        return [f for f in findings
                if f.get("path") == label["path"] and overlaps(finding_anchor(f), label["lines"], slack)]

    tp = [l for l in must_flag if hits(l)]
    fn = [l for l in must_flag if not hits(l)]
    fp_labelled = [l for l in must_not if hits(l)]
    if clean:
        fp_any = findings
    else:
        # A finding that hits no label is unlabelled: reported, not scored.
        fp_any = [f for l in must_not for f in hits(l)]
    unlabelled = [f for f in findings
                  if not any(f.get("path") == l["path"] and overlaps(finding_anchor(f), l["lines"], slack)
                             for l in must_flag + must_not)]
    errored = [fo for fo in record.get("files") or [] if fo.get("state") == "errored"]
    return {
        "tp": len(tp), "fn": len(fn), "fp_labels": len(fp_labelled),
        "fp_findings": len(fp_any), "findings": len(findings),
        "blocking": sum(1 for f in findings if f.get("blocks")),
        "unlabelled": len(unlabelled) if not clean else 0,
        "errored_files": [(fo.get("path"), fo.get("reason")) for fo in errored],
        "verdict": record.get("verdict"),
        "missed": [l.get("rubric", "") for l in fn],
        "false_positive_titles": [f"{f.get('path')}:{finding_anchor(f)[0]} {f.get('title')}" for f in fp_any],
        "unlabelled_titles": [f"{f.get('path')}:{finding_anchor(f)[0]} {f.get('title')}" for f in unlabelled] if not clean else [],
        "usage": record.get("usage") or {},
    }


def run_case(case, args, rep):
    bare = ensure_repo(Path(args.cache), case["repo"], [case["base"], case["head"]])
    out_dir = Path(args.out) / case["id"]
    out_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="cite-eval-") as tmp:
        wt = Path(tmp) / "wt"
        sh(["git", "worktree", "add", "--detach", "--quiet", str(wt), case["head"]], cwd=bare)
        try:
            diff = sh(["git", "diff", f"{case['base']}...{case['head']}"], cwd=wt).stdout
            diff_path = Path(tmp) / "pr.diff"
            diff_path.write_text(diff)
            cfg_path = Path(tmp) / "cite.yml"
            cfg_path.write_text(case.get("config", ""))
            desc_path = Path(tmp) / "body.md"
            desc_path.write_text(case.get("description", ""))
            record_path = out_dir / f"record-{rep}.json"
            log_path = out_dir / f"log-{rep}.txt"
            env = dict(os.environ)
            cmd = [str(Path(args.cite).resolve()), "review", "--diff", str(diff_path), "--config", str(cfg_path),
                   "--report", "json", "--out", str(record_path)]
            if case.get("description"):
                cmd += ["--description", str(desc_path)]
            t0 = time.time()
            r = subprocess.run(cmd, cwd=wt, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            elapsed = time.time() - t0
            log_path.write_text(r.stdout)
        finally:
            sh(["git", "worktree", "remove", "--force", str(wt)], cwd=bare, check=False)
    if not record_path.exists():
        return {"id": case["id"], "rep": rep, "error": "no run record", "elapsed": elapsed}
    record = json.loads(record_path.read_text()).get("run") or {}
    s = score(case, record, args.slack)
    s.update({"id": case["id"], "rep": rep, "elapsed": round(elapsed, 1)})
    return s


def load_cases(paths):
    cases = []
    for p in paths:
        p = Path(p)
        files = sorted(p.glob("*.json")) if p.is_dir() else [p]
        for f in files:
            c = json.loads(f.read_text())
            c.setdefault("id", f.stem)
            cases.append(c)
    return cases


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("cases", nargs="+", help="case files or directories of them")
    ap.add_argument("--cite", required=True, help="path to the cite binary under test")
    ap.add_argument("--cache", default=os.path.expanduser("~/.cache/cite-eval"))
    ap.add_argument("--out", default="bench/eval/out")
    ap.add_argument("--repeat", type=int, default=1)
    ap.add_argument("--jobs", type=int, default=4)
    ap.add_argument("--slack", type=int, default=2, help="lines of tolerance around a label")
    ap.add_argument("--json", help="write every per-run result to this file")
    args = ap.parse_args()

    cases = load_cases(args.cases)
    jobs = [(c, rep) for c in cases for rep in range(args.repeat)]
    results = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as ex:
        futs = {ex.submit(run_case, c, args, rep): (c, rep) for c, rep in jobs}
        for fut in concurrent.futures.as_completed(futs):
            c, rep = futs[fut]
            try:
                res = fut.result()
            except Exception as e:  # one broken case must not hide the rest
                res = {"id": c["id"], "rep": rep, "error": str(e)}
            results.append(res)
            print(json.dumps(res), file=sys.stderr, flush=True)

    results.sort(key=lambda r: (r["id"], r["rep"]))
    tot = {k: 0 for k in ("tp", "fn", "fp_labels", "fp_findings", "findings", "blocking", "errored", "runs")}
    print(f"{'case':44} {'rep':>3} {'tp':>3} {'fn':>3} {'fpL':>3} {'fpF':>3} {'all':>3} {'blk':>3} {'err':>3} {'secs':>6}")
    for r in results:
        if "error" in r:
            print(f"{r['id'][:44]:44} {r['rep']:>3} ERROR {r['error'][:80]}")
            tot["errored"] += 1
            tot["runs"] += 1
            continue
        err = len(r["errored_files"])
        print(f"{r['id'][:44]:44} {r['rep']:>3} {r['tp']:>3} {r['fn']:>3} {r['fp_labels']:>3} {r['fp_findings']:>3} "
              f"{r['findings']:>3} {r['blocking']:>3} {err:>3} {r['elapsed']:>6}")
        for k in ("tp", "fn", "fp_labels", "fp_findings", "findings", "blocking"):
            tot[k] += r[k]
        tot["errored"] += 1 if err else 0
        tot["runs"] += 1
    labelled = tot["tp"] + tot["fn"]
    recall = tot["tp"] / labelled if labelled else float("nan")
    print()
    print(f"runs={tot['runs']} recall={recall:.2f} ({tot['tp']}/{labelled}) "
          f"false_positive_findings={tot['fp_findings']} labelled_fp_hits={tot['fp_labels']} "
          f"findings={tot['findings']} blocking={tot['blocking']} runs_with_errored_files={tot['errored']}")
    if args.json:
        Path(args.json).write_text(json.dumps(results, indent=2))


if __name__ == "__main__":
    main()
