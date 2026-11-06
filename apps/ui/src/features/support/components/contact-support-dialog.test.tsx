import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ContactSupportDialog } from "@/features/support";
import { renderWithProviders } from "@/test/utils";

const prefilledDescription = "Profile: prod\nFailed step: Starting database";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** Answers each send in turn, so a retry can succeed where the first try failed. */
function stubSend(...responses: Response[]) {
  const fetchMock = vi.fn().mockImplementation(() => {
    return Promise.resolve(responses.shift() ?? jsonResponse({}));
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function renderDialog(overrides: Partial<Parameters<typeof ContactSupportDialog>[0]> = {}) {
  return renderWithProviders(
    <ContactSupportDialog
      open
      onOpenChange={vi.fn()}
      subject="Cannot create database orders-dev"
      description={prefilledDescription}
      {...overrides}
    />,
  );
}

function sendButton() {
  return screen.getByRole("button", { name: "Send ticket" });
}

function descriptionField() {
  return screen.getByLabelText<HTMLTextAreaElement>("Description");
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ContactSupportDialog", () => {
  it("opens pre-filled with the subject and the context", async () => {
    renderDialog();

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByLabelText("Subject")).toHaveValue("Cannot create database orders-dev");
    expect(descriptionField().value).toContain("Failed step: Starting database");
  });

  it("lets the developer edit the ticket before sending it", async () => {
    renderDialog();

    const subject = await screen.findByLabelText("Subject");
    await userEvent.clear(subject);
    await userEvent.type(subject, "Create fails on staging");

    expect(subject).toHaveValue("Create fails on staging");
  });

  it("sends what the dialog shows, including the database context", async () => {
    const fetchMock = stubSend(jsonResponse({ number: "CS0061234" }));
    renderDialog({ context: { database: "orders-dev", profile: "prod" } });

    await userEvent.click(sendButton());

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/support/tickets");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      subject: "Cannot create database orders-dev",
      description: prefilledDescription,
      context: { database: "orders-dev", profile: "prod" },
    });
  });

  // The number is what a developer quotes, and the portal is where the reply
  // arrives.
  it("shows the ticket number and the portal link once the ticket is open", async () => {
    stubSend(
      jsonResponse({
        number: "CS0061234",
        portalUrl: "https://example.service-now.com/percona",
        externalUrl: "https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
      }),
    );
    renderDialog();

    await userEvent.click(sendButton());

    const created = await screen.findByTestId("support-ticket-created");
    expect(created).toHaveTextContent("CS0061234");
    expect(screen.getByRole("link", { name: /Open the ticket in ServiceNow/ })).toHaveAttribute(
      "href",
      "https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
    );
    expect(screen.getByRole("link", { name: "View ticket" })).toHaveAttribute(
      "href",
      "/support/CS0061234",
    );
    expect(screen.queryByRole("button", { name: "Send ticket" })).not.toBeInTheDocument();
  });

  it("keeps the case deep link when the ticket number is pending", async () => {
    stubSend(
      jsonResponse({
        id: "sys-1",
        externalUrl: "https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
      }),
    );
    renderDialog();

    await userEvent.click(sendButton());

    const created = await screen.findByTestId("support-ticket-created");
    expect(created).toHaveTextContent("Your ticket is open");
    expect(screen.getByRole("link", { name: "Open the ticket in ServiceNow" })).toHaveAttribute(
      "href",
      "https://example.service-now.com/sn_customerservice_case.do?sys_id=sys-1",
    );
    expect(screen.queryByRole("link", { name: "View ticket" })).not.toBeInTheDocument();
  });

  it("keeps every word of the ticket when the send fails, and sends it again", async () => {
    const fetchMock = stubSend(
      jsonResponse({ code: "ErrDownstream", message: "Support is unreachable right now." }, 502),
      jsonResponse({ number: "CS0061235" }),
    );
    renderDialog();

    await userEvent.type(descriptionField(), "\nIt failed twice in a row.");
    await userEvent.click(sendButton());

    expect(await screen.findByTestId("support-send-error")).toHaveTextContent(
      "Support is unreachable right now.",
    );
    expect(descriptionField().value).toBe(`${prefilledDescription}\nIt failed twice in a row.`);
    expect(screen.getByLabelText("Subject")).toHaveValue("Cannot create database orders-dev");

    await userEvent.click(sendButton());

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(await screen.findByTestId("support-ticket-created")).toHaveTextContent("CS0061235");
  });

  // An environment with no ServiceNow Secret gets the portal, not a button that
  // cannot work.
  it("offers the public portal instead of sending when support is not connected", async () => {
    const fetchMock = stubSend();
    renderDialog({ connected: false });

    expect(await screen.findByTestId("support-not-connected")).toHaveTextContent(
      "Support is not connected yet.",
    );
    expect(screen.queryByRole("button", { name: "Send ticket" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open the support portal/ })).toHaveAttribute(
      "href",
      "https://www.percona.com/services/support",
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
