import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMockActivity, createMockActivityEntry, renderWithProviders } from "@/test/utils";
import { ActivityPage } from "./activity";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function Probe() {
  const location = useLocation();

  return <span data-testid="url">{location.pathname + location.search}</span>;
}

const entries = [
  createMockActivityEntry({
    id: "000004",
    action: "database.delete",
    databaseId: "spoke-a/pds.billing",
    target: "billing",
    outcome: "failed",
    error: "The database is still restoring.",
  }),
  createMockActivityEntry({
    id: "000003",
    action: "database.scale",
    databaseId: "spoke-a/pds.orders-dev",
    client: "chat",
    agent: "Anthropic",
  }),
  createMockActivityEntry({
    id: "000002",
    action: "connection.update",
    databaseId: undefined,
    target: "spoke-a",
    client: "mcp",
    agent: "Cursor",
  }),
  createMockActivityEntry({
    id: "000001",
    action: "database.backup",
    databaseId: "spoke-a/pds.orders-dev",
  }),
];

/**
 * Answers the unfiltered read with every entry and a database read with only
 * that database's, the way the control plane does.
 */
function stubActivity() {
  const fetchMock = vi.fn((url: string) => {
    const database = new URL(url, "http://localhost").searchParams.get("databaseId");
    const shown = database ? entries.filter((entry) => entry.databaseId === database) : entries;

    return Promise.resolve(jsonResponse(createMockActivity({ entries: shown })));
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function renderPage(route = "/activity") {
  return renderWithProviders(
    <>
      <ActivityPage />
      <Probe />
    </>,
    { route },
  );
}

function actionLabels(): string[] {
  return screen
    .getAllByTestId("activity-detail-link")
    .map((link) => link.textContent?.trim() ?? "");
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ActivityPage", () => {
  it("lists every loaded change newest first with a count", async () => {
    stubActivity();
    renderPage();

    await screen.findAllByTestId("activity-row");
    expect(actionLabels()).toEqual([
      "Delete database",
      "Scale database",
      "Update connection",
      "Back up",
    ]);
    expect(screen.getByTestId("activity-count")).toHaveTextContent(
      "Showing 4 of 4 loaded changes.",
    );
  });

  it("narrows the loaded changes by action without asking the control plane", async () => {
    const fetchMock = stubActivity();
    const user = userEvent.setup();
    renderPage();
    await screen.findAllByTestId("activity-row");
    const calls = fetchMock.mock.calls.length;

    await user.selectOptions(screen.getByLabelText("Action"), "database.scale");

    expect(actionLabels()).toEqual(["Scale database"]);
    expect(screen.getByTestId("activity-count")).toHaveTextContent(
      "Showing 1 of 4 loaded changes.",
    );
    expect(screen.getByTestId("url")).toHaveTextContent("action=database.scale");
    expect(fetchMock.mock.calls.length).toBe(calls);
  });

  it("matches the outcome a row shows, so an older backup reads as accepted", async () => {
    stubActivity();
    const user = userEvent.setup();
    renderPage();
    await screen.findAllByTestId("activity-row");

    await user.selectOptions(screen.getByLabelText("Outcome"), "accepted");

    expect(actionLabels()).toEqual(["Scale database", "Back up"]);
  });

  it("narrows by who made the change", async () => {
    stubActivity();
    const user = userEvent.setup();
    renderPage();
    await screen.findAllByTestId("activity-row");

    await user.selectOptions(screen.getByLabelText("Made by"), "mcp");
    expect(actionLabels()).toEqual(["Update connection"]);

    await user.selectOptions(screen.getByLabelText("Made by"), "people");
    expect(actionLabels()).toEqual(["Delete database", "Back up"]);
  });

  it("asks the control plane for one database's changes", async () => {
    const fetchMock = stubActivity();
    const user = userEvent.setup();
    renderPage();
    await screen.findAllByTestId("activity-row");

    await user.selectOptions(screen.getByLabelText("Database"), "spoke-a/pds.orders-dev");

    await waitFor(() =>
      expect(fetchMock.mock.calls.map(([url]) => String(url))).toContainEqual(
        expect.stringContaining("databaseId=spoke-a%2Fpds.orders-dev"),
      ),
    );
    await waitFor(() => expect(actionLabels()).toEqual(["Scale database", "Back up"]));
    // The other database stays a choice after the read narrowed.
    expect(
      within(screen.getByLabelText("Database")).getByRole("option", {
        name: "spoke-a/pds.billing",
      }),
    ).toBeInTheDocument();
  });

  it("offers to clear filters that match nothing", async () => {
    stubActivity();
    const user = userEvent.setup();
    renderPage("/activity?action=database.scale&by=people");

    expect(await screen.findByText("No changes match these filters")).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "Clear filters" })[0] as HTMLElement);

    expect(screen.getByTestId("url")).toHaveTextContent(/^\/activity$/);
    expect(await screen.findAllByTestId("activity-row")).toHaveLength(4);
  });

  it("keeps the Live switch", async () => {
    stubActivity();
    const user = userEvent.setup();
    renderPage();
    await screen.findAllByTestId("activity-row");

    const live = screen.getByRole("button", { name: "Live" });
    expect(live).toHaveAttribute("aria-pressed", "true");
    await user.click(live);
    expect(live).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText("Paused. New changes are not shown.")).toBeInTheDocument();
  });
});
