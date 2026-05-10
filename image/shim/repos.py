"""Repository cloning logic for the shim.

Each entry in ``agentd.greet`` -> ``repos`` is cloned into ``/work/<basename>``
in parallel (capped at 4 workers). The clone-time SHA and branch are recorded
in ``/work/.agentctl/repo-bases.json`` for the R8 diff path. Per-repo failures
emit ``runtime.error{fatal:false}`` and never abort startup.
"""

from __future__ import annotations

import json
import os
import subprocess
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import Callable, List, Optional


@dataclass
class RepoSpec:
    name: str
    url: str
    branch: Optional[str] = None


@dataclass
class RepoResult:
    name: str
    url: str
    base_sha: str = ""
    branch: str = ""
    error: str = ""


def _basename_from_url(name: str, url: str) -> str:
    if name:
        return name
    base = url.rsplit("/", 1)[-1]
    if base.endswith(".git"):
        base = base[:-4]
    return base


def _git(args: list[str], cwd: Optional[str] = None) -> str:
    proc = subprocess.run(
        ["git", *args],
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)}: {proc.stderr.strip()}")
    return proc.stdout.strip()


def clone_one(spec: RepoSpec, work_root: str = "/work") -> RepoResult:
    basename = _basename_from_url(spec.name, spec.url)
    target = os.path.join(work_root, basename)
    if os.path.exists(target):
        try:
            sha = _git(["rev-parse", "HEAD"], cwd=target)
            branch = _git(["rev-parse", "--abbrev-ref", "HEAD"], cwd=target)
            return RepoResult(name=basename, url=spec.url, base_sha=sha, branch=branch)
        except RuntimeError as exc:
            return RepoResult(name=basename, url=spec.url, error=str(exc))
    cmd = ["clone", spec.url, target]
    if spec.branch:
        cmd = ["clone", "--branch", spec.branch, spec.url, target]
    try:
        _git(cmd)
        sha = _git(["rev-parse", "HEAD"], cwd=target)
        branch = spec.branch or _git(["rev-parse", "--abbrev-ref", "HEAD"], cwd=target)
        return RepoResult(name=basename, url=spec.url, base_sha=sha, branch=branch)
    except RuntimeError as exc:
        return RepoResult(name=basename, url=spec.url, error=str(exc))


def clone_all(
    specs: List[RepoSpec],
    *,
    work_root: str = "/work",
    max_workers: int = 4,
    on_error: Optional[Callable[[RepoResult], None]] = None,
) -> List[RepoResult]:
    if not specs:
        return []
    results: List[RepoResult] = []
    with ThreadPoolExecutor(max_workers=min(max_workers, max(1, len(specs)))) as ex:
        for res in ex.map(lambda s: clone_one(s, work_root=work_root), specs):
            results.append(res)
            if res.error and on_error is not None:
                on_error(res)
    return results


def write_repo_bases(results: List[RepoResult], work_root: str = "/work") -> str:
    out_dir = os.path.join(work_root, ".agentctl")
    os.makedirs(out_dir, exist_ok=True)
    out_path = os.path.join(out_dir, "repo-bases.json")
    payload = {
        r.name: {"url": r.url, "base_sha": r.base_sha, "branch": r.branch}
        for r in results
        if not r.error
    }
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(payload, f, indent=2, sort_keys=True)
    return out_path


def parse_repo_specs(raw: list) -> List[RepoSpec]:
    specs: List[RepoSpec] = []
    for entry in raw or []:
        if isinstance(entry, str):
            specs.append(RepoSpec(name="", url=entry))
            continue
        url = entry.get("url", "")
        if not url:
            continue
        specs.append(RepoSpec(name=entry.get("name", ""), url=url, branch=entry.get("branch")))
    return specs
