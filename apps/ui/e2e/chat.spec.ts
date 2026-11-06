import { expect, test } from "./fixtures";
import type { Page } from "@playwright/test";

// The preview server serves the built SPA only, so the adapter is stubbed here.
// Chat HTTP and action resolution are covered by Vitest and Go tests; this spec
// proves the page sends one turn.
const ordersProd = {
  id: "spoke-a/pds.orders-prod",
  environment: "spoke-a",
  name: "orders-prod",
  engine: "mysql",
  version: "8.4.3",
  profile: "prod",
  namespace: "pds",
  status: "ready",
  members: 3,
  cpu: 4,
  memoryGb: 16,
  storageGb: 250,
  createdAt: "2026-08-20T09:30:00Z",
  backupSchedule: { name: "daily", cron: "0 2 * * *", retentionCopies: 7 },
  connection: {
    host: "orders-prod.pds.svc",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-prod.pds.svc:3306/orders-prod",
    secretName: "orders-prod-app-credentials",
  },
};

const detailRoute = "databases/spoke-a/pds.orders-prod";

type SeriesInput = {
  kind: string;
  unit: string;
  values: number[];
};

function series({ kind, unit, values }: SeriesInput) {
  const end = Date.parse("2026-08-25T12:00:00Z");
  const stepMs = 5 * 60 * 1000;
  const points = values.map((v, index) => ({
    t: new Date(end - (values.length - 1 - index) * stepMs).toISOString(),
    v,
  }));

  return {
    kind,
    unit,
    pending: false,
    current: values.at(-1) ?? 0,
    min: Math.min(...values),
    max: Math.max(...values),
    average: values.reduce((total, v) => total + v, 0) / values.length,
    trend: "steady",
    points,
  };
}

const sustainedHighCpu = [88, 91, 87, 93, 90, 89, 92, 88, 94];

const unwellMetrics = {
  databaseId: ordersProd.id,
  window: "1h",
  start: "2026-08-25T11:00:00Z",
  end: "2026-08-25T12:00:00Z",
  stepSeconds: 300,
  pending: false,
  metrics: [
    series({ kind: "cpu", unit: "percent", values: sustainedHighCpu }),
    series({ kind: "memory", unit: "percent", values: [41, 44, 43] }),
    series({ kind: "connections", unit: "count", values: [18, 22, 19] }),
    series({ kind: "replication-lag", unit: "seconds", values: [12, 38, 45] }),
    series({ kind: "disk", unit: "percent", values: [78, 83, 91] }),
  ],
};

const failedBackup = {
  id: "orders-prod-20260825020000",
  status: "failed",
  sizeBytes: 0,
  scheduled: true,
  createdAt: "2026-08-25T02:00:00Z",
};

const chatReply = {
  conversationId: "c1",
  text: "orders-dev is ready.",
  actions: [{ label: "View status", kind: "status", id: "spoke-a/pds.orders-dev" }],
};

/**
 * The conversation chatReply saves, as the control plane reads it back. It
 * holds whatever message was sent last, so a refetch after the reply shows
 * the same thread the browser just drew.
 */
function savedThread(asked: string) {
  return {
    id: "c1",
    title: asked,
    provider: "anthropic",
    model: "claude-sonnet-4-20250514",
    updatedAt: "2026-09-28T10:00:00Z",
    contextWindow: 32,
    messages: [
      { role: "user", content: asked, actions: [], createdAt: "2026-09-28T10:00:00Z" },
      {
        role: "assistant",
        content: chatReply.text,
        actions: chatReply.actions,
        createdAt: "2026-09-28T10:00:01Z",
      },
    ],
  };
}

/** Serves the saved chat routes. Returns a setter for the last message sent. */
async function stubConversations(page: Page) {
  let asked = "what databases exist?";
  await page.route("**/api/chat/conversations", (route) => {
    const { messages: _messages, contextWindow: _window, ...summary } = savedThread(asked);
    return route.fulfill({ json: { conversations: [summary] } });
  });
  await page.route("**/api/chat/conversations/c1", (route) =>
    route.fulfill({ json: savedThread(asked) }),
  );

  return (message: string) => {
    asked = message;
  };
}

function sentMessage(request: { postDataJSON: () => unknown }): string {
  return (request.postDataJSON() as { message: string }).message;
}

async function stubConnections(
  page: Page,
  config: {
    assistant?: boolean;
    cursor?: boolean;
  },
) {
  await page.route("**/api/connections", (route) =>
    route.fulfill({
      json: {
        servicenow: { configured: false },
        assistant: { configured: config.assistant === true },
        cursor: { configured: config.cursor === true },
      },
    }),
  );
}

