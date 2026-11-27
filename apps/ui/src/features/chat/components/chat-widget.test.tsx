import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatUiProvider, ChatWidget, useChatUi } from "@/features/chat";
import type { FeatureFlags } from "@/features/flags";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: status === 204 ? undefined : { "Content-Type": "application/json" },
  });
}

type StubMessage = {
  role: "user" | "assistant";
  content: string;
  actions: unknown[];
  createdAt: string;
};

type StubConversation = {
  id: string;
  title: string;
  provider: string;
  model: string;
  databaseId?: string;
  updatedAt: string;
  contextWindow: number;
  messages: StubMessage[];
};

/**
 * A control plane in memory: POST /api/chat saves the message and the reply,
 * and the conversation routes read back what was saved, so a test sees the
 * same thread after a reopen that a reload would.
 */
function stubChatApi({
  connections = {
    servicenow: { configured: false },
    assistant: { configured: false },
    cursor: { configured: false },
  },
  models = [{ id: "claude-sonnet-4-20250514", displayName: "Claude Sonnet 4" }],
  chatReply = {
    text: "CPU is high on orders-dev. Scale up to avoid throttling.",
    actions: [
      { label: "View metrics", kind: "metrics", metric: "cpu", id: "spoke-a/pds.orders-dev" },
    ],
  },
  chatError,
  modelsError,
  conversations = [],
  conversationsError,
}: {
  connections?: {
    servicenow: { configured: boolean };
    assistant: { configured: boolean };
    cursor: { configured: boolean };
  };
  models?: { id: string; displayName: string; default?: boolean }[];
  chatReply?: { text: string; actions: unknown[] };
  chatError?: { status: number; body: unknown };
  modelsError?: { status: number; body: unknown };
  conversations?: StubConversation[];
  conversationsError?: { status: number; body: unknown };
} = {}) {
  const saved = new Map(conversations.map((conversation) => [conversation.id, conversation]));
  let minted = 0;

  const fetchMock = vi.fn((input: string, init?: RequestInit) => {
    const path = String(input);
    const method = init?.method ?? "GET";

    if (path === "/api/connections" && method === "GET") {
      return Promise.resolve(jsonResponse(connections));
    }

    if (path.startsWith("/api/assistant/models") && method === "GET") {
      if (modelsError) {
        return Promise.resolve(jsonResponse(modelsError.body, modelsError.status));
      }

      return Promise.resolve(jsonResponse({ models }));
    }

    if (path === "/api/chat" && method === "POST") {
      if (chatError) {
        return Promise.resolve(jsonResponse(chatError.body, chatError.status));
      }

      const body = JSON.parse(String(init?.body)) as {
        conversationId?: string;
        databaseId?: string;
        provider: string;
        model: string;
        message: string;
      };
      const now = new Date().toISOString();
      let conversation = body.conversationId ? saved.get(body.conversationId) : undefined;
      if (!conversation) {
        minted += 1;
        conversation = {
          id: `new-${minted}`,
          title: body.message,
          provider: body.provider,
          model: body.model,
          databaseId: body.databaseId,
          updatedAt: now,
          contextWindow: 32,
          messages: [],
        };
        saved.set(conversation.id, conversation);
      }
      conversation.messages.push(
        { role: "user", content: body.message, actions: [], createdAt: now },
        { role: "assistant", content: chatReply.text, actions: chatReply.actions, createdAt: now },
      );
      conversation.updatedAt = now;

      return Promise.resolve(jsonResponse({ conversationId: conversation.id, ...chatReply }));
    }

    if (path === "/api/chat/conversations" && method === "GET") {
      if (conversationsError) {
        return Promise.resolve(jsonResponse(conversationsError.body, conversationsError.status));
      }
      const list = [...saved.values()]
        .toSorted((a, b) => b.updatedAt.localeCompare(a.updatedAt))
        .map(({ messages: _messages, contextWindow: _window, ...summary }) => summary);

      return Promise.resolve(jsonResponse({ conversations: list }));
    }

    if (path.startsWith("/api/chat/conversations/")) {
      const id = decodeURIComponent(path.slice("/api/chat/conversations/".length));
      const conversation = saved.get(id);
      if (!conversation) {
        return Promise.resolve(
          jsonResponse(
            {
              code: "ErrNotFound",
              message: "That conversation does not exist. It may have been deleted.",
            },
            404,
          ),
        );
      }
      if (method === "DELETE") {
        saved.delete(id);
        return Promise.resolve(jsonResponse(null, 204));
      }

      return Promise.resolve(jsonResponse(conversation));
    }

    if (path === "/api/support/tickets") {
      return Promise.resolve(jsonResponse({ tickets: [] }));
    }

    return Promise.resolve(jsonResponse({ code: "ErrNotFound", message: "missing" }, 404));
  });

  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

/** Only the Anthropic key is pasted, which is the common single provider case. */
const anthropicOnly = {
  servicenow: { configured: false },
  assistant: { configured: true },
  cursor: { configured: false },
};

async function renderOpenChat(flags?: Partial<FeatureFlags>, route?: string) {
  const result = renderWithProviders(
    <ChatUiProvider>
      <ChatWidget />
    </ChatUiProvider>,
    { flags, route },
  );
  await userEvent.click(screen.getByRole("button", { name: "Ask PDS" }));
  return result;
}

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

describe("ChatWidget", () => {
  it("starts closed so the rest of the app stays usable", () => {
    stubChatApi();

    renderWithProviders(
      <ChatUiProvider>
        <ChatWidget />
      </ChatUiProvider>,
    );

    expect(screen.getByRole("button", { name: "Ask PDS" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Chat" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Message")).not.toBeInTheDocument();
  });

  it("shows the empty state when no assistant key is configured", async () => {
    const fetchMock = stubChatApi();

    await renderOpenChat();

    expect(
      await screen.findByText("Paste a Claude or Cursor key to start chatting."),
    ).toBeInTheDocument();
    expect(screen.getByTestId("connection-assistant")).toBeInTheDocument();
    expect(screen.getByTestId("connection-cursor")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Health page" })).not.toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([url]) => String(url).includes("/api/assistant/models")),
    ).toBe(false);
  });

  it("sends a message and renders the assistant reply with actions", async () => {
    const fetchMock = stubChatApi({
      connections: {
        servicenow: { configured: false },
        assistant: { configured: true },
        cursor: { configured: false },
      },
    });

    await renderOpenChat();

    await screen.findByLabelText("Provider");
    expect(screen.getByRole("combobox", { name: "Provider" })).toHaveTextContent("Anthropic");

    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    const send = screen.getByRole("button", { name: "Send" });
    expect(send).toBeDisabled();

    await userEvent.type(screen.getByLabelText("Message"), "Why is CPU high?");
    expect(send).toBeEnabled();

    await userEvent.click(send);

    expect(await screen.findByText("Why is CPU high?")).toBeInTheDocument();
    expect(
      await screen.findByText("CPU is high on orders-dev. Scale up to avoid throttling."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View metrics" })).toBeInTheDocument();

    const chatCall = fetchMock.mock.calls.find(
      ([url, init]) => String(url) === "/api/chat" && init?.method === "POST",
    );
    expect(chatCall).toBeDefined();
    expect(JSON.parse(String(chatCall?.[1]?.body))).toEqual({
      provider: "anthropic",
      model: "claude-sonnet-4-20250514",
      message: "Why is CPU high?",
      requestKey: expect.stringMatching(/^[0-9a-f]{32}$/),
    });
  });

  it("shows only the buttons that open something and none that change a database", async () => {
    stubChatApi({
      connections: anthropicOnly,
      chatReply: {
        text: "orders-dev is short on CPU.",
        actions: [
          { label: "Scale up", kind: "scale", field: "cpu", id: "spoke-a/pds.orders-dev" },
          { label: "Retry backup", kind: "backup", id: "spoke-a/pds.orders-dev" },
          { label: "Enable schedule", kind: "schedule", id: "spoke-a/pds.orders-dev" },
          { label: "View status", kind: "status", id: "spoke-a/pds.orders-dev" },
          { label: "Contact support", kind: "support" },
        ],
      },
    });

    await renderOpenChat();
    await waitForModel();
    await userEvent.type(screen.getByLabelText("Message"), "CPU?");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByRole("button", { name: "View status" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Contact support" })).toBeInTheDocument();
    for (const gone of ["Scale up", "Retry backup", "Enable schedule"]) {
      expect(screen.queryByRole("button", { name: gone })).not.toBeInTheDocument();
    }
  });

  it.each([
    { engine: "postgresql", name: "app-pg", members: 3 },
    { engine: "mysql", name: "app-my", members: 1 },
  ])(
    "files the ticket from a support action with the $engine database's engine",
    async ({ engine, name, members }) => {
      const id = `spoke-a/pds.${name}`;
      const fetchMock = stubChatApi({
        connections: { ...anthropicOnly, servicenow: { configured: true } },
        chatReply: {
          text: `${name} is failing.`,
          actions: [{ label: "Contact support", kind: "support", id }],
        },
      });
      const base = fetchMock.getMockImplementation();
      fetchMock.mockImplementation((input: string, init?: RequestInit) => {
        if (String(input) === `/api/databases/${id}`) {
          return Promise.resolve(
            jsonResponse({ id, name, engine, namespace: "prod", members, status: "ready" }),
          );
        }
        if (String(input) === "/api/support/tickets" && init?.method === "POST") {
          return Promise.resolve(jsonResponse({ number: "CS0001" }));
        }

        return base!(input, init);
      });

      await renderOpenChat();
      await waitFor(() =>
        expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent(
          "Claude Sonnet 4",
        ),
      );
      await userEvent.type(screen.getByLabelText("Message"), "Help");
      await userEvent.click(screen.getByRole("button", { name: "Send" }));
      await userEvent.click(await screen.findByRole("button", { name: "Contact support" }));
      await userEvent.click(await screen.findByRole("button", { name: "Send ticket" }));

      await waitFor(() => {
        const post = fetchMock.mock.calls.find(
          ([url, init]) => String(url) === "/api/support/tickets" && init?.method === "POST",
        );
        expect(post).toBeDefined();
        expect(JSON.parse(String(post![1]?.body)).context).toMatchObject({
          database: name,
          engine,
          namespace: "prod",
          members,
        });
      });
    },
  );

  it("no longer warns that a refresh clears the conversation", async () => {
    stubChatApi({ connections: anthropicOnly });

    await renderOpenChat();

    await screen.findByLabelText("Message");
    expect(screen.queryByText(/not saved/)).not.toBeInTheDocument();
  });

  it("shows a working status while the request is in flight", async () => {
    let resolveChat!: (value: Response) => void;
    const chatPromise = new Promise<Response>((resolve) => {
      resolveChat = resolve;
    });

    const fetchMock = stubChatApi({
      connections: {
        servicenow: { configured: false },
        assistant: { configured: true },
        cursor: { configured: false },
      },
    });
    const originalImpl = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";

      if (path === "/api/chat" && method === "POST") {
        return chatPromise;
      }

      return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    await renderOpenChat();

    await screen.findByLabelText("Provider");
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    await userEvent.type(screen.getByLabelText("Message"), "Hello");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(screen.getByRole("button", { name: "Sending..." })).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent("Working on a reply");
    expect(screen.getByText("This can take a minute.")).toBeInTheDocument();

    resolveChat(
      jsonResponse({
        conversationId: "c1",
        text: "Hello back.",
        actions: [],
      }),
    );

    await waitFor(() => expect(screen.getByText("Hello back.")).toBeInTheDocument());
    expect(screen.queryByText("Working on a reply")).not.toBeInTheDocument();
    expect(screen.queryByText("This can take a minute.")).not.toBeInTheDocument();
  });

  it("shows each streamed step until the reply arrives", async () => {
    const encoder = new TextEncoder();
    let stream!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        stream = controller;
      },
    });
    const push = (chunk: string) => stream.enqueue(encoder.encode(chunk));

    const fetchMock = stubChatApi({ connections: anthropicOnly });
    const originalImpl = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input, init) => {
      if (String(input) === "/api/chat" && init?.method === "POST") {
        return Promise.resolve(
          new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
        );
      }

      return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    await renderOpenChat();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    await userEvent.type(screen.getByLabelText("Message"), "How is orders-dev?");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(screen.getByRole("status")).toHaveTextContent("Working on a reply");

    push('event: progress\ndata: {"label":"Thinking"}\n\n');
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Thinking"));

    push('event: progress\ndata: {"label":"Reading metrics"}\n\n');
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Reading metrics"));

    push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
    push(
      'event: reply\ndata: {"conversationId":"c1","text":"orders-dev is healthy.","actions":[]}\n\n',
    );
    stream.close();

    await waitFor(() => expect(screen.getByText("orders-dev is healthy.")).toBeInTheDocument());
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("shows the answer's text as it arrives and the actions after it", async () => {
    const encoder = new TextEncoder();
    let stream!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        stream = controller;
      },
    });
    const push = (chunk: string) => stream.enqueue(encoder.encode(chunk));

    const fetchMock = stubChatApi({ connections: anthropicOnly });
    const originalImpl = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input, init) => {
      if (String(input) === "/api/chat" && init?.method === "POST") {
        return Promise.resolve(
          new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
        );
      }

      return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    await renderOpenChat();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    await userEvent.type(screen.getByLabelText("Message"), "How is orders-dev?");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
    push('event: text\ndata: {"delta":"orders-dev is"}\n\n');
    expect(await screen.findByText("orders-dev is")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "View status" })).not.toBeInTheDocument();
    // The turn is still running, so its step still shows.
    expect(screen.getByRole("status")).toBeInTheDocument();

    push('event: text\ndata: {"delta":" healthy."}\n\n');
    await waitFor(() => expect(screen.getByText("orders-dev is healthy.")).toBeInTheDocument());

    push(
      'event: reply\ndata: {"conversationId":"c1","text":"orders-dev is healthy.","actions":[{"label":"View status","kind":"status","id":"pds.orders-dev"}]}\n\n',
    );
    stream.close();

    expect(await screen.findByRole("button", { name: "View status" })).toBeInTheDocument();
    expect(screen.getAllByText("orders-dev is healthy.")).toHaveLength(1);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("replaces the streamed text with the reply when they differ", async () => {
    const encoder = new TextEncoder();
    let stream!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        stream = controller;
      },
    });
    const push = (chunk: string) => stream.enqueue(encoder.encode(chunk));

    const fetchMock = stubChatApi({ connections: anthropicOnly });
    const originalImpl = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input, init) => {
      if (String(input) === "/api/chat" && init?.method === "POST") {
        return Promise.resolve(
          new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
        );
      }

      return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    await renderOpenChat();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    await userEvent.type(screen.getByLabelText("Message"), "Tell me something");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    push('event: text\ndata: {"delta":"Here is how to"}\n\n');
    expect(await screen.findByText("Here is how to")).toBeInTheDocument();

    push(
      'event: reply\ndata: {"conversationId":"c1","text":"The assistant can\'t answer that.","actions":[]}\n\n',
    );
    stream.close();

    expect(await screen.findByText("The assistant can't answer that.")).toBeInTheDocument();
    expect(screen.queryByText("Here is how to")).not.toBeInTheDocument();
  });

  it("keeps the in-flight turn when the panel is hidden", async () => {
    const chatPromise = new Promise<Response>(() => undefined);

    const fetchMock = stubChatApi({
      connections: {
        servicenow: { configured: false },
        assistant: { configured: true },
        cursor: { configured: false },
      },
    });
    const originalImpl = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input, init) => {
      const path = String(input);
      const method = init?.method ?? "GET";

      if (path === "/api/chat" && method === "POST") {
        return chatPromise;
      }

      return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    await renderOpenChat();

    await screen.findByLabelText("Provider");
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    await userEvent.type(screen.getByLabelText("Message"), "Hello");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await userEvent.click(screen.getByRole("button", { name: "Hide chat" }));

    expect(screen.queryByRole("region", { name: "Chat" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Working on a reply" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Working on a reply" }));

    expect(screen.getByRole("region", { name: "Chat" })).toBeInTheDocument();
    expect(screen.getByText("Hello")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Working on a reply");
  });

  it("rolls back the user turn and restores the draft when chat returns 502", async () => {
    stubChatApi({
      connections: {
        servicenow: { configured: false },
        assistant: { configured: true },
        cursor: { configured: false },
      },
      chatError: {
        status: 502,
        body: {
          code: "ErrDownstream",
          message: "The assistant did not answer. Try again in a moment.",
        },
      },
    });

    await renderOpenChat();

    await screen.findByLabelText("Provider");
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "Hello");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "The assistant did not answer. Try again in a moment.",
      ),
    );
    const helloMatches = screen.getAllByText("Hello");
    expect(helloMatches).toHaveLength(1);
    expect(helloMatches[0]).toBe(message);
    expect(message).toHaveValue("Hello");
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("shows a model list error under the Model select and keeps Send disabled", async () => {
    stubChatApi({
      connections: {
        servicenow: { configured: false },
        assistant: { configured: true },
        cursor: { configured: false },
      },
      modelsError: {
        status: 502,
        body: {
          code: "ErrDownstream",
          message: "The assistant did not answer. Try again in a moment.",
        },
      },
    });

    await renderOpenChat();

    await screen.findByLabelText("Provider");
    expect(
      await screen.findByText("The assistant did not answer. Try again in a moment."),
    ).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Message"), "Hello");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });

  it("narrows a long model list by typing instead of scrolling a wall of names", async () => {
    stubChatApi({
      connections: anthropicOnly,
      models: [
        { id: "claude-opus-5", displayName: "Claude Opus 5" },
        { id: "claude-sonnet-5", displayName: "Claude Sonnet 5" },
        { id: "claude-haiku-4-5", displayName: "Claude Haiku 4.5" },
      ],
    });

    await renderOpenChat();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Opus 5"),
    );

    await userEvent.click(screen.getByRole("combobox", { name: "Model" }));
    await userEvent.type(screen.getByLabelText("Search models"), "haiku");
    await userEvent.click(screen.getByRole("option", { name: /Claude Haiku 4.5/ }));

    expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Haiku 4.5");
  });

  it("starts a new chat on the model the list marks as the default", async () => {
    stubChatApi({
      connections: anthropicOnly,
      models: [
        { id: "claude-opus-5-5", displayName: "Claude Opus 5.5" },
        { id: "claude-sonnet-5-5", displayName: "Claude Sonnet 5.5", default: true },
        { id: "claude-haiku-4-5", displayName: "Claude Haiku 4.5" },
      ],
    });

    await renderOpenChat();

    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent(
        "Claude Sonnet 5.5",
      ),
    );
  });

  it("keeps the model list inside the window however many models there are", async () => {
    stubChatApi({
      connections: anthropicOnly,
      models: Array.from({ length: 120 }, (_, index) => ({
        id: `model-${index}`,
        displayName: `Model ${index}`,
      })),
    });

    await renderOpenChat();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Model 0"),
    );

    await userEvent.click(screen.getByRole("combobox", { name: "Model" }));

    const panel = screen.getByTestId("combobox-panel");
    expect(Number.parseFloat(panel.style.maxHeight)).toBeLessThanOrEqual(window.innerHeight);
    expect(screen.getByRole("listbox").className).toContain("overflow-y-auto");
  });

  it("loads the last sent message back into the box on arrow up", async () => {
    stubChatApi({ connections: anthropicOnly });

    await renderOpenChat();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "Why is CPU high?{Enter}");
    await waitFor(() => expect(message).toHaveValue(""));
    await waitFor(() => expect(message).toBeEnabled());

    await userEvent.type(message, "{ArrowUp}");

    expect(message).toHaveValue("Why is CPU high?");
  });

  it("walks back through earlier messages and forward to the draft it interrupted", async () => {
    stubChatApi({ connections: anthropicOnly });

    await renderOpenChat();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "first question{Enter}");
    await waitFor(() => expect(message).toBeEnabled());
    await userEvent.type(message, "second question{Enter}");
    await waitFor(() => expect(message).toBeEnabled());

    await userEvent.type(message, "half typed");
    await userEvent.type(message, "{ArrowUp}");
    expect(message).toHaveValue("second question");

    await userEvent.type(message, "{ArrowUp}");
    expect(message).toHaveValue("first question");

    // The oldest message is where the walk stops.
    await userEvent.type(message, "{ArrowUp}");
    expect(message).toHaveValue("first question");

    await userEvent.type(message, "{ArrowDown}");
    expect(message).toHaveValue("second question");

    await userEvent.type(message, "{ArrowDown}");
    expect(message).toHaveValue("half typed");
  });

  it("leaves arrow up alone when nothing has been sent yet", async () => {
    stubChatApi({ connections: anthropicOnly });

    await renderOpenChat();
    await screen.findByLabelText("Provider");

    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "half typed{ArrowUp}");

    expect(message).toHaveValue("half typed");
    expect(
      screen.queryByText("Press the up arrow to load a message you already sent."),
    ).not.toBeInTheDocument();
  });

  it("moves the caret rather than recalling when the caret is on a later line", async () => {
    stubChatApi({ connections: anthropicOnly });

    await renderOpenChat();
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
    );

    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "sent once{Enter}");
    await waitFor(() => expect(message).toBeEnabled());

    await userEvent.type(message, "first line{Shift>}{Enter}{/Shift}second line");
    await userEvent.type(message, "{ArrowUp}");

    expect(message).toHaveValue("first line\nsecond line");
  });
});

