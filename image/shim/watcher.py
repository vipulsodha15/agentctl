"""Per-repo filesystem watcher emitting throttled ``repo.changed`` frames.

Throttle: max 2 emits per second per repo. ``.git/objects`` and dotfiles
under each repo's ``.git`` are excluded from the count to keep churn from
git internals out of the signal.
"""

from __future__ import annotations

import os
import threading
import time
from typing import Any, Callable, Optional

try:
    from watchdog.events import FileSystemEventHandler
    from watchdog.observers import Observer
except Exception:  # noqa: BLE001
    FileSystemEventHandler = object  # type: ignore[misc,assignment]
    Observer = None  # type: ignore[assignment]


EmitFn = Callable[[dict], None]
THROTTLE_INTERVAL_SEC = 0.5  # 2 events / second / repo


class _RepoHandler(FileSystemEventHandler):  # type: ignore[misc]
    def __init__(self, repo_name: str, repo_root: str, emit: EmitFn) -> None:
        self._repo_name = repo_name
        self._repo_root = repo_root
        self._emit = emit
        self._lock = threading.Lock()
        self._last_emit = 0.0
        self._pending_added = 0
        self._pending_removed = 0
        self._pending_modified = 0
        self._timer: Optional[threading.Timer] = None

    def _ignored(self, path: str) -> bool:
        rel = os.path.relpath(path, self._repo_root)
        if rel.startswith(".git" + os.sep) and ".git/objects" in rel.replace(os.sep, "/"):
            return True
        if rel.startswith(".git" + os.sep):
            return True
        return False

    def _record(self, *, added: int = 0, removed: int = 0, modified: int = 0) -> None:
        with self._lock:
            self._pending_added += added
            self._pending_removed += removed
            self._pending_modified += modified
            now = time.monotonic()
            if now - self._last_emit >= THROTTLE_INTERVAL_SEC:
                self._flush_locked(now)
            elif self._timer is None:
                delay = max(0.0, THROTTLE_INTERVAL_SEC - (now - self._last_emit))
                self._timer = threading.Timer(delay, self._timer_flush)
                self._timer.daemon = True
                self._timer.start()

    def _timer_flush(self) -> None:
        with self._lock:
            self._timer = None
            if not (self._pending_added or self._pending_removed or self._pending_modified):
                return
            self._flush_locked(time.monotonic())

    def _flush_locked(self, now: float) -> None:
        payload = {
            "repo": self._repo_name,
            "files_changed": self._pending_modified,
            "additions": self._pending_added,
            "deletions": self._pending_removed,
        }
        self._pending_added = 0
        self._pending_removed = 0
        self._pending_modified = 0
        self._last_emit = now
        try:
            self._emit(payload)
        except Exception:  # noqa: BLE001
            pass

    # watchdog hooks
    def on_created(self, event):  # type: ignore[no-untyped-def]
        if event.is_directory or self._ignored(event.src_path):
            return
        self._record(added=1)

    def on_deleted(self, event):  # type: ignore[no-untyped-def]
        if event.is_directory or self._ignored(event.src_path):
            return
        self._record(removed=1)

    def on_modified(self, event):  # type: ignore[no-untyped-def]
        if event.is_directory or self._ignored(event.src_path):
            return
        self._record(modified=1)


class RepoWatcher:
    def __init__(self, *, work_root: str, repos: list[str], emit: EmitFn) -> None:
        self._work_root = work_root
        self._repos = repos
        self._emit = emit
        self._observer: Optional[Any] = None

    def start(self) -> None:
        if Observer is None or not self._repos:
            return
        self._observer = Observer()
        for name in self._repos:
            root = os.path.join(self._work_root, name)
            if not os.path.isdir(root):
                continue
            handler = _RepoHandler(name, root, self._emit)
            self._observer.schedule(handler, root, recursive=True)
        try:
            self._observer.start()
        except Exception:  # noqa: BLE001
            self._observer = None

    def stop(self) -> None:
        if self._observer is None:
            return
        try:
            self._observer.stop()
            self._observer.join(timeout=2.0)
        except Exception:  # noqa: BLE001
            pass
        self._observer = None
