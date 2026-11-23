import { screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BackupList, DeleteDialog, SettingsTab } from "@/features/databases";
import { createMockBackup, createMockDatabase, renderWithProviders } from "@/test/utils";

// The MongoDB engine is hidden in the engine registry, so none of this can be
// reached from the app yet. These render each component with the engine on.
const database = createMockDatabase({
  id: "spoke-a/pds.orders",
  name: "orders",
  engine: "mongodb",
  profile: "staging",
  members: 3,
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("MongoDB backups", () => {
  // The provider reports when a backup finished and nothing reliable about
  // when it started, so the list shows the finish time alone.
  it("lists when a backup finished, never when it started", () => {
    const finished = createMockBackup({
      id: "cron-orders-1",
      createdAt: undefined,
      finishedAt: "2026-08-25T02:00:05Z",
    });
    const inFlight = createMockBackup({
      id: "cron-orders-2",
      status: "running",
      sizeBytes: 0,
      createdAt: undefined,
      finishedAt: undefined,
    });

    renderWithProviders(
      <BackupList backups={[finished, inFlight]} finishOnly onBackUpNow={vi.fn()} />,
    );

    expect(screen.getByRole("columnheader", { name: "Finished" })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "Started" })).not.toBeInTheDocument();
    const row = within(screen.getByTestId("backup-row-cron-orders-1"));
    expect(row.getByTitle(/2026/)).toBeInTheDocument();
    expect(
      within(screen.getByTestId("backup-row-cron-orders-2")).getByText("In progress"),
    ).toBeInTheDocument();
  });

  it("keeps the Started column for the engines that report one", () => {
    renderWithProviders(<BackupList backups={[createMockBackup()]} onBackUpNow={vi.fn()} />);

    expect(screen.getByRole("columnheader", { name: "Started" })).toBeInTheDocument();
  });

  // Deleting the database deletes its backups and what they wrote to the
  // backup storage.
  it("says the backups are deleted with the database, in the dialog and in settings", () => {
    renderWithProviders(<DeleteDialog database={database} open onOpenChange={vi.fn()} />);
    expect(screen.getByTestId("delete-consequence")).toHaveTextContent(
      "This deletes orders with its data and its backups, and there is no undo. MongoDB backups are deleted from the backup storage too.",
    );

    renderWithProviders(<SettingsTab database={database} onDelete={vi.fn()} />);
    expect(screen.getByTestId("danger-zone")).toHaveTextContent(
      "Deleting orders takes its data and its backups with it. MongoDB backups are deleted from the backup storage too.",
    );
  });
});
