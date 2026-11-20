import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { createMockDatabase, createMockLifecycle, renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(handler: (path: string) => unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input).split("?")[0];
      if (path === "/api/connections") {
        return Promise.resolve(
          jsonResponse({
            servicenow: { configured: false },
            assistant: { configured: false },
            cursor: { configured: false },
          }),
        );
      }
      if (path.startsWith("/api/activity")) {
        return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
      }

      return Promise.resolve(jsonResponse(handler(path)));
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

describe("EnvironmentsPage", () => {
  it("opens an environment's monitoring from its row", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return {
          hub: true,
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: new Date().toISOString() },
          ],
        };
      }
      if (path === "/api/environments/spoke-a/monitoring") {
        return {
          settings: { access: "cluster", cpuMillicores: 1000, memoryMi: 1024, storageGi: 5 },
          access: {
            username: "admin",
            passwordCommand: "kubectl get secret pmm-secret -n everest-system",
            portForwardCommand:
              "kubectl port-forward -n everest-system svc/monitoring-service 8443:443",
            url: "https://localhost:8443",
          },
        };
      }

      return {};
    });
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await user.click(await screen.findByRole("button", { name: "Monitoring" }));

    expect(await screen.findByLabelText("Storage (GiB)")).toHaveValue(5);
    expect(screen.getByText(/get secret pmm-secret/)).toBeInTheDocument();
  });

  it("names the Team of each environment when the list covers every Team", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return {
          hub: true,
          environments: [
            { name: "spoke-a", status: "connected", team: "Payments" },
            { name: "spoke-b", status: "connected" },
          ],
        };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    const payments = (await screen.findByText("spoke-a")).closest("li");
    const own = screen.getByText("spoke-b").closest("li");
    expect(payments).toHaveTextContent("Payments");
    expect(own).not.toHaveTextContent("Team");
  });

  it("shows an empty state and Create an environment on a hub with no spokes", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { hub: true, environments: [] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByText("No environments yet")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Environments" })).toBeInTheDocument();
    expect(
      screen.getByText(/Add an environment to run MySQL or PostgreSQL on it from here/),
    ).toBeInTheDocument();

    const connect = screen.getAllByRole("link", { name: "Create an environment" });

    expect(connect.length).toBeGreaterThan(0);
    expect(connect[0]).toHaveAttribute("href", "/environments/new");
  });

  it("tells an environment still joining apart from one that went quiet", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return {
          hub: true,
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
            // Beaten once, then stopped. That is a problem.
            { name: "spoke-b", status: "unreachable", lastSeen: "2026-09-08T11:00:00Z" },
            // Registered when its token was minted and never beaten. That
            // is the normal state for the 15 minutes eksctl takes.
            { name: "spoke-c", status: "unreachable" },
          ],
        };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByText("spoke-a")).toBeInTheDocument();
    expect(screen.getByText("spoke-b")).toBeInTheDocument();
    expect(screen.getByText("spoke-c")).toBeInTheDocument();
    expect(screen.getByText("Connected")).toBeInTheDocument();
    expect(screen.getByText("Unreachable")).toBeInTheDocument();
    expect(screen.getByText("Joining")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /spoke-a/ })).toHaveAttribute(
      "href",
      "/environments/spoke-a",
    );
  });

  it("opens an environment and lists its databases", async () => {
    const orders = createMockDatabase({
      id: "spoke-a/pds.orders-dev",
      name: "orders-dev",
      environment: "spoke-a",
    });

    stubApi((path) => {
      if (path === "/api/environments") {
        return {
          hub: true,
          environments: [{ name: "spoke-a", status: "connected" }],
        };
      }
      if (path.startsWith("/api/databases")) {
        return { databases: [orders] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await userEvent.click(await screen.findByRole("link", { name: /spoke-a/ }));

    expect(await screen.findByRole("heading", { level: 1, name: "Databases" })).toBeInTheDocument();
    expect(screen.getByText("MySQL or PostgreSQL on spoke-a.")).toBeInTheDocument();
    expect(screen.getByText("orders-dev")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create database" })).toHaveAttribute(
      "href",
      "/environments/spoke-a/new",
    );
    expect(screen.getByRole("link", { name: "Back to environments" })).toHaveAttribute(
      "href",
      "/environments",
    );
  });

  it("sends the environment query when listing databases for one spoke", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (path === "/api/connections") {
        return Promise.resolve(
          jsonResponse({
            servicenow: { configured: false },
            assistant: { configured: false },
            cursor: { configured: false },
          }),
        );
      }
      if (path === "/api/environments") {
        return Promise.resolve(
          jsonResponse({ hub: true, environments: [{ name: "spoke-a", status: "connected" }] }),
        );
      }
      if (path.startsWith("/api/activity")) {
        return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
      }
      if (path.startsWith("/api/databases")) {
        return Promise.resolve(jsonResponse({ databases: [] }));
      }

      return Promise.resolve(jsonResponse({}));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    await screen.findByRole("heading", { level: 1, name: "Databases" });

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([input]) =>
          String(input).includes("/api/databases?environment=spoke-a"),
        ),
      ).toBe(true),
    );
  });
});

