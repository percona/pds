import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TicketList } from "@/features/support";
import type { SupportTicket } from "@/features/support";
import { renderWithProviders } from "@/test/utils";

const openTicket: SupportTicket = {
  number: "CS0061234",
  id: "6f1c0b1a4f8b4a10",
  state: "In progress",
  priority: "3 - Moderate",
  subject: "Cannot create database orders-prod",
  openedAt: "2026-08-25T11:00:00Z",
  updatedAt: "2026-08-25T11:45:00Z",
};

const waitingTicket: SupportTicket = {
  number: "CS0061190",
  state: "Awaiting customer",
  subject: "Replication lag on orders-prod",
  openedAt: "2026-08-24T09:00:00Z",
  updatedAt: "2026-08-24T18:00:00Z",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubTickets(response: Response) {
  const fetchMock = vi.fn().mockResolvedValue(response);
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("TicketList", () => {
  it("lists a row per ticket, each linking to the ticket", async () => {
    stubTickets(jsonResponse({ tickets: [openTicket, waitingTicket] }));
    renderWithProviders(<TicketList />);

    expect(await screen.findByTestId("ticket-list")).toBeInTheDocument();
    expect(screen.getByTestId("ticket-row-CS0061234")).toHaveTextContent(
      "Cannot create database orders-prod",
    );
    expect(screen.getByRole("link", { name: "CS0061234" })).toHaveAttribute(
      "href",
      "/support/CS0061234",
    );
    expect(screen.getByTestId("ticket-row-CS0061190")).toHaveTextContent("Awaiting customer");
  });

  // There is no paging on this page, so a ticket past the page it asks for is
  // one nobody can reach.
  it("asks for as many tickets as the adapter will answer with", async () => {
    const fetchMock = stubTickets(jsonResponse({ tickets: [openTicket] }));
    renderWithProviders(<TicketList />);

    expect(await screen.findByTestId("ticket-row-CS0061234")).toBeInTheDocument();

    const [target] = fetchMock.mock.calls[0] as [string];
    expect(new URL(target, "http://localhost").searchParams.get("limit")).toBe("100");
  });

  it("says the account has no tickets rather than showing an empty table", async () => {
    stubTickets(jsonResponse({ tickets: [] }));
    renderWithProviders(<TicketList />);

    expect(await screen.findByText("No tickets on this account")).toBeInTheDocument();
    expect(screen.queryByTestId("ticket-list")).not.toBeInTheDocument();
  });

  // The adapter answers 501 when it has no ServiceNow Secret. That is a
  // deployment fact, so the page states it and points at where it is fixed.
  it("points at Connectors when the adapter has no ServiceNow Secret", async () => {
    stubTickets(
      jsonResponse(
        { code: "ErrNotImplemented", message: "Support tickets are not configured." },
        501,
      ),
    );
    renderWithProviders(<TicketList />);

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

  it("offers a retry when the read failed for a reason that may pass", async () => {
    stubTickets(
      jsonResponse({ code: "ErrDownstream", message: "Support is unreachable right now." }, 502),
    );
    renderWithProviders(<TicketList />);

    expect(await screen.findByText("Tickets could not be read")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
