"""In-shim git operations for R8 diff/export.

Each function reads ``/work/.agentctl/repo-bases.json`` (written by ``repos.py``
at clone time) to find the recorded base SHA per repo. Output is yielded as
``bytes`` chunks so callers can stream over the control socket without
materialising whole patches in memory.
"""

from __future__ import annotations

import io
import json
import os
import subprocess
from dataclasses import dataclass
from typing import Iterator, List, Optional


REPO_BASES_PATH = "/work/.agentctl/repo-bases.json"
WORK_ROOT = "/work"
DEFAULT_CHUNK_BYTES = 512 * 1024


@dataclass
class RepoBase:
    name: str
    path: str
    base_sha: str
    branch: str
    note: str = ""


def load_repo_bases(work_root: str = WORK_ROOT) -> List[RepoBase]:
    path = os.path.join(work_root, ".agentctl", "repo-bases.json")
    if not os.path.exists(path):
        return _fallback_repo_bases(work_root)
    try:
        with open(path, "r", encoding="utf-8") as f:
            payload = json.load(f) or {}
    except (OSError, ValueError):
        return _fallback_repo_bases(work_root)
    out: List[RepoBase] = []
    for name, entry in sorted(payload.items()):
        if not isinstance(entry, dict):
            continue
        repo_path = os.path.join(work_root, name)
        out.append(RepoBase(
            name=name,
            path=repo_path,
            base_sha=str(entry.get("base_sha") or ""),
            branch=str(entry.get("branch") or ""),
        ))
    if not out:
        return _fallback_repo_bases(work_root)
    return out


def _fallback_repo_bases(work_root: str) -> List[RepoBase]:
    if not os.path.isdir(work_root):
        return []
    out: List[RepoBase] = []
    for entry in sorted(os.listdir(work_root)):
        if entry.startswith("."):
            continue
        repo_path = os.path.join(work_root, entry)
        if not os.path.isdir(os.path.join(repo_path, ".git")):
            continue
        out.append(RepoBase(
            name=entry,
            path=repo_path,
            base_sha="HEAD",
            branch="",
            note="repo-bases.json missing; falling back to HEAD (diff will be empty)",
        ))
    return out


def select_repo(bases: List[RepoBase], name: str) -> Optional[RepoBase]:
    if not name:
        return None
    for b in bases:
        if b.name == name:
            return b
    return None


def _run(cmd: List[str], *, cwd: Optional[str] = None) -> subprocess.CompletedProcess:
    return subprocess.run(
        cmd,
        cwd=cwd,
        capture_output=True,
        check=False,
    )


def _chunk_bytes(data: bytes, chunk_bytes: int) -> Iterator[bytes]:
    if not data:
        return
    if len(data) <= chunk_bytes:
        yield data
        return
    for i in range(0, len(data), chunk_bytes):
        yield data[i:i + chunk_bytes]


def _untracked_patch(repo: RepoBase) -> bytes:
    proc = _run(
        ["git", "-C", repo.path, "ls-files", "--others", "--exclude-standard", "-z"],
    )
    if proc.returncode != 0 or not proc.stdout:
        return b""
    paths = [p for p in proc.stdout.split(b"\x00") if p]
    if not paths:
        return b""
    buf = io.BytesIO()
    for raw in paths:
        try:
            rel = raw.decode("utf-8")
        except UnicodeDecodeError:
            continue
        np = _run(
            ["git", "-C", repo.path, "diff", "--no-color", "--no-index",
             "--", os.devnull, rel],
        )
        if np.stdout:
            buf.write(np.stdout)
    return buf.getvalue()


def diff(
    repo: RepoBase,
    *,
    fmt: str = "unified",
    chunk_bytes: int = DEFAULT_CHUNK_BYTES,
) -> Iterator[bytes]:
    """Yield diff output bytes for *repo* against ``repo.base_sha``.

    ``fmt`` is ``"unified"`` (default) or ``"stat"``. Untracked files are
    appended for the unified format using ``git diff --no-index`` against
    ``/dev/null``.
    """

    base = repo.base_sha or "HEAD"
    args = ["git", "-C", repo.path, "diff", "--no-color"]
    if fmt == "stat":
        args.append("--stat")
    else:
        args.append("--unified=3")
    args.append(base)
    proc = _run(args)
    if proc.stdout:
        yield from _chunk_bytes(proc.stdout, chunk_bytes)
    if fmt != "stat":
        untracked = _untracked_patch(repo)
        if untracked:
            yield from _chunk_bytes(untracked, chunk_bytes)


def diff_exit_code(repo: RepoBase, *, fmt: str = "unified") -> int:
    base = repo.base_sha or "HEAD"
    args = ["git", "-C", repo.path, "diff", "--quiet"]
    if fmt == "stat":
        args.append("--stat")
    args.append(base)
    proc = _run(args)
    return proc.returncode


def export_patch(
    repo: RepoBase,
    *,
    chunk_bytes: int = DEFAULT_CHUNK_BYTES,
) -> Iterator[bytes]:
    """Same as ``diff`` but uses ``--patch`` explicitly per R8."""

    base = repo.base_sha or "HEAD"
    args = ["git", "-C", repo.path, "diff", "--no-color", "--patch", base]
    proc = _run(args)
    if proc.stdout:
        yield from _chunk_bytes(proc.stdout, chunk_bytes)
    untracked = _untracked_patch(repo)
    if untracked:
        yield from _chunk_bytes(untracked, chunk_bytes)


@dataclass
class PushResult:
    success: bool
    branch: str
    output: str
    error: str = ""
    exit_code: int = 0


def export_push(repo: RepoBase, branch: str, message: str = "") -> PushResult:
    """``git checkout -B`` / ``add -A`` / ``commit`` / ``push -u origin <branch>``.

    A "nothing to commit" exit from ``git commit`` is tolerated. The push step
    is the authoritative success signal.
    """

    if not branch:
        return PushResult(success=False, branch="", output="", error="branch required", exit_code=64)
    if not os.path.isdir(os.path.join(repo.path, ".git")):
        return PushResult(success=False, branch=branch, output="", error=f"{repo.path}: not a git repo", exit_code=2)
    out = io.StringIO()
    msg = message or f"agentctl session changes ({branch})"

    def step(label: str, args: List[str], tolerate: bool = False) -> int:
        proc = _run(args, cwd=repo.path)
        out.write(f"$ {' '.join(args)}\n")
        if proc.stdout:
            out.write(proc.stdout.decode("utf-8", "replace"))
        if proc.stderr:
            out.write(proc.stderr.decode("utf-8", "replace"))
        if proc.returncode != 0 and not tolerate:
            out.write(f"({label} exited {proc.returncode})\n")
        return proc.returncode

    if step("checkout", ["git", "-C", repo.path, "checkout", "-B", branch]) != 0:
        return PushResult(success=False, branch=branch, output=out.getvalue(),
                          error="checkout failed", exit_code=2)
    if step("add", ["git", "-C", repo.path, "add", "-A"]) != 0:
        return PushResult(success=False, branch=branch, output=out.getvalue(),
                          error="add failed", exit_code=2)
    step("commit", ["git", "-C", repo.path, "commit", "-m", msg], tolerate=True)
    rc = step("push", ["git", "-C", repo.path, "push", "-u", "origin", branch])
    if rc != 0:
        return PushResult(success=False, branch=branch, output=out.getvalue(),
                          error="push failed", exit_code=rc)
    return PushResult(success=True, branch=branch, output=out.getvalue(), exit_code=0)
