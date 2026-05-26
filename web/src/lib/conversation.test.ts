import { describe, it, expect } from "vitest";
import {
  conversationReducer,
  INITIAL_CONVERSATION_STATE,
  type ConversationState,
} from "./conversation";
import type { WireEvent } from "../types";

// Helpers ---------------------------------------------------------------

function ev(kind: string, data: Record<string, unknown>, id = ""): WireEvent {
  return {
    kind,
    data,
    ...(id ? { event_id: id } : {}),
  } as unknown as WireEvent;
}

function reduce(state: ConversationState, ...evts: WireEvent[]): ConversationState {
  let s = state;
  for (const e of evts) s = conversationReducer(s, { type: "event", e });
  return s;
}

function startTurn(id = "t1") {
  return ev("turn.start", { turn_id: id });
}

function delta(text: string, turn = "t1", eid = `d-${text}`) {
  return ev("assistant.delta", { turn_id: turn, delta: text }, eid);
}

function toolCall(useId: string, turn = "t1") {
  return ev(
    "tool.call",
    { turn_id: turn, tool_use_id: useId, name: "Bash", input: { command: "ls" } },
    `tc-${useId}`,
  );
}

function toolResult(useId: string, turn = "t1") {
  return ev(
    "tool.result",
    { turn_id: turn, tool_use_id: useId, content: "ok" },
    `tr-${useId}`,
  );
}

// Drive a turn that has an in-flight assistant bubble and a pending tool.
function streamingTurn(): ConversationState {
  return reduce(
    INITIAL_CONVERSATION_STATE,
    startTurn("t1"),
    delta("Working on it...", "t1"),
    toolCall("u1", "t1"),
  );
}

// Tests -----------------------------------------------------------------

describe("cancel_requested (optimistic ESC / Stop)", () => {
  it("clears the in-flight pill immediately", () => {
    const before = streamingTurn();
    expect(before.inFlight).toBe(true);
    expect(before.inFlightCount).toBe(1);
    const after = conversationReducer(before, { type: "cancel_requested" });
    expect(after.inFlight).toBe(false);
    expect(after.inFlightCount).toBe(0);
    expect(after.openBubbleByTurn).toEqual({});
  });

  it("flips pending tools to 'cancelled' (not 'done')", () => {
    const before = streamingTurn();
    const after = conversationReducer(before, { type: "cancel_requested" });
    const tool = after.messages.find((m) => m.kind === "tool");
    expect(tool?.status).toBe("cancelled");
    expect(tool?.ended_at).toBeDefined();
  });

  it("seals open assistant bubbles", () => {
    const before = streamingTurn();
    const after = conversationReducer(before, { type: "cancel_requested" });
    const asst = after.messages.find((m) => m.kind === "assistant");
    expect(asst?.inFlight).toBe(false);
  });

  it("populates closedTurnIds from openBubbleByTurn AND pending tool turn_ids", () => {
    const before = streamingTurn();
    const after = conversationReducer(before, { type: "cancel_requested" });
    expect(after.closedTurnIds.has("t1")).toBe(true);
  });

  it("is a no-op when nothing is in-flight (still returns same state)", () => {
    const empty = INITIAL_CONVERSATION_STATE;
    const after = conversationReducer(empty, { type: "cancel_requested" });
    expect(after).toBe(empty);
  });
});

describe("late streaming frames after cancel are dropped", () => {
  it("drops a late tool.call for a cancelled turn", () => {
    const before = streamingTurn();
    const cancelled = conversationReducer(before, { type: "cancel_requested" });
    const toolCountBefore = cancelled.messages.filter((m) => m.kind === "tool").length;

    // Runtime flushes another tool_use frame after the actor accepted the
    // interrupt but before the shim's receive loop unwound.
    const after = conversationReducer(cancelled, {
      type: "event",
      e: toolCall("u-late", "t1"),
    });

    const toolCountAfter = after.messages.filter((m) => m.kind === "tool").length;
    expect(toolCountAfter).toBe(toolCountBefore);
    // Pending pile must NOT grow back.
    expect(after.messages.find((m) => m.id === "tc-u-late")).toBeUndefined();
  });

  it("drops a late tool.result for a cancelled turn", () => {
    const before = streamingTurn();
    const cancelled = conversationReducer(before, { type: "cancel_requested" });
    const after = conversationReducer(cancelled, {
      type: "event",
      e: toolResult("u-late", "t1"),
    });
    // No standalone tr- row appended.
    expect(after.messages.find((m) => m.id === "tr-u-late")).toBeUndefined();
    // Existing cancelled tool stays cancelled — late result must not flip it
    // to done.
    const u1 = after.messages.find((m) => m.tool_use_id === "u1");
    expect(u1?.status).toBe("cancelled");
  });

  it("drops a late assistant.delta for a cancelled turn (no orphan bubble)", () => {
    const before = streamingTurn();
    const cancelled = conversationReducer(before, { type: "cancel_requested" });
    const bubblesBefore = cancelled.messages.filter((m) => m.kind === "assistant").length;
    const after = conversationReducer(cancelled, {
      type: "event",
      e: delta("more text", "t1", "d-late"),
    });
    const bubblesAfter = after.messages.filter((m) => m.kind === "assistant").length;
    expect(bubblesAfter).toBe(bubblesBefore);
    expect(after.inFlight).toBe(false);
    // No new bubble with inFlight=true should have appeared.
    expect(after.messages.find((m) => m.kind === "assistant" && m.inFlight)).toBeUndefined();
  });

  it("still records the event_id so a retry of the same late frame is also a no-op", () => {
    const before = streamingTurn();
    const cancelled = conversationReducer(before, { type: "cancel_requested" });
    const e = toolCall("u-late", "t1");
    const once = conversationReducer(cancelled, { type: "event", e });
    expect(once.seenEventIds.has("tc-u-late")).toBe(true);
    const twice = conversationReducer(once, { type: "event", e });
    expect(twice).toBe(once);
  });

  it("does NOT drop frames for a different (still-active) turn", () => {
    const before = streamingTurn();
    const cancelled = conversationReducer(before, { type: "cancel_requested" });
    // A fresh, unrelated turn starts.
    const fresh = reduce(cancelled, startTurn("t2"), delta("hi", "t2"));
    expect(fresh.inFlight).toBe(true);
    const asst = fresh.messages.find((m) => m.kind === "assistant" && m.turn_id === "t2");
    expect(asst?.text).toBe("hi");
  });
});

