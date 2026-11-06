import { inActingTeam } from "@/lib/acting-team";
import { notifySignedOut } from "@/lib/signed-out";

/** Error codes the adapter returns. */
export type ApiErrorCode =
  | "ErrValidation"
  | "ErrNotFound"
  | "ErrConflict"
  | "ErrDownstream"
  | "ErrNotImplemented"
  | "ErrForbidden"
  | "ErrInternal";

/**
 * ApiError carries the adapter's structured error so a screen can tell a
 * missing feature (501) apart from a downstream failure (502).
 */
export class ApiError extends Error {
  readonly code: ApiErrorCode;
  readonly status: number;

  constructor(code: ApiErrorCode, message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
  }
}

const fallbackMessage = "The control plane could not be reached.";
const timeoutMessage = "The request took too long. Try again.";

type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
  headers?: Record<string, string>;
  /** The Team a write is for, when it must not follow the Team the console acts in. */
  team?: string;
  /**
   * Sees a successful response before its body is read. Returning true hands
   * the body to the caller, who reads it as a stream, and the call resolves
   * with undefined.
   */
  onResponse?: (response: Response) => boolean;
};

/**
 * Calls the adapter and returns the parsed body, or throws an ApiError built
 * from the adapter's error payload. Callers that need a shape guarantee check
 * the body themselves: the adapter is behind the same origin as the SPA, so a
 * 200 can still be index.html when /api is not proxied.
 */
export async function requestJson<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, signal, headers, team, onResponse } = options;
  const requestHeaders: Record<string, string> = { ...headers };
  if (body !== undefined) {
    requestHeaders["Content-Type"] = "application/json";
  }

  const response = await send(path, method, requestHeaders, body, signal, team);

  // The session is a cookie the browser attaches on its own, so a 401 here
  // means it stopped being accepted: signed out elsewhere, password reset, or
  // simply expired. The gate around the app listens for this and sends the
  // customer to the login page rather than leaving a screen half loaded.
  //
  // The auth routes are left out. They answer 401 as their ordinary result,
  // and a wrong password is not the session ending.
  if (response.status === 401 && !isAuthRoute(path)) {
    notifySignedOut();
  }

  if (response.status === 204 || (response.ok && onResponse?.(response))) {
    return undefined as T;
  }

  const payload: unknown = await response.json().catch(() => null);

  if (!response.ok) {
    throw toApiError(payload, response.status);
  }

  return payload as T;
}

/** Reports whether a path is one of the routes that answer without a session. */
function isAuthRoute(path: string): boolean {
  return path.startsWith("/api/auth/");
}

/** Sends one request, mapping a transport failure to an ApiError. */
async function send(
  path: string,
  method: string,
  headers: Record<string, string>,
  body: unknown,
  signal?: AbortSignal,
  team?: string,
): Promise<Response> {
  try {
    return await fetch(inActingTeam(path, method, team), {
      method,
      signal,
      headers: Object.keys(headers).length === 0 ? undefined : headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (error) {
    if (error instanceof Error && error.name === "AbortError") {
      throw error;
    }
    throw new ApiError("ErrDownstream", fallbackMessage, 0);
  }
}

/** toApiError reads the adapter's `{code, message}` error body. */
export function toApiError(body: unknown, status: number): ApiError {
  const payload = (body ?? {}) as { code?: ApiErrorCode; message?: string };
  const message = payload.message ?? (status === 504 ? timeoutMessage : fallbackMessage);

  return new ApiError(payload.code ?? "ErrInternal", message, status);
}
