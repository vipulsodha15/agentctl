"""Round-trip and framing tests for the shim's control client.

Runs without claude_agent_sdk installed: nothing in this file imports
``runtime`` or ``__main__``.
"""

from __future__ import annotations

import json
import os
import socket
import sys
import threading
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(os.path.dirname(HERE)))

from shim import control  # noqa: E402


def make_socket_pair():
    server, client = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    return server, client


class FrameTests(unittest.TestCase):
    def test_round_trip(self):
        server, client = make_socket_pair()
        srv_client = control.ControlClient(server)
        cli_client = control.ControlClient(client)
        srv_client.send(control.KIND_HEARTBEAT, {"foo": "bar"})
        frame = cli_client.recv()
        self.assertIsNotNone(frame)
        self.assertEqual(frame["kind"], control.KIND_HEARTBEAT)
        self.assertEqual(frame["data"], {"foo": "bar"})
        self.assertEqual(frame["v"], 1)
        srv_client.close()
        cli_client.close()

    def test_oversize_frame_rejected(self):
        server, client = make_socket_pair()
        cc = control.ControlClient(server)
        big = "x" * (control.MAX_FRAME_BYTES + 10)
        with self.assertRaises(control.FrameTooLarge):
            cc.send(control.KIND_EVENT, {"text": big})
        cc.close()
        client.close()

    def test_hello_payload_includes_token(self):
        payload = control.hello_payload("tok-1", shim_version="1.0.0", sdk_version="0.1.80")
        self.assertEqual(payload["session_token"], "tok-1")
        self.assertEqual(payload["shim_version"], "1.0.0")
        self.assertIn(control.KIND_HELLO, payload["capabilities"])

    def test_stderr_filter_drops_known_noise(self):
        line = "Error in hook callback hook_0: Stream closed"
        self.assertFalse(control.stderr_filter(line))
        self.assertTrue(control.stderr_filter("real diagnostic"))


class HandshakeTests(unittest.TestCase):
    def test_hello_then_greet_sequence(self):
        server, client = make_socket_pair()
        agentd = control.ControlClient(server)
        shim = control.ControlClient(client)

        sent = {}

        def shim_thread():
            shim.send(control.KIND_HELLO, control.hello_payload("good", shim_version="1.0", sdk_version="0.1.80"))
            f = shim.recv()
            sent["greet_kind"] = f["kind"] if f else None
            sent["greet_data"] = (f or {}).get("data", {})

        t = threading.Thread(target=shim_thread)
        t.start()

        hello = agentd.recv()
        self.assertEqual(hello["kind"], control.KIND_HELLO)
        self.assertEqual(hello["data"]["session_token"], "good")
        agentd.send(control.KIND_GREET, {
            "session_id": "sess-1",
            "model": "claude-3-5-sonnet",
            "repos": [],
            "mcps": [],
            "limits": {},
        })
        t.join(timeout=2.0)

        self.assertEqual(sent["greet_kind"], control.KIND_GREET)
        self.assertEqual(sent["greet_data"]["session_id"], "sess-1")

        agentd.close()
        shim.close()


if __name__ == "__main__":
    unittest.main()
