import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { failureSettleMs } from "@/features/databases";
import { allMetricKinds, overviewMetricKinds } from "@/features/monitoring";
import { AppRoutes } from "@/routes";
import {
  createMockBackup,
  createMockDatabase,
  createMockMetrics,
  renderWithProviders,
} from "@/test/utils";

/** What the card renders. The real string only arrives on the copy click. */
const maskedUri = "mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev";
/** What the credential route answers with, and the only place it appears. */
const realUri = "mysql://app:secret@orders-dev.pds.svc:3306/orders-dev";

const ready = createMockDatabase({
  id: "spoke-a/pds.orders-dev",
  environment: "spoke-a",
  name: "orders-dev",
  profile: "prod",
  version: "8.4.3",
  status: "ready",
  members: 3,
  cpu: 4,
  memoryGb: 16,
  storageGb: 250,
  createdAt: "2026-08-20T09:30:00Z",
  connection: {
    host: "orders-dev.pds.svc",
    port: 3306,
    username: "app",
    maskedUri,
    secretName: "orders-dev-app-credentials",
  },
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * Routes the reads the detail page makes: the database, its metrics, and its
 * backups. Passing no metrics answers a pending snapshot, which is what a
 * environment whose monitoring has not registered the target looks like.
 */
function stubDatabase(
  database: unknown,
  metrics?: unknown,
  backups: unknown[] = [],
  environmentSnapshot?: unknown,
) {
  const started: unknown[] = [];

  const fetchMock = vi.fn((input: string, init?: RequestInit) => {
    const path = String(input);

    if (init?.method === "DELETE") {
      return Promise.resolve(new Response(null, { status: 204 }));
    }

    if (path.endsWith("/credential")) {
      return Promise.resolve(jsonResponse({ uri: realUri }));
    }

    if (path.includes("/metrics")) {
      return Promise.resolve(
        jsonResponse(
          metrics ??
            createMockMetrics(allMetricKinds, {
              pending: true,
              pendingReason: "PMM has no target for this database yet.",
            }),
        ),
      );
    }

    if (path.endsWith("/backups")) {
      return Promise.resolve(jsonResponse({ backups: [...started, ...backups] }));
    }

    if (path.endsWith("/restore") && init?.method === "POST") {
      return Promise.resolve(jsonResponse({ status: "accepted" }, 202));
    }

    if (path.startsWith("/api/support/tickets")) {
      return Promise.resolve(
        jsonResponse({ tickets: [], portalUrl: "https://example.service-now.com/percona" }),
      );
    }

    if (path === "/api/environments") {
      return Promise.resolve(jsonResponse(environmentSnapshot ?? { hub: false, environments: [] }));
    }

    if (path === "/api/connections") {
      return Promise.resolve(
        jsonResponse({ servicenow: { configured: false }, assistant: { configured: false } }),
      );
    }

    if (path.endsWith("/backup") && init?.method === "POST") {
      const backup = createMockBackup({
        id: "orders-dev-new",
        status: "pending",
        sizeBytes: 0,
        scheduled: false,
      });
      started.push(backup);

      return Promise.resolve(jsonResponse(backup, 202));
    }

    return Promise.resolve(jsonResponse(database));
  });

  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

const detailRoute = "/databases/spoke-a/pds.orders-dev";

beforeEach(() => {
  window.localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DatabaseDetailPage", () => {
  it("heads the page with the engine, name, status, and profile", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    expect(
      await screen.findByRole("heading", { level: 1, name: "orders-dev" }),
    ).toBeInTheDocument();

    const header = within(screen.getByTestId("database-header"));

    expect(header.getByText("MySQL 8.4.3")).toBeInTheDocument();
    expect(header.getByText("Ready")).toBeInTheDocument();
    expect(header.getByText("Prod")).toBeInTheDocument();
  });

  it("offers scale, back up, support, and delete in that order", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");

    const actions = within(screen.getByTestId("database-actions"))
      .getAllByRole("button")
      .map((button) => button.textContent);

    expect(actions).toEqual(["Scale", "Back up now", "Contact support", "Delete"]);
  });

  it("opens the support dialog from the header", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await userEvent.click(await screen.findByRole("button", { name: "Contact support" }));

    expect(await screen.findByRole("dialog", { name: "Contact support" })).toBeInTheDocument();
  });

  it("hides contact support in the header when support is not connected", async () => {
    const fetchMock = stubDatabase(ready);
    const originalFetch = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input: string, init?: RequestInit) => {
      if (String(input).startsWith("/api/support/tickets")) {
        return Promise.resolve(
          jsonResponse(
            { code: "ErrNotImplemented", message: "Support tickets are not configured." },
            501,
          ),
        );
      }

      return originalFetch?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");

    expect(screen.queryByRole("button", { name: "Contact support" })).not.toBeInTheDocument();
  });

  it("opens the scale dialog from the header", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");
    await userEvent.click(screen.getByRole("button", { name: "Scale" }));

    const dialog = within(await screen.findByRole("dialog"));

    expect(dialog.getByText("Scale orders-dev")).toBeInTheDocument();
    expect(dialog.getByLabelText("CPU cores")).toHaveValue("4");
    expect(dialog.getByRole("radio", { name: "3 members" })).toBeChecked();
  });

  it("opens the scale dialog when chat hands over a scale intent", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, {
      route: detailRoute,
      state: { chatIntent: { kind: "scale", field: "memory" } },
    });

    expect(await screen.findByRole("dialog", { name: "Scale orders-dev" })).toBeInTheDocument();
    expect(screen.getByLabelText("Memory in GB")).toHaveValue("16");
  });

  it("opens the Backups tab on a backups intent and sends no write", async () => {
    const fetchMock = stubDatabase(ready);

    renderWithProviders(<AppRoutes />, {
      route: detailRoute,
      state: { chatIntent: { kind: "backups" } },
    });

    expect(await screen.findByRole("tab", { name: "Backups", selected: true })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Back up now" })[0]).toBeEnabled();
    const writes = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(writes).toHaveLength(0);
  });

  it("shows the six tabs in their fixed order", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");

    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "Overview",
      "Backups",
      "Metrics",
      "Activity",
      "Alerts",
      "History",
      "Settings",
    ]);
  });

  // Reaching a new database is the first thing anyone does with it, so the
  // Overview opens on Connect from a laptop, with the commands on screen.
  it("leads the overview with connect, then the details, then the metrics", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    expect(await screen.findByRole("button", { name: "From your laptop" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    const blocks = screen.getByTestId("overview-blocks");
    expect(Array.from(blocks.children).map((block) => block.getAttribute("data-block"))).toEqual([
      "connect",
      "details",
      "metrics",
    ]);
    expect(screen.getByText(/This database runs on spoke-a/)).toBeInTheDocument();
    expect(screen.getByText("mysql -h 127.0.0.1 -P '3306' -u 'app' -p")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("shows the connection string masked from your app, and never the password", async () => {
    stubDatabase(ready);

    const { container } = renderWithProviders(<AppRoutes />, { route: detailRoute });

    await userEvent.click(await screen.findByRole("button", { name: "From your app" }));

    expect(screen.getByText(maskedUri)).toBeInTheDocument();
    expect(container.textContent).not.toContain(realUri);
  });

  it("sends ticket context with environment type from the support dialog", async () => {
    const fetchMock = stubDatabase(ready);

    const originalFetch = fetchMock.getMockImplementation();
    fetchMock.mockImplementation((input: string, init?: RequestInit) => {
      const path = String(input);

      if (path === "/api/support/tickets" && init?.method === "POST") {
        return Promise.resolve(
          jsonResponse({
            number: "CS0061234",
            portalUrl: "https://example.service-now.com/percona",
          }),
        );
      }

      if (path === "/api/support/tickets") {
        return Promise.resolve(
          jsonResponse({ tickets: [], portalUrl: "https://example.service-now.com/percona" }),
        );
      }

      return originalFetch?.(input, init) ?? Promise.resolve(jsonResponse({}));
    });

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await userEvent.click(await screen.findByRole("button", { name: "Contact support" }));
    await userEvent.click(screen.getByRole("button", { name: "Send ticket" }));

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([url, init]) => String(url) === "/api/support/tickets" && init?.method === "POST",
        ),
      ).toBe(true),
    );

    const [, init] = fetchMock.mock.calls.find(
      ([url, requestInit]) =>
        String(url) === "/api/support/tickets" && requestInit?.method === "POST",
    ) as [string, RequestInit];

    expect(JSON.parse(String(init.body))).toMatchObject({
      context: {
        database: "orders-dev",
        profile: "prod",
        engine: "mysql",
        engineVersion: "8.4.3",
        clusterType: "cluster",
        namespace: "pds",
        members: 3,
        status: "ready",
      },
    });
  });

  it("says metrics are pending rather than drawing an empty chart", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");

    expect(await screen.findAllByText("Metrics pending")).toHaveLength(3);
  });

  it("draws the overview minis once PMM reports the database", async () => {
    stubDatabase(ready, createMockMetrics(overviewMetricKinds));

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await waitFor(() =>
      expect(screen.getByTestId("metric-mini-cpu")).toHaveAttribute("data-state", "data"),
    );

    expect(screen.getByTestId("metric-mini-memory")).toBeInTheDocument();
    expect(screen.getByTestId("metric-mini-connections")).toBeInTheDocument();
  });

  it("opens the five charts on the metrics tab", async () => {
    stubDatabase(ready, createMockMetrics(allMetricKinds));

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");
    await userEvent.click(screen.getByRole("tab", { name: "Metrics" }));

    expect(await screen.findByTestId("metric-panel-cpu")).toBeInTheDocument();
    expect(screen.getAllByTestId(/^metric-panel-/)).toHaveLength(5);
    expect(screen.getByRole("group", { name: "Time window" })).toBeInTheDocument();
  });

  it("opens the backup list and the schedule on the backups tab", async () => {
    stubDatabase(ready, undefined, [
      createMockBackup({ id: "orders-dev-nightly", createdAt: "2026-08-25T02:00:00Z" }),
    ]);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");
    await userEvent.click(screen.getByRole("tab", { name: "Backups" }));

    expect(await screen.findByTestId("backup-row-orders-dev-nightly")).toBeInTheDocument();
    expect(screen.getByTestId("backup-schedule")).toHaveTextContent("On-demand backups");
  });

  it("starts a backup from the header and shows the new row", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");

    const backUpNow = screen.getByRole("button", { name: "Back up now" });
    expect(backUpNow).toBeEnabled();

    await userEvent.click(backUpNow);
    await userEvent.click(screen.getByRole("tab", { name: "Backups" }));

    expect(await screen.findByTestId("backup-row-orders-dev-new")).toBeInTheDocument();
  });

  it("restores a backup into a new database", async () => {
    const fetchMock = stubDatabase(ready, undefined, [
      createMockBackup({ id: "orders-dev-nightly", createdAt: "2026-08-25T02:00:00Z" }),
    ]);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");
    await userEvent.click(screen.getByRole("tab", { name: "Backups" }));

    const row = within(await screen.findByTestId("backup-row-orders-dev-nightly"));
    await userEvent.click(row.getByRole("button", { name: "Restore" }));

    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByTestId("restore-consequence")).toHaveTextContent(
      "This restores the backup into a new database on spoke-a. orders-dev is not touched.",
    );

    const confirm = dialog.getByRole("button", { name: "Restore" });
    await waitFor(() =>
      expect(dialog.getByLabelText("New database name")).toHaveValue("orders-de-restore-1"),
    );
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    const restored = fetchMock.mock.calls.find(
      ([path, init]) => String(path).endsWith("/restore") && init?.method === "POST",
    );
    expect(restored).toBeDefined();
    expect(JSON.parse(String(restored?.[1]?.body))).toEqual({
      backupId: "orders-dev-nightly",
      name: "orders-de-restore-1",
    });
  });

  it("disables backup and restore actions while the environment is unreachable", async () => {
    stubDatabase(
      { ...ready, id: "spoke-a/pds.orders-dev", environment: "spoke-a" },
      undefined,
      [createMockBackup({ id: "orders-dev-nightly", createdAt: "2026-08-25T02:00:00Z" })],
      {
        hub: true,
        environments: [
          { name: "spoke-a", status: "unreachable", lastSeen: "2026-09-08T12:00:00Z" },
        ],
      },
    );

    renderWithProviders(<AppRoutes />, {
      route: "/databases/spoke-a/pds.orders-dev",
    });

    expect(await screen.findByText("This environment is unreachable")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back up now" })).toBeDisabled();

    await userEvent.click(screen.getByRole("tab", { name: "Backups" }));
    expect(await screen.findByRole("button", { name: "Restore" })).toBeDisabled();
  });

  it("shows honest placeholders on the tabs later tasks fill", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");

    await userEvent.click(screen.getByRole("tab", { name: "Settings" }));
    expect(screen.getByTestId("settings-not-editable")).toBeInTheDocument();
  });

  it("deletes from the header once the name is typed, and leaves for the list", async () => {
    const fetchMock = stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");
    await userEvent.click(
      within(screen.getByTestId("database-actions")).getByRole("button", { name: "Delete" }),
    );

    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByTestId("delete-consequence")).toHaveTextContent(
      "This deletes orders-dev with its data and its backups, and there is no undo.",
    );

    const confirm = dialog.getByRole("button", { name: "Delete" });
    expect(confirm).toBeDisabled();

    await userEvent.type(dialog.getByLabelText("Database name"), "orders-dev");
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    expect(await screen.findByRole("heading", { level: 1, name: "Databases" })).toBeInTheDocument();

    const deleted = fetchMock.mock.calls.find(([, init]) => init?.method === "DELETE");
    expect(deleted?.[0]).toBe("/api/databases/spoke-a/pds.orders-dev");
  });

  it("opens the same dialog from the settings danger zone", async () => {
    stubDatabase(ready);

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    await screen.findByTestId("database-header");
    await userEvent.click(screen.getByRole("tab", { name: "Settings" }));
    await userEvent.click(
      within(screen.getByTestId("danger-zone")).getByRole("button", { name: "Delete" }),
    );

    expect(
      within(await screen.findByRole("dialog")).getByTestId("delete-consequence"),
    ).toBeVisible();
  });

  it("still shows the provisioning screen while a create is running", async () => {
    stubDatabase(
      createMockDatabase({
        id: "pds.orders-dev",
        name: "orders-dev",
        status: "provisioning",
        message: "Waiting for volume orders-dev-pvc to bind",
      }),
    );

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    expect(await screen.findByText("Usually 6 to 12 minutes")).toBeInTheDocument();
    expect(screen.queryByRole("tab")).not.toBeInTheDocument();
  });

  it("keeps the create on screen while a failure could still be the operator retrying", async () => {
    stubDatabase(
      createMockDatabase({
        id: "pds.orders-dev",
        name: "orders-dev",
        status: "failed",
        message: "smart update: failed to wait pxc sync",
      }),
    );

    renderWithProviders(<AppRoutes />, { route: detailRoute });

    expect(await screen.findByText("Usually 6 to 12 minutes")).toBeInTheDocument();
    expect(screen.queryByText(/failed\./)).not.toBeInTheDocument();
  });

  it("shows the failed create with its step and retry once the failure holds", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });

    try {
      stubDatabase(
        createMockDatabase({
          id: "pds.orders-dev",
          name: "orders-dev",
          status: "failed",
          message: "No node has enough memory for 3 members.",
        }),
      );

      renderWithProviders(<AppRoutes />, { route: detailRoute });

      expect(await screen.findByText("Usually 6 to 12 minutes")).toBeInTheDocument();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(failureSettleMs);
      });

      expect(await screen.findByText("Starting database failed.")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  // A PostgreSQL restore whose seed failed commits nothing and has no
  // connection, so this screen is the only place the developer can clear it.
  it("deletes a failed restore copy from the failed screen and leaves for the list", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });

    try {
      const fetchMock = stubDatabase(
        createMockDatabase({
          id: "spoke-a/pds.orders-restore-1",
          name: "orders-restore-1",
          engine: "postgresql",
          environment: "spoke-a",
          status: "failed",
          message:
            "The backup is missing or did not finish. Pick a finished backup and restore again.",
        }),
      );

      renderWithProviders(<AppRoutes />, { route: "/databases/spoke-a/pds.orders-restore-1" });

      await act(async () => {
        await vi.advanceTimersByTimeAsync(failureSettleMs);
      });
      await userEvent.click(await screen.findByRole("button", { name: "Delete" }));

      const dialog = within(await screen.findByRole("dialog"));
      await userEvent.type(dialog.getByLabelText("Database name"), "orders-restore-1");
      await userEvent.click(dialog.getByRole("button", { name: "Delete" }));

      expect(
        await screen.findByRole("heading", { level: 1, name: "Databases" }),
      ).toBeInTheDocument();
      const deleted = fetchMock.mock.calls.find(([, init]) => init?.method === "DELETE");
      expect(deleted?.[0]).toBe("/api/databases/spoke-a/pds.orders-restore-1");
    } finally {
      vi.useRealTimers();
    }
  });
});
