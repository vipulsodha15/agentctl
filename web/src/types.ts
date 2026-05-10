// Wire types mirroring architecture/api.md §1, §2.4, §5.
// Forward-compat: clients ignore unknown fields and unknown event kinds.

export type SessionStatus =
  | "starting"
  | "running"
  | "stopping"
  | "stopped"
  | "terminated"
  | "error";

export interface SessionRow {
  session_id: string;
  name: string;
  status: SessionStatus;
  last_activity_at?: string;
  image_id?: string;
  cost_usd?: number | null;
  in_flight?: boolean;
  queue_depth?: number;
  mcps?: string[];
  repos?: RepoInfo[];
  created_at?: string;
  model?: string;
}

export interface RepoInfo {
  name: string;
  url: string;
  base_sha?: string;
  branch?: string;
}

export interface ListSessionsResponse {
  sessions: SessionRow[];
  next_cursor?: string | null;
}

export interface CreateSessionRequest {
  name: string;
  mcps?: string[] | null;
  exclude_mcps?: string[];
  repos?: string[];
  model?: string | null;
  mem_limit_bytes?: number | null;
  cpu_limit_cores?: number | null;
}

export interface CreateSessionResponse {
  session_id: string;
  status: SessionStatus;
  web_url?: string;
  attach?: { stream_op: string };
}

export interface SendMessageRequest {
  content: string;
  client_id?: string;
  idempotency_key?: string;
}

export interface SendMessageResponse {
  message_id: string;
  queued: boolean;
  queue_depth: number;
}

export interface InterruptResponse {
  interrupted: boolean;
  cleared_queue_depth: number;
}

// Registry shapes (api.md §2.4 / R5)
export interface McpEntry {
  name: string;
  url: string;
  transport: string;
  kind: string;
  auth_config?: unknown | null;
  default_enabled: boolean;
  description?: string;
  created_at?: string;
}

export interface AddMcpRequest {
  name: string;
  url: string;
  transport: string;
  kind: string;
  auth_config?: unknown | null;
  default_enabled: boolean;
  description?: string;
}

export interface UpdateMcpRequest {
  url?: string;
  transport?: string;
  kind?: string;
  auth_config?: unknown | null;
  default_enabled?: boolean;
  description?: string;
}

// Skills manifest shape (R9)
export interface SkillEntry {
  name: string;
  description: string;
  source?: "builtin" | "custom";
  overrides?: boolean;
}

export interface ListSkillsResponse {
  skills: SkillEntry[];
}

// Per-session MCP status reported in snapshots / events.
export type McpHealth = "ok" | "unreachable" | "skipped";

export interface McpStatus {
  name: string;
  url?: string;
  transport?: string;
  kind?: string;
  status: McpHealth;
  error?: string;
}

// Event vocabulary (api.md §5).
// Conversation as carried in the snapshot is opaque shim-formatted JSONL;
// for rendering we coerce to a list of normalized "messages."
export type EventKind =
  | "session.snapshot"
  | "session.starting"
  | "session.running"
  | "session.stopping"
  | "session.stopped"
  | "session.resumed"
  | "session.terminated"
  | "session.error"
  | "mcp.unreachable"
  | "mcp.skipped"
  | "turn.start"
  | "turn.end"
  | "turn.cancelled"
  | "assistant.delta"
  | "assistant.message"
  | "tool.call"
  | "tool.result"
  | "user.message"
  | "usage"
  | "queue.depth"
  | "repo.changed"
  | "skills.changed"
  | "runtime.throttled";

export interface WireEvent<T = unknown> {
  v?: number;
  id?: string;
  event_id?: string;
  kind: EventKind | string;
  ts?: string;
  data: T;
  // Frame-level reasons appear on stream_end frames; tolerate both.
  reason?: string;
}

// Cost / usage (R10) wire shapes — mirror internal/proto + internal/usage.

export interface CostModelTotals {
  model: string;
  turns: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
  has_unknown_model?: boolean;
}

export interface CostTurnRow {
  turn_id: string;
  at: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd?: number | null;
}

export interface SessionCostTotals {
  session_id: string;
  turns: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
  has_unknown_model?: boolean;
  by_model: CostModelTotals[];
  timeline: CostTurnRow[];
}

export interface RangeSessionTotals {
  session_id: string;
  name?: string;
  status?: string;
  turns: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  has_unknown_model?: boolean;
}

export interface RangeCostTotals {
  start: string;
  end: string;
  turns: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
  has_unknown_model?: boolean;
  by_session: RangeSessionTotals[];
}

export interface SnapshotData {
  session: SessionRow;
  conversation: ConversationMessage[];
  queue_depth: number;
  in_flight: boolean;
  mcps_status: McpStatus[];
  repos: RepoInfo[];
}

// Normalized conversation message rendered in ConversationView.
export type RenderedMessageKind =
  | "user"
  | "assistant"
  | "tool_call"
  | "tool_result"
  | "system";

export interface ConversationMessage {
  // Stable identifier for de-dup. Prefer message_id / turn_id from the wire;
  // fall back to a synthetic id for entries reconstructed from the snapshot.
  id: string;
  kind: RenderedMessageKind;
  // For user/assistant: rendered text. For tool_call: stringified input.
  // For tool_result: stringified output.
  text: string;
  // Tool name (when kind is tool_call/tool_result).
  tool?: string;
  is_error?: boolean;
  // Marks an in-flight assistant bubble that is still receiving deltas.
  inFlight?: boolean;
  turn_id?: string;
}
