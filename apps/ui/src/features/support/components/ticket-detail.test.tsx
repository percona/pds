import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TicketDetail } from "@/features/support";
import type { SupportTicket } from "@/features/support";
import { renderWithProviders } from "@/test/utils";

const ticket: SupportTicket = {
  number: "CS0061234",
  id: "6f1c0b1a4f8b4a10",
  state: "In progress",
  priority: "3 - Moderate",
  account: "Acme Bank",
  contact: "platform@acme.test",
  subject: "Cannot create database orders-prod",
  description: "Database: orders-prod\nProfile: prod\nReason: insufficient memory",
  technology: "MySQL",
  openedAt: "2026-08-25T11:00:00Z",
  updatedAt: "2026-08-25T11:45:00Z",
  portalUrl: "https://example.service-now.com/percona",
  externalUrl: "https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
  comments: [
    {
      at: "2026-08-25T11:20:00Z",
      author: "Percona support",
      label: "Additional comments",
      body: "Looking at the pod events.",
    },
    { at: "2026-08-25T11:45:00Z", author: "platform@acme.test", body: "Thanks, standing by." },
  ],
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubTicket(response: Response) {
  const fetchMock = vi.fn().mockResolvedValue(response);
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function renderDetail(number = "CS0061234") {
  return renderWithProviders(<TicketDetail ticketNumber={number} />, {
    route: `/support/${number}`,
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("TicketDetail", () => {
  it("reads the ticket the route asked for", async () => {
    const fetchMock = stubTicket(jsonResponse(ticket));
    renderDetail();

    expect(await screen.findByTestId("ticket-detail")).toBeInTheDocument();
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/support/tickets/CS0061234");
  });

  it("states the number, the subject, and where the ticket stands", async () => {
    stubTicket(jsonResponse(ticket));
    renderDetail();

    const detail = await screen.findByTestId("ticket-detail");
    expect(detail).toHaveTextContent("CS0061234");
    expect(detail).toHaveTextContent("Cannot create database orders-prod");
    expect(screen.getByTestId("ticket-state")).toHaveTextContent("In progress");
    expect(detail).toHaveTextContent("Acme Bank");
    expect(screen.getByTestId("ticket-description")).toHaveTextContent(
      "Reason: insufficient memory",
    );
  });

  it("renders each comment with its author", async () => {
    stubTicket(jsonResponse(ticket));
    renderDetail();

    const comments = await screen.findByTestId("ticket-comments");
    expect(screen.getAllByTestId("ticket-comment")).toHaveLength(2);
    expect(comments).toHaveTextContent("Looking at the pod events.");
    expect(comments).toHaveTextContent("platform@acme.test");
    expect(screen.getByTestId("ticket-comment-label")).toHaveTextContent("Additional comments");
    expect(screen.getAllByTestId("ticket-comment-body")[0]).toHaveTextContent(
      "Looking at the pod events.",
    );
  });

  it("says nobody has commented yet rather than showing an empty list", async () => {
    stubTicket(jsonResponse({ ...ticket, comments: [] }));
    renderDetail();

    expect(await screen.findByText("No comments yet")).toBeInTheDocument();
    expect(screen.queryByTestId("ticket-comments")).not.toBeInTheDocument();
  });

  it("omits the comment timestamp tooltip when the date cannot be formatted", async () => {
    stubTicket(
      jsonResponse({
        ...ticket,
        comments: [{ at: "not-a-date", author: "Percona support", body: "Reply text." }],
      }),
    );
    renderDetail();

    const comment = await screen.findByTestId("ticket-comment");
    expect(comment.querySelector("time")).not.toHaveAttribute("title");
  });

  it("uses neutral metadata when a comment has no author", async () => {
    stubTicket(
      jsonResponse({
        ...ticket,
        comments: [{ body: "ServiceNow returned this comment without metadata." }],
      }),
    );
    renderDetail();

    const comment = await screen.findByTestId("ticket-comment");
    expect(comment).toHaveTextContent("Author unavailable");
    expect(comment).not.toHaveTextContent("Percona support");
  });

  // A ticket waiting on the customer closes itself, so the page says so while
  // the developer can still act on it.
  it("warns that a ticket awaiting the customer closes itself", async () => {
    stubTicket(jsonResponse({ ...ticket, state: "Awaiting customer" }));
    renderDetail();

    expect(await screen.findByTestId("ticket-awaiting-customer")).toHaveTextContent(
      "A ticket with no reply closes after 7 days.",
    );
  });

  it("links to the ticket in the portal", async () => {
    stubTicket(jsonResponse(ticket));
    renderDetail();

    expect(await screen.findByRole("link", { name: "Open in ServiceNow" })).toHaveAttribute(
      "href",
      "https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
    );
  });

  it("labels a portal fallback accurately", async () => {
    stubTicket(jsonResponse({ ...ticket, externalUrl: undefined }));
    renderDetail();

    expect(await screen.findByRole("link", { name: "Open in the support portal" })).toHaveAttribute(
      "href",
      "https://example.service-now.com/percona",
    );
    expect(screen.queryByRole("link", { name: "Open in ServiceNow" })).not.toBeInTheDocument();
  });

  it("points at Connectors when the adapter has no ServiceNow Secret", async () => {
    stubTicket(
      jsonResponse(
        { code: "ErrNotImplemented", message: "Support tickets are not configured." },
        501,
      ),
    );
    renderDetail();

    expect(await screen.findByText("Support is not connected")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open Connectors" })).toHaveAttribute(
      "href",
      "/connectors",
    );
    expect(screen.getByRole("link", { name: "Open the support portal" })).toHaveAttribute(
      "href",
      "https://www.percona.com/services/support",
    );
  });

  it("offers a retry when the ticket could not be read", async () => {
    stubTicket(jsonResponse({ code: "ErrNotFound", message: "No ticket CS0061234." }, 404));
    renderDetail();

    expect(await screen.findByText("This ticket could not be read")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("posts a reply and refetches a lowercase ticket route", async () => {
    const routeNumber = "cs0061234";
    const fetchMock = vi.fn().mockImplementation((_: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        return Promise.resolve(new Response(null, { status: 204 }));
      }

      return Promise.resolve(jsonResponse(ticket));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderDetail(routeNumber);

    await screen.findByTestId("ticket-detail");
    await userEvent.type(screen.getByLabelText("Reply"), "The operator events are attached.");
    await userEvent.click(screen.getByRole("button", { name: "Send reply" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/support/tickets/CS0061234/replies",
        expect.objectContaining({ method: "POST" }),
      ),
    );
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(
          ([url, init]) =>
            url === `/api/support/tickets/${routeNumber}` &&
            (init as RequestInit | undefined)?.method !== "POST",
        ),
      ).toHaveLength(2),
    );
    expect(screen.getByLabelText("Reply")).toHaveValue("");
  });

  it("keeps the reply draft when sending fails", async () => {
    const fetchMock = vi.fn().mockImplementation((_: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        return Promise.resolve(
          jsonResponse({ code: "ErrDownstream", message: "Percona support did not answer." }, 502),
        );
      }

      return Promise.resolve(jsonResponse(ticket));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderDetail();

    const message = "The operator events are attached.";
    await screen.findByTestId("ticket-detail");
    await userEvent.type(screen.getByLabelText("Reply"), message);
    await userEvent.click(screen.getByRole("button", { name: "Send reply" }));

    expect(await screen.findByTestId("ticket-reply-error")).toHaveTextContent(
      "Percona support did not answer.",
    );
    expect(screen.getByLabelText("Reply")).toHaveValue(message);
  });
});