/** Opens the provider select and reads the providers it lists. */
async function providerOptions() {
  // Radix opens its select from the keyboard; jsdom has no pointer capture.
  const trigger = await screen.findByRole("combobox", { name: "Provider" });
  trigger.focus();
  await userEvent.keyboard("{ArrowDown}");

  return screen.getAllByRole("option").map((option) => option.textContent?.trim());
}

/** A turn whose stream the test feeds by hand, and the approvals it answers. */
function holdTurnOpen(fetchMock: ReturnType<typeof stubChatApi>) {
  const encoder = new TextEncoder();
  let stream!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      stream = controller;
    },
  });
  const originalImpl = fetchMock.getMockImplementation();
  fetchMock.mockImplementation((input, init) => {
    const path = String(input);
    if (path === "/api/chat" && init?.method === "POST") {
      return Promise.resolve(
        new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
      );
    }
    if (path.startsWith("/api/chat/approvals/") && init?.method === "POST") {
      return Promise.resolve(jsonResponse(null, 204));
    }

    return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
  });

  return {
    push: (chunk: string) => stream.enqueue(encoder.encode(chunk)),
    close: () => stream.close(),
  };
}

const scaleApproval = {
  id: "a1",
  tool: "scale_database",
  title: "Scale a database",
  database: "orders-dev",
  destructive: false,
  lines: [
    { label: "Database", value: "spoke-a/pds.orders-dev" },
    { label: "CPU cores", value: "2" },
  ],
  expiresAt: "2099-01-01T00:00:00Z",
};

