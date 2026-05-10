// WebSocket attach client.
//
// Per architecture/api.md §3.5 and ADR 0015:
//   - Subprotocol "agentctl.v1".
//   - First frame is session.snapshot; subsequent frames are live events.
//   - Server sends pings every 20s (the browser auto-pongs).
//   - On disconnect, reconnect with backoff; the new snapshot replaces local
//     rendering state (no client-side replay buffer, no since_event cursor).

import type { WireEvent } from "./types";

export interface AttachHandle {
  close(): void;
  // True between an open WS and its onclose / explicit close().
  isOpen(): boolean;
}

export interface AttachOptions {
  onEvent: (e: WireEvent) => void;
  onOpen?: () => void;
  // Called any time the underlying socket closes; the manager will
  // schedule a reconnect unless close() was called by the consumer.
  onDisconnect?: (reason: string) => void;
}

const RECONNECT_DELAY_MS = 1000;
const MAX_RECONNECT_DELAY_MS = 10_000;

export function attach(
  sessionId: string,
  opts: AttachOptions,
): AttachHandle {
  let closedByUser = false;
  let socket: WebSocket | null = null;
  let backoff = RECONNECT_DELAY_MS;
  let reconnectTimer: number | null = null;

  function url(): string {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/v1/sessions/${encodeURIComponent(
      sessionId,
    )}/stream`;
  }

  function connect() {
    if (closedByUser) return;
    let ws: WebSocket;
    try {
      ws = new WebSocket(url(), "agentctl.v1");
    } catch (err) {
      scheduleReconnect(String(err));
      return;
    }
    socket = ws;
    ws.onopen = () => {
      backoff = RECONNECT_DELAY_MS;
      if (opts.onOpen) opts.onOpen();
    };
    ws.onmessage = (msg) => {
      if (typeof msg.data !== "string") return;
      try {
        const parsed = JSON.parse(msg.data) as WireEvent;
        opts.onEvent(parsed);
      } catch {
        // ignore malformed frames (forward-compat per api.md §2.5)
      }
    };
    ws.onerror = () => {
      // onclose will follow; nothing to do here.
    };
    ws.onclose = (ev) => {
      socket = null;
      if (closedByUser) return;
      const reason =
        ev.reason ||
        (ev.code ? `ws closed (code=${ev.code})` : "ws closed");
      if (opts.onDisconnect) opts.onDisconnect(reason);
      scheduleReconnect(reason);
    };
  }

  function scheduleReconnect(_reason: string) {
    if (closedByUser) return;
    if (reconnectTimer !== null) return;
    const delay = backoff;
    backoff = Math.min(backoff * 2, MAX_RECONNECT_DELAY_MS);
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = null;
      connect();
    }, delay);
  }

  connect();

  return {
    close() {
      closedByUser = true;
      if (reconnectTimer !== null) {
        window.clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
      if (socket) {
        try {
          socket.close(1000, "client_detach");
        } catch {
          // ignore
        }
        socket = null;
      }
    },
    isOpen() {
      return socket !== null && socket.readyState === WebSocket.OPEN;
    },
  };
}