describe("connections on the hub", () => {
  it("puts the connections panel under the environment list", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return {
          hub: true,
          environments: [{ name: "spoke-a", status: "connected" }],
        };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(
      await screen.findByRole("heading", { level: 2, name: "Connections" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("connections-panel")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 3, name: "Support" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Connections" }));
    expect(screen.getByRole("heading", { level: 3, name: "Support" })).toBeInTheDocument();
  });

  it("shows the connections panel on a hub with no spokes yet", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { hub: true, environments: [] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByText("No environments yet")).toBeInTheDocument();
    expect(screen.getByTestId("connections-panel")).toBeInTheDocument();
  });

  it("keeps the connections panel on the environment list", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { environments: [] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Environments" }),
    ).toBeInTheDocument();
    expect(await screen.findByTestId("connections-panel")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1, name: "Databases" })).not.toBeInTheDocument();
  });

  it("leaves the connections panel out when that surface is off", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { hub: true, environments: [] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments", flags: { connections: false } });

    expect(await screen.findByText("No environments yet")).toBeInTheDocument();
    expect(screen.queryByTestId("connections-panel")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Connections" })).not.toBeInTheDocument();
  });
});

describe("home in hub mode", () => {
  it("sends the hub home to the environment list", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { hub: true, environments: [] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/" });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Environments" }),
    ).toBeInTheDocument();
  });

  it("reports a failed environment read on the home page", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/connections") {
          return Promise.resolve(
            jsonResponse({
              servicenow: { configured: false },
              assistant: { configured: false },
              cursor: { configured: false },
            }),
          );
        }
        if (path === "/api/environments") {
          return Promise.resolve(
            jsonResponse(
              { code: "ErrDownstream", message: "the git backend is not answering" },
              502,
            ),
          );
        }
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }
        if (path.startsWith("/api/databases")) {
          return Promise.resolve(jsonResponse({ databases: [] }));
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/" });

    expect(await screen.findByText("The environment list could not be read")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Environments" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1, name: "Databases" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Create database" })).not.toBeInTheDocument();
  });
});