async function sendAndWaitForTurn(message: string) {
  await renderOpenChat();
  await waitForModel();
  await userEvent.type(screen.getByLabelText("Message"), message);
  await userEvent.click(screen.getByRole("button", { name: "Send" }));
}

describe("ChatWidget approvals", () => {
  it("shows the change the model asked for and sends the person's answer", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    const turn = holdTurnOpen(fetchMock);

    await sendAndWaitForTurn("Scale orders-dev to 2 CPUs");
    turn.push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
    turn.push(`event: approval\ndata: ${JSON.stringify(scaleApproval)}\n\n`);

    expect(await screen.findByText("Scale a database")).toBeInTheDocument();
    expect(screen.getByText("spoke-a/pds.orders-dev")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Waiting for your approval");

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === "/api/chat/approvals/a1" && init?.method === "POST",
      );
      expect(post).toBeDefined();
      expect(JSON.parse(String(post![1]?.body))).toEqual({ decision: "approve" });
    });

    turn.push('event: approval_end\ndata: {"id":"a1","outcome":"approved"}\n\n');
    expect(await screen.findByText("Approved. The change is being made.")).toBeInTheDocument();
    turn.push(
      'event: reply\ndata: {"conversationId":"c1","text":"Scaled orders-dev to 2 CPUs.","actions":[]}\n\n',
    );
    turn.close();

    expect(await screen.findByText("Scaled orders-dev to 2 CPUs.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("sends the name that was typed for a delete", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    const turn = holdTurnOpen(fetchMock);

    await sendAndWaitForTurn("Delete orders-dev");
    turn.push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
    turn.push(
      `event: approval\ndata: ${JSON.stringify({
        ...scaleApproval,
        id: "a2",
        tool: "delete_database",
        title: "Delete a database",
        destructive: true,
        lines: [{ label: "Database", value: "spoke-a/pds.orders-dev" }],
      })}\n\n`,
    );

    await userEvent.type(await screen.findByLabelText("Type orders-dev to confirm"), "orders-dev");
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        ([url, init]) => String(url) === "/api/chat/approvals/a2" && init?.method === "POST",
      );
      expect(JSON.parse(String(post![1]?.body))).toEqual({
        decision: "approve",
        name: "orders-dev",
      });
    });
  });

  it("shows an approval that expired and ends the turn with the reply", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    const turn = holdTurnOpen(fetchMock);

    await sendAndWaitForTurn("Scale orders-dev to 2 CPUs");
    turn.push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
    turn.push(`event: approval\ndata: ${JSON.stringify(scaleApproval)}\n\n`);
    await screen.findByRole("button", { name: "Approve" });

    turn.push('event: approval_end\ndata: {"id":"a1","outcome":"expired"}\n\n');

    expect(await screen.findByText("This approval expired. Nothing changed.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("tells the person an approval is waiting when the panel is hidden", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    const turn = holdTurnOpen(fetchMock);

    await sendAndWaitForTurn("Scale orders-dev to 2 CPUs");
    turn.push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
    turn.push(`event: approval\ndata: ${JSON.stringify(scaleApproval)}\n\n`);
    await screen.findByRole("button", { name: "Approve" });
    await userEvent.click(screen.getByRole("button", { name: "Hide chat" }));

    const waiting = screen.getByRole("button", { name: "Waiting for your approval" });
    await userEvent.click(waiting);

    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
  });
});