async function stubChatModels(page: Page, provider: "anthropic" | "cursor") {
  const models: Record<typeof provider, { id: string; displayName: string }> = {
    anthropic: { id: "claude-sonnet-4-20250514", displayName: "Claude Sonnet 4" },
    cursor: { id: "composer-2", displayName: "Composer 2" },
  };

  await page.route(`**/api/assistant/models?provider=${provider}`, (route) =>
    route.fulfill({ json: { models: [models[provider]] } }),
  );
}

async function stubChat(page: Page) {
  const remember = await stubConversations(page);
  await page.route("**/api/chat", (route) => {
    remember(sentMessage(route.request()));
    return route.fulfill({ json: chatReply });
  });
}

async function openChat(page: Page) {
  await page.goto("environments/spoke-a");
  await page.getByRole("button", { name: "Open chat" }).click();
  await expect(page.getByRole("region", { name: "Chat" })).toBeVisible();
}

/** Overview reads for a database with a failed backup. */
async function stubUnwellOverview(page: Page) {
  await page.route("**/api/databases/**/metrics*", (route) =>
    route.fulfill({ json: unwellMetrics }),
  );

  await page.route("**/api/databases/**/backups", (route) =>
    route.fulfill({ json: { backups: [failedBackup] } }),
  );

  await page.route("**/api/databases/*/*", (route) => route.fulfill({ json: ordersProd }));
}

test("anthropic configured sends one turn with assistant text and an action", async ({ page }) => {
  await stubConnections(page, { assistant: true });
  await stubChatModels(page, "anthropic");
  await stubChat(page);

  await openChat(page);

  await expect(page.getByRole("button", { name: "Open chat" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Hide chat" })).toBeVisible();

  await page.getByLabel("Message").fill("what databases exist?");
  await page.getByRole("button", { name: "Send" }).click();

  await expect(page.getByText("orders-dev is ready.")).toBeVisible();
  await expect(page.getByRole("button", { name: "View status" })).toBeVisible();
});

test("cursor only chat works", async ({ page }) => {
  await stubConnections(page, { cursor: true });
  await stubChatModels(page, "cursor");
  await stubChat(page);
  await stubUnwellOverview(page);

  await openChat(page);

  await page.getByLabel("Message").fill("what databases exist?");
  await page.getByRole("button", { name: "Send" }).click();

  await expect(page.getByText("orders-dev is ready.")).toBeVisible();
  await expect(page.getByRole("button", { name: "View status" })).toBeVisible();

  await page.goto(detailRoute);
  await expect(page.getByRole("heading", { level: 1, name: "orders-prod" })).toBeVisible();
  await expect(page.getByTestId("recommendation-list")).toHaveCount(0);
  await expect(page.getByTestId("investigate-button")).toHaveCount(0);
});

/** A provider that answers with a catalogue rather than a handful of models. */
async function stubManyModels(page: Page) {
  await page.route("**/api/assistant/models?provider=anthropic", (route) =>
    route.fulfill({
      json: {
        models: Array.from({ length: 120 }, (_, index) => ({
          id: `vendor/model-${index}`,
          displayName: `Vendor: Model ${index}`,
        })),
      },
    }),
  );
}

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("pds-theme", value), theme);
}

test("the model picker stays inside the window and narrows by search", async ({ page }) => {
  await stubConnections(page, { assistant: true });
  await stubManyModels(page);
  await stubChat(page);

  await openChat(page);

  const picker = page.getByRole("combobox", { name: "Model" });
  await expect(picker).toHaveText("Vendor: Model 0");

  await picker.click();

  const panel = page.getByTestId("combobox-panel");
  await expect(panel).toBeVisible();

  // 120 models used to run off the top of the window. Now the list scrolls.
  const box = (await panel.boundingBox())!;
  const viewport = page.viewportSize()!;
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.y + box.height).toBeLessThanOrEqual(viewport.height);

  await page.getByLabel("Search models").fill("Model 117");
  await expect(page.getByRole("option")).toHaveCount(1);

  await page.getByRole("option", { name: "Vendor: Model 117" }).click();
  await expect(panel).toHaveCount(0);
  await expect(picker).toHaveText("Vendor: Model 117");

  await page.getByLabel("Message").fill("what databases exist?");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("orders-dev is ready.")).toBeVisible();
});

