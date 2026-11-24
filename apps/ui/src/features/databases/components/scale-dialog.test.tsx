import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ScaleDialog } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

const database = createMockDatabase({
  id: "spoke-a/pds.orders-dev",
  name: "orders-dev",
  members: 1,
  cpu: 1,
  memoryGb: 2,
  storageGb: 20,
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubScale(response: Response) {
  const fetchMock = vi.fn(() => Promise.resolve(response));
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function renderDialog(props: Partial<Parameters<typeof ScaleDialog>[0]> = {}) {
  return renderWithProviders(
    <ScaleDialog database={database} open onOpenChange={vi.fn()} {...props} />,
  );
}

function submitButton() {
  return screen.getByRole("button", { name: "Scale" });
}

function requestBody(fetchMock: ReturnType<typeof stubScale>): unknown {
  const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];

  return JSON.parse(String(init.body));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ScaleDialog", () => {
  it("opens on the current sizing", () => {
    renderDialog();

    expect(screen.getByLabelText("CPU cores")).toHaveValue("1");
    expect(screen.getByLabelText("Memory in GB")).toHaveValue("2");
    expect(screen.getByLabelText("Storage in GB")).toHaveValue("20");
    expect(screen.getByRole("radio", { name: "1 member" })).toBeChecked();
  });

  it("keeps submit disabled until something changes", async () => {
    renderDialog();

    expect(submitButton()).toBeDisabled();

    await userEvent.clear(screen.getByLabelText("CPU cores"));
    await userEvent.type(screen.getByLabelText("CPU cores"), "4");

    await waitFor(() => expect(submitButton()).toBeEnabled());
  });

  it("refuses a storage shrink with the same sentence the adapter uses", async () => {
    renderDialog();

    await userEvent.clear(screen.getByLabelText("Storage in GB"));
    await userEvent.type(screen.getByLabelText("Storage in GB"), "10");
    await userEvent.tab();

    expect(await screen.findByText("Storage can only be increased.")).toBeInTheDocument();
    await waitFor(() => expect(submitButton()).toBeDisabled());
    expect(screen.getByLabelText("Storage in GB")).toHaveAttribute("aria-invalid", "true");
  });

  it("states the bounds when a value is out of range", async () => {
    renderDialog();

    await userEvent.clear(screen.getByLabelText("CPU cores"));
    await userEvent.type(screen.getByLabelText("CPU cores"), "128");
    await userEvent.tab();

    expect(await screen.findByText("CPU must be between 1 and 64 cores.")).toBeInTheDocument();
    await waitFor(() => expect(submitButton()).toBeDisabled());
  });

  it("offers only the member counts that hold quorum", () => {
    renderDialog();

    expect(screen.getAllByRole("radio").map((radio) => radio.getAttribute("value"))).toEqual([
      "1",
      "3",
      "5",
    ]);
  });

  it("states what three members buys", async () => {
    renderDialog();

    expect(screen.getByText("One member. A restart is downtime.")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "3 members" }));

    expect(
      await screen.findByText("Three members with HA. The database stays up while one restarts."),
    ).toBeInTheDocument();
  });

  it("tells a MongoDB database that loses members that their storage stays", async () => {
    renderDialog({
      database: createMockDatabase({ ...database, engine: "mongodb", members: 3 }),
    });

    expect(screen.queryByText(/Storage of the removed members/)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "1 member" }));

    expect(
      await screen.findByText(
        "Storage of the removed members stays until the database is deleted.",
      ),
    ).toBeInTheDocument();
  });

  it("says nothing about removed storage for a MySQL database", async () => {
    renderDialog({ database: createMockDatabase({ ...database, members: 3 }) });

    await userEvent.click(screen.getByRole("radio", { name: "1 member" }));

    await screen.findByText("One member. A restart is downtime.");
    expect(screen.queryByText(/Storage of the removed members/)).not.toBeInTheDocument();
  });

  it("sends only the fields that moved", async () => {
    const fetchMock = stubScale(jsonResponse({ ...database, members: 3 }));
    renderDialog();

    await userEvent.click(screen.getByRole("radio", { name: "3 members" }));
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    const [path] = fetchMock.mock.calls[0] as unknown as [string];
    expect(path).toBe("/api/databases/spoke-a/pds.orders-dev/scale");
    expect(requestBody(fetchMock)).toEqual({ replicas: 3 });
  });

  it("closes and reports the change once the adapter answers", async () => {
    stubScale(jsonResponse({ ...database, cpu: 4 }));
    const onOpenChange = vi.fn();
    renderDialog({ onOpenChange });

    await userEvent.clear(screen.getByLabelText("CPU cores"));
    await userEvent.type(screen.getByLabelText("CPU cores"), "4");
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("keeps the dialog open and shows the adapter message on a conflict", async () => {
    stubScale(
      jsonResponse(
        { code: "ErrConflict", message: "A backup is running. Scale once it finishes." },
        409,
      ),
    );
    const onOpenChange = vi.fn();
    renderDialog({ onOpenChange });

    await userEvent.clear(screen.getByLabelText("CPU cores"));
    await userEvent.type(screen.getByLabelText("CPU cores"), "4");
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    expect(
      await screen.findByText("A backup is running. Scale once it finishes."),
    ).toBeInTheDocument();
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });

  // An alert or a chat action opens this dialog with one field pre-selected.
  it("focuses the field an action pre-selects", async () => {
    renderDialog({ focusField: "storage" });

    await waitFor(() => expect(screen.getByLabelText("Storage in GB")).toHaveFocus());
  });

  it("focuses cpu by default", async () => {
    renderDialog();

    await waitFor(() => expect(screen.getByLabelText("CPU cores")).toHaveFocus());
  });
});
