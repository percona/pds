import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { ManagedDeleteDialog } from "./managed-delete-dialog";

function stubDelete(status = 202, body: unknown = { run: { id: 9 }, provisioning: {} }) {
  const fetchMock = vi.fn<(input: string, init?: RequestInit) => Promise<Response>>(() =>
    Promise.resolve(
      new Response(status === 204 ? null : JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

describe("ManagedDeleteDialog", () => {
  it("keeps Delete disabled until the name is typed exactly", async () => {
    const fetchMock = stubDelete();
    const user = userEvent.setup();

    renderWithProviders(
      <ManagedDeleteDialog environment="orders" account="payments" open onOpenChange={vi.fn()} />,
    );

    const confirm = screen.getByRole("button", { name: "Delete" });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText("Environment name"), "order");
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText("Environment name"), "s");
    expect(confirm).toBeEnabled();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("says what it deletes in the AWS account", () => {
    stubDelete();

    renderWithProviders(
      <ManagedDeleteDialog environment="orders" account="payments" open onOpenChange={vi.fn()} />,
    );

    expect(screen.getByTestId("delete-consequence")).toHaveTextContent("payments");
    expect(screen.getByTestId("delete-consequence")).toHaveTextContent("there is no undo");
  });

  it("deletes with the typed name and closes", async () => {
    const fetchMock = stubDelete();
    const onOpenChange = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <ManagedDeleteDialog
        environment="orders"
        account="payments"
        open
        onOpenChange={onOpenChange}
      />,
    );
    await user.type(screen.getByLabelText("Environment name"), "orders");
    await user.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/environments/orders");
    expect(JSON.parse(String(init?.body))).toEqual({ confirm: "orders" });
  });

  it("shows the refusal and stays open", async () => {
    stubDelete(409, {
      code: "ErrConflict",
      message:
        "This environment already has a run in progress. Wait for it to finish, or retry it if it failed.",
    });
    const onOpenChange = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <ManagedDeleteDialog
        environment="orders"
        account="payments"
        open
        onOpenChange={onOpenChange}
      />,
    );
    await user.type(screen.getByLabelText("Environment name"), "orders");
    await user.click(screen.getByRole("button", { name: "Delete" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("already has a run in progress");
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });

  it("offers Remove from PDS only when the AWS account cannot be reached", async () => {
    const fetchMock = stubDelete(204);
    const user = userEvent.setup();

    const { unmount } = renderWithProviders(
      <ManagedDeleteDialog environment="orders" account="payments" open onOpenChange={vi.fn()} />,
    );
    expect(screen.queryByRole("button", { name: "Remove from PDS only" })).not.toBeInTheDocument();
    unmount();

    renderWithProviders(
      <ManagedDeleteDialog
        environment="orders"
        account="payments"
        accountUnreachable
        open
        onOpenChange={vi.fn()}
      />,
    );
    await user.type(screen.getByLabelText("Environment name"), "orders");
    await user.click(screen.getByRole("button", { name: "Remove from PDS only" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      confirm: "orders",
      scope: "pds_only",
    });
  });
});