for (const theme of ["light", "dark"] as const) {
  test(`a streamed reply lands in the thread in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubConnections(page, { assistant: true });
    await stubChatModels(page, "anthropic");

    let accept = "";
    const remember = await stubConversations(page);
    await page.route("**/api/chat", (route) => {
      accept = route.request().headers()["accept"] ?? "";
      remember(sentMessage(route.request()));
      return route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: [
          'event: conversation\ndata: {"conversationId":"c1"}\n\n',
          'event: progress\ndata: {"label":"Thinking"}\n\n',
          ": ping\n\n",
          'event: progress\ndata: {"label":"Listing databases"}\n\n',
          `event: reply\ndata: ${JSON.stringify(chatReply)}\n\n`,
        ].join(""),
      });
    });

    await openChat(page);
    await page.getByLabel("Message").fill("what databases exist?");
    await page.getByRole("button", { name: "Send" }).click();

    await expect(page.getByText("orders-dev is ready.")).toBeVisible();
    await expect(page.getByRole("button", { name: "View status" })).toBeVisible();
    await expect(page.getByRole("status")).toHaveCount(0);
    expect(accept).toContain("text/event-stream");
  });
}

/**
 * Holds the chat answer's stream open, so a spec can see the thread while the
 * model is still writing. Playwright's route.fulfill sends a body whole, so the
 * page's fetch is wrapped instead and the spec pushes events into it.
 */
async function holdChatStreamOpen(page: Page) {
  await page.addInitScript(() => {
    const original = window.fetch.bind(window);
    const encoder = new TextEncoder();
    let controller: ReadableStreamDefaultController<Uint8Array> | undefined;
    const w = window as unknown as {
      pushChatEvents: (chunk: string) => void;
    };
    w.pushChatEvents = (chunk) => controller?.enqueue(encoder.encode(chunk));
    window.fetch = (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (!url.endsWith("/api/chat") || init?.method !== "POST") {
        return original(input, init);
      }
      const body = new ReadableStream<Uint8Array>({
        start(c) {
          controller = c;
        },
      });
      return Promise.resolve(
        new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }),
      );
    };
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`the answer's text shows while the model writes it in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubConnections(page, { assistant: true });
    await stubChatModels(page, "anthropic");
    const remember = await stubConversations(page);
    remember("how is orders-dev?");
    await holdChatStreamOpen(page);

    await openChat(page);
    await page.getByLabel("Message").fill("how is orders-dev?");
    await page.getByRole("button", { name: "Send" }).click();

    const push = (chunk: string) =>
      page.evaluate(
        (value) =>
          (window as unknown as { pushChatEvents: (c: string) => void }).pushChatEvents(value),
        chunk,
      );
    await push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
    await push('event: progress\ndata: {"label":"Reading status"}\n\n');
    await push('event: text\ndata: {"delta":"orders-dev is "}\n\n');
    await push('event: text\ndata: {"delta":"ready."}\n\n');

    // The first words are on screen while the turn is still running.
    await expect(page.getByText("orders-dev is ready.")).toBeVisible();
    await expect(page.getByRole("status")).toContainText("Reading status");
    await expect(page.getByRole("button", { name: "View status" })).toHaveCount(0);
    await page.screenshot({ path: `test-results/chat-streaming-${theme}.png`, fullPage: true });

    await push(`event: reply\ndata: ${JSON.stringify(chatReply)}\n\n`);

    await expect(page.getByRole("button", { name: "View status" })).toBeVisible();
    await expect(page.getByText("orders-dev is ready.")).toHaveCount(1);
    await expect(page.getByRole("status")).toHaveCount(0);
    await page.screenshot({ path: `test-results/chat-answered-${theme}.png`, fullPage: true });
  });
}

