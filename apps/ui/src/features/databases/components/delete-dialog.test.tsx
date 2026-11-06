import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DeleteDialog } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

const database = createMockDatabase({
  id: "spoke-a/pds.orders-prod",
  name: "orders-prod",
  profile: "prod",
  members: 3,
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** The adapter answers a delete with 204 and no body. */
function noContent() {
  return new Response(null, { status: 204 });
}

function stubDelete(response: Response) {
  const fetchMock = vi.fn(() => Promise.resolve(response));
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function renderDialog(props: Partial<Parameters<typeof DeleteDialog>[0]> = {}) {
  return renderWithProviders(
    <DeleteDialog database={database} open onOpenChange={vi.fn()} {...props} />,
  );
}

function confirmButton() {
  return screen.getByRole("button", { name: "Delete" });
}

function nameField() {
  return screen.getByLabelText("Database name");
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DeleteDialog", () => {
  // The consequence goes in one sentence. It is the whole reason the dialog
  // exists.
  it("states the consequence in one sentence", () => {
    renderDialog();

    expect(screen.getByTestId("delete-consequence")).toHaveTextContent(
      "This deletes orders-prod with its data and its backups, and there is no undo.",
    );
  });

  // The backup data of a PostgreSQL database is not removed by the delete, so
  // the dialog must not claim it is.
  it("says PostgreSQL backups stay in the backup storage", () => {
    renderDialog({ database: createMockDatabase({ ...database, engine: "postgresql" }) });

    const consequence = screen.getByTestId("delete-consequence");
    expect(consequence).toHaveTextContent("PostgreSQL backups stay in the backup storage");
    expect(consequence).not.toHaveTextContent("with its data and its backups");
  });

  it("keeps confirm disabled until the name matches", async () => {
    renderDialog();

    expect(confirmButton()).toBeDisabled();

    await userEvent.type(nameField(), "orders");
    expect(confirmButton()).toBeDisabled();

    await userEvent.type(nameField(), "-prod");
    await waitFor(() => expect(confirmButton()).toBeEnabled());
  });

  it("does not accept a name in the wrong case", async () => {
    renderDialog();

    await userEvent.type(nameField(), "Orders-Prod");

    expect(confirmButton()).toBeDisabled();
  });

  it("says what is wrong once the field is left", async () => {
    renderDialog();

    await userEvent.type(nameField(), "orders");
    await userEvent.tab();

    expect(await screen.findByText("That is not this database name.")).toBeInTheDocument();
    expect(nameField()).toHaveAttribute("aria-invalid", "true");
  });

  it("stays quiet while the field is still empty", async () => {
    renderDialog();

    await userEvent.click(nameField());
    await userEvent.tab();

    expect(screen.queryByText("That is not this database name.")).not.toBeInTheDocument();
  });

  it("sends one DELETE for this database", async () => {
    const fetchMock = stubDelete(noContent());
    renderDialog();

    await userEvent.type(nameField(), "orders-prod");
    await userEvent.click(confirmButton());

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    const [path, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(path).toBe("/api/databases/spoke-a/pds.orders-prod");
    expect(init.method).toBe("DELETE");
    expect(init.body).toBeUndefined();
  });

  it("closes and reports the delete once the adapter accepts it", async () => {
    stubDelete(noContent());
    const onOpenChange = vi.fn();
    const onDeleted = vi.fn();
    renderDialog({ onOpenChange, onDeleted });

    await userEvent.type(nameField(), "orders-prod");
    await userEvent.click(confirmButton());

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(onDeleted).toHaveBeenCalledTimes(1);
  });

  it("keeps the dialog open and shows the adapter message on a refusal", async () => {
    stubDelete(
      jsonResponse({ code: "ErrNotFound", message: "No database named `orders-prod`." }, 404),
    );
    const onOpenChange = vi.fn();
    const onDeleted = vi.fn();
    renderDialog({ onOpenChange, onDeleted });

    await userEvent.type(nameField(), "orders-prod");
    await userEvent.click(confirmButton());

    expect(await screen.findByText("No database named `orders-prod`.")).toBeInTheDocument();
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
    expect(onDeleted).not.toHaveBeenCalled();
  });

  it("cancels without calling the adapter", async () => {
    const fetchMock = stubDelete(noContent());
    const onOpenChange = vi.fn();
    renderDialog({ onOpenChange });

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  // Esc closes the dialogs that change nothing. This one destroys a database,
  // so leaving it is a decision, and Cancel is how it is made.
  it("does not close on Esc", async () => {
    const onOpenChange = vi.fn();
    renderDialog({ onOpenChange });

    await userEvent.keyboard("{Escape}");

    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByTestId("delete-consequence")).toBeInTheDocument();
  });

  // A reopened dialog must not arrive already confirmed.
  it("starts empty every time it opens", async () => {
    const { rerender } = renderDialog();

    await userEvent.type(nameField(), "orders-prod");
    await waitFor(() => expect(confirmButton()).toBeEnabled());

    rerender(<DeleteDialog database={database} open={false} onOpenChange={vi.fn()} />);
    rerender(<DeleteDialog database={database} open onOpenChange={vi.fn()} />);

    expect(nameField()).toHaveValue("");
    expect(confirmButton()).toBeDisabled();
  });

  it("focuses the field the confirmation is typed into", async () => {
    renderDialog();

    await waitFor(() => expect(nameField()).toHaveFocus());
  });
});
