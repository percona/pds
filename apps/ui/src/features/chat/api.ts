import { ApiError, requestJson, type ApiErrorCode } from "@/lib/api";
import type {
  ChatApproval,
  ChatApprovalEnd,
  ChatConversationPage,
  ChatModel,
  ChatProvider,
  ChatReply,
  ChatThread,
} from "./types";

type ModelsResponse = { models: ChatModel[] };

export function fetchModels(provider: ChatProvider, signal?: AbortSignal): Promise<ChatModel[]> {
  return requestJson<ModelsResponse>(
    `/api/assistant/models?provider=${encodeURIComponent(provider)}`,
    { signal },
  ).then((payload) => payload.models);
}

/** Lists saved conversations, newest first. cursor is the previous page's nextCursor. */
export function fetchConversations(
  cursor?: string,
  signal?: AbortSignal,
): Promise<ChatConversationPage> {
  const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : "";
  return requestJson<ChatConversationPage>(`/api/chat/conversations${query}`, { signal });
}

/** Reads one conversation with every message and the buttons its replies carried. */
export function fetchConversation(id: string, signal?: AbortSignal): Promise<ChatThread> {
  return requestJson<ChatThread>(`/api/chat/conversations/${encodeURIComponent(id)}`, { signal });
}

/** Deletes one conversation and its messages for good. */
export function deleteConversation(id: string): Promise<void> {
  return requestJson<void>(`/api/chat/conversations/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

/** The person's answer to a change the model asked for. name is typed for a delete. */
export type ApprovalDecision = { approve: boolean; name?: string };

/**
 * Answers an approval. It is refused when the approval is gone, answered or not
 * the person's, and when a delete is approved without the database's name.
 */
export function decideApproval(id: string, decision: ApprovalDecision): Promise<void> {
  return requestJson<void>(`/api/chat/approvals/${encodeURIComponent(id)}`, {
    method: "POST",
    body: {
      decision: decision.approve ? "approve" : "reject",
      ...(decision.name === undefined ? {} : { name: decision.name }),
    },
  });
}

/**
 * One new message. The control plane keeps the thread, so earlier turns are
 * never sent: a first message has no conversationId and starts one, and every
 * later message names it.
 */
export type ChatMessageInput = {
  conversationId?: string;
  /** Recorded when a conversation starts from a database page. */
  databaseId?: string;
  /**
   * Names a first message, so a retry of it after a dropped connection is
   * answered from the conversation it already opened instead of opening another.
   */
  requestKey?: string;
  provider: ChatProvider;
  model: string;
  message: string;
};

export type SendChatOptions = {
  signal?: AbortSignal;
  /** Hears each step of the turn while the model works, such as "Reading metrics". */
  onProgress?: (label: string) => void;
  /**
   * Hears the next piece of the answer's text as the model writes it. The
   * pieces, in order, add up to the reply's text, and the reply replaces them.
   */
  onText?: (delta: string) => void;
  /** Hears a change the model asked for. The turn waits until it is answered. */
  onApproval?: (approval: ChatApproval) => void;
  /** Hears how an approval ended, whoever ended it. */
  onApprovalEnd?: (end: ChatApprovalEnd) => void;
};

const cutOffMessage = "The reply was cut off. Try again.";
const savedCutOffMessage = "The connection dropped. The reply shows in this chat once it is saved.";

/**
 * The stream ended without a reply or an error. When the conversation event
 * had already arrived, the message is saved and the turn keeps running on the
 * control plane, so conversationId names where its reply will land and a retry
 * would only ask twice.
 */
export class ChatCutOffError extends ApiError {
  readonly conversationId: string | undefined;

  constructor(status: number, conversationId?: string) {
    super("ErrDownstream", conversationId ? savedCutOffMessage : cutOffMessage, status);
    this.conversationId = conversationId;
  }
}

/**
 * Sends one chat turn and asks for it as an event stream, so the thread shows
 * each step, and the answer's text, while the model works instead of waiting in
 * silence. A turn that
 * fails before the stream opens, such as a provider with no key, answers a
 * plain JSON error, and that path goes through requestJson unchanged.
 */
export async function sendChat(
  input: ChatMessageInput,
  options: SendChatOptions = {},
): Promise<ChatReply> {
  const { signal } = options;

  let streamed: Response | undefined;
  const reply = await requestJson<ChatReply>("/api/chat", {
    method: "POST",
    body: input,
    signal,
    headers: { Accept: "text/event-stream" },
    onResponse: (response) => {
      if (response.ok && isEventStream(response)) {
        streamed = response;
        return true;
      }
      return false;
    },
  });

  if (!streamed) {
    return reply;
  }

  return readChatStream(streamed, options);
}

function isEventStream(response: Response): boolean {
  return (response.headers.get("Content-Type") ?? "").includes("text/event-stream");
}

/**
 * Reads server-sent events until the reply or error event arrives. A dropped
 * connection or a frame that does not parse reads as a cut off reply, rather
 * than as whatever the browser happens to call it.
 */
async function readChatStream(response: Response, options: SendChatOptions): Promise<ChatReply> {
  if (!response.body) {
    throw new ChatCutOffError(response.status);
  }

  const seen: { conversationId?: string } = {};
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  try {
    return await readEvents(reader, response.status, options, seen);
  } catch (error) {
    void reader.cancel().catch(() => undefined);
    const cutOff = error instanceof ChatCutOffError || !(error instanceof ApiError);
    if ((error as { name?: unknown } | null)?.name === "AbortError" || !cutOff) {
      throw error;
    }
    throw new ChatCutOffError(response.status, seen.conversationId);
  }
}

async function readEvents(
  reader: ReadableStreamDefaultReader<string>,
  status: number,
  { onProgress, onText, onApproval, onApprovalEnd }: SendChatOptions,
  seen: { conversationId?: string },
): Promise<ChatReply> {
  let buffer = "";

  for (;;) {
    // Each chunk depends on the previous read, so these cannot run together.
    // oxlint-disable-next-line no-await-in-loop
    const { value, done } = await reader.read();
    if (done) {
      break;
    }
    buffer += value;

    let boundary = buffer.indexOf("\n\n");
    while (boundary !== -1) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      boundary = buffer.indexOf("\n\n");

      const event = parseFrame(frame);
      if (!event) {
        continue;
      }
      if (event.name === "conversation") {
        const { conversationId } = JSON.parse(event.data) as { conversationId?: string };
        seen.conversationId = conversationId || undefined;
      } else if (event.name === "pending") {
        // A retry of a message still being answered: wait on its conversation.
        throw new ChatCutOffError(status, seen.conversationId);
      } else if (event.name === "progress") {
        const { label } = JSON.parse(event.data) as { label?: string };
        if (label) {
          onProgress?.(label);
        }
      } else if (event.name === "text") {
        const { delta } = JSON.parse(event.data) as { delta?: string };
        if (delta) {
          onText?.(delta);
        }
      } else if (event.name === "approval") {
        onApproval?.(JSON.parse(event.data) as ChatApproval);
      } else if (event.name === "approval_end") {
        onApprovalEnd?.(JSON.parse(event.data) as ChatApprovalEnd);
      } else if (event.name === "reply") {
        const reply = JSON.parse(event.data) as ChatReply;
        void reader.cancel().catch(() => undefined);
        return reply;
      } else if (event.name === "error") {
        const payload = JSON.parse(event.data) as { code?: ApiErrorCode; message?: string };
        throw new ApiError(
          payload.code ?? "ErrDownstream",
          payload.message ?? cutOffMessage,
          status,
        );
      }
    }
  }

  throw new ChatCutOffError(status);
}

/** Parses one SSE frame. A comment-only frame, such as a heartbeat, is skipped. */
function parseFrame(frame: string): { name: string; data: string } | undefined {
  let name = "message";
  const data: string[] = [];
  for (const line of frame.split("\n")) {
    if (line.startsWith("event:")) {
      name = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      data.push(line.slice(5).trimStart());
    }
  }

  return data.length === 0 ? undefined : { name, data: data.join("\n") };
}