describe("unscoped database routes", () => {
  it("answers not found for /databases", async () => {
    const errors: unknown[] = [];
    const spy = vi.spyOn(console, "error").mockImplementation((...args) => {
      errors.push(args[0]);
    });

    stubApi((path) => {
      if (path === "/api/environments") {
        return {
          hub: true,
          environments: [
            { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
          ],
        };
      }
      if (path === "/api/databases") {
        return { databases: [] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/databases" });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Page not found" }),
    ).toBeInTheDocument();
    expect(
      errors.filter((entry) => String(entry).includes("Rendered fewer hooks than expected")),
    ).toHaveLength(0);

    spy.mockRestore();
  });
});

describe("EnvironmentsPage when the read fails", () => {
  it("reports the failure on the environments page", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/environments") {
          return Promise.resolve(
            jsonResponse(
              { code: "ErrDownstream", message: "the git backend is not answering" },
              502,
            ),
          );
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByText("The environment list could not be read")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Environments" })).toBeInTheDocument();
  });
});

// Nothing covered the create path that hub mode actually uses, so regressing
// the environment prop would have left every hub create answering "Pick a
// environment" with a green suite.
describe("CreateDatabasePage in hub mode", () => {
  it("sends the environment from the route with the create", async () => {
    const posts: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input).split("?")[0];
        if (path === "/api/environments") {
          return Promise.resolve(
            jsonResponse({
              hub: true,
              environments: [
                { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" },
              ],
            }),
          );
        }
        if (path === "/api/databases" && init?.method === "POST") {
          posts.push(String(init.body));

          return Promise.resolve(
            jsonResponse(createMockDatabase({ id: "spoke-a/pds.orders-dev" }), 201),
          );
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/new" });

    expect(
      await screen.findByText(/That environment is not checked before a create/),
    ).toBeInTheDocument();
    const name = await screen.findByLabelText("Name");
    await userEvent.type(name, "orders-dev");
    await userEvent.click(screen.getByRole("button", { name: "Create database" }));

    await waitFor(() => expect(posts).toHaveLength(1));
    expect(JSON.parse(posts[0])).toMatchObject({ name: "orders-dev", environment: "spoke-a" });
  });

  it("answers not found for an unscoped create url", () => {
    renderWithProviders(<AppRoutes />, { route: "/databases/new" });

    expect(screen.getByRole("heading", { level: 1, name: "Page not found" })).toBeInTheDocument();
    expect(screen.queryByText("Pick an environment first")).not.toBeInTheDocument();
  });
});

// A row appears the moment a token is minted, so a typo was permanent.
describe("Disconnecting an environment", () => {
  it("asks first, then removes it", async () => {
    const deletes: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input).split("?")[0];
        if (path === "/api/environments" && init?.method === "DELETE") {
          return Promise.resolve(jsonResponse({}));
        }
        if (path.startsWith("/api/environments/") && init?.method === "DELETE") {
          deletes.push(path);

          return Promise.resolve(new Response(null, { status: 204 }));
        }
        if (path === "/api/environments") {
          return Promise.resolve(
            jsonResponse({
              hub: true,
              environments: [{ name: "spoke-typo", namespace: "databases", status: "unreachable" }],
            }),
          );
        }
        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await screen.findByRole("button", { name: "Disconnect" });
    expect(
      screen.queryByRole("link", { name: "Continue with OpenEverest" }),
    ).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Disconnect" }));
    expect(
      screen.getByText(/The Kubernetes cluster and its databases keep running/),
    ).toBeInTheDocument();
    // Nothing in AWS is touched, so the confirmation must not read as a cleanup.
    expect(screen.getByText(/Nothing in AWS is deleted/)).toBeInTheDocument();
    expect(screen.getByText(/If the join created an EKS cluster/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View delete command" })).toHaveAttribute(
      "href",
      "/environments/spoke-typo/aws-cleanup?namespace=databases",
    );
    // Keeping the databases is the other way out, and it sits where the
    // cleanup link does: after a disconnect there is nothing left to hand over.
    expect(screen.getByRole("link", { name: "Continue with OpenEverest" })).toHaveAttribute(
      "href",
      "/environments/spoke-typo/openeverest?namespace=databases",
    );
    expect(deletes).toHaveLength(0);
    expect(screen.queryByLabelText("Hub operator token")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(deletes).toEqual(["/api/environments/spoke-typo"]));

    const removed = vi.mocked(fetch).mock.calls.find(([input, init]) => {
      return String(input).startsWith("/api/environments/") && init?.method === "DELETE";
    });
    // Nothing is typed for this write. A hub that refuses it answers 401, and
    // the request helper fetches the token and repeats the call.
    expect(removed?.[1]?.headers).not.toMatchObject({ Authorization: expect.any(String) });
  });
});

describe("Updating an environment", () => {
  function stubList(environments: unknown[]) {
    stubApi((path) => (path === "/api/environments" ? { hub: true, environments } : {}));
  }

  it("offers Update beside Disconnect on a connected and an unreachable environment", async () => {
    stubList([
      { name: "spoke-a", status: "connected", lastSeen: new Date().toISOString() },
      {
        name: "spoke-quiet",
        status: "unreachable",
        lastSeen: new Date(Date.now() - 3600_000).toISOString(),
      },
    ]);

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    const links = await screen.findAllByRole("link", { name: "Update" });
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "/environments/spoke-a/update",
      "/environments/spoke-quiet/update",
    ]);
    expect(screen.getAllByRole("button", { name: "Disconnect" })).toHaveLength(2);
  });

  it("does not offer Update on an environment that never connected", async () => {
    stubList([{ name: "spoke-joining", status: "unreachable" }]);

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await screen.findByRole("button", { name: "Disconnect" });
    expect(screen.queryByRole("link", { name: "Update" })).not.toBeInTheDocument();
  });

  it("carries the Team of the row in the Update link", async () => {
    stubList([
      { name: "prod", team: "alpha", status: "connected", lastSeen: new Date().toISOString() },
      { name: "prod", team: "beta", status: "connected", lastSeen: new Date().toISOString() },
    ]);

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    const links = await screen.findAllByRole("link", { name: "Update" });
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "/environments/prod/update?team=alpha",
      "/environments/prod/update?team=beta",
    ]);
  });
});

