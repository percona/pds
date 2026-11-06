import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectionsPanel, connectionsKey } from "@/features/connections";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: status === 204 ? undefined : { "Content-Type": "application/json" },
  });
}

function stubConnections(
  status = {
    servicenow: { configured: false },
    assistant: { configured: false },
    cursor: { configured: false },
  },
) {
  const fetchMock = vi.fn((input: string, init?: RequestInit) => {
    const path = String(input);
    const method = init?.method ?? "GET";

    if (path === "/api/connections" && method === "GET") {
      return Promise.resolve(jsonResponse(status));
    }
    if (path === "/api/connections/servicenow" && method === "PUT") {
      status = { ...status, servicenow: { configured: true } };
      return Promise.resolve(jsonResponse(undefined, 204));
    }
    if (path === "/api/connections/assistant" && method === "PUT") {
      status = { ...status, assistant: { configured: true } };
      return Promise.resolve(jsonResponse(undefined, 204));
    }
    if (path === "/api/connections/cursor" && method === "PUT") {
      status = { ...status, cursor: { configured: true } };
      return Promise.resolve(jsonResponse(undefined, 204));
    }
    if (path === "/api/connections/servicenow" && method === "DELETE") {
      status = { ...status, servicenow: { configured: false } };
      return Promise.resolve(jsonResponse(undefined, 204));
    }
    if (path === "/api/connections/assistant" && method === "DELETE") {
      status = { ...status, assistant: { configured: false } };
      return Promise.resolve(jsonResponse(undefined, 204));
    }
    if (path === "/api/connections/cursor" && method === "DELETE") {
      status = { ...status, cursor: { configured: false } };
      return Promise.resolve(jsonResponse(undefined, 204));
    }
    if (path === "/api/support/tickets") {
      return Promise.resolve(jsonResponse({ tickets: [] }));
    }

    return Promise.resolve(jsonResponse({ code: "ErrNotFound", message: "missing" }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

async function openConnections() {
  const toggle = await screen.findByRole("button", { name: "Connections" });
  if (toggle.getAttribute("aria-expanded") !== "true") {
    await userEvent.click(toggle);
  }
}

describe("ConnectionsPanel", () => {
  it("starts closed and toggles the forms from the heading", async () => {
    stubConnections();

    renderWithProviders(<ConnectionsPanel />);

    const toggle = await screen.findByRole("button", { name: "Connections" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(
      screen.queryByText(/Tokens and keys are stored by the control plane/),
    ).not.toBeInTheDocument();
    expect(screen.queryByTestId("connection-servicenow")).not.toBeInTheDocument();

    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/Tokens and keys are stored by the control plane/)).toBeInTheDocument();
    expect(screen.getByTestId("connection-servicenow")).toBeInTheDocument();

    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("connection-servicenow")).not.toBeInTheDocument();
  });

  it("keeps a failed read closed until the heading is opened", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          jsonResponse({ code: "ErrDownstream", message: "Connections could not be read." }, 502),
        ),
      ),
    );

    renderWithProviders(<ConnectionsPanel />);

    const toggle = await screen.findByRole("button", { name: "Connections" });
    expect(screen.queryByTestId("connections-error")).not.toBeInTheDocument();
    await userEvent.click(toggle);
    expect(await screen.findByTestId("connections-error")).toHaveTextContent(
      "Connections could not be read.",
    );
  });

  it("replaces the cards when a later read fails", async () => {
    let fail = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fail
            ? jsonResponse(
                { code: "ErrDownstream", message: "Connections could not be read." },
                502,
              )
            : jsonResponse({
                servicenow: { configured: false },
                assistant: { configured: false },
                cursor: { configured: false },
              }),
        ),
      ),
    );

    const { queryClient } = renderWithProviders(<ConnectionsPanel />);
    await openConnections();
    expect(await screen.findByTestId("connection-servicenow")).toBeInTheDocument();

    fail = true;
    await queryClient.invalidateQueries({ queryKey: connectionsKey });

    expect(await screen.findByTestId("connections-error")).toHaveTextContent(
      "Connections could not be read.",
    );
    expect(screen.queryByTestId("connection-servicenow")).not.toBeInTheDocument();
  });

  it("shows paste forms when nothing is configured", async () => {
    stubConnections();

    renderWithProviders(<ConnectionsPanel />);
    await openConnections();

    const support = await screen.findByTestId("connection-servicenow");
    expect(within(support).getByRole("button", { name: "Save token" })).toBeDisabled();
    expect(within(support).queryByLabelText("API key")).not.toBeInTheDocument();
    expect(within(support).queryByLabelText("Instance URL")).not.toBeInTheDocument();
    expect(
      within(screen.getByTestId("connection-assistant")).getByRole("button", { name: "Save key" }),
    ).toBeDisabled();
    expect(
      within(screen.getByTestId("connection-cursor")).getByRole("button", { name: "Save key" }),
    ).toBeDisabled();
  });

  it("saves a ServiceNow client token and then shows connected", async () => {
    const fetchMock = stubConnections();

    renderWithProviders(<ConnectionsPanel />);
    await openConnections();

    const support = await screen.findByTestId("connection-servicenow");
    await userEvent.type(within(support).getByLabelText("Client token"), "client-token");
    await userEvent.click(within(support).getByRole("button", { name: "Save token" }));

    expect(await screen.findByText("Connected")).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Support token saved");

    const put = fetchMock.mock.calls.find(
      ([path, init]) => String(path) === "/api/connections/servicenow" && init?.method === "PUT",
    );
    expect(put).toBeDefined();
    expect(put?.[1]?.body).toBe(JSON.stringify({ clientToken: "client-token" }));
  });

  it("clears the ServiceNow token after confirming", async () => {
    stubConnections({
      servicenow: { configured: true },
      assistant: { configured: false },
      cursor: { configured: false },
    });

    renderWithProviders(<ConnectionsPanel />);
    await openConnections();

    const support = await screen.findByTestId("connection-servicenow");
    await userEvent.click(within(support).getByRole("button", { name: "Clear token" }));
    expect(
      screen.getByText("The control plane will not open tickets until you paste a new token."),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));

    await waitFor(() => {
      expect(within(support).getByRole("button", { name: "Save token" })).toBeInTheDocument();
    });
    expect(within(support).getByLabelText("Client token")).toBeInTheDocument();
  });

  it("shows an adapter error under the assistant form", async () => {
    const fetchMock = stubConnections();
    fetchMock.mockImplementation((input: string, init?: RequestInit) => {
      const path = String(input);
      if (path === "/api/connections" && (init?.method ?? "GET") === "GET") {
        return Promise.resolve(
          jsonResponse({
            servicenow: { configured: false },
            assistant: { configured: false },
            cursor: { configured: false },
          }),
        );
      }
      if (path === "/api/connections/assistant") {
        return Promise.resolve(
          jsonResponse(
            {
              code: "ErrDownstream",
              message: "Grant the adapter secrets write. See chart rbac.",
            },
            502,
          ),
        );
      }

      return Promise.resolve(jsonResponse({ tickets: [] }));
    });

    renderWithProviders(<ConnectionsPanel />);
    await openConnections();

    const assistant = await screen.findByTestId("connection-assistant");
    await userEvent.type(assistant.querySelector("input")!, "sk-ant-test");
    await userEvent.click(within(assistant).getByRole("button", { name: "Save key" }));

    expect(
      await screen.findByText("Grant the adapter secrets write. See chart rbac."),
    ).toBeInTheDocument();
  });

  it("clears the assistant key after confirming", async () => {
    stubConnections({
      servicenow: { configured: false },
      assistant: { configured: true },
      cursor: { configured: false },
    });

    renderWithProviders(<ConnectionsPanel />);
    await openConnections();

    await userEvent.click(await screen.findByRole("button", { name: "Clear key" }));
    expect(
      screen.getByText("The control plane will not use Claude in Chat until you paste a new key."),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));

    await waitFor(() => {
      expect(
        within(screen.getByTestId("connection-assistant")).getByRole("button", {
          name: "Save key",
        }),
      ).toBeInTheDocument();
    });
  });

  it("saves a Cursor key and never sends the value back on GET", async () => {
    const fetchMock = stubConnections();

    renderWithProviders(<ConnectionsPanel />);
    await openConnections();

    const cursor = await screen.findByTestId("connection-cursor");
    await userEvent.type(cursor.querySelector("input")!, "sk-cursor-secret");
    await userEvent.click(within(cursor).getByRole("button", { name: "Save key" }));

    expect(within(cursor).getByText("Connected")).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Cursor key saved");

    const getCalls = fetchMock.mock.calls.filter(
      ([path, init]) => String(path) === "/api/connections" && (init?.method ?? "GET") === "GET",
    );
    expect(getCalls.length).toBeGreaterThan(0);
    for (const [, init] of getCalls) {
      const body = init?.body;
      if (typeof body === "string") {
        expect(body).not.toContain("sk-cursor-secret");
      }
    }
    expect(JSON.stringify(fetchMock.mock.results)).not.toContain("sk-cursor-secret");
  });
});

