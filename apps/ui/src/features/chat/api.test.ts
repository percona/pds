import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatCutOffError, decideApproval, sendChat } from "./api";
import type { ChatMessageInput } from "./api";

const input: ChatMessageInput = {
  provider: "anthropic",
  model: "m",
  message: "How is orders-dev?",
};

/** A controllable event stream: the test pushes chunks and closes it. */
function eventStream() {
  const encoder = new TextEncoder();
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      controller = c;
    },
  });

  return {
    response: new Response(body, {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    }),
    push: (chunk: string) => controller.enqueue(encoder.encode(chunk)),
    close: () => controller.close(),
    fail: (error: Error) => controller.error(error),
  };
}

function stubFetch(response: Response) {
  const fetchMock = vi.fn(() => Promise.resolve(response));
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("sendChat", () => {
  it("asks for an event stream", async () => {
    const stream = eventStream();
    const fetchMock = stubFetch(stream.response);

    const pending = sendChat(input);
    stream.push('event: reply\ndata: {"text":"ok","actions":[]}\n\n');
    stream.close();
    await pending;

    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>).Accept).toBe("text/event-stream");
  });

  it("sends one message, not the thread", async () => {
    const stream = eventStream();
    const fetchMock = stubFetch(stream.response);

    const pending = sendChat({ ...input, conversationId: "c1" });
    stream.push('event: reply\ndata: {"conversationId":"c1","text":"ok","actions":[]}\n\n');
    stream.close();
    await pending;

    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(JSON.parse(String(init.body))).toEqual({
      conversationId: "c1",
      provider: "anthropic",
      model: "m",
      message: "How is orders-dev?",
    });
  });

  it("keeps the saved conversation when the stream drops after it", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat(input);
    stream.push('event: conversation\ndata: {"conversationId":"c9"}\n\n');
    stream.push('event: progress\ndata: {"label":"Thinking"}\n\n');
    stream.close();

    const error = await pending.catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(ChatCutOffError);
    expect((error as ChatCutOffError).conversationId).toBe("c9");
    expect((error as ChatCutOffError).message).toBe(
      "The connection dropped. The reply shows in this chat once it is saved.",
    );
  });

  it("waits on the conversation a retried message is still being answered in", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat({ ...input, requestKey: "0123456789abcdef0123456789abcdef" });
    stream.push('event: conversation\ndata: {"conversationId":"c9"}\n\n');
    stream.push("event: pending\ndata: {}\n\n");
    stream.close();

    const error = await pending.catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(ChatCutOffError);
    expect((error as ChatCutOffError).conversationId).toBe("c9");
  });

  it("names no conversation when the stream drops before one was saved", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat(input);
    stream.push('event: progress\ndata: {"label":"Thinking"}\n\n');
    stream.close();

    const error = await pending.catch((caught: unknown) => caught);
    expect(error).toBeInstanceOf(ChatCutOffError);
    expect((error as ChatCutOffError).conversationId).toBeUndefined();
    expect((error as ChatCutOffError).message).toBe("The reply was cut off. Try again.");
  });

  it("reads past the conversation event to the reply", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat(input);
    stream.push('event: conversation\ndata: {"conversationId":"c9"}\n\n');
    stream.push('event: reply\ndata: {"conversationId":"c9","text":"ok","actions":[]}\n\n');
    stream.close();

    await expect(pending).resolves.toEqual({ conversationId: "c9", text: "ok", actions: [] });
  });

  it("hands over each piece of text as it arrives, before the reply", async () => {
    const stream = eventStream();
    stubFetch(stream.response);
    const onText = vi.fn();

    const pending = sendChat(input, { onText });

    stream.push('event: text\ndata: {"delta":"orders-dev is"}\n\n');
    await vi.waitFor(() => expect(onText).toHaveBeenCalledWith("orders-dev is"));
    stream.push('event: text\ndata: {"delta":" healthy.\\nAll members are ready."}\n\n');
    await vi.waitFor(() => expect(onText).toHaveBeenCalledTimes(2));
    expect(onText).toHaveBeenLastCalledWith(" healthy.\nAll members are ready.");

    stream.push(
      'event: reply\ndata: {"conversationId":"c1","text":"orders-dev is healthy.","actions":[]}\n\n',
    );
    stream.close();
    await expect(pending).resolves.toMatchObject({ text: "orders-dev is healthy." });
  });

  it("reports progress as it arrives and resolves with the reply", async () => {
    const stream = eventStream();
    stubFetch(stream.response);
    const onProgress = vi.fn();

    const pending = sendChat(input, { onProgress });

    stream.push('event: progress\ndata: {"label":"Thinking"}\n\n');
    await vi.waitFor(() => expect(onProgress).toHaveBeenCalledWith("Thinking"));

    // A frame split across chunks, then a heartbeat comment, then a reply.
    stream.push('event: progress\ndata: {"label":"Read');
    stream.push('ing metrics"}\n\n: ping\n\n');
    stream.push(
      'event: reply\ndata: {"text":"Healthy.","actions":[{"label":"View status","kind":"status","id":"pds.orders-dev"}]}\n\n',
    );
    stream.close();

    await expect(pending).resolves.toEqual({
      text: "Healthy.",
      actions: [{ label: "View status", kind: "status", id: "pds.orders-dev" }],
    });
    expect(onProgress.mock.calls.map(([label]) => label)).toEqual(["Thinking", "Reading metrics"]);
  });

  it("rejects with the error event's message", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat(input);
    stream.push('event: progress\ndata: {"label":"Thinking"}\n\n');
    stream.push(
      'event: error\ndata: {"code":"ErrDownstream","message":"The assistant did not answer. Try again in a moment."}\n\n',
    );
    stream.close();

    await expect(pending).rejects.toMatchObject({
      name: "ApiError",
      code: "ErrDownstream",
      message: "The assistant did not answer. Try again in a moment.",
    });
  });

  it("rejects when the stream ends without a reply", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat(input);
    stream.push('event: progress\ndata: {"label":"Thinking"}\n\n');
    stream.close();

    await expect(pending).rejects.toMatchObject({
      name: "ApiError",
      code: "ErrDownstream",
      message: "The reply was cut off. Try again.",
    });
  });

  it("maps a connection dropped mid-stream to the cut off message", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat(input);
    stream.push('event: progress\ndata: {"label":"Thinking"}\n\n');
    stream.fail(new TypeError("network error"));

    await expect(pending).rejects.toMatchObject({
      name: "ApiError",
      code: "ErrDownstream",
      message: "The reply was cut off. Try again.",
    });
  });

  it("maps a malformed event to the cut off message", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat(input);
    stream.push("event: reply\ndata: {not json\n\n");

    await expect(pending).rejects.toMatchObject({
      name: "ApiError",
      message: "The reply was cut off. Try again.",
    });
  });

  it("lets an abort through unchanged", async () => {
    const stream = eventStream();
    stubFetch(stream.response);

    const pending = sendChat(input);
    stream.fail(new DOMException("The operation was aborted.", "AbortError"));

    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });

  it("reads a plain JSON error the adapter answers before streaming", async () => {
    stubFetch(
      new Response(
        JSON.stringify({
          code: "ErrNotImplemented",
          message: "Chat needs an Anthropic key, which is not configured.",
        }),
        { status: 501, headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(sendChat(input)).rejects.toMatchObject({
      code: "ErrNotImplemented",
      status: 501,
      message: "Chat needs an Anthropic key, which is not configured.",
    });
  });

  it("reads a plain JSON reply with its conversation", async () => {
    stubFetch(
      new Response(JSON.stringify({ conversationId: "c3", text: "ok", actions: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(sendChat(input)).resolves.toEqual({
      conversationId: "c3",
      text: "ok",
      actions: [],
    });
  });

  it("hands each approval and how it ended to the caller, and keeps waiting for the reply", async () => {
    const stream = eventStream();
    stubFetch(stream.response);
    const approvals: string[] = [];
    const ended: string[] = [];

    const pending = sendChat(input, {
      onApproval: (approval) => approvals.push(`${approval.id}:${approval.tool}`),
      onApprovalEnd: (end) => ended.push(`${end.id}:${end.outcome}`),
    });
    stream.push(
      'event: approval\ndata: {"id":"a1","tool":"scale_database","title":"Scale a database","destructive":false,"lines":[],"expiresAt":"2099-01-01T00:00:00Z"}\n\n',
    );
    stream.push('event: approval_end\ndata: {"id":"a1","outcome":"rejected"}\n\n');
    stream.push('event: reply\ndata: {"conversationId":"c1","text":"Left it.","actions":[]}\n\n');
    stream.close();

    await expect(pending).resolves.toMatchObject({ text: "Left it." });
    expect(approvals).toEqual(["a1:scale_database"]);
    expect(ended).toEqual(["a1:rejected"]);
  });
});

describe("decideApproval", () => {
  it("posts the decision to the approval", async () => {
    const fetchMock = stubFetch(new Response(null, { status: 204 }));

    await decideApproval("a1", { approve: true, name: "orders-dev" });

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/chat/approvals/a1");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({ decision: "approve", name: "orders-dev" });
  });

  it("sends a rejection with no name", async () => {
    const fetchMock = stubFetch(new Response(null, { status: 204 }));

    await decideApproval("a1", { approve: false });

    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(JSON.parse(String(init.body))).toEqual({ decision: "reject" });
  });

  it("raises the control plane's sentence when the approval is gone", async () => {
    stubFetch(
      new Response(
        JSON.stringify({
          code: "ErrNotFound",
          message: "That approval expired or was already answered.",
        }),
        { status: 404, headers: { "Content-Type": "application/json" } },
      ),
    );

    await expect(decideApproval("a1", { approve: true })).rejects.toThrow(
      "That approval expired or was already answered.",
    );
  });
});
