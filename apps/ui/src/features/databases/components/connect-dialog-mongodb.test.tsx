import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectDialog } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const members = [
  "orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017",
  "orders-rs0-1.orders-rs0.pds.svc.cluster.local:27017",
  "orders-rs0-2.orders-rs0.pds.svc.cluster.local:27017",
];
const list = members.join(",");
const real = `mongodb://databaseAdmin:s3cr3t@${list}/admin?replicaSet=rs0&ssl=false`;

const connection = {
  host: "orders-rs0-0.orders-rs0.pds.svc.cluster.local",
  port: 27017,
  members,
  username: "databaseAdmin",
  secretName: "orders-conn",
  serviceName: "orders-rs0-0",
};

/** The engine is hidden in the create form, so the panel is rendered directly. */
const inCluster = createMockDatabase({
  id: "spoke-a/pds.orders",
  name: "orders",
  engine: "mongodb",
  members: 3,
  connection,
});

const accountLine = "This account reads and writes every database and can manage users.";
const plainLine = "The connection inside the Kubernetes cluster is not encrypted.";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ConnectDialog for MongoDB", () => {
  it("shows the masked string and says what the account can do", () => {
    const { container } = renderWithProviders(
      <ConnectDialog database={inCluster} open onOpenChange={() => {}} />,
    );

    expect(
      screen.getByText(`mongodb://databaseAdmin:••••••••@${list}/admin?replicaSet=rs0&ssl=false`),
    ).toBeInTheDocument();
    expect(screen.getByText(new RegExp(accountLine))).toBeInTheDocument();
    expect(screen.getByText(new RegExp(plainLine))).toBeInTheDocument();
    expect(container.textContent).not.toContain("s3cr3t");
  });

  it("gives the member port-forward and a direct mongosh against it", () => {
    renderWithProviders(<ConnectDialog database={inCluster} open onOpenChange={() => {}} />);

    expect(
      screen.getByText("kubectl -n 'pds' port-forward 'svc/orders-rs0-0' '27017:27017'"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "mongosh 'mongodb://databaseAdmin@127.0.0.1:27017/admin?directConnection=true&ssl=false'",
      ),
    ).toBeInTheDocument();
  });

  it("copies a public database's string with its password, which never renders", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ uri: real }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      ),
    );
    const external = createMockDatabase({
      ...inCluster,
      access: { external: true, visibility: "public", address: "198.51.100.40" },
    });

    const { container } = renderWithProviders(
      <ConnectDialog database={external} open onOpenChange={() => {}} />,
    );

    expect(container.textContent).not.toContain("s3cr3t");
    expect(screen.getByText(new RegExp(accountLine))).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Copy connection string" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(real));
    expect(container.textContent).not.toContain("s3cr3t");
  });
});
