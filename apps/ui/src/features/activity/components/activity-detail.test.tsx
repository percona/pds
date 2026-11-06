import { screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createMockActivityDetail,
  createMockActivityEntry,
  renderWithProviders,
} from "@/test/utils";
import { activityDetailPollMs } from "../hooks/use-activity-detail";
import { ActivityDetailView } from "./activity-detail";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("ActivityDetailView", () => {
  it("says which client made the request when the entry has one", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          createMockActivityDetail({
            event: createMockActivityEntry({ actor: "acme", client: "mcp" }),
          }),
        ),
      ),
    );

    renderWithProviders(<ActivityDetailView id="000001" />);

    await screen.findByTestId("activity-detail");
    expect(screen.getByText("Client")).toBeInTheDocument();
    expect(screen.getByText("MCP server")).toBeInTheDocument();
  });

  it("says which agent made the request when the entry has one", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          createMockActivityDetail({
            event: createMockActivityEntry({ actor: "acme", client: "chat", agent: "Anthropic" }),
          }),
        ),
      ),
    );

    renderWithProviders(<ActivityDetailView id="000001" />);

    await screen.findByTestId("activity-detail");
    expect(screen.getByText("Client")).toBeInTheDocument();
    expect(screen.getByText("Chat")).toBeInTheDocument();
    expect(screen.getByText("Agent")).toBeInTheDocument();
    expect(screen.getByText("Anthropic")).toBeInTheDocument();
  });

  it("shows no agent fact when the entry has none", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          createMockActivityDetail({
            event: createMockActivityEntry({ actor: "acme", client: "mcp" }),
          }),
        ),
      ),
    );

    renderWithProviders(<ActivityDetailView id="000001" />);

    await screen.findByTestId("activity-detail");
    expect(screen.queryByText("Agent")).not.toBeInTheDocument();
  });

  it("shows no client fact when the entry has none", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivityDetail())));

    renderWithProviders(<ActivityDetailView id="000001" />);

    await screen.findByTestId("activity-detail");
    expect(screen.queryByText("Client")).not.toBeInTheDocument();
  });

  it("shows a skeleton while it loads", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );

    renderWithProviders(<ActivityDetailView id="000001" />);

    expect(screen.getByTestId("activity-detail-loading")).toBeInTheDocument();
  });

  it("reads the entry by id", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(createMockActivityDetail()));
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<ActivityDetailView id="000042" />);

    await screen.findByTestId("activity-detail");
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/activity/000042");
  });

  it("opens the timeline with the request and follows it with what the cluster saw", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(createMockActivityDetail())));

    renderWithProviders(<ActivityDetailView id="000001" />);

    await screen.findByTestId("activity-timeline");
    const rows = screen.getAllByTestId("timeline-row");
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveAttribute("data-source", "request");
    expect(within(rows[0] as HTMLElement).getByText("Requested")).toBeInTheDocument();
    expect(rows[1]).toHaveAttribute("data-source", "cluster");
    expect(within(rows[1] as HTMLElement).getByText(/assigned to node-2/)).toBeInTheDocument();
  });

  /**
   * The case the feed alone cannot show. The row says succeeded because the
   * request was accepted; the backup died four minutes later.
   */
  it("shows an operation that failed after the request succeeded", async () => {
    const detail = createMockActivityDetail({
      resource: {
        kind: "backup",
        name: "orders-dev-20260825120000",
        state: "failed",
        message: "exit code 137, agent terminated",
      },
      timeline: [
        {
          at: "2026-08-25T12:00:00Z",
          source: "request",
          type: "normal",
          reason: "Requested",
          message: "on demand",
        },
        {
          at: "2026-08-25T12:01:41Z",
          source: "cluster",
          type: "warning",
          reason: "BackOff",
          message: "Pod/orders-dev-backup Back-off restarting failed container",
          count: 3,
        },
      ],
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail)));

    renderWithProviders(<ActivityDetailView id="000001" />);

    await screen.findByTestId("activity-detail");
    // A backup is asynchronous, so the request was only accepted. The pill is
    // what carries the outcome of the work the request started.
    expect(screen.getByTestId("detail-outcome")).toHaveTextContent("Request accepted");
    expect(screen.getByTestId("status-pill")).toHaveTextContent("Failed");
    expect(screen.getByTestId("detail-message")).toHaveTextContent("exit code 137");

    const warning = screen.getAllByTestId("timeline-row")[1];
    expect(warning).toHaveAttribute("data-type", "warning");
    expect(within(warning as HTMLElement).getByTestId("timeline-count")).toHaveTextContent(
      "3 times",
    );
  });

  it("states a source it could not read rather than leaving a gap", async () => {
    const detail = createMockActivityDetail({
      notes: ["Cluster events are not available on this adapter."],
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(detail)));

    renderWithProviders(<ActivityDetailView id="000001" />);

    expect(await screen.findByTestId("detail-note")).toHaveTextContent(
      "Cluster events are not available",
    );
  });

  it("keeps polling while the operation is still running", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const running = createMockActivityDetail({
      resource: {
        kind: "instance",
        name: "orders-dev",
        state: "provisioning",
      },
    });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(running));
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<ActivityDetailView id="000001" />);

    await screen.findByTestId("activity-detail");
    const before = fetchMock.mock.calls.length;

    await vi.advanceTimersByTimeAsync(activityDetailPollMs + 100);

    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(before));
  });

  it("stops polling once the operation has finished", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(createMockActivityDetail()));
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<ActivityDetailView id="000001" />);

    await screen.findByTestId("activity-detail");
    expect(screen.getByText("This operation has finished.")).toBeInTheDocument();
    const before = fetchMock.mock.calls.length;

    await vi.advanceTimersByTimeAsync(activityDetailPollMs * 3);

    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("offers a retry when the entry could not be read", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse(
            { code: "ErrNotFound", message: "That activity entry is not in the log." },
            404,
          ),
        ),
    );

    renderWithProviders(<ActivityDetailView id="000001" />);

    expect(await screen.findByText("This entry could not be read")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("says so when the adapter has no ring", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          {
            code: "ErrNotImplemented",
            message: "The activity log is not enabled on this adapter.",
          },
          501,
        ),
      ),
    );

    renderWithProviders(<ActivityDetailView id="000001" />);

    expect(await screen.findByText("The activity log is not enabled")).toBeInTheDocument();
  });
});