describe("EnvironmentsPage managed environments", () => {
  const now = new Date().toISOString();
  const account = (state: string) => ({
    id: "cc_1",
    accountId: "123456789012",
    alias: "payments",
    state,
    allowedRegions: ["eu-west-1"],
    stackRegion: "eu-west-1",
    templateVersion: 1,
    latestTemplateVersion: 1,
    createdAt: now,
    environments: ["orders"],
  });
  const managedRow = (extra: Record<string, unknown> = {}) => ({
    name: "orders",
    status: "pending",
    origin: "managed",
    state: "creating",
    connectionId: "cc_1",
    region: "eu-west-1",
    provisioning: {
      run: { id: 3, kind: "create", status: "running", startedAt: now },
      progress: "in_progress",
      phases: [
        { name: "Creating network and Kubernetes cluster", status: "running" },
        { name: "Connecting to the environment", status: "pending" },
        { name: "Installing PDS", status: "pending" },
        { name: "Finishing", status: "pending" },
      ],
    },
    ...extra,
  });

  function stubManaged(row: Record<string, unknown>, accountState = "ready") {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { hub: true, environments: [row] };
      }
      if (path === "/api/cloud-connections") {
        return { cloudConnections: [account(accountState)] };
      }

      return {};
    });
  }

  it("shows a managed environment with its account, state and four phases", async () => {
    stubManaged(managedRow());

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByText("Creating")).toBeInTheDocument();
    expect(
      await screen.findByText(
        (_, element) =>
          element?.tagName === "P" &&
          element.textContent === "Created by PDS in AWS account payments in eu-west-1",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("Creating network and Kubernetes cluster")).toBeInTheDocument();
    expect(screen.getByText("Finishing")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disconnect" })).not.toBeInTheDocument();
    // There is nothing to monitor or open support on until the environment exists.
    expect(screen.queryByRole("button", { name: "Monitoring" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Support access" })).not.toBeInTheDocument();
  });

  it("offers Retry after a failure and links an unreachable account to its fix", async () => {
    stubManaged(
      managedRow({
        state: "failed",
        provisioning: {
          run: { id: 3, kind: "create", status: "failed", startedAt: now },
          progress: "failed",
          phases: [{ name: "Creating network and Kubernetes cluster", status: "failed" }],
          failure: {
            sentence: "AWS could not create the network. Retry the step.",
            action: "retry",
          },
        },
      }),
      "unreachable",
    );

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Fix the AWS account" })).toHaveAttribute(
      "href",
      "/environments?fix=cc_1#aws-accounts",
    );
  });

  it("lists the AWS accounts under the environments", async () => {
    stubManaged(
      managedRow({ state: "ready", provisioning: undefined, status: "connected", lastSeen: now }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByRole("heading", { name: "AWS accounts" })).toBeInTheDocument();
  });

  it("notes an available update and drift on the row and opens the lifecycle panel", async () => {
    stubManaged(
      managedRow({
        state: "ready",
        provisioning: undefined,
        status: "connected",
        lastSeen: now,
        lifecycle: createMockLifecycle({
          pendingChartVersion: "1.5.0",
          drift: [{ resource: "NodeGroup", detail: "Instance type changed" }],
        }),
      }),
    );
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByText("Update available")).toBeInTheDocument();
    expect(screen.getByText("Changed outside PDS")).toBeInTheDocument();
    expect(screen.queryByText("PDS 1.5.0 is available.")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Updates" }));

    expect(await screen.findByText("PDS 1.5.0 is available.")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Changed outside PDS" })).toHaveTextContent(
      "NodeGroup",
    );
  });

  it("shows a lost environment with Rebuild or Remove from PDS only, and no Delete", async () => {
    stubManaged(
      managedRow({
        state: "lost",
        provisioning: undefined,
        status: "pending",
        lifecycle: createMockLifecycle({ lost: true, lostReason: "Infrastructure missing" }),
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByText("Lost")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Infrastructure missing");
    expect(screen.getByRole("button", { name: "Rebuild" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove from PDS only" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Monitoring" })).not.toBeInTheDocument();
  });
});
