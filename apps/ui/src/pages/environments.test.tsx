import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useLocation } from "react-router";
import { AppRoutes } from "@/routes";
import { createMockDatabase, createMockLifecycle, renderWithProviders } from "@/test/utils";

function LocationProbe() {
  const location = useLocation();

  return <p data-testid="location">{`${location.pathname}${location.search}`}</p>;
}

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

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    await user.click(await screen.findByRole("tab", { name: "Monitoring" }));

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

    expect(await screen.findByRole("heading", { level: 1, name: "spoke-a" })).toBeInTheDocument();
    expect(
      screen.getByText(
        (_, element) =>
          element?.tagName === "P" && element.textContent === "MySQL or PostgreSQL on spoke-a.",
      ),
    ).toBeInTheDocument();
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

    await screen.findByRole("heading", { level: 1, name: "spoke-a" });

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([input]) =>
          String(input).includes("/api/databases?environment=spoke-a"),
        ),
      ).toBe(true),
    );
  });
});

describe("the environment page", () => {
  const monitoring = {
    settings: { access: "cluster", cpuMillicores: 1000, memoryMi: 1024, storageGi: 5 },
    access: {
      username: "admin",
      passwordCommand: "kubectl get secret pmm-secret -n everest-system",
      portForwardCommand: "kubectl port-forward -n everest-system svc/monitoring-service 8443:443",
      url: "https://localhost:8443",
    },
  };

  function stubOne(environment: Record<string, unknown>) {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { hub: true, environments: [environment] };
      }
      if (path.endsWith("/monitoring")) {
        return monitoring;
      }

      return {};
    });
  }

  it("is titled with the name, its status, Team and last report", async () => {
    stubOne({
      name: "spoke-a",
      team: "alpha",
      status: "connected",
      lastSeen: new Date().toISOString(),
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(await screen.findByRole("heading", { level: 1, name: "spoke-a" })).toBeInTheDocument();
    const header = screen.getByRole("banner");
    expect(header).toHaveTextContent("Connected");
    expect(header).toHaveTextContent("alpha");
    expect(header).toHaveTextContent(/Last seen/);
    expect(header).not.toHaveTextContent("Managed by PDS");
  });

  it("offers no Monitoring or Support access on a joined environment that never reported", async () => {
    stubOne({ name: "spoke-new", status: "unreachable" });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-new?tab=monitoring" });

    expect(await screen.findByRole("tab", { name: "Databases" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("Joining")).toBeInTheDocument();
    expect(screen.getByText("No report yet")).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Monitoring" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Support access" })).not.toBeInTheDocument();
  });

  it("opens the tab the address names, and puts the chosen tab in the address", async () => {
    stubOne({ name: "spoke-a", status: "connected", lastSeen: new Date().toISOString() });
    const user = userEvent.setup();

    renderWithProviders(
      <>
        <AppRoutes />
        <LocationProbe />
      </>,
      { route: "/environments/spoke-a?tab=monitoring" },
    );

    expect(await screen.findByRole("tab", { name: "Monitoring" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(await screen.findByLabelText("Storage (GiB)")).toHaveValue(5);

    await user.click(screen.getByRole("tab", { name: "Settings" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/environments/spoke-a?tab=settings");

    await user.click(screen.getByRole("tab", { name: "Databases" }));
    expect(screen.getByTestId("location")).toHaveTextContent(/^\/environments\/spoke-a$/);
  });
});

describe("connections on the environments page", () => {
  it("leaves the connections to their own page", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { hub: true, environments: [{ name: "spoke-a", status: "connected" }] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByRole("link", { name: /spoke-a/ })).toBeInTheDocument();
    expect(screen.queryByTestId("connections-panel")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Connections" })).not.toBeInTheDocument();
  });
});

describe("home in hub mode", () => {
  it("shows the setup checklist on home when there are no environments", async () => {
    stubApi((path) => {
      if (path === "/api/environments") {
        return { hub: true, environments: [] };
      }

      return {};
    });

    renderWithProviders(<AppRoutes />, { route: "/" });

    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "Setup progress" })).toBeInTheDocument();
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
    expect(screen.getByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
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

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-typo" });

    await userEvent.click(await screen.findByRole("tab", { name: "Settings" }));
    expect(
      screen.queryByRole("link", { name: "Continue with OpenEverest" }),
    ).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Disconnect" }));
    expect(
      within(screen.getByRole("dialog")).getByText(
        /The Kubernetes cluster and its databases keep running/,
      ),
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
    expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled();

    await userEvent.type(screen.getByLabelText("Environment name"), "spoke-typo");
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

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    await userEvent.click(await screen.findByRole("tab", { name: "Settings" }));
    expect(screen.getByRole("link", { name: "Update" })).toHaveAttribute(
      "href",
      "/environments/spoke-a/update",
    );
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeInTheDocument();
  });

  it("does not offer Update on an environment that never connected", async () => {
    stubList([{ name: "spoke-joining", status: "unreachable" }]);

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-joining" });

    await userEvent.click(await screen.findByRole("tab", { name: "Settings" }));
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Update" })).not.toBeInTheDocument();
  });

  it("carries the Team of the row in the Update link", async () => {
    stubList([
      { name: "prod", team: "alpha", status: "connected", lastSeen: new Date().toISOString() },
      { name: "prod", team: "beta", status: "connected", lastSeen: new Date().toISOString() },
    ]);

    renderWithProviders(<AppRoutes />, { route: "/environments/prod" });

    await userEvent.click(await screen.findByRole("tab", { name: "Settings" }));
    expect(screen.getByRole("link", { name: "Update" })).toHaveAttribute(
      "href",
      "/environments/prod/update?team=alpha",
    );
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

  it("summarises a managed environment on its row", async () => {
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
    // The run belongs to the environment's page, not to the row.
    expect(screen.queryByText("Creating network and Kubernetes cluster")).not.toBeInTheDocument();
  });

  it("shows a managed environment being built with its account, state and four phases", async () => {
    stubManaged(managedRow());
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments/orders" });

    expect(await screen.findByRole("heading", { level: 1, name: "orders" })).toBeInTheDocument();
    expect(screen.getByText("Creating")).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("banner")).toHaveTextContent(
        "Managed by PDS in AWS account payments, eu-west-1",
      ),
    );
    expect(screen.getByText("No report yet")).toBeInTheDocument();
    expect(screen.getByText("Creating network and Kubernetes cluster")).toBeInTheDocument();
    expect(screen.getByText("Finishing")).toBeInTheDocument();
    // There is nothing to monitor or open support on until the environment exists.
    expect(screen.getByRole("tab", { name: "Databases" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Monitoring" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Support access" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create database" })).toBeDisabled();

    await user.click(screen.getByRole("tab", { name: "Settings" }));
    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disconnect" })).not.toBeInTheDocument();
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

    renderWithProviders(<AppRoutes />, { route: "/environments/orders" });

    expect(await screen.findByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Fix the AWS account" })).toHaveAttribute(
      "href",
      "/environments?fix=cc_1#aws-accounts",
    );
  });

  it("gives a ready managed environment Monitoring and Updates, but no Support access", async () => {
    stubManaged(
      managedRow({
        state: "ready",
        provisioning: undefined,
        status: "connected",
        lastSeen: now,
        lifecycle: createMockLifecycle(),
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/orders" });

    expect(await screen.findByRole("tab", { name: "Monitoring" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Updates" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Support access" })).not.toBeInTheDocument();
    expect(screen.getByText(/^Last seen/)).toBeInTheDocument();
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
    await user.click(screen.getByRole("link", { name: /orders/ }));
    await user.click(await screen.findByRole("tab", { name: "Updates" }));

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
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments/orders" });

    expect(await screen.findByText("Lost")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Infrastructure missing");
    expect(screen.getByRole("button", { name: "Rebuild" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove from PDS only" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Monitoring" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Updates" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Settings" }));
    const settings = screen.getByRole("tabpanel");
    expect(within(settings).queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
    expect(
      within(settings).getByRole("button", { name: "Remove from PDS only" }),
    ).toBeInTheDocument();
  });
});
