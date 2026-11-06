import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { environmentsPollMs } from "@/features/environments";
import { AppRoutes } from "@/routes";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

const greenStatus = {
  ready: true,
  canCreate: true,
  checks: [
    {
      id: "storage-class",
      label: "Storage class",
      status: "ok",
      detail: "Default StorageClass gp3 is set.",
      autoCreate: false,
      blocking: true,
    },
  ],
};

const blockedStatus = {
  ready: false,
  canCreate: false,
  checks: [
    {
      id: "storage-class",
      label: "Storage class",
      status: "action_required",
      detail: "A database volume cannot bind without a default StorageClass.",
      fix: "No default StorageClass. Do you want to create one automatically?",
      autoCreate: true,
      blocking: true,
    },
  ],
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Handler = (url: string, init?: RequestInit) => Response;

function stubApi(handler: Handler) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(handler(String(input), init)),
  );
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  sessionStorage.clear();
});

describe("CreateDatabasePage", () => {
  it("reports a failed environment read instead of an empty form", async () => {
    stubApi((url) => {
      if (url.split("?")[0] === "/api/environments") {
        return jsonResponse(
          { code: "ErrDownstream", message: "the git backend is not answering" },
          502,
        );
      }

      return jsonResponse({});
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/new" });

    expect(await screen.findByText("The environment list could not be read.")).toBeInTheDocument();
    expect(screen.getByText("the git backend is not answering")).toBeInTheDocument();
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
  });

  it("keeps the form when a later environment read fails", async () => {
    let failEnvironments = false;
    stubApi((url) => {
      const path = url.split("?")[0];
      if (path === "/api/environments") {
        return failEnvironments
          ? jsonResponse(
              { code: "ErrDownstream", message: "the git backend is not answering" },
              502,
            )
          : jsonResponse({
              environments: [
                { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
              ],
            });
      }

      return jsonResponse({});
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/new" });

    expect(await screen.findByLabelText("Name")).toBeInTheDocument();
    failEnvironments = true;
    await vi.advanceTimersByTimeAsync(environmentsPollMs);

    expect(screen.getByLabelText("Name")).toBeInTheDocument();
    expect(screen.queryByText("The environment list could not be read.")).not.toBeInTheDocument();
  });

  it("shows the no such environment state for an unknown hub environment", async () => {
    stubApi((url) => {
      if (url === "/api/environments") {
        return jsonResponse({
          hub: true,
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
          ],
        });
      }

      return jsonResponse(greenStatus);
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-typo/new" });

    expect(await screen.findByText("This environment is not on the list.")).toBeInTheDocument();
    expect(
      screen.getByText((_, node) => node?.textContent === "No environment named spoke-typo"),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
  });

  it("creates on a hub with no credential of its own, because the session is the cookie", async () => {
    const created = createMockDatabase({
      id: "spoke-a/pds.orders",
      name: "orders",
      environment: "spoke-a",
      status: "provisioning",
    });
    const fetchMock = stubApi((url, init) => {
      if (url === "/api/environments") {
        return jsonResponse({
          hub: true,
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
          ],
        });
      }
      if (url === "/api/databases" && init?.method === "POST") {
        return jsonResponse(created, 201);
      }
      if (url.startsWith("/api/databases")) {
        return jsonResponse({ databases: [] });
      }

      return jsonResponse(greenStatus);
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/new" });

    // Nothing is typed and nothing is fetched: a signed in customer already
    // satisfies the write guard.
    expect(screen.queryByLabelText("Hub operator token")).not.toBeInTheDocument();
    await userEvent.type(await screen.findByLabelText("Name"), "orders");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Create database" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Create database" }));

    await waitFor(() => {
      const posted = fetchMock.mock.calls.filter(
        ([input, init]) => String(input) === "/api/databases" && init?.method === "POST",
      );
      expect(posted).toHaveLength(1);
      expect(posted[0]?.[1]?.headers).not.toHaveProperty("Authorization");
    });
  });

  it("shows the form without consulting platform status", async () => {
    const fetchMock = stubApi((url) => {
      if (url === "/api/environments") {
        return jsonResponse({
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
          ],
        });
      }
      if (url === "/api/platform/status") return jsonResponse(blockedStatus);

      return jsonResponse(greenStatus);
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/new" });

    expect(await screen.findByLabelText("Name")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Dev" })).toBeInTheDocument();
    expect(
      screen.getByText("A MySQL or PostgreSQL database on this Kubernetes cluster. On spoke-a."),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Databases cannot be created until these checks pass."),
    ).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.map(([input]) => String(input))).not.toContain(
      "/api/platform/status",
    );
  });

  it("moves to the provisioning screen after the adapter accepts the create", async () => {
    const created = createMockDatabase({
      id: "spoke-a/pds.orders-dev",
      name: "orders-dev",
      environment: "spoke-a",
      status: "provisioning",
      message: "Reconciling database environment",
    });

    stubApi((url, init) => {
      if (url === "/api/environments") {
        return jsonResponse({
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
          ],
        });
      }
      if (url === "/api/platform/status") return jsonResponse(greenStatus);
      if (url === "/api/databases" && init?.method === "POST") return jsonResponse(created, 201);

      return jsonResponse(created);
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/new" });

    await userEvent.type(await screen.findByLabelText("Name"), "orders-dev");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Create database" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Create database" }));

    expect(await screen.findByText("Usually 6 to 12 minutes")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Creating orders-dev");
    expect(screen.getByTestId("provisioning-step-volumes")).toHaveTextContent("Creating volumes");
  });

  it("lands on connect once the connection is published", async () => {
    const ready = createMockDatabase({
      id: "spoke-a/pds.orders-dev",
      name: "orders-dev",
      environment: "spoke-a",
      status: "ready",
      connection: {
        host: "orders-dev.pds.svc",
        port: 3306,
        username: "app",
        maskedUri: "mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev",
      },
    });

    stubApi((url) => {
      if (url === "/api/environments") {
        return jsonResponse({
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
          ],
        });
      }
      if (url === "/api/platform/status") return jsonResponse(greenStatus);

      return jsonResponse(ready);
    });

    renderWithProviders(<AppRoutes />, { route: "/databases/spoke-a/pds.orders-dev" });

    expect(
      await screen.findByText("mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev"),
    ).toBeInTheDocument();
    // The header and the card both lead to the same dialog, which is where
    // every copy happens.
    expect(screen.getAllByRole("button", { name: "Connect" })).toHaveLength(2);
  });

  it("keeps the user on the form when the adapter rejects the name", async () => {
    stubApi((url, init) => {
      if (url === "/api/environments") {
        return jsonResponse({
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
          ],
        });
      }
      if (url === "/api/platform/status") return jsonResponse(greenStatus);
      if (url === "/api/databases" && init?.method === "POST") {
        return jsonResponse(
          { code: "ErrConflict", message: "A database named `orders-dev` already exists." },
          409,
        );
      }

      return jsonResponse(greenStatus);
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/new" });

    await userEvent.type(await screen.findByLabelText("Name"), "orders-dev");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Create database" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Create database" }));

    expect(
      await screen.findByText("A database named `orders-dev` already exists."),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("orders-dev");
  });
});

describe("DatabasesPage", () => {
  it("sends the create button to the create form", async () => {
    stubApi((url) => {
      if (url === "/api/environments") {
        return jsonResponse({
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
          ],
        });
      }

      return jsonResponse(greenStatus);
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    const create = await screen.findByRole("link", { name: "Create database" });
    expect(create).toHaveAttribute("href", "/environments/spoke-a/new");

    await userEvent.click(create);

    expect(await screen.findByLabelText("Name")).toBeInTheDocument();
  });
});
