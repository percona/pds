import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RestoreDialog } from "@/features/databases";
import { createMockBackup, createMockDatabase, renderWithProviders } from "@/test/utils";

const nightly = createMockBackup({
  id: "orders-prod-20260825020000",
  status: "succeeded",
  sizeBytes: 2147483648,
  scheduled: true,
  createdAt: "2026-08-25T02:00:00Z",
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function confirmButton() {
  return screen.getByRole("button", { name: "Restore" });
}

function newNameField() {
  return screen.getByLabelText("New database name");
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("RestoreDialog", () => {
  const hubDatabase = createMockDatabase({
    id: "spoke-a/pds.orders-prod",
    name: "orders-prod",
    environment: "spoke-a",
    profile: "prod",
    members: 3,
  });

  /**
   * The dialog reads the environment's databases to offer a name nothing uses, so
   * it makes two calls and the restore is the one under test.
   */
  function stubHubRestore(response: Response, names: string[] = ["orders-prod"]) {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith("/restore")) {
        return Promise.resolve(response);
      }

      return Promise.resolve(
        jsonResponse({
          databases: names.map((name) =>
            createMockDatabase({
              id: `spoke-a/pds.${name}`,
              name,
              environment: "spoke-a",
            }),
          ),
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    return fetchMock;
  }

  function renderHub(props: Partial<Parameters<typeof RestoreDialog>[0]> = {}) {
    return renderWithProviders(
      <RestoreDialog
        database={hubDatabase}
        backup={nightly}
        open
        onOpenChange={vi.fn()}
        {...props}
      />,
    );
  }

  it("states that a new database is created and the source is left alone", () => {
    stubHubRestore(jsonResponse({ status: "accepted" }, 202));
    renderHub();

    expect(screen.getByTestId("restore-consequence")).toHaveTextContent(
      "This restores the backup into a new database on spoke-a. orders-prod is not touched.",
    );
  });

  // Nothing is provisioned for a restore any more, so nothing in the dialog
  // may ask for an environment, warn about cost, or hand over a join command.
  it("never asks for an environment to build", async () => {
    stubHubRestore(jsonResponse({ status: "accepted" }, 202));
    renderHub();

    expect(await screen.findByLabelText("New database name")).toBeInTheDocument();
    expect(screen.queryByLabelText("New environment name")).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByText(/EKS/)).not.toBeInTheDocument();
    expect(screen.queryByText(/cost money/)).not.toBeInTheDocument();
  });

  it("prefills a name the environment does not already run", async () => {
    stubHubRestore(jsonResponse({ status: "accepted" }, 202), [
      "orders-prod",
      "orders-pr-restore-1",
    ]);
    renderHub();

    await waitFor(() => expect(newNameField()).toHaveValue("orders-pr-restore-2"));
    await waitFor(() => expect(confirmButton()).toBeEnabled());
  });

  // The hub lists a copy whose seed failed with no connection and accepts its
  // name again, so the dialog must not report it as in use.
  it("accepts the name of a restore copy whose seed failed", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith("/restore")) {
        return Promise.resolve(jsonResponse({ status: "accepted" }, 202));
      }

      return Promise.resolve(
        jsonResponse({
          databases: [
            createMockDatabase({ id: "spoke-a/pds.orders-prod", name: "orders-prod" }),
            createMockDatabase({
              id: "spoke-a/pds.orders-pr-restore-1",
              name: "orders-pr-restore-1",
              engine: "postgresql",
              status: "failed",
              environment: "spoke-a",
            }),
          ],
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    renderHub();

    await waitFor(() => expect(confirmButton()).toBeEnabled());
    await userEvent.clear(newNameField());
    await userEvent.type(newNameField(), "orders-pr-restore-1");
    await userEvent.tab();

    expect(screen.queryByText(/is already on/)).not.toBeInTheDocument();
    expect(confirmButton()).toBeEnabled();
  });

  it("refuses a name Kubernetes would not take", async () => {
    stubHubRestore(jsonResponse({ status: "accepted" }, 202));
    renderHub();

    await waitFor(() => expect(newNameField()).toHaveValue("orders-pr-restore-1"));
    await userEvent.clear(newNameField());
    await userEvent.type(newNameField(), "Orders Restore");

    expect(confirmButton()).toBeDisabled();
    await userEvent.tab();
    expect(await screen.findByText("That is not a database name.")).toBeInTheDocument();
  });

  // A PostgreSQL copy is held to the PostgreSQL limit and its reason, not to
  // the MySQL restore job's 19 characters.
  it("holds a PostgreSQL copy to the PostgreSQL name limit", async () => {
    stubHubRestore(jsonResponse({ status: "accepted" }, 202));
    renderHub({ database: { ...hubDatabase, engine: "postgresql" } });

    await waitFor(() => expect(newNameField()).toHaveValue("orders-prod-restore-1"));
    await userEvent.clear(newNameField());
    await userEvent.type(newNameField(), "orders-prod-reporting-copy");
    await waitFor(() => expect(confirmButton()).toBeEnabled());

    await userEvent.type(newNameField(), "-x".repeat(6));
    await userEvent.tab();
    expect(
      await screen.findByText(
        "Use 37 characters or fewer. Every member's labels carry this name, and a longer one is refused.",
      ),
    ).toBeInTheDocument();
    expect(confirmButton()).toBeDisabled();
  });

  // The adapter refuses this too, but a developer should not have to submit to
  // find out that the name is the database they are restoring from.
  it("refuses a name the environment already runs", async () => {
    stubHubRestore(jsonResponse({ status: "accepted" }, 202));
    renderHub();

    await waitFor(() => expect(newNameField()).toHaveValue("orders-pr-restore-1"));
    await userEvent.clear(newNameField());
    await userEvent.type(newNameField(), "orders-prod");

    expect(confirmButton()).toBeDisabled();
    await userEvent.tab();
    expect(
      await screen.findByText("A database named orders-prod is already on spoke-a."),
    ).toBeInTheDocument();
  });

  // Before the environment's databases land there is nothing to check a name
  // against, so offering one that turns out to be taken would be a refusal the
  // developer did not cause.
  it("holds confirm until it knows what the environment runs", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith("/restore")) {
        return Promise.resolve(jsonResponse({ status: "accepted" }, 202));
      }

      return new Promise<Response>(() => {});
    });
    vi.stubGlobal("fetch", fetchMock);
    renderHub();

    expect(confirmButton()).toBeDisabled();
  });

  // The button waits for the list, but the field must not invent a reason.
  // Deriving the message from the same flag claimed a free name was taken.
  it("says nothing is wrong while it is still reading the list", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith("/restore")) {
        return Promise.resolve(jsonResponse({ status: "accepted" }, 202));
      }

      return new Promise<Response>(() => {});
    });
    vi.stubGlobal("fetch", fetchMock);
    renderHub();

    await userEvent.type(newNameField(), "orders-prod-restore-9");
    await userEvent.tab();

    expect(screen.queryByText(/is already on/)).not.toBeInTheDocument();
    expect(screen.queryByText("That is not a database name.")).not.toBeInTheDocument();
  });

  // A read that failed outright is not a reason to block the restore: the
  // adapter refuses a name in use, and that is the answer that counts.
  it("lets the adapter decide when the list cannot be read", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (String(input).endsWith("/restore")) {
        return Promise.resolve(jsonResponse({ status: "accepted" }, 202));
      }

      return Promise.resolve(jsonResponse({ code: "ErrDownstream", message: "no" }, 502));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderHub();

    await waitFor(() => expect(newNameField()).toHaveValue("orders-pr-restore-1"));
    await waitFor(() => expect(confirmButton()).toBeEnabled());
  });

  it("sends the new name with the backup", async () => {
    const fetchMock = stubHubRestore(
      jsonResponse(
        {
          status: "accepted",
          environment: "spoke-a",
          name: "orders-prod-restore-1",
          id: "spoke-a/pds.orders-prod-restore-1",
        },
        202,
      ),
    );
    renderHub();

    await waitFor(() => expect(newNameField()).toHaveValue("orders-pr-restore-1"));
    await userEvent.click(confirmButton());

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/restore")),
      ).toHaveLength(1),
    );
    const [path, init] = fetchMock.mock.calls.find(([input]) =>
      String(input).endsWith("/restore"),
    ) as unknown as [string, RequestInit];
    expect(path).toBe("/api/databases/spoke-a/pds.orders-prod/restore");
    expect(JSON.parse(String(init.body))).toEqual({
      backupId: "orders-prod-20260825020000",
      name: "orders-pr-restore-1",
    });
  });

  it("closes once the adapter accepts it", async () => {
    stubHubRestore(
      jsonResponse(
        { status: "accepted", environment: "spoke-a", name: "orders-prod-restore-1" },
        202,
      ),
    );
    const onOpenChange = vi.fn();
    renderHub({ onOpenChange });

    await waitFor(() => expect(newNameField()).toHaveValue("orders-pr-restore-1"));
    await userEvent.click(confirmButton());

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("keeps the dialog open and shows the adapter message on a refusal", async () => {
    stubHubRestore(
      jsonResponse(
        {
          code: "ErrConflict",
          message: "A database named orders-prod-restore-1 is already on spoke-a.",
        },
        409,
      ),
    );
    const onOpenChange = vi.fn();
    renderHub({ onOpenChange });

    await waitFor(() => expect(newNameField()).toHaveValue("orders-pr-restore-1"));
    await userEvent.click(confirmButton());

    expect(
      await screen.findByText("A database named orders-prod-restore-1 is already on spoke-a."),
    ).toBeInTheDocument();
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });
});
