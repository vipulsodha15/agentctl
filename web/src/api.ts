// fetch wrapper: pulls the bearer token from the agentctl_token cookie set
// by the loader page (architecture/api.md §3.3, ADR 0007). The cookie
// usually rides along automatically on same-origin, but we also set the
// Authorization header so the daemon's bearer middleware does not need to
// fall back to cookie parsing in practice.

export class ApiError extends Error {
  status: number;
  code?: string;
  details?: unknown;
  constructor(
    message: string,
    status: number,
    code?: string,
    details?: unknown,
  ) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

export function readCookie(name: string): string | null {
  const re = new RegExp("(?:^|; )" + name + "=([^;]*)");
  const m = document.cookie.match(re);
  return m ? decodeURIComponent(m[1]) : null;
}

export function hasToken(): boolean {
  return readCookie("agentctl_token") !== null;
}

export async function api(
  path: string,
  init?: RequestInit,
): Promise<Response> {
  const token = readCookie("agentctl_token");
  const headers = new Headers(init?.headers);
  if (token) headers.set("Authorization", "Bearer " + token);
  const method = (init?.method || "GET").toUpperCase();
  if (method !== "GET" && method !== "HEAD") {
    // Browsers set Origin automatically for cross-origin and most same-origin
    // POSTs; setting it explicitly is defensive (api.md §3.4 requires it).
    headers.set("Origin", window.location.origin);
    if (!headers.has("Content-Type") && init?.body) {
      headers.set("Content-Type", "application/json");
    }
  }
  return fetch(path, {
    ...init,
    headers,
    credentials: "same-origin",
  });
}

async function parseError(res: Response): Promise<ApiError> {
  let code: string | undefined;
  let message = res.statusText;
  let details: unknown;
  try {
    const body = await res.json();
    if (body && typeof body === "object") {
      const data = (body as { data?: unknown }).data ?? body;
      if (data && typeof data === "object") {
        const d = data as Record<string, unknown>;
        if (typeof d.code === "string") code = d.code;
        if (typeof d.message === "string") message = d.message;
        if (d.details !== undefined) details = d.details;
      }
    }
  } catch {
    // ignore parse failures; fall through with statusText
  }
  return new ApiError(message, res.status, code, details);
}

export async function apiJson<T>(
  path: string,
  init?: RequestInit,
): Promise<T> {
  const res = await api(path, init);
  if (!res.ok) throw await parseError(res);
  if (res.status === 204) return undefined as unknown as T;
  return (await res.json()) as T;
}

export function jsonBody(value: unknown): RequestInit {
  return {
    body: JSON.stringify(value),
    headers: { "Content-Type": "application/json" },
  };
}