describe("ChatWidget with keys stored for removed providers", () => {
  // A control plane that has not restarted yet may still report the old keys.
  const everyKey = {
    servicenow: { configured: false },
    assistant: { configured: true },
    cursor: { configured: true },
    openai: { configured: true },
    gemini: { configured: true },
    openrouter: { configured: true },
  };

  beforeEach(() => {
    // Radix scrolls the open list to the selected option, which jsdom lacks.
    Element.prototype.scrollIntoView ??= vi.fn();
  });

  it("offers only Anthropic and Cursor, whatever keys are stored", async () => {
    const fetchMock = stubChatApi({ connections: everyKey });

    await renderOpenChat();

    expect(await providerOptions()).toEqual(["Anthropic", "Cursor"]);
    const asked = fetchMock.mock.calls
      .map(([url]) => String(url))
      .filter((url) => url.startsWith("/api/assistant/models"));
    expect(asked.some((url) => /provider=(openai|gemini|openrouter)/.test(url))).toBe(false);
  });

  it("shows the empty state when only a removed provider has a key", async () => {
    stubChatApi({
      connections: {
        ...everyKey,
        assistant: { configured: false },
        cursor: { configured: false },
      },
    });

    await renderOpenChat();

    expect(
      await screen.findByText("Paste a Claude or Cursor key to start chatting."),
    ).toBeInTheDocument();
    expect(screen.getByTestId("connection-assistant")).toBeInTheDocument();
    expect(screen.queryByTestId("connection-openai")).not.toBeInTheDocument();
  });
});