for (const theme of ["light", "dark"] as const) {
  test(`a change waits for an approval card in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubConnections(page, { assistant: true });
    await stubChatModels(page, "anthropic");
    const remember = await stubConversations(page);
    remember("scale orders-dev to 2 CPUs");
    await holdChatStreamOpen(page);
    const answers: unknown[] = [];
    await page.route("**/api/chat/approvals/*", (route) => {
      answers.push(route.request().postDataJSON());
      return route.fulfill({ status: 204 });
    });

    await openChat(page);
    await page.getByLabel("Message").fill("scale orders-dev to 2 CPUs");
    await page.getByRole("button", { name: "Send" }).click();

    const push = (chunk: string) =>
      page.evaluate(
        (value) =>
          (window as unknown as { pushChatEvents: (c: string) => void }).pushChatEvents(value),
        chunk,
      );
    const expiresAt = new Date(Date.now() + 15 * 60 * 1000).toISOString();
    await push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
    await push(
      `event: approval\ndata: ${JSON.stringify({
        id: "a1",
        tool: "scale_database",
        title: "Scale a database",
        database: "orders-dev",
        destructive: false,
        lines: [
          { label: "Database", value: "spoke-a/pds.orders-dev" },
          { label: "CPU cores", value: "2" },
          { label: "Memory (GB)", value: "8" },
        ],
        expiresAt,
      })}\n\n`,
    );

    await expect(page.getByRole("region", { name: "Scale a database" })).toBeVisible();
    await expect(page.getByRole("status")).toContainText("Waiting for your approval");
    await page.screenshot({ path: `test-results/chat-approval-${theme}.png`, fullPage: true });

    await page.getByRole("button", { name: "Approve" }).click();
    await expect.poll(() => answers).toEqual([{ decision: "approve" }]);

    await push('event: approval_end\ndata: {"id":"a1","outcome":"approved"}\n\n');
    await expect(page.getByText("Approved. The change is being made.")).toBeVisible();
    await push(`event: reply\ndata: ${JSON.stringify(chatReply)}\n\n`);
    await expect(page.getByRole("region", { name: "Scale a database" })).toHaveCount(0);
  });

  test(`a delete asks for the database name in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubConnections(page, { assistant: true });
    await stubChatModels(page, "anthropic");
    const remember = await stubConversations(page);
    remember("delete orders-dev");
    await holdChatStreamOpen(page);

    await openChat(page);
    await page.getByLabel("Message").fill("delete orders-dev");
    await page.getByRole("button", { name: "Send" }).click();

    await page.evaluate(
      (value) => {
        const push = (window as unknown as { pushChatEvents: (c: string) => void }).pushChatEvents;
        push('event: conversation\ndata: {"conversationId":"c1"}\n\n');
        push(`event: approval\ndata: ${value}\n\n`);
      },
      JSON.stringify({
        id: "a2",
        tool: "delete_database",
        title: "Delete a database",
        database: "orders-dev",
        destructive: true,
        lines: [{ label: "Database", value: "spoke-a/pds.orders-dev" }],
        expiresAt: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
      }),
    );

    const approve = page.getByRole("button", { name: "Approve" });
    await expect(approve).toBeDisabled();
    await page.getByLabel("Type orders-dev to confirm").fill("orders-dev");
    await expect(approve).toBeEnabled();
    await page.screenshot({
      path: `test-results/chat-approval-delete-${theme}.png`,
      fullPage: true,
    });
  });
}

test("the model picker reads the same in dark mode", async ({ page }) => {
  await useTheme(page, "dark");
  await stubConnections(page, { assistant: true });
  await stubManyModels(page);
  await stubChat(page);

  await openChat(page);
  await page.getByRole("combobox", { name: "Model" }).click();

  await expect(page.locator("html")).toHaveClass(/dark/);

  const panel = page.getByTestId("combobox-panel");
  await expect(panel).toBeVisible();
  await expect(page.getByLabel("Search models")).toBeFocused();
  await expect(page.getByRole("option", { name: "Vendor: Model 0" })).toBeVisible();

  await page.getByLabel("Search models").fill("Model 37");
  await expect(page.getByRole("option")).toHaveCount(1);
  await expect(page.getByRole("option", { name: "Vendor: Model 37" })).toBeVisible();
});

test("arrow up loads a message already sent", async ({ page }) => {
  await stubConnections(page, { assistant: true });
  await stubChatModels(page, "anthropic");
  await stubChat(page);

  await openChat(page);

  const message = page.getByLabel("Message");
  await message.fill("why is cpu high?");
  await message.press("Enter");
  await expect(page.getByText("orders-dev is ready.")).toBeVisible();
  await expect(message).toHaveValue("");

  await message.press("ArrowUp");
  await expect(message).toHaveValue("why is cpu high?");

  // Walking forward past the newest message puts the empty draft back.
  await message.press("ArrowDown");
  await expect(message).toHaveValue("");
});

for (const theme of ["light", "dark"] as const) {
  test(`a saved chat reopens from the list in ${theme} mode`, async ({ page }) => {
    await useTheme(page, theme);
    await stubConnections(page, { assistant: true });
    await stubChatModels(page, "anthropic");
    await stubChat(page);

    await openChat(page);
    await page.getByRole("button", { name: "Show chats" }).click();
    const list = page.getByRole("list", { name: "Saved chats" });
    await expect(list).toContainText("what databases exist?");

    await list.getByRole("button", { name: /^what databases exist\?/ }).click();
    await expect(page.getByText("orders-dev is ready.")).toBeVisible();
    await expect(page.getByRole("button", { name: "View status" })).toBeVisible();
    await expect(page.getByText("Changing the provider or model starts a new chat.")).toBeVisible();
    await expect(page.getByText(/not saved/)).toHaveCount(0);
  });
}
