import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BackupsTab } from "@/features/databases";
import { createMockBackup, createMockDatabase, renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubBackups(response: Response) {
  const fetchMock = vi.fn((_input: string) => Promise.resolve(response));
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

const scheduled = createMockDatabase({
  id: "spoke-a/pds.orders-prod",
  name: "orders-prod",
  profile: "prod",
  backupSchedule: { name: "daily", cron: "0 2 * * *", retentionCopies: 7 },
});

const onDemand = createMockDatabase({ id: "spoke-a/pds.orders-dev", profile: "dev" });

function renderTab(props: Partial<Parameters<typeof BackupsTab>[0]> = {}) {
  return renderWithProviders(
    <BackupsTab
      database={scheduled}
      onBackUpNow={vi.fn()}
      backingUp={false}
      onRestore={vi.fn()}
      {...props}
    />,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("BackupsTab", () => {
  it("states the schedule the environment actually runs", async () => {
    stubBackups(jsonResponse({ backups: [] }));

    renderTab();

    const status = await screen.findByTestId("backup-schedule");
    expect(status).toHaveTextContent("Daily backups");
    // The time is stated in the viewer's zone first, so only the UTC half is fixed.
    expect(status).toHaveTextContent(/02:00 UTC\)?\. The last 7 are kept\./);
  });

  it("says on demand when the spec carries no schedule", async () => {
    stubBackups(jsonResponse({ backups: [] }));

    renderTab({ database: onDemand });

    const status = await screen.findByTestId("backup-schedule");
    expect(status).toHaveTextContent("On-demand backups");
    expect(status).toHaveTextContent("No schedule runs for this database.");
  });

  it("puts the schedule editor under the summary", async () => {
    stubBackups(jsonResponse({ backups: [] }));

    renderTab();

    expect(await screen.findByTestId("backup-schedule-editor")).toBeInTheDocument();
    expect(screen.getByLabelText("Frequency")).toHaveValue("daily");
  });

  it("puts Back up now next to the editor once there are backups", async () => {
    stubBackups(jsonResponse({ backups: [createMockBackup({ id: "orders-prod-1" })] }));
    const onBackUpNow = vi.fn();

    renderTab({ onBackUpNow });

    await screen.findByTestId("backup-row-orders-prod-1");
    await userEvent.click(screen.getByRole("button", { name: "Back up now" }));

    expect(onBackUpNow).toHaveBeenCalledTimes(1);
  });

  it("offers Back up now once on an empty list", async () => {
    stubBackups(jsonResponse({ backups: [] }));

    renderTab();

    await screen.findByTestId("backups-empty");
    expect(screen.getAllByRole("button", { name: "Back up now" })).toHaveLength(1);
  });

  it("lists what the adapter returns", async () => {
    const fetchMock = stubBackups(
      jsonResponse({ backups: [createMockBackup({ id: "orders-prod-1" })] }),
    );

    renderTab();

    expect(await screen.findByTestId("backup-row-orders-prod-1")).toBeInTheDocument();
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/databases/spoke-a/pds.orders-prod/backups");
  });

  it("starts a backup from the empty state", async () => {
    stubBackups(jsonResponse({ backups: [] }));
    const onBackUpNow = vi.fn();

    renderTab({ onBackUpNow });

    await userEvent.click(await screen.findByRole("button", { name: "Back up now" }));

    expect(onBackUpNow).toHaveBeenCalledTimes(1);
  });

  it("hands the row a restore was asked for to the dialog", async () => {
    const finished = createMockBackup({ id: "orders-prod-1", status: "succeeded" });
    stubBackups(jsonResponse({ backups: [finished] }));
    const onRestore = vi.fn();

    renderTab({ onRestore });

    await userEvent.click(await screen.findByRole("button", { name: "Restore" }));

    expect(onRestore).toHaveBeenCalledWith(finished);
  });

  it("reports a read that failed instead of an empty list", async () => {
    stubBackups(
      jsonResponse(
        { code: "ErrDownstream", message: "The database platform did not answer." },
        502,
      ),
    );

    renderTab();

    expect(await screen.findByText("Backups could not be read")).toBeInTheDocument();
    expect(screen.getByText("The database platform did not answer.")).toBeInTheDocument();
    expect(screen.queryByTestId("backups-empty")).not.toBeInTheDocument();
  });
});