function savedConversation(overrides: Partial<StubConversation> = {}): StubConversation {
  return {
    id: "c-backups",
    title: "Why did the backup fail?",
    provider: "anthropic",
    model: "claude-sonnet-4-20250514",
    updatedAt: "2026-09-27T10:00:00Z",
    contextWindow: 32,
    messages: [
      {
        role: "user",
        content: "Why did the backup fail?",
        actions: [],
        createdAt: "2026-09-27T10:00:00Z",
      },
      {
        role: "assistant",
        content: "The storage bucket refused the upload.",
        actions: [{ label: "Retry backup", kind: "backup", id: "spoke-a/pds.orders-dev" }],
        createdAt: "2026-09-27T10:00:01Z",
      },
    ],
    ...overrides,
  };
}

function chatBodies(fetchMock: ReturnType<typeof stubChatApi>) {
  return fetchMock.mock.calls
    .filter(([url, init]) => String(url) === "/api/chat" && init?.method === "POST")
    .map(([, init]) => JSON.parse(String(init?.body)) as Record<string, unknown>);
}

async function waitForModel() {
  await waitFor(() =>
    expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Claude Sonnet 4"),
  );
}

describe("ChatWidget saved conversations", () => {
  it("lists saved chats newest first", async () => {
    stubChatApi({
      connections: anthropicOnly,
      conversations: [
        savedConversation({ id: "older", title: "Older chat", updatedAt: "2026-09-20T10:00:00Z" }),
        savedConversation({ id: "newer", title: "Newer chat", updatedAt: "2026-09-27T10:00:00Z" }),
      ],
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));

    const list = await screen.findByRole("list", { name: "Saved chats" });
    const titles = Array.from(list.querySelectorAll("li")).map(
      (row) => row.querySelector("span")?.textContent,
    );
    expect(titles).toEqual(["Newer chat", "Older chat"]);
  });

  it("opens a chat from a provider that is no longer offered as read only", async () => {
    const fetchMock = stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation({ provider: "openai", model: "gpt-4o" })],
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));

    expect(await screen.findByText("The storage bucket refused the upload.")).toBeInTheDocument();
    expect(
      screen.getByText("This chat used a provider that is no longer offered. Start a new chat."),
    ).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Message"), "Did the retry work?");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    const asked = fetchMock.mock.calls
      .map(([url]) => String(url))
      .filter((url) => url.startsWith("/api/assistant/models"));
    expect(asked.some((url) => url.includes("provider=openai"))).toBe(false);
  });

  it("reopens a chat with its messages, without the change buttons it once had, and continues it", async () => {
    const fetchMock = stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation()],
      chatReply: { text: "It uploads now.", actions: [] },
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));

    expect(await screen.findByText("The storage bucket refused the upload.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry backup" })).not.toBeInTheDocument();
    expect(
      screen.getByText("Changing the provider or model starts a new chat."),
    ).toBeInTheDocument();

    await waitForModel();
    await userEvent.type(screen.getByLabelText("Message"), "Did the retry work?");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await screen.findByText("It uploads now.")).toBeInTheDocument();
    expect(screen.getByText("The storage bucket refused the upload.")).toBeInTheDocument();
    expect(chatBodies(fetchMock)).toEqual([
      {
        conversationId: "c-backups",
        provider: "anthropic",
        model: "claude-sonnet-4-20250514",
        message: "Did the retry work?",
      },
    ]);
  });

  it("starts a new chat that does not carry the open one", async () => {
    const fetchMock = stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation()],
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));
    expect(await screen.findByText("The storage bucket refused the upload.")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "New chat" }));

    expect(screen.queryByText("The storage bucket refused the upload.")).not.toBeInTheDocument();
    await waitForModel();
    await userEvent.type(screen.getByLabelText("Message"), "A new question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await screen.findByText("CPU is high on orders-dev. Scale up to avoid throttling.");
    expect(chatBodies(fetchMock)[0]).not.toHaveProperty("conversationId");
  });

  it("keeps a first reply open as a saved chat", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });

    await renderOpenChat();
    await waitForModel();
    await userEvent.type(screen.getByLabelText("Message"), "First question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByText("CPU is high on orders-dev. Scale up to avoid throttling.");

    await userEvent.type(screen.getByLabelText("Message"), "Second question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(chatBodies(fetchMock)).toHaveLength(2));

    expect(chatBodies(fetchMock)[1]).toMatchObject({
      conversationId: "new-1",
      message: "Second question",
    });
    expect(
      screen.getAllByText("CPU is high on orders-dev. Scale up to avoid throttling."),
    ).toHaveLength(2);
  });

  it("deletes a chat after a confirm", async () => {
    const fetchMock = stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation()],
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(
      await screen.findByRole("button", { name: "Delete chat Why did the backup fail?" }),
    );

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(
      "The conversation and every message in it are deleted for good.",
    );
    await userEvent.click(screen.getByRole("button", { name: "Delete chat" }));

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Chat deleted"));
    expect(
      fetchMock.mock.calls.some(
        ([url, init]) =>
          String(url) === "/api/chat/conversations/c-backups" && init?.method === "DELETE",
      ),
    ).toBe(true);
    expect(await screen.findByText("No saved chats yet")).toBeInTheDocument();
  });

  it("keeps the chat when the delete is cancelled", async () => {
    const fetchMock = stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation()],
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(
      await screen.findByRole("button", { name: "Delete chat Why did the backup fail?" }),
    );
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));

    expect(screen.getByRole("button", { name: /^Why did the backup fail/ })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
  });

  it("records the database a chat starts on and shows it in the list", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });

    await renderOpenChat(undefined, "/databases/spoke-a/pds.orders-dev");
    await waitForModel();
    await userEvent.type(screen.getByLabelText("Message"), "How is this one?");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByText("CPU is high on orders-dev. Scale up to avoid throttling.");

    expect(chatBodies(fetchMock)[0]).toMatchObject({ databaseId: "spoke-a/pds.orders-dev" });

    await userEvent.click(screen.getByRole("button", { name: "Chat history" }));
    const list = await screen.findByRole("list", { name: "Saved chats" });
    expect(list).toHaveTextContent("orders-dev");
  });

  it("says when older messages are out of the model's context", async () => {
    const messages = Array.from({ length: 34 }, (_, index) => ({
      role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
      content: `message ${index}`,
      actions: [],
      createdAt: "2026-09-27T10:00:00Z",
    }));
    stubChatApi({ connections: anthropicOnly, conversations: [savedConversation({ messages })] });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));

    const note = await screen.findByRole("note");
    expect(note).toHaveTextContent("Messages above this line are no longer sent to the model.");
    // The next message takes one of the 32 places, and the window opens on a
    // user turn, so the model's next read starts at message 4.
    expect(note.nextElementSibling).toHaveTextContent("message 4");
  });

  it("shows no context note on a short chat", async () => {
    stubChatApi({ connections: anthropicOnly, conversations: [savedConversation()] });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));
    await screen.findByText("The storage bucket refused the upload.");

    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });

  it("offers a retry when saved chats are not available yet", async () => {
    const fetchMock = stubChatApi({
      connections: anthropicOnly,
      conversationsError: {
        status: 503,
        body: {
          code: "ErrDownstream",
          message: "Saved chats are not available yet. Try again in a moment.",
        },
      },
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));

    expect(
      await screen.findByText("Saved chats are not available yet. Try again in a moment."),
    ).toBeInTheDocument();
    const before = fetchMock.mock.calls.filter(
      ([url]) => String(url) === "/api/chat/conversations",
    ).length;
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(([url]) => String(url) === "/api/chat/conversations").length,
      ).toBeGreaterThan(before),
    );
  });

  it("offers a retry on a send the control plane could not save", async () => {
    stubChatApi({
      connections: anthropicOnly,
      chatError: {
        status: 503,
        body: {
          code: "ErrDownstream",
          message: "Saved chats are not available yet. Try again in a moment.",
        },
      },
    });

    await renderOpenChat();
    await waitForModel();
    await userEvent.type(screen.getByLabelText("Message"), "Hello");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "Saved chats are not available yet. Try again in a moment.",
        expect.objectContaining({ action: expect.objectContaining({ label: "Try again" }) }),
      ),
    );
    expect(screen.getByLabelText("Message")).toHaveValue("Hello");
  });

  it("keeps the panel on the chat whose reply is in flight", async () => {
    const fetchMock = stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation()],
    });
    const originalImpl = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input, init) => {
      if (String(input) === "/api/chat" && init?.method === "POST") {
        return new Promise<Response>(() => undefined);
      }
      return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    await renderOpenChat();
    await waitForModel();
    await userEvent.type(screen.getByLabelText("Message"), "Slow question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(screen.getByRole("button", { name: "New chat" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Chat history" }));
    expect(await screen.findByRole("button", { name: /^Why did the backup fail/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: "New chat" })).toBeDisabled();
  });

  it("puts a failed message back in the box and keeps the saved thread", async () => {
    stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation()],
      chatError: {
        status: 502,
        body: {
          code: "ErrDownstream",
          message: "The assistant did not answer. Try again in a moment.",
        },
      },
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));
    await screen.findByText("The storage bucket refused the upload.");
    await waitForModel();

    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "Did the retry work?");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "The assistant did not answer. Try again in a moment.",
      ),
    );
    expect(message).toHaveValue("Did the retry work?");
    await waitFor(() => expect(screen.getAllByText("Did the retry work?")).toEqual([message]));
    expect(screen.getByText("The storage bucket refused the upload.")).toBeInTheDocument();
  });

  it("opens the saved chat when the stream drops after the message was saved", async () => {
    const live = savedConversation({
      id: "c-live",
      title: "Slow question",
      messages: [
        {
          role: "user",
          content: "Slow question",
          actions: [],
          createdAt: new Date().toISOString(),
        },
      ],
    });
    const fetchMock = stubChatApi({ connections: anthropicOnly, conversations: [live] });
    const originalImpl = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input, init) => {
      if (String(input) === "/api/chat" && init?.method === "POST") {
        const encoder = new TextEncoder();
        const body = new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(
              encoder.encode('event: conversation\ndata: {"conversationId":"c-live"}\n\n'),
            );
            controller.close();
          },
        });
        return Promise.resolve(
          new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
        );
      }
      return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    await renderOpenChat();
    await waitForModel();
    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "Slow question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "The connection dropped. The reply shows in this chat once it is saved.",
      ),
    );
    expect(message).toHaveValue("");
    expect(await screen.findByText("Slow question")).toBeInTheDocument();
    expect(await screen.findByRole("status")).toHaveTextContent("Waiting for the reply");

    // The turn finishes on the control plane; the open chat picks the reply up.
    live.messages.push({
      role: "assistant",
      content: "Here is the slow answer.",
      actions: [],
      createdAt: new Date().toISOString(),
    });
    expect(
      await screen.findByText("Here is the slow answer.", {}, { timeout: 6000 }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  }, 10000);

  it("keeps a reopened chat sendable when the model list does not load", async () => {
    stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation()],
      modelsError: {
        status: 502,
        body: {
          code: "ErrDownstream",
          message: "The assistant did not answer. Try again in a moment.",
        },
      },
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));
    await screen.findByText("The storage bucket refused the upload.");

    await userEvent.type(screen.getByLabelText("Message"), "Still there?");
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("starts a new chat when the model changes on an open one", async () => {
    const fetchMock = stubChatApi({
      connections: anthropicOnly,
      models: [
        { id: "claude-sonnet-4-20250514", displayName: "Claude Sonnet 4" },
        { id: "claude-haiku-4-5", displayName: "Claude Haiku 4.5" },
      ],
      conversations: [savedConversation()],
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));
    await screen.findByText("The storage bucket refused the upload.");
    await waitForModel();

    await userEvent.click(screen.getByRole("combobox", { name: "Model" }));
    await userEvent.click(screen.getByRole("option", { name: /Claude Haiku 4.5/ }));

    expect(screen.queryByText("The storage bucket refused the upload.")).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Message"), "Fresh start");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(chatBodies(fetchMock)).toHaveLength(1));
    expect(chatBodies(fetchMock)[0]).toEqual({
      provider: "anthropic",
      model: "claude-haiku-4-5",
      message: "Fresh start",
      requestKey: expect.stringMatching(/^[0-9a-f]{32}$/),
    });
  });
});

