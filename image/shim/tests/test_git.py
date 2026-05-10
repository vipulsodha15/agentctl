"""Unit tests for the in-shim git helpers (R8).

Exercises ``diff`` / ``export_patch`` against a temporary git repo, plus the
``repo-bases.json`` fallback. ``export_push`` is exercised by pushing into a
bare repo created in the same temp dir (no real remote needed).
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(os.path.dirname(HERE)))

from shim import git as gitops  # noqa: E402


def _git(args, cwd):
    proc = subprocess.run(["git", *args], cwd=cwd, capture_output=True, check=False)
    if proc.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)}: {proc.stderr.decode('utf-8', 'replace')}")
    return proc.stdout.decode("utf-8", "replace").strip()


def _init_repo(work_root, name="alpha"):
    repo = os.path.join(work_root, name)
    os.makedirs(repo, exist_ok=True)
    _git(["init", "--initial-branch=main"], cwd=repo)
    _git(["config", "user.email", "test@example.com"], cwd=repo)
    _git(["config", "user.name", "test"], cwd=repo)
    _git(["config", "commit.gpgsign", "false"], cwd=repo)
    _git(["config", "tag.gpgsign", "false"], cwd=repo)
    with open(os.path.join(repo, "README.md"), "w", encoding="utf-8") as f:
        f.write("hello\n")
    _git(["add", "-A"], cwd=repo)
    _git(["commit", "-m", "init"], cwd=repo)
    base_sha = _git(["rev-parse", "HEAD"], cwd=repo)
    return repo, base_sha


def _write_repo_bases(work_root, name, base_sha, branch="main"):
    out_dir = os.path.join(work_root, ".agentctl")
    os.makedirs(out_dir, exist_ok=True)
    with open(os.path.join(out_dir, "repo-bases.json"), "w", encoding="utf-8") as f:
        json.dump({name: {"url": "x", "base_sha": base_sha, "branch": branch}}, f)


class DiffTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="shim-git-")

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_diff_after_edit(self):
        repo, base = _init_repo(self.tmp)
        _write_repo_bases(self.tmp, "alpha", base)
        with open(os.path.join(repo, "README.md"), "a", encoding="utf-8") as f:
            f.write("world\n")
        bases = gitops.load_repo_bases(self.tmp)
        self.assertEqual(len(bases), 1)
        self.assertEqual(bases[0].name, "alpha")
        chunks = b"".join(gitops.diff(bases[0]))
        text = chunks.decode("utf-8")
        self.assertIn("README.md", text)
        self.assertIn("+world", text)

    def test_diff_includes_untracked(self):
        repo, base = _init_repo(self.tmp)
        _write_repo_bases(self.tmp, "alpha", base)
        with open(os.path.join(repo, "new.txt"), "w", encoding="utf-8") as f:
            f.write("brand new\n")
        bases = gitops.load_repo_bases(self.tmp)
        chunks = b"".join(gitops.diff(bases[0]))
        text = chunks.decode("utf-8")
        self.assertIn("new.txt", text)
        self.assertIn("+brand new", text)

    def test_export_patch_format(self):
        repo, base = _init_repo(self.tmp)
        _write_repo_bases(self.tmp, "alpha", base)
        with open(os.path.join(repo, "README.md"), "a", encoding="utf-8") as f:
            f.write("more\n")
        bases = gitops.load_repo_bases(self.tmp)
        chunks = b"".join(gitops.export_patch(bases[0]))
        self.assertTrue(chunks.startswith(b"diff --git"))
        self.assertIn(b"+more", chunks)

    def test_diff_stat_format(self):
        repo, base = _init_repo(self.tmp)
        _write_repo_bases(self.tmp, "alpha", base)
        with open(os.path.join(repo, "README.md"), "a", encoding="utf-8") as f:
            f.write("more\n")
        bases = gitops.load_repo_bases(self.tmp)
        chunks = b"".join(gitops.diff(bases[0], fmt="stat"))
        text = chunks.decode("utf-8")
        self.assertIn("README.md", text)
        self.assertIn("|", text)

    def test_repo_bases_fallback_to_HEAD(self):
        _init_repo(self.tmp, name="solo")
        bases = gitops.load_repo_bases(self.tmp)
        self.assertEqual(len(bases), 1)
        self.assertEqual(bases[0].base_sha, "HEAD")
        self.assertNotEqual(bases[0].note, "")

    def test_select_repo(self):
        repo, base = _init_repo(self.tmp)
        _write_repo_bases(self.tmp, "alpha", base)
        bases = gitops.load_repo_bases(self.tmp)
        self.assertIsNotNone(gitops.select_repo(bases, "alpha"))
        self.assertIsNone(gitops.select_repo(bases, "missing"))


class PushTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="shim-git-push-")

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_push_to_local_bare_remote(self):
        bare = os.path.join(self.tmp, "bare.git")
        os.makedirs(bare)
        _git(["init", "--bare", "--initial-branch=main"], cwd=bare)
        repo, base = _init_repo(self.tmp, name="alpha")
        _git(["remote", "add", "origin", bare], cwd=repo)
        _write_repo_bases(self.tmp, "alpha", base)
        with open(os.path.join(repo, "fresh.txt"), "w", encoding="utf-8") as f:
            f.write("fresh\n")
        bases = gitops.load_repo_bases(self.tmp)
        res = gitops.export_push(bases[0], "feature/x", "from agentctl")
        self.assertTrue(res.success, msg=res.output + "\n" + res.error)
        self.assertEqual(res.branch, "feature/x")
        # Bare remote saw the new branch.
        listing = _git(["branch", "--list"], cwd=bare)
        self.assertIn("feature/x", listing)

    def test_push_failure_when_no_remote(self):
        repo, base = _init_repo(self.tmp, name="orphan")
        _write_repo_bases(self.tmp, "orphan", base)
        bases = gitops.load_repo_bases(self.tmp)
        res = gitops.export_push(bases[0], "feature/y", "no remote")
        self.assertFalse(res.success)
        self.assertNotEqual(res.exit_code, 0)
        self.assertIn("origin", res.output.lower() + " " + res.error.lower())


if __name__ == "__main__":
    unittest.main()
