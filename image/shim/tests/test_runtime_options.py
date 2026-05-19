"""Tests for RuntimeDriver._options() kwarg assembly.

Guards the system_prompt plumbing that lets tm.SessionRuntime run each
task-chat stage with the stage agent's own system prompt instead of the
SDK's default Claude Code prompt.
"""

from __future__ import annotations

import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(os.path.dirname(HERE)))

from shim import runtime  # noqa: E402


class _FakeOptions:
    last_kwargs: dict = {}

    def __init__(self, **kwargs) -> None:
        _FakeOptions.last_kwargs = kwargs


class OptionsAssemblyTest(unittest.TestCase):
    def setUp(self) -> None:
        self._real_options = runtime.ClaudeAgentOptions
        runtime.ClaudeAgentOptions = _FakeOptions  # type: ignore[assignment]
        self.addCleanup(self._restore)

    def _restore(self) -> None:
        runtime.ClaudeAgentOptions = self._real_options  # type: ignore[assignment]

    def _driver(self, cfg: runtime.RuntimeConfig) -> runtime.RuntimeDriver:
        return runtime.RuntimeDriver(
            cfg,
            emit_event=lambda *_a, **_kw: None,
            emit_session_id=lambda *_a, **_kw: None,
        )

    def test_system_prompt_forwarded_when_set(self) -> None:
        cfg = runtime.RuntimeConfig(
            model="claude-sonnet-4-6",
            system_prompt="you are a helpful test agent",
        )
        self._driver(cfg)._options()
        self.assertEqual(
            _FakeOptions.last_kwargs.get("system_prompt"),
            "you are a helpful test agent",
        )

    def test_system_prompt_omitted_when_unset(self) -> None:
        cfg = runtime.RuntimeConfig(model="claude-sonnet-4-6")
        self._driver(cfg)._options()
        self.assertNotIn("system_prompt", _FakeOptions.last_kwargs)

    def test_system_prompt_omitted_when_empty_string(self) -> None:
        cfg = runtime.RuntimeConfig(model="claude-sonnet-4-6", system_prompt="")
        self._driver(cfg)._options()
        self.assertNotIn("system_prompt", _FakeOptions.last_kwargs)

    def test_sandbox_disabled_by_default(self) -> None:
        # Outer Docker profile (CapDrop ALL + no-new-privileges + ReadOnlyRootFS)
        # blocks the unshare the CLI's bwrap-based bash sandbox needs, so every
        # tool call fails fast unless we explicitly tell the SDK to skip it.
        cfg = runtime.RuntimeConfig(model="claude-sonnet-4-6")
        self._driver(cfg)._options()
        self.assertEqual(
            _FakeOptions.last_kwargs.get("sandbox"),
            {"enabled": False},
        )

    def test_sandbox_override_forwarded(self) -> None:
        override = {"enabled": True, "excludedCommands": ["git"]}
        cfg = runtime.RuntimeConfig(model="claude-sonnet-4-6", sandbox=override)
        self._driver(cfg)._options()
        self.assertEqual(_FakeOptions.last_kwargs.get("sandbox"), override)


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