describe("ChatWidget retries of a first message", () => {
  function stubDroppedFirstSend(fetchMock: ReturnType<typeof stubChatApi>) {
    const originalImpl = fetchMock.getMockImplementation();
    let calls = 0;
    fetchMock.mockImplementation((input, init) => {
      if (String(input) === "/api/chat" && init?.method === "POST") {
        calls += 1;
        if (calls === 1) {
          // The connection drops before the conversation event arrives.
          const body = new ReadableStream<Uint8Array>({
            start(controller) {
              controller.close();
            },
          });
          return Promise.resolve(
            new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
          );
        }
      }
      return originalImpl?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });
  }

  it("sends the same request key when the same first message is retried", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    stubDroppedFirstSend(fetchMock);

    await renderOpenChat();
    await waitForModel();
    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "First question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(message).toHaveValue("First question"));

    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByText("CPU is high on orders-dev. Scale up to avoid throttling.");

    const [first, retry] = chatBodies(fetchMock);
    expect(first?.requestKey).toMatch(/^[0-9a-f]{32}$/);
    expect(retry?.requestKey).toBe(first?.requestKey);
  });

  it("mints a new request key once a new chat is started", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    stubDroppedFirstSend(fetchMock);

    await renderOpenChat();
    await waitForModel();
    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "First question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(message).toHaveValue("First question"));

    await userEvent.click(screen.getByRole("button", { name: "New chat" }));
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(chatBodies(fetchMock)).toHaveLength(2));

    const [first, second] = chatBodies(fetchMock);
    expect(second?.requestKey).toMatch(/^[0-9a-f]{32}$/);
    expect(second?.requestKey).not.toBe(first?.requestKey);
  });

  it("mints a new request key for a different message", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    stubDroppedFirstSend(fetchMock);

    await renderOpenChat();
    await waitForModel();
    const message = screen.getByLabelText("Message");
    await userEvent.type(message, "First question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(message).toHaveValue("First question"));

    await userEvent.clear(message);
    await userEvent.type(message, "A different question");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(chatBodies(fetchMock)).toHaveLength(2));

    const [first, second] = chatBodies(fetchMock);
    expect(second?.requestKey).toMatch(/^[0-9a-f]{32}$/);
    expect(second?.requestKey).not.toBe(first?.requestKey);
  });
});