describe("ConnectionsPanel with the assistant off", () => {
  it("hides every model key, and keeps ServiceNow", async () => {
    stubConnections();

    renderWithProviders(<ConnectionsPanel />, { flags: { assistant: false } });
    await openConnections();

    expect(await screen.findByText("Support")).toBeInTheDocument();
    expect(screen.queryByText("Assistant")).not.toBeInTheDocument();
    expect(screen.queryByText("Cursor")).not.toBeInTheDocument();
  });
});

describe("ConnectionsPanel with a stored key for a removed provider", () => {
  it("offers only the Support, Assistant and Cursor cards", async () => {
    // A control plane that has not restarted yet may still report the old keys.
    const stale = {
      servicenow: { configured: false },
      assistant: { configured: false },
      cursor: { configured: false },
      openai: { configured: true },
      gemini: { configured: true },
      openrouter: { configured: true },
    };
    stubConnections(stale);

    renderWithProviders(<ConnectionsPanel />);
    await openConnections();

    expect(await screen.findByTestId("connection-assistant")).toBeInTheDocument();
    expect(screen.getByTestId("connection-cursor")).toBeInTheDocument();
    expect(screen.getByTestId("connection-servicenow")).toBeInTheDocument();
    expect(screen.queryByTestId("connection-openai")).not.toBeInTheDocument();
    expect(screen.queryByTestId("connection-gemini")).not.toBeInTheDocument();
    expect(screen.queryByTestId("connection-openrouter")).not.toBeInTheDocument();
  });
});
