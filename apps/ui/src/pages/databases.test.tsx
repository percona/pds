import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { environmentsPollMs } from "@/features/environments";
import { AppRoutes } from "@/routes";
import {
  createMockAlert,
  createMockAlertEnvironment,
  createMockAlertReport,
  createMockDatabase,
  createMockMetrics,
  renderWithProviders,
} from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const spoke = { name: "spoke-a", status: "connected", lastSeen: "2026-09-08T12:00:00Z" };

const ordersDev = createMockDatabase({
  id: "spoke-a/pds.orders-dev",
  name: "orders-dev",
  environment: "spoke-a",
  profile: "prod",
  status: "ready",
});

function stubFetch(handler: (path: string) => Response | Promise<Response>) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input).split("?")[0];
      return Promise.resolve(handler(path));
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("DatabasesPage", () => {
  it("shows skeleton rows shaped like the list while it loads", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(screen.getByTestId("databases-loading")).toBeInTheDocument();
  });

  it("lists what the adapter returns", async () => {
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({ environments: [spoke] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev] });
      }

      return jsonResponse({});
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(await screen.findByText("orders-dev")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "spoke-a" })).toBeInTheDocument();
    expect(
      screen.getByText(
        (_, element) =>
          element?.tagName === "P" && element.textContent === "MySQL or PostgreSQL on spoke-a.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create database" })).toHaveAttribute(
      "href",
      "/environments/spoke-a/new",
    );
    expect(screen.queryByTestId("connections-panel")).not.toBeInTheDocument();
  });

  it("keeps one create call to action next to the empty state", async () => {
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({ environments: [spoke] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [] });
      }

      return jsonResponse({});
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(await screen.findByText("No databases yet")).toBeInTheDocument();
    expect(
      screen.getByText("Create a MySQL or PostgreSQL database on this Kubernetes cluster."),
    ).toBeInTheDocument();

    const create = screen.getAllByRole("link", { name: "Create database" });

    expect(create).toHaveLength(1);
    expect(create[0]).toHaveAttribute("href", "/environments/spoke-a/new");
  });

  it("draws a CPU line per row once monitoring has readings", async () => {
    const provisioning = createMockDatabase({
      id: "spoke-a/pds.carts-dev",
      name: "carts-dev",
      environment: "spoke-a",
      status: "provisioning",
    });
    const fetchMock = vi.fn((input: string) => {
      const path = String(input);
      if (path === "/api/environments") {
        return Promise.resolve(jsonResponse({ environments: [spoke] }));
      }
      if (path.includes("/metrics")) {
        return Promise.resolve(jsonResponse(createMockMetrics(["cpu"])));
      }

      return Promise.resolve(jsonResponse({ databases: [ordersDev, provisioning] }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    const ready = await screen.findByTestId(`database-row-${ordersDev.id}`);

    await waitFor(() =>
      expect(within(ready).getByRole("img", { name: "CPU 31%, trending up" })).toBeInTheDocument(),
    );

    const pending = screen.getByTestId(`database-row-${provisioning.id}`);

    expect(within(pending).getByText("Metrics pending")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.filter(([input]) => String(input).includes("/metrics")),
    ).toHaveLength(1);
  });

  it("explains a failed read and offers one retry", async () => {
    const failed = {
      code: "ErrDownstream",
      message: "The database platform did not answer.",
    };
    let failReads = true;
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = String(input).split("?")[0];
      if (path === "/api/environments") {
        return Promise.resolve(jsonResponse({ environments: [spoke] }));
      }
      if (path.startsWith("/api/databases")) {
        return Promise.resolve(
          failReads ? jsonResponse(failed, 502) : jsonResponse({ databases: [ordersDev] }),
        );
      }

      return Promise.resolve(jsonResponse({}));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(await screen.findByText("Databases could not be read")).toBeInTheDocument();
    expect(screen.getByText("The database platform did not answer.")).toBeInTheDocument();

    failReads = false;
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(screen.getByText("orders-dev")).toBeInTheDocument());
  });

  it("reports a failed environment read instead of staying on a skeleton", async () => {
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse(
          { code: "ErrDownstream", message: "the git backend is not answering" },
          502,
        );
      }

      return jsonResponse({});
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(await screen.findByText("The environment list could not be read")).toBeInTheDocument();
    expect(screen.getByText("the git backend is not answering")).toBeInTheDocument();
    expect(screen.queryByTestId("databases-loading")).not.toBeInTheDocument();
  });

  it("keeps the database list when a later environment read fails", async () => {
    let failEnvironments = false;
    stubFetch((path) => {
      if (path === "/api/environments") {
        return failEnvironments
          ? jsonResponse(
              { code: "ErrDownstream", message: "the git backend is not answering" },
              502,
            )
          : jsonResponse({ environments: [spoke] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev] });
      }

      return jsonResponse({});
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(await screen.findByText("orders-dev")).toBeInTheDocument();
    failEnvironments = true;
    await vi.advanceTimersByTimeAsync(environmentsPollMs);

    await waitFor(() => expect(screen.getByText("orders-dev")).toBeInTheDocument());
    expect(screen.queryByText("The environment list could not be read")).not.toBeInTheDocument();
  });

  it("marks databases with firing alerts from the shared alerts read", async () => {
    const alertReads: string[] = [];
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({ environments: [spoke] });
      }
      if (path === "/api/alerts") {
        alertReads.push(path);
        return jsonResponse(
          createMockAlertReport({
            alerts: [createMockAlert({ databaseId: ordersDev.id })],
          }),
        );
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev] });
      }

      return jsonResponse({});
    });

    const { queryClient } = renderWithProviders(<AppRoutes />, {
      route: "/environments/spoke-a",
    });

    expect(await screen.findByText("1 critical")).toBeInTheDocument();
    // A second consumer that fetched on its own would still be in flight here.
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(alertReads).toHaveLength(1);
  });

  it("says once above the list that the environment cut its alerts", async () => {
    const quiet = { ...ordersDev, id: "spoke-a/ns.quiet", name: "quiet" };
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({ environments: [spoke] });
      }
      if (path === "/api/alerts") {
        return jsonResponse(
          createMockAlertReport({
            alerts: [],
            environments: [
              createMockAlertEnvironment({
                total: 60,
                shown: 50,
                note: "Showing 50 of 60 alerts in spoke-a. Open monitoring for the rest.",
              }),
            ],
          }),
        );
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev, quiet] });
      }

      return jsonResponse({});
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(
      await screen.findByText("Showing 50 of 60 alerts in spoke-a. Open monitoring for the rest."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Alerts may be incomplete")).not.toBeInTheDocument();
  });

  it.each(["off", "stale"] as const)(
    "explains once above the list why alerts are unknown when monitoring is %s",
    async (state) => {
      stubFetch((path) => {
        if (path === "/api/environments") {
          return jsonResponse({ environments: [spoke] });
        }
        if (path === "/api/alerts") {
          return jsonResponse(
            createMockAlertReport({
              alerts: [],
              environments: [
                createMockAlertEnvironment({
                  state,
                  reason: "Monitoring is not set up for this environment.",
                  readAt: null,
                }),
              ],
            }),
          );
        }
        if (path.startsWith("/api/databases")) {
          return jsonResponse({ databases: [ordersDev] });
        }

        return jsonResponse({});
      });

      renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

      expect(
        await screen.findByText("Monitoring is not set up for this environment."),
      ).toBeInTheDocument();
    },
  );

  it("shows one banner for an unreachable environment whose monitoring is stale", async () => {
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({
          environments: [{ ...spoke, status: "unreachable" }],
        });
      }
      if (path === "/api/alerts") {
        return jsonResponse(
          createMockAlertReport({
            alerts: [],
            environments: [
              createMockAlertEnvironment({
                state: "stale",
                reason: "Monitoring has not reported recently.",
                readAt: null,
              }),
            ],
          }),
        );
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev] });
      }

      return jsonResponse({});
    });

    const { queryClient } = renderWithProviders(<AppRoutes />, {
      route: "/environments/spoke-a",
    });

    expect(await screen.findByText("orders-dev")).toBeInTheDocument();
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(screen.queryByText("Monitoring has not reported recently.")).not.toBeInTheDocument();
  });

  it("keeps the monitoring action for an unreachable environment whose monitoring is off", async () => {
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({
          environments: [{ ...spoke, status: "unreachable" }],
        });
      }
      if (path === "/api/alerts") {
        return jsonResponse(
          createMockAlertReport({
            alerts: [],
            environments: [
              createMockAlertEnvironment({
                state: "off",
                reason: "Monitoring is not set up for this environment.",
                readAt: null,
                action: { kind: "set_up_monitoring", label: "Set up monitoring" },
              }),
            ],
          }),
        );
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev] });
      }

      return jsonResponse({});
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(
      await screen.findByText("Monitoring is not set up for this environment."),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Set up monitoring" })).toBeInTheDocument();
  });

  it("says alerts could not be read when the read fails and nothing is cached", async () => {
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({ environments: [spoke] });
      }
      if (path === "/api/alerts") {
        return jsonResponse({ error: "boom" }, 500);
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev] });
      }

      return jsonResponse({});
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(await screen.findByText("orders-dev")).toBeInTheDocument();
    expect(await screen.findByRole("status")).toHaveTextContent("Alerts could not be read");
  });

  it("keeps the alert marks when a later alerts read fails", async () => {
    let alertReads = 0;
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({ environments: [spoke] });
      }
      if (path === "/api/alerts") {
        alertReads += 1;
        return alertReads === 1
          ? jsonResponse(
              createMockAlertReport({ alerts: [createMockAlert({ databaseId: ordersDev.id })] }),
            )
          : jsonResponse({ error: "boom" }, 500);
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev] });
      }

      return jsonResponse({});
    });

    const { queryClient } = renderWithProviders(<AppRoutes />, {
      route: "/environments/spoke-a",
    });
    expect(await screen.findByText("1 critical")).toBeInTheDocument();

    await queryClient.invalidateQueries({ queryKey: ["alerts"] });

    await waitFor(() => expect(alertReads).toBe(2));
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(queryClient.getQueryState(["alerts"])?.status).toBe("error");
    expect(screen.getByText("1 critical")).toBeInTheDocument();
    expect(screen.queryByText(/Alerts could not be read/)).not.toBeInTheDocument();
  });

  it("shows no alerts banner for a live environment", async () => {
    stubFetch((path) => {
      if (path === "/api/environments") {
        return jsonResponse({ environments: [spoke] });
      }
      if (path === "/api/alerts") {
        return jsonResponse(createMockAlertReport({ alerts: [] }));
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [ordersDev] });
      }

      return jsonResponse({});
    });

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a" });

    expect(await screen.findByText("orders-dev")).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