describe("server-initiated turn.cancelled", () => {
  it("flips pending tools to 'cancelled' (not 'done')", () => {
    const before = streamingTurn();
    const after = reduce(before, ev("turn.cancelled", { turn_id: "t1" }));
    const tool = after.messages.find((m) => m.kind === "tool");
    expect(tool?.status).toBe("cancelled");
  });

  it("seeds closedTurnIds so the runtime's post-cancel flush is dropped", () => {
    const before = streamingTurn();
    const after = reduce(before, ev("turn.cancelled", { turn_id: "t1" }));
    expect(after.closedTurnIds.has("t1")).toBe(true);

    // Subsequent late tool.call for t1 is dropped.
    const after2 = reduce(after, toolCall("u-late", "t1"));
    expect(after2.messages.find((m) => m.id === "tc-u-late")).toBeUndefined();
  });
});

describe("closedTurnIds lifecycle", () => {
  it("is cleared by reset", () => {
    const before = conversationReducer(streamingTurn(), { type: "cancel_requested" });
    expect(before.closedTurnIds.size).toBeGreaterThan(0);
    const after = conversationReducer(before, { type: "reset" });
    expect(after.closedTurnIds.size).toBe(0);
  });

  it("is preserved by snapshot replace", () => {
    const before = conversationReducer(streamingTurn(), { type: "cancel_requested" });
    const after = conversationReducer(before, {
      type: "snapshot",
      data: {
        session: { status: "running" } as never,
        conversation: [],
        in_flight: false,
        queue_depth: 0,
        mcps_status: {},
      } as never,
    });
    expect(after.closedTurnIds.has("t1")).toBe(true);
  });

  it("is un-set for a turn_id when a fresh turn.start reuses it", () => {
    const before = conversationReducer(streamingTurn(), { type: "cancel_requested" });
    expect(before.closedTurnIds.has("t1")).toBe(true);
    const after = reduce(before, startTurn("t1"));
    expect(after.closedTurnIds.has("t1")).toBe(false);
    // And now a tool.call for t1 lands normally.
    const after2 = reduce(after, toolCall("u2", "t1"));
    expect(after2.messages.find((m) => m.id === "tc-u2")).toBeDefined();
  });
});

describe("baseline invariants (regression guards)", () => {
  it("turn.cancelled with no exact match still seals leftover open bubbles when counter hits 0", () => {
    // Daemon's turn.start.turn_id != shim's turn.cancelled.turn_id case.
    const s = reduce(
      INITIAL_CONVERSATION_STATE,
      startTurn("daemon-id"),
      delta("partial", "shim-id"),
      ev("turn.cancelled", { turn_id: "other-id" }),
    );
    const asst = s.messages.find((m) => m.kind === "assistant");
    expect(asst?.inFlight).toBe(false);
    expect(s.inFlight).toBe(false);
  });

  it("tool.call closes the open bubble first (chronological order within a turn)", () => {
    const s = reduce(
      INITIAL_CONVERSATION_STATE,
      startTurn("t1"),
      delta("thinking aloud", "t1"),
      toolCall("u1", "t1"),
    );
    const order = s.messages.map((m) => m.kind);
    expect(order).toEqual(["assistant", "tool"]);
    const asst = s.messages.find((m) => m.kind === "assistant");
    expect(asst?.inFlight).toBe(false);
  });

  it("tool.result folds into the existing pending tool row, not a new row", () => {
    const s = reduce(
      INITIAL_CONVERSATION_STATE,
      startTurn("t1"),
      toolCall("u1", "t1"),
      toolResult("u1", "t1"),
    );
    const toolRows = s.messages.filter((m) => m.kind === "tool");
    expect(toolRows).toHaveLength(1);
    expect(toolRows[0].status).toBe("done");
    expect(toolRows[0].output).toBe("ok");
  });
});
