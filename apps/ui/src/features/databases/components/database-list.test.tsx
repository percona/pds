import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { DatabaseList } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

const ordersDev = createMockDatabase({
  id: "spoke-a/pds.orders-dev",
  name: "orders-dev",
  profile: "prod",
  version: "8.4.3",
  status: "ready",
  lastBackupAt: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(),
  connection: {
    host: "orders-dev.pds.svc",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-dev.pds.svc:3306",
  },
});

const cartsStaging = createMockDatabase({
  id: "spoke-a/pds.carts-staging",
  name: "carts-staging",
  profile: "staging",
  status: "provisioning",
});

describe("DatabaseList", () => {
  it("names the Team of a database when the list covers every Team", () => {
    renderWithProviders(
      <DatabaseList databases={[{ ...ordersDev, team: "Payments" }, cartsStaging]} />,
    );

    expect(
      within(screen.getByTestId(`database-row-${ordersDev.id}`)).getByText("Payments"),
    ).toBeInTheDocument();
    expect(screen.getByTestId(`database-row-${cartsStaging.id}`)).not.toHaveTextContent("Team");
  });

  it("schedules a backup from the row", async () => {
    renderWithProviders(<DatabaseList databases={[ordersDev]} />);

    await userEvent.click(screen.getByRole("button", { name: "Schedule backup" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Schedule backup");
    expect(within(dialog).getByTestId("backup-schedule-editor")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Frequency")).toBeInTheDocument();
  });

  it("links the row to the detail page", () => {
    renderWithProviders(<DatabaseList databases={[ordersDev, cartsStaging]} />);

    const row = screen.getByRole("link", { name: /orders-dev/ });

    expect(row).toHaveAttribute("href", "/databases/spoke-a/pds.orders-dev");
    expect(screen.getAllByRole("link")).toHaveLength(2);
  });

  it("shows engine and version, status, profile, and the last backup on each row", () => {
    renderWithProviders(<DatabaseList databases={[ordersDev]} />);

    const row = within(screen.getByTestId("database-row-spoke-a/pds.orders-dev"));

    expect(row.getByText("orders-dev")).toBeInTheDocument();
    expect(row.getByText("MySQL 8.4.3")).toBeInTheDocument();
    expect(row.getByText("Ready")).toBeInTheDocument();
    expect(row.getByText("Prod")).toBeInTheDocument();
    expect(row.getByText("3 hours ago")).toBeInTheDocument();
  });

  it("says there is no backup rather than leaving the cell blank", () => {
    renderWithProviders(<DatabaseList databases={[cartsStaging]} />);

    const row = within(screen.getByTestId("database-row-spoke-a/pds.carts-staging"));

    expect(row.getByText("No backups yet")).toBeInTheDocument();
    expect(row.getByText("Creating volumes")).toBeInTheDocument();
  });

  it("names the engine without a version when the adapter does not report one", () => {
    renderWithProviders(<DatabaseList databases={[cartsStaging]} />);

    expect(screen.getByText("MySQL")).toBeInTheDocument();
  });

  it("shows the metrics pending pill where the sparkline goes until PMM reports", () => {
    renderWithProviders(<DatabaseList databases={[ordersDev]} />);

    expect(screen.getByText("Metrics pending")).toBeInTheDocument();
  });

  it("draws the sparkline once there is a series", () => {
    renderWithProviders(
      <DatabaseList databases={[ordersDev]} cpuSeries={{ [ordersDev.id]: [10, 20, 42] }} />,
    );

    expect(screen.getByRole("img", { name: "CPU 42%, trending up" })).toBeInTheDocument();
    expect(screen.queryByText("Metrics pending")).not.toBeInTheDocument();
  });

  it("shows Custom when the adapter reports no catalog profile", () => {
    const observed = createMockDatabase({
      id: "eks-testing/pds.dev-testing",
      name: "dev-testing",
      profile: "",
    });

    renderWithProviders(<DatabaseList databases={[observed]} />);

    const row = within(screen.getByTestId("database-row-eks-testing/pds.dev-testing"));

    expect(row.getByText("dev-testing")).toBeInTheDocument();
    expect(row.getByText("Custom")).toBeInTheDocument();
  });

  it("composes an empty state instead of bare headers", () => {
    renderWithProviders(<DatabaseList databases={[]} />);

    expect(screen.getByText("No databases yet")).toBeInTheDocument();
    expect(
      screen.getByText("Create a MySQL or PostgreSQL database on this Kubernetes cluster."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("marks a row with its firing alerts as an icon plus text, and leaves quiet rows bare", () => {
    renderWithProviders(
      <DatabaseList
        databases={[ordersDev, cartsStaging]}
        alerts={{ [ordersDev.id]: { total: 2, worst: "critical", worstCount: 1 } }}
      />,
    );

    const row = within(screen.getByTestId(`database-row-${ordersDev.id}`));
    const quiet = within(screen.getByTestId(`database-row-${cartsStaging.id}`));

    expect(row.getByText("1 critical, 1 more")).toBeInTheDocument();
    expect(quiet.queryByText(/critical|warning|info/)).not.toBeInTheDocument();
  });
});
