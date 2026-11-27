import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AlertReport } from "@/features/alerts";
import { ChatUiProvider, useChatUi } from "@/features/chat";
import {
  createMockAlert,
  createMockAlertEnvironment,
  createMockAlertReport,
  renderWithProviders,
} from "@/test/utils";
import { AlertsPage } from "./alerts";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubAlerts(report: AlertReport | Response) {
  const fetchMock = vi.fn(() =>
    Promise.resolve(report instanceof Response ? report : jsonResponse(report)),
  );
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function Probe() {
  const location = useLocation();

  return <span data-testid="url">{location.pathname + location.search}</span>;
}

/** What Ask PDS was asked to open with, since the widget is not on this page. */
function ChatProbe() {
  const { open, pendingAsk } = useChatUi();

  return (
    <span data-testid="chat">
      {open ? "open" : "closed"} {pendingAsk?.question ?? ""} {pendingAsk?.databaseId ?? ""}
    </span>
  );
}

function renderPage(route = "/alerts", flags?: { chat: boolean }) {
  return renderWithProviders(
    <ChatUiProvider>
      <AlertsPage />
      <Probe />
      <ChatProbe />
    </ChatUiProvider>,
    { route, flags },
  );
}

const critical = createMockAlert({
  id: "a1",
  summary: "orders-dev is down",
  startsAt: "2026-10-13T09:00:00Z",
});
const warning = createMockAlert({
  id: "a2",
  environment: "spoke-b",
  databaseId: "spoke-b/pds.billing",
  rule: "disk_used",
  severity: "warning",
  summary: "billing disk is 91% full",
  action: { kind: "scale_storage", label: "Scale storage" },
});

const twoEnvironments = createMockAlertReport({
  alerts: [critical, warning],
  environments: [
    createMockAlertEnvironment({ name: "spoke-a", total: 1, shown: 1 }),
    createMockAlertEnvironment({ name: "spoke-b", total: 1, shown: 1 }),
  ],
});

/** The summaries in the order the rows appear: the first paragraph of a row. */
function rowSummaries(): string[] {
  return screen.getAllByRole("listitem").map((row) => row.querySelector("p")?.textContent ?? "");
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AlertsPage", () => {
  it("shows a skeleton while the first read is in flight", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );

    renderPage();

    expect(screen.getByTestId("alerts-loading")).toBeInTheDocument();
  });

  it("lists rows in the order the control plane sorted them", async () => {
    const info = createMockAlert({
      id: "a3",
      severity: "info",
      summary: "a note",
      action: { kind: "view_environment", label: "View environment" },
    });
    stubAlerts(createMockAlertReport({ alerts: [critical, warning, info] }));

    renderPage();

    await screen.findByText("orders-dev is down");
    expect(rowSummaries()).toEqual(["orders-dev is down", "billing disk is 91% full", "a note"]);
  });

  it("says each severity in text, not only in color", async () => {
    stubAlerts(twoEnvironments);

    renderPage();

    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]).getByText("Critical")).toBeInTheDocument();
    expect(within(rows[1]).getByText("Warning")).toBeInTheDocument();
  });

  it("narrows the list by severity and keeps the choice in the URL", async () => {
    stubAlerts(twoEnvironments);
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("orders-dev is down");
    await user.selectOptions(screen.getByLabelText("Severity"), "warning");

    expect(rowSummaries()).toEqual(["billing disk is 91% full"]);
    expect(screen.getByTestId("url")).toHaveTextContent("/alerts?severity=warning");
  });

  it("narrows the list by environment", async () => {
    stubAlerts(twoEnvironments);
    const user = userEvent.setup();

    renderPage();

    await screen.findByText("orders-dev is down");
    await user.selectOptions(screen.getByLabelText("Environment"), "spoke-a");

    expect(rowSummaries()).toEqual(["orders-dev is down"]);
    expect(screen.getByTestId("url")).toHaveTextContent("/alerts?environment=spoke-a");
  });

  it("restores the filters from the URL on a reload", async () => {
    stubAlerts(twoEnvironments);

    renderPage("/alerts?severity=critical&environment=spoke-a");

    await screen.findByText("orders-dev is down");
    expect(rowSummaries()).toEqual(["orders-dev is down"]);
    expect(screen.getByLabelText("Severity")).toHaveValue("critical");
    expect(screen.getByLabelText("Environment")).toHaveValue("spoke-a");
  });

  it("offers to clear filters that match nothing", async () => {
    stubAlerts(twoEnvironments);
    const user = userEvent.setup();

    renderPage("/alerts?severity=info");

    expect(await screen.findByText("No alerts match these filters")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear filters" }));

    expect(rowSummaries()).toHaveLength(2);
    expect(screen.getByTestId("url")).toHaveTextContent(/^\/alerts$/);
  });

  it("shows when each row was last read", async () => {
    // A fixed date drifts past the 30 day cutoff, where the age becomes a date.
    const readAt = new Date(Date.now() - 5 * 60_000).toISOString();
    stubAlerts(
      createMockAlertReport({
        alerts: [critical, warning],
        environments: [
          createMockAlertEnvironment({ name: "spoke-a", total: 1, shown: 1, readAt }),
          createMockAlertEnvironment({ name: "spoke-b", total: 1, shown: 1, readAt }),
        ],
      }),
    );

    renderPage();

    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]).getByText(/Read .* ago/)).toBeInTheDocument();
  });

  it("calls out a stale environment and does not read it as quiet", async () => {
    stubAlerts(
      createMockAlertReport({
        alerts: [],
        environments: [
          createMockAlertEnvironment({
            name: "spoke-a",
            state: "stale",
            reason: "Alerts are not current in spoke-a because it has not reported in 12 minutes.",
            action: { kind: "view_environment", label: "View environment" },
          }),
        ],
        criticals: 0,
      }),
    );

    renderPage();

    const banner = await screen.findByRole("status");
    expect(banner).toHaveTextContent("Alerts are not current in spoke-a");
    expect(within(banner).getByText("Stale")).toBeInTheDocument();
    expect(within(banner).getByRole("link", { name: "View environment" })).toHaveAttribute(
      "href",
      "/environments/spoke-a",
    );
    expect(screen.queryByText("No alerts are firing")).not.toBeInTheDocument();
  });

  it("calls out an environment with monitoring off and links its one action", async () => {
    stubAlerts(
      createMockAlertReport({
        alerts: [],
        environments: [
          createMockAlertEnvironment({
            name: "spoke-a",
            state: "off",
            pending: "not-set-up",
            reason: "Alerts are off in spoke-a because monitoring is not set up.",
            action: { kind: "set_up_monitoring", label: "Set up monitoring" },
            readAt: null,
          }),
        ],
        criticals: 0,
      }),
    );

    renderPage();

    const banner = await screen.findByRole("status");
    expect(within(banner).getByText("Off")).toBeInTheDocument();
    expect(within(banner).getByRole("link", { name: "Set up monitoring" })).toHaveAttribute(
      "href",
      "/environments/spoke-a",
    );
  });

  it("shows the empty state only when every environment is live and nothing fires", async () => {
    stubAlerts(createMockAlertReport({ alerts: [], criticals: 0 }));

    renderPage();

    expect(await screen.findByText("No alerts are firing")).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("offers to connect an environment when there are none", async () => {
    stubAlerts(createMockAlertReport({ alerts: [], environments: [], criticals: 0 }));

    renderPage();

    expect(await screen.findByText("No environments yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create an environment" })).toHaveAttribute(
      "href",
      "/environments/new",
    );
  });

  it("says what was cut when the control plane truncated an environment", async () => {
    stubAlerts(
      createMockAlertReport({
        alerts: [critical],
        environments: [
          createMockAlertEnvironment({
            total: 250,
            shown: 200,
            note: "Showing 200 of 250 alerts in spoke-a. Open monitoring for the rest.",
          }),
        ],
      }),
    );

    renderPage();

    expect(
      await screen.findByText(
        "Showing 200 of 250 alerts in spoke-a. Open monitoring for the rest.",
      ),
    ).toBeInTheDocument();
  });

  it("renders each rule's action, and an unmapped alert links to its environment", async () => {
    const unmapped = createMockAlert({
      id: "a4",
      databaseId: undefined,
      rule: "my_rule",
      severity: "warning",
      summary: "custom rule fired",
      action: { kind: "view_environment", label: "View environment" },
    });
    stubAlerts(createMockAlertReport({ alerts: [critical, warning, unmapped] }));

    renderPage();

    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]).getByRole("link", { name: "Open database" })).toHaveAttribute(
      "href",
      "/databases/spoke-a/pds.orders-dev",
    );
    expect(within(rows[1]).getByRole("link", { name: "Scale storage" })).toHaveAttribute(
      "href",
      "/databases/spoke-b/pds.billing",
    );
    expect(within(rows[2]).getByRole("link", { name: "View environment" })).toHaveAttribute(
      "href",
      "/environments/spoke-a",
    );
  });

  it("shows an inline error with a retry when the read fails", async () => {
    const fetchMock = stubAlerts(
      jsonResponse(
        { code: "ErrDownstream", message: "The control plane could not read alerts." },
        502,
      ),
    );
    const user = userEvent.setup();

    renderPage();

    expect(await screen.findByText("The control plane could not read alerts.")).toBeInTheDocument();
    fetchMock.mockImplementation(() => Promise.resolve(jsonResponse(twoEnvironments)));
    await user.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(screen.getByText("orders-dev is down")).toBeInTheDocument());
  });

  it("reads again every 30 seconds", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = stubAlerts(twoEnvironments);

    renderPage();
    await screen.findByText("orders-dev is down");
    const before = fetchMock.mock.calls.length;

    await vi.advanceTimersByTimeAsync(30_100);

    expect(fetchMock.mock.calls.length).toBeGreaterThan(before);
    vi.useRealTimers();
  });

  it("keeps relative times current between reads that return the same data", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const now = Date.now();
    const report = createMockAlertReport({
      alerts: [createMockAlert({ startsAt: new Date(now - 10_000).toISOString() })],
      environments: [
        createMockAlertEnvironment({ readAt: new Date(now - 10_000).toISOString() }),
        createMockAlertEnvironment({
          name: "spoke-b",
          state: "stale",
          reason: "Alerts are not current in spoke-b.",
          readAt: new Date(now - 10_000).toISOString(),
        }),
      ],
    });
    stubAlerts(report);

    renderPage();
    await screen.findByText("orders-dev is down");
    expect(screen.getByText("Firing since Just now")).toBeInTheDocument();
    expect(screen.getByText("Last read Just now")).toBeInTheDocument();

    await vi.advanceTimersByTimeAsync(5 * 60_000);

    // The last tick's render commits after the timers settle, so wait for it.
    expect(await screen.findByText("Firing since 5 minutes ago")).toBeInTheDocument();
    expect(await screen.findByText("Last read 5 minutes ago")).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("says a filtered environment has no current reading instead of no match", async () => {
    stubAlerts(
      createMockAlertReport({
        alerts: [critical],
        environments: [
          createMockAlertEnvironment({ name: "spoke-a" }),
          createMockAlertEnvironment({
            name: "spoke-b",
            state: "stale",
            reason: "Alerts are not current in spoke-b.",
          }),
        ],
      }),
    );

    renderPage("/alerts?environment=spoke-b");

    expect(await screen.findByText("spoke-b has no current reading")).toBeInTheDocument();
    expect(screen.queryByText("No alerts match these filters")).not.toBeInTheDocument();
  });

  it("ignores an environment in the URL that the report does not name", async () => {
    stubAlerts(twoEnvironments);

    renderPage("/alerts?environment=gone");

    await screen.findByText("orders-dev is down");
    expect(rowSummaries()).toHaveLength(2);
    expect(screen.getByLabelText("Environment")).toHaveValue("");
  });

  describe("the alert panel", () => {
    const labelled = createMockAlert({
      id: "a1",
      summary: "orders-dev is down",
      labels: { instance: "orders-dev-0", service: "mysql" },
    });

    async function openPanel() {
      stubAlerts(createMockAlertReport({ alerts: [labelled] }));
      const user = userEvent.setup();
      renderPage();
      await user.click(await screen.findByRole("button", { name: "orders-dev is down" }));

      return { user, panel: await screen.findByRole("dialog", { name: "orders-dev is down" }) };
    }

    it("opens from a row with the labels monitoring sent", async () => {
      const { panel } = await openPanel();

      const labels = within(panel).getByTestId("alert-labels");
      expect(labels).toHaveTextContent("instance");
      expect(labels).toHaveTextContent("orders-dev-0");
      expect(labels).toHaveTextContent("service");
      expect(within(panel).getByText("Critical")).toBeInTheDocument();
    });

    it("says what the alert's action does before it is followed", async () => {
      const { panel } = await openPanel();

      expect(within(panel).getByRole("link", { name: "Open database" })).toHaveAttribute(
        "href",
        "/databases/spoke-a/pds.orders-dev",
      );
      expect(within(panel).getByText("Opens the database.")).toBeInTheDocument();
    });

    it("closes on Escape and returns focus to the row", async () => {
      const { user } = await openPanel();

      await user.keyboard("{Escape}");

      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      await waitFor(() =>
        expect(screen.getByRole("button", { name: "orders-dev is down" })).toHaveFocus(),
      );
    });

    it("asks PDS about the alert in a new chat, with its database", async () => {
      const { user, panel } = await openPanel();

      await user.click(within(panel).getByRole("button", { name: "Ask PDS about this alert" }));

      expect(screen.getByTestId("chat")).toHaveTextContent(
        'open Why is spoke-a/pds.orders-dev showing "orders-dev is down"? spoke-a/pds.orders-dev',
      );
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it("hides Ask PDS when Chat is off", async () => {
      stubAlerts(createMockAlertReport({ alerts: [labelled] }));
      const user = userEvent.setup();
      renderPage("/alerts", { chat: false });
      await user.click(await screen.findByRole("button", { name: "orders-dev is down" }));

      const panel = await screen.findByRole("dialog", { name: "orders-dev is down" });
      expect(
        within(panel).queryByRole("button", { name: "Ask PDS about this alert" }),
      ).not.toBeInTheDocument();
    });

    it("opens a support case with the alert attached", async () => {
      const { user, panel } = await openPanel();

      await user.click(within(panel).getByRole("button", { name: "Contact support" }));

      const dialog = await screen.findByRole("dialog", { name: "Contact support" });
      expect(within(dialog).getByLabelText("Subject")).toHaveValue("orders-dev is down");
      const description = (within(dialog).getByLabelText("Description") as HTMLTextAreaElement)
        .value;
      expect(description).toContain("Database: spoke-a/pds.orders-dev\nAlert: orders-dev is down");
      expect(description).toContain("instance: orders-dev-0");
    });
  });
});
