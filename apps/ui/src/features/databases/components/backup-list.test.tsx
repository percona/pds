import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { BackupList } from "@/features/databases";
import type { BackupSchedule } from "@/features/databases";
import { createMockBackup, renderWithProviders } from "@/test/utils";

const daily: BackupSchedule = { name: "daily", cron: "0 2 * * *", retentionCopies: 7 };

const nightly = createMockBackup({
  id: "orders-dev-20260825020000",
  status: "succeeded",
  sizeBytes: 2147483648,
  scheduled: true,
  createdAt: "2026-08-25T02:00:00Z",
});

const running = createMockBackup({
  id: "orders-dev-20260825140000",
  status: "running",
  sizeBytes: 0,
  scheduled: false,
  createdAt: "2026-08-25T14:00:00Z",
});

describe("BackupList", () => {
  it("reads out when a backup ran, how big it is, and how it got there", () => {
    renderWithProviders(<BackupList backups={[nightly]} schedule={daily} onBackUpNow={vi.fn()} />);

    const row = screen.getByTestId(`backup-row-${nightly.id}`);
    expect(within(row).getByText("2 GB")).toBeInTheDocument();
    expect(within(row).getByText("Scheduled")).toBeInTheDocument();
    expect(within(row).getByTestId("status-pill")).toHaveTextContent("Succeeded");
  });

  it("shows a failed backup reason instead of leaving it on a title tooltip", () => {
    const failed = createMockBackup({
      id: "orders-dev-failed",
      status: "failed",
      sizeBytes: 0,
      message: "the spoke never reported this backup",
    });

    renderWithProviders(<BackupList backups={[failed]} schedule={daily} onBackUpNow={vi.fn()} />);

    const row = screen.getByTestId(`backup-row-${failed.id}`);
    expect(within(row).getByTestId("status-pill")).toHaveTextContent("Failed");
    expect(within(row).getByText("the spoke never reported this backup")).toBeInTheDocument();
    expect(
      within(row).getByText("the spoke never reported this backup").closest("td"),
    ).not.toHaveAttribute("title");
  });

  it("says a running backup has no size yet rather than showing a zero", () => {
    renderWithProviders(<BackupList backups={[running]} schedule={daily} onBackUpNow={vi.fn()} />);

    const row = screen.getByTestId(`backup-row-${running.id}`);
    expect(within(row).getByText("Not reported")).toBeInTheDocument();
    expect(within(row).getByText("On demand")).toBeInTheDocument();
    expect(within(row).getByTestId("status-pill")).toHaveTextContent("Running");
  });

  // Tables carry at most five columns.
  it("stays inside the five column budget", () => {
    renderWithProviders(
      <BackupList backups={[nightly, running]} schedule={daily} onBackUpNow={vi.fn()} />,
    );

    expect(screen.getAllByRole("columnheader")).toHaveLength(5);
  });

  it("offers a restore only from a backup that finished", async () => {
    const onRestore = vi.fn();
    renderWithProviders(
      <BackupList
        backups={[nightly, running]}
        schedule={daily}
        onBackUpNow={vi.fn()}
        onRestore={onRestore}
      />,
    );

    const finished = within(screen.getByTestId(`backup-row-${nightly.id}`));
    const inFlight = within(screen.getByTestId(`backup-row-${running.id}`));

    expect(inFlight.getByRole("button", { name: "Restore" })).toBeDisabled();

    await userEvent.click(finished.getByRole("button", { name: "Restore" }));

    expect(onRestore).toHaveBeenCalledWith(nightly);
  });

  // The detail page owns the dialog, so a list rendered without a handler is a
  // list with nowhere to send a restore.
  it("disables restore while no handler is wired", () => {
    renderWithProviders(<BackupList backups={[nightly]} schedule={daily} onBackUpNow={vi.fn()} />);

    expect(screen.getByRole("button", { name: "Restore" })).toBeDisabled();
  });

  describe("with nothing to list", () => {
    it("says when the first scheduled backup runs and offers a backup now", async () => {
      const onBackUpNow = vi.fn();
      renderWithProviders(<BackupList backups={[]} schedule={daily} onBackUpNow={onBackUpNow} />);

      expect(screen.getByText("No backups yet")).toBeInTheDocument();
      expect(
        screen.getByText(/^The first scheduled backup runs at .*02:00 UTC\)?\.$/),
      ).toBeInTheDocument();
      expect(screen.queryByRole("table")).not.toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: "Back up now" }));

      expect(onBackUpNow).toHaveBeenCalledTimes(1);
    });

    it("does not promise a schedule this database does not have", () => {
      renderWithProviders(<BackupList backups={[]} onBackUpNow={vi.fn()} />);

      expect(
        screen.getByText("This database has no schedule, so back it up when you need one."),
      ).toBeInTheDocument();
    });

    it("keeps the button honest while a backup is starting", () => {
      renderWithProviders(<BackupList backups={[]} onBackUpNow={vi.fn()} backingUp />);

      expect(screen.getByRole("button", { name: "Backing up..." })).toBeDisabled();
    });
  });
});