describe("ChatWidget chat history", () => {
  it("expands in place to list the chats beside the open one, and collapses again", async () => {
    stubChatApi({ connections: anthropicOnly, conversations: [savedConversation()] });

    await renderOpenChat();
    const toggle = await screen.findByRole("button", { name: "Chat history" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("complementary", { name: "Chat history" })).not.toBeInTheDocument();

    await userEvent.click(toggle);

    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const history = screen.getByRole("complementary", { name: "Chat history" });
    expect(history).toBeVisible();
    expect(screen.getByRole("region", { name: "Chat" })).toContainElement(history);
    expect(await screen.findByRole("list", { name: "Saved chats" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "New chat" })).toHaveLength(1);

    await userEvent.click(toggle);

    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("complementary", { name: "Chat history" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Message")).toBeInTheDocument();
  });

  it("offers no history before a key is pasted", async () => {
    stubChatApi();

    await renderOpenChat();
    await screen.findByText("Paste a Claude or Cursor key to start chatting.");

    expect(screen.queryByRole("button", { name: "Chat history" })).not.toBeInTheDocument();
  });

  it("closes on Escape", async () => {
    stubChatApi({ connections: anthropicOnly });

    await renderOpenChat();
    await userEvent.keyboard("{Escape}");

    expect(screen.queryByRole("region", { name: "Chat" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ask PDS" })).toBeInTheDocument();
  });
});

describe("ChatWidget context", () => {
  it("reads across everything away from a database page", async () => {
    stubChatApi({ connections: anthropicOnly });

    await renderOpenChat();

    expect(await screen.findByText("Across everything you can see")).toBeInTheDocument();
  });

  it("says a new chat on a database page starts from that database", async () => {
    stubChatApi({ connections: anthropicOnly });

    await renderOpenChat(undefined, "/databases/spoke-a/pds.orders-dev");

    expect(await screen.findByText(/Starts from/)).toHaveTextContent("Starts from orders-dev");
  });

  it("names the database a saved chat is about", async () => {
    stubChatApi({
      connections: anthropicOnly,
      conversations: [savedConversation({ databaseId: "spoke-a/pds.orders-dev" })],
    });

    await renderOpenChat();
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));

    expect(await screen.findByText(/^About/)).toHaveTextContent("About orders-dev");
  });
});

/** Stands in for a page that asks Ask PDS about something on it. */
function AskButton({ question, databaseId }: { question: string; databaseId?: string }) {
  const { ask } = useChatUi();

  return (
    <button type="button" onClick={() => ask(question, { databaseId })}>
      Ask about it
    </button>
  );
}

function renderWithAsk(props: { question: string; databaseId?: string }) {
  return renderWithProviders(
    <ChatUiProvider>
      <AskButton {...props} />
      <ChatWidget />
    </ChatUiProvider>,
  );
}

describe("useChatUi().ask", () => {
  it("opens Ask PDS on a new chat with the question in the box and sends nothing", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    renderWithAsk({ question: "Why is orders-dev short on disk?" });

    await userEvent.click(screen.getByRole("button", { name: "Ask about it" }));

    expect(screen.getByRole("region", { name: "Chat" })).toBeInTheDocument();
    const message = await screen.findByLabelText("Message");
    expect(message).toHaveValue("Why is orders-dev short on disk?");
    await waitFor(() => expect(message).toHaveFocus());
    expect(chatBodies(fetchMock)).toEqual([]);
  });

  it("records the database it was asked about on the chat it starts", async () => {
    const fetchMock = stubChatApi({ connections: anthropicOnly });
    renderWithAsk({ question: "Why is this alert firing?", databaseId: "spoke-a/pds.orders-dev" });

    await userEvent.click(screen.getByRole("button", { name: "Ask about it" }));
    expect(await screen.findByText(/Starts from/)).toHaveTextContent("Starts from orders-dev");
    await waitForModel();
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(chatBodies(fetchMock)).toHaveLength(1));
    expect(chatBodies(fetchMock)[0]).toMatchObject({
      message: "Why is this alert firing?",
      databaseId: "spoke-a/pds.orders-dev",
    });
    expect(chatBodies(fetchMock)[0]).not.toHaveProperty("conversationId");
  });

  it("leaves the open chat for a new one", async () => {
    stubChatApi({ connections: anthropicOnly, conversations: [savedConversation()] });
    renderWithAsk({ question: "What changed?" });

    await userEvent.click(screen.getByRole("button", { name: "Ask PDS" }));
    await userEvent.click(await screen.findByRole("button", { name: "Chat history" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Why did the backup fail/ }));
    expect(await screen.findByText("The storage bucket refused the upload.")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Ask about it" }));

    await waitFor(() =>
      expect(screen.queryByText("The storage bucket refused the upload.")).not.toBeInTheDocument(),
    );
    expect(screen.getByLabelText("Message")).toHaveValue("What changed?");
  });
});
