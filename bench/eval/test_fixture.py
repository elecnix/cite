import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import fixture


def write(root: Path, rel: str, text: str):
    p = root / rel
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(text)


def git(repo, *args):
    return subprocess.run(["git", *args], cwd=repo, text=True, check=True,
                          stdout=subprocess.PIPE).stdout


class BuildTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)
        self.case = self.tmp / "case"
        write(self.case, "case.json", "{}")
        write(self.case, "base/a.txt", "one\n")
        write(self.case, "base/keep.txt", "keep\n")
        write(self.case, "steps/01/a.txt", "two\n")
        write(self.case, "steps/01/new/b.txt", "b\n")
        write(self.case, "steps/02/.delete", "keep.txt\n")

    def build(self, name="repo"):
        return fixture.build_repo(self.case, self.tmp / name)

    def test_commits_base_then_one_per_step(self):
        repo, base, head = self.build()
        self.assertEqual(git(repo, "rev-list", "--count", head).strip(), "3")
        self.assertEqual(git(repo, "rev-parse", base + "~0").strip(), base)
        self.assertEqual(git(repo, "rev-list", "--count", base).strip(), "1")

    def test_steps_overlay_and_delete(self):
        repo, _, _ = self.build()
        self.assertEqual((repo / "a.txt").read_text(), "two\n")
        self.assertTrue((repo / "new" / "b.txt").exists())
        self.assertFalse((repo / "keep.txt").exists())
        self.assertFalse((repo / ".delete").exists())

    def test_diff_spans_every_step(self):
        repo, base, head = self.build()
        diff = git(repo, "diff", base, head)
        self.assertIn("+two", diff)
        self.assertIn("+++ b/new/b.txt", diff)
        self.assertIn("deleted file mode", diff)

    def test_shas_are_reproducible(self):
        _, base1, head1 = self.build("one")
        _, base2, head2 = self.build("two")
        self.assertEqual((base1, head1), (base2, head2))

    def test_fixed_identity(self):
        repo, _, head = self.build()
        self.assertEqual(git(repo, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%aI", head).strip(),
                         "Cite Eval <eval@cite.invalid>|Cite Eval <eval@cite.invalid>|2000-01-01T00:02:00Z")

    def test_case_without_steps_is_rejected(self):
        shutil.rmtree(self.case / "steps")
        with self.assertRaises(ValueError):
            self.build()

    def test_delete_cannot_leave_the_repository(self):
        outside = self.tmp / "outside.txt"
        outside.write_text("x")
        write(self.case, "steps/02/.delete", "../outside.txt\n")
        with self.assertRaises(ValueError):
            self.build()
        self.assertTrue(outside.exists())

    def test_delete_of_missing_path_is_rejected(self):
        write(self.case, "steps/02/.delete", "nope.txt\n")
        with self.assertRaises(ValueError):
            self.build()


FIXTURES = Path(__file__).parent / "testdata"


class SeedFixturesTest(unittest.TestCase):
    def test_loader_finds_every_fixture(self):
        import run
        ids = {c["id"] for c in run.load_cases([FIXTURES])}
        self.assertEqual(ids, {d.name for d in FIXTURES.iterdir() if d.is_dir()})

    def test_labels_point_at_lines_of_the_final_tree(self):
        import run
        for case in run.load_cases([FIXTURES]):
            with self.subTest(case["id"]), tempfile.TemporaryDirectory() as tmp:
                repo, _, _ = fixture.build_repo(case["fixture_dir"], Path(tmp) / "r")
                labels = (case.get("must_flag") or []) + (case.get("must_not_flag") or [])
                self.assertTrue(labels, "a fixture needs a label")
                for label in labels:
                    n = len((repo / label["path"]).read_text().splitlines())
                    self.assertTrue(1 <= label["lines"][0] <= label["lines"][1] <= n, label)


if __name__ == "__main__":
    unittest.main()
