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
        const raw = JSON.parse(msg.data) as
          | (WireEvent & { data?: unknown })
          | { kind?: string; data?: unknown };
        // Per api.md §3.5, the server wraps each delivery in a §2.2 frame
        // envelope: {v, id, kind: "event"|"stream_chunk"|"stream_end"|"error",
        // data: <inner>}. Unwrap so consumers see the inner event's own
        // `kind` (e.g. "session.snapshot", "assistant.delta").
        const outerKind = (raw as { kind?: string }).kind;
        if (outerKind === "event" || outerKind === "stream_chunk") {
          const inner = (raw as { data?: unknown }).data;
          if (inner && typeof inner === "object" && "kind" in (inner as object)) {
            opts.onEvent(inner as WireEvent);
            return;
          }
        }
        if (outerKind === "stream_end" || outerKind === "error") {
          opts.onEvent({
            kind: outerKind,
            data: (raw as { data?: unknown }).data ?? {},
          } as WireEvent);
          return;
        }
        // Older / direct shape: the message is already an inner event.
        opts.onEvent(raw as WireEvent);
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
