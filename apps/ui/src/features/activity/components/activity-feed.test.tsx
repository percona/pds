import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMockActivity, createMockActivityEntry, renderWithProviders } from "@/test/utils";
import { activityPollMs } from "../hooks/use-activity";
import { ActivityFeed } from "./activity-feed";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** The URLs the feed asked for, newest last. */
function requestedUrls(fetchMock: ReturnType<typeof vi.fn>): string[] {
  return fetchMock.mock.calls.map(([url]) => String(url));
}

/** The router is in memory, so the current path needs somewhere to show up. */
function LocationProbe() {
  return <span data-testid="location">{useLocation().pathname}</span>;
}

function renderFeed(props: Partial<Parameters<typeof ActivityFeed>[0]> = {}) {
  return renderWithProviders(
    <>
      <ActivityFeed
        live={props.live ?? true}
        onLiveChange={props.onLiveChange ?? vi.fn()}
        {...props}
      />
      <LocationProbe />
    </>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

/** Steps past one poll interval without making the suite wait for it. */
async function tickOnePoll() {
  await vi.advanceTimersByTimeAsync(activityPollMs + 100);
}

describe("ActivityFeed", () => {
  it("shows a skeleton shaped like the rows while it loads", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );

    renderFeed();

    expect(screen.getByTestId("activity-loading")).toBeInTheDocument();
  });

  it("names the Team under the target when the feed covers every Team", async () => {
    const page = createMockActivity();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          ...page,
          entries: page.entries.map((entry, index) =>
            index === 0 ? { ...entry, team: "Payments" } : entry,
          ),
        }),
      ),
    );

    renderFeed();

    const rows = await screen.findAllByTestId("activity-row");
    expect(within(rows[0] as HTMLElement).getByTestId("activity-team")).toHaveTextContent(
      "Payments",
    );
    expect(within(rows[1] as HTMLElement).queryByTestId("activity-team")).not.toBeInTheDocument();
  });

  it("renders a row per mutation with its time, action, target, actor, and outcome", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivity())));

    renderFeed();

    const rows = await screen.findAllByTestId("activity-row");
    expect(rows).toHaveLength(3);

    const first = within(rows[0] as HTMLElement);
    expect(first.getByText("Scale database")).toBeInTheDocument();
    expect(first.getByText("orders-dev")).toBeInTheDocument();
    expect(first.getByText("unknown")).toBeInTheDocument();
    // A scale is handed to the operator, so the row reports what it knows.
    expect(first.getByTestId("activity-outcome")).toHaveTextContent("Accepted");
    expect(first.getByTestId("activity-detail")).toHaveTextContent("replicas 3");
  });

  it("shows the client under the actor when the entry has one", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          entries: [
            createMockActivityEntry({ id: "000002", actor: "acme", client: "mcp" }),
            createMockActivityEntry({ id: "000001", actor: "acme" }),
          ],
          cursor: "000002",
          truncated: false,
        }),
      ),
    );

    renderFeed();

    const rows = await screen.findAllByTestId("activity-row");
    const agent = within(rows[0] as HTMLElement);
    expect(agent.getByText("acme")).toBeInTheDocument();
    expect(agent.getByTestId("activity-client")).toHaveTextContent("MCP server");
    expect(within(rows[1] as HTMLElement).queryByTestId("activity-client")).not.toBeInTheDocument();
  });

  it("shows the Chat and the agent as text next to the actor", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          entries: [
            createMockActivityEntry({
              id: "000003",
              actor: "acme",
              client: "chat",
              agent: "Anthropic",
            }),
            createMockActivityEntry({
              id: "000002",
              actor: "acme",
              client: "mcp",
              agent: "Claude Code",
            }),
            createMockActivityEntry({ id: "000001", actor: "acme" }),
          ],
          cursor: "000003",
          truncated: false,
        }),
      ),
    );

    renderFeed();

    const rows = await screen.findAllByTestId("activity-row");
    expect(within(rows[0] as HTMLElement).getByTestId("activity-client")).toHaveTextContent(
      "Chat (Anthropic)",
    );
    expect(within(rows[1] as HTMLElement).getByTestId("activity-client")).toHaveTextContent(
      "MCP server (Claude Code)",
    );
    expect(within(rows[2] as HTMLElement).queryByTestId("activity-client")).not.toBeInTheDocument();
  });

  it("keeps five columns, so the detail rides under the action", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivity())));

    renderFeed();

    await screen.findByTestId("activity-feed");
    expect(screen.getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual([
      "Time",
      "Action",
      "Target",
      "Actor",
      "Outcome",
    ]);
  });

  it("opens the full timeline for one change from its action", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivity())));

    renderFeed();

    await screen.findByTestId("activity-feed");
    const rows = screen.getAllByTestId("activity-row");
    expect(within(rows[0] as HTMLElement).getByTestId("activity-detail-link")).toHaveAttribute(
      "href",
      "/activity/000003",
    );
  });

  /**
   * The action label alone is a small target, and a row that opens something is
   * what a person clicks first. Both have to work.
   */
  it("opens the change from anywhere in the row", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivity())));

    renderFeed();

    await screen.findByTestId("activity-feed");
    const row = screen.getAllByTestId("activity-row")[0] as HTMLElement;
    await userEvent.click(within(row).getByText("orders-dev"));

    expect(screen.getByTestId("location")).toHaveTextContent("/activity/000003");
  });

  it("leaves a selection alone rather than navigating out of it", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivity())));

    renderFeed();

    await screen.findByTestId("activity-feed");
    const row = screen.getAllByTestId("activity-row")[0] as HTMLElement;

    // Someone dragging across a backup id to copy it is not asking to navigate.
    vi.spyOn(window, "getSelection").mockReturnValue({
      toString: () => "orders-dev",
    } as unknown as Selection);
    await userEvent.click(within(row).getByText("orders-dev"));

    expect(screen.getByTestId("location")).toHaveTextContent("/");
    expect(screen.getByTestId("location")).not.toHaveTextContent("/activity/000003");
  });

  it("renders the time in mono, because it is data", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivity())));

    renderFeed();

    const time = (await screen.findAllByTestId("activity-time"))[0] as HTMLElement;
    expect(time.className).toContain("font-mono");
  });

  it("shows the reason under a failed action rather than the detail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          createMockActivity({
            entries: [
              createMockActivityEntry({
                outcome: "failed",
                detail: "on demand",
                error: "bucket permissions rejected the write",
              }),
            ],
          }),
        ),
      ),
    );

    renderFeed();

    const row = (await screen.findAllByTestId("activity-row"))[0] as HTMLElement;
    expect(within(row).getByTestId("activity-outcome")).toHaveTextContent("Failed");
    expect(within(row).getByTestId("activity-detail")).toHaveTextContent(
      "bucket permissions rejected the write",
    );
  });

  it("composes an empty state rather than bare headers", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse({ entries: [], cursor: "", truncated: false })),
    );

    renderFeed();

    expect(await screen.findByText("Nothing has changed yet")).toBeInTheDocument();
    expect(screen.queryByTestId("activity-feed")).not.toBeInTheDocument();
  });

  it("says the log is off when the adapter answers 501", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse(
            { code: "ErrNotImplemented", message: "The activity log is not enabled." },
            501,
          ),
        ),
    );

    renderFeed();

    expect(await screen.findByText("The activity log is not enabled")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });

  it("offers a retry when the read fails", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        jsonResponse(
          { code: "ErrDownstream", message: "The activity log could not be read." },
          502,
        ),
      )
      .mockResolvedValue(jsonResponse(createMockActivity()));
    vi.stubGlobal("fetch", fetchMock);

    renderFeed();

    const retry = await screen.findByRole("button", { name: "Try again" });
    await userEvent.click(retry);

    expect(await screen.findAllByTestId("activity-row")).toHaveLength(3);
  });

  it("scopes the read to one database when it is given one", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(createMockActivity()));
    vi.stubGlobal("fetch", fetchMock);

    renderFeed({ databaseId: "pds.orders-dev" });

    await screen.findByTestId("activity-feed");
    expect(requestedUrls(fetchMock)[0]).toContain("databaseId=pds.orders-dev");
  });

  // The whole point of the cursor: a poll asks only for what it has not seen.
  it("polls with the cursor from the previous answer", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(createMockActivity()));
    vi.stubGlobal("fetch", fetchMock);

    renderFeed();

    await screen.findByTestId("activity-feed");
    await tickOnePoll();
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(1));

    expect(requestedUrls(fetchMock)[0]).not.toContain("since=");
    expect(requestedUrls(fetchMock)[1]).toContain("since=000003");
  });

  it("keeps rows on screen when a poll returns nothing new", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(createMockActivity()))
      .mockResolvedValue(jsonResponse({ entries: [], cursor: "000003", truncated: false }));
    vi.stubGlobal("fetch", fetchMock);

    renderFeed();

    await screen.findByTestId("activity-feed");
    await tickOnePoll();
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(1));

    expect(screen.getAllByTestId("activity-row")).toHaveLength(3);
  });

  it("adds a row that arrives on a later poll, newest first", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(createMockActivity()))
      .mockResolvedValue(
        jsonResponse({
          entries: [createMockActivityEntry({ id: "000004", action: "database.delete" })],
          cursor: "000004",
          truncated: false,
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    renderFeed();

    await screen.findByTestId("activity-feed");
    await tickOnePoll();
    await waitFor(() => expect(screen.getAllByTestId("activity-row")).toHaveLength(4));

    const rows = screen.getAllByTestId("activity-row");
    expect(within(rows[0] as HTMLElement).getByText("Delete database")).toBeInTheDocument();
  });

  it("marks the live toggle pressed and hands a change back", async () => {
    const onLiveChange = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivity())));

    renderFeed({ onLiveChange });

    const toggle = await screen.findByRole("button", { name: "Live" });
    expect(toggle).toHaveAttribute("aria-pressed", "true");

    await userEvent.click(toggle);
    expect(onLiveChange).toHaveBeenCalledWith(false);
  });

  it("stops polling while it is paused", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(createMockActivity()));
    vi.stubGlobal("fetch", fetchMock);

    renderFeed({ live: false });

    await screen.findByTestId("activity-feed");
    expect(screen.getByRole("button", { name: "Live" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText("Paused. New changes are not shown.")).toBeInTheDocument();

    const afterFirst = fetchMock.mock.calls.length;
    await new Promise((resolve) => setTimeout(resolve, 120));
    expect(fetchMock.mock.calls.length).toBe(afterFirst);
  });
});
