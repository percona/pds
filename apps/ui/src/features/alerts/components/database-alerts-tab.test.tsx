import { screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMockAlert, createMockAlertEnvironment, renderWithProviders } from "@/test/utils";
import type { DatabaseAlertReport } from "../types";
import { DatabaseAlertsTab } from "./database-alerts-tab";

const databaseId = "spoke-a/pds.orders-dev";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * The alert rows read the signed in Team for their links, so the session read
 * answers on its own and the mock keeps counting only the alert reads.
 */
function withSession(alerts: (input: RequestInfo | URL) => Promise<Response>) {
  return (input: RequestInfo | URL) =>
    String(input).startsWith("/api/auth/session")
      ? Promise.resolve(jsonResponse({}))
      : alerts(input);
}

function serve(report: DatabaseAlertReport) {
  const fetchMock = vi.fn(() => Promise.resolve(jsonResponse(report)));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DatabaseAlertsTab", () => {
  it("lists the alerts the database route returns", async () => {
    const fetchMock = serve({
      databaseId,
      environment: createMockAlertEnvironment(),
      alerts: [
        createMockAlert({ id: "a", summary: "orders-dev is down" }),
        createMockAlert({
          id: "b",
          rule: "disk_used",
          severity: "warning",
          summary: "orders-dev storage is 91% full",
          action: { kind: "scale_storage", label: "Scale storage" },
        }),
      ],
    });

    renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);

    expect(await screen.findByText("orders-dev is down")).toBeInTheDocument();
    expect(screen.getByText("orders-dev storage is 91% full")).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/databases/spoke-a/pds.orders-dev/alerts",
      expect.anything(),
    );
  });

  it("says nothing is firing only when the environment was read", async () => {
    serve({ databaseId, environment: createMockAlertEnvironment(), alerts: [] });

    renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);

    expect(await screen.findByText("No alerts are firing for this database")).toBeInTheDocument();
  });

  it("does not call a database healthy when its environment cut the list", async () => {
    const note = "Showing 50 of 60 alerts in spoke-a. Open monitoring for the rest.";
    serve({
      databaseId,
      environment: createMockAlertEnvironment({ total: 60, shown: 50, note }),
      alerts: [],
    });

    renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);

    expect(
      await screen.findByText("Alerts for this database may be incomplete"),
    ).toBeInTheDocument();
    expect(screen.getByText(note)).toBeInTheDocument();
    expect(screen.queryByText("No alerts are firing for this database")).not.toBeInTheDocument();
  });

  it("keeps the listed alerts and shows the note when the list was cut", async () => {
    const note = "Showing 50 of 60 alerts in spoke-a. Open monitoring for the rest.";
    serve({
      databaseId,
      environment: createMockAlertEnvironment({ total: 60, shown: 50, note }),
      alerts: [createMockAlert({ id: "a", summary: "orders-dev is down" })],
    });

    renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);

    expect(await screen.findByText("orders-dev is down")).toBeInTheDocument();
    expect(screen.getByText(note)).toBeInTheDocument();
  });

  it("shows the reason instead of an empty state when the environment is stale", async () => {
    serve({
      databaseId,
      environment: createMockAlertEnvironment({
        state: "stale",
        reason: "Alerts are not current in spoke-a because it has not reported in 12 minutes.",
        action: { kind: "view_environment", label: "View environment" },
      }),
      alerts: [],
    });

    renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);

    expect(await screen.findByRole("status")).toHaveTextContent("has not reported in 12 minutes");
    expect(screen.getByRole("link", { name: "View environment" })).toBeInTheDocument();
    expect(screen.queryByText("No alerts are firing for this database")).not.toBeInTheDocument();
    expect(screen.getByText("Alerts for this database are unknown")).toBeInTheDocument();
  });

  it("shows the reason when monitoring is off", async () => {
    serve({
      databaseId,
      environment: createMockAlertEnvironment({
        state: "off",
        reason: "Alerts are off in spoke-a because monitoring is not set up.",
        readAt: null,
      }),
      alerts: [],
    });

    renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);

    expect(await screen.findByRole("status")).toHaveTextContent("monitoring is not set up");
    expect(screen.queryByText("No alerts are firing for this database")).not.toBeInTheDocument();
  });

  it("keeps the alerts it has beside the banner when the environment is stale", async () => {
    serve({
      databaseId,
      environment: createMockAlertEnvironment({ state: "stale", reason: "Not current." }),
      alerts: [createMockAlert()],
    });

    renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);

    expect(await screen.findByText("orders-dev is down")).toBeInTheDocument();
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("offers a retry when the alerts cannot be read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          jsonResponse({ code: "ErrInternal", message: "The alerts are unavailable." }, 500),
        ),
      ),
    );

    renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);

    expect(await screen.findByText("Alerts could not be read")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(screen.queryByText("No alerts are firing for this database")).not.toBeInTheDocument();
  });

  it("keeps the cached alerts and says so when a later read fails", async () => {
    const report: DatabaseAlertReport = {
      databaseId,
      environment: createMockAlertEnvironment(),
      alerts: [createMockAlert({ summary: "orders-dev is down" })],
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(report))
      .mockResolvedValue(jsonResponse({ code: "ErrInternal", message: "Unavailable." }, 500));
    vi.stubGlobal("fetch", withSession(fetchMock));

    const { queryClient } = renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);
    expect(await screen.findByText("orders-dev is down")).toBeInTheDocument();

    await queryClient.invalidateQueries();

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole("status")).toHaveTextContent("could not be refreshed");
    expect(screen.getByText("orders-dev is down")).toBeInTheDocument();
    expect(screen.queryByText("Alerts could not be read")).not.toBeInTheDocument();
  });

  it("does not claim a recent reading when a later read fails on an empty list", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        jsonResponse({ databaseId, environment: createMockAlertEnvironment(), alerts: [] }),
      )
      .mockResolvedValue(jsonResponse({ code: "ErrInternal", message: "Unavailable." }, 500));
    vi.stubGlobal("fetch", withSession(fetchMock));

    const { queryClient } = renderWithProviders(<DatabaseAlertsTab databaseId={databaseId} />);
    expect(await screen.findByText("No alerts are firing for this database")).toBeInTheDocument();

    await queryClient.invalidateQueries();

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole("status")).toHaveTextContent("could not be refreshed");
    expect(screen.getByText("No alerts were firing at the last reading")).toBeInTheDocument();
    expect(screen.queryByText("No alerts are firing for this database")).not.toBeInTheDocument();
    expect(screen.queryByText(/reported its monitoring recently/)).not.toBeInTheDocument();
  });
});
