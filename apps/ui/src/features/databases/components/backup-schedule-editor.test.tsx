import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BackupScheduleEditor } from "@/features/databases";
import type { Database } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

// Mid July, when Berlin is two hours ahead of UTC.
const july = new Date("2026-07-15T12:00:00Z");

const daily = createMockDatabase({
  id: "spoke-a/pds.orders",
  name: "orders",
  profile: "prod",
  backupSchedule: {
    name: "daily",
    enabled: true,
    cron: "0 2 * * *",
    period: "daily",
    minute: 0,
    hour: 2,
    retentionCopies: 7,
  },
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubSave(response: Response) {
  const fetchMock = vi.fn((_input: string, _init?: RequestInit) => Promise.resolve(response));
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function sentBody(fetchMock: ReturnType<typeof stubSave>): unknown {
  const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];

  return JSON.parse(String(init.body));
}

function renderEditor(database: Database = daily, enabled = true) {
  return renderWithProviders(
    <BackupScheduleEditor
      database={database}
      enabled={enabled}
      timeZone="Europe/Berlin"
      now={july}
    />,
  );
}

function saveButton() {
  return screen.getByRole("button", { name: "Save schedule" });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

describe("BackupScheduleEditor", () => {
  it("opens on the schedule the database has, in the viewer's time", () => {
    renderEditor();

    expect(screen.getByLabelText("Frequency")).toHaveValue("daily");
    expect(screen.getByLabelText("Time")).toHaveValue("04:00");
    expect(screen.getByLabelText("Copies to keep")).toHaveValue("7");
    expect(saveButton()).toBeDisabled();
  });

  it("names the time zone and warns about daylight saving", () => {
    renderEditor();

    expect(screen.getByText(/Europe\/Berlin/)).toBeInTheDocument();
    expect(screen.getByText(/can move by an hour when daylight saving/)).toBeInTheDocument();
  });

  it("opens a database with no schedule on off, and asks for nothing else", () => {
    renderEditor(createMockDatabase({ id: "spoke-a/pds.orders-dev" }));

    expect(screen.getByLabelText("Frequency")).toHaveValue("off");
    expect(screen.queryByLabelText("Time")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Copies to keep")).not.toBeInTheDocument();
  });

  it("saves a weekly schedule as UTC", async () => {
    const fetchMock = stubSave(jsonResponse(daily));
    renderEditor();

    await userEvent.selectOptions(screen.getByLabelText("Frequency"), "weekly");
    await userEvent.selectOptions(screen.getByLabelText("Day"), "Monday");
    fireEvent.change(screen.getByLabelText("Time"), { target: { value: "01:30" } });
    await userEvent.clear(screen.getByLabelText("Copies to keep"));
    await userEvent.type(screen.getByLabelText("Copies to keep"), "4");

    await waitFor(() => expect(saveButton()).toBeEnabled());
    await userEvent.click(saveButton());

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/databases/spoke-a/pds.orders/backup-schedule");
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST");
    expect(sentBody(fetchMock)).toEqual({
      enabled: true,
      period: "weekly",
      minute: 30,
      hour: 23,
      weekday: 0,
      retentionCopies: 4,
    });
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Backup schedule saved"));
  });

  it("saves an hourly schedule by the minute", async () => {
    const fetchMock = stubSave(jsonResponse(daily));
    renderEditor();

    await userEvent.selectOptions(screen.getByLabelText("Frequency"), "hourly");
    expect(screen.queryByLabelText("Time")).not.toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Minutes past the hour"));
    await userEvent.type(screen.getByLabelText("Minutes past the hour"), "15");
    await userEvent.click(saveButton());

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(sentBody(fetchMock)).toEqual({
      enabled: true,
      period: "hourly",
      minute: 15,
      retentionCopies: 7,
    });
  });

  it("turns the schedule off", async () => {
    const fetchMock = stubSave(jsonResponse(daily));
    renderEditor();

    await userEvent.selectOptions(screen.getByLabelText("Frequency"), "off");
    expect(screen.queryByLabelText("Time")).not.toBeInTheDocument();
    await userEvent.click(saveButton());

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(sentBody(fetchMock)).toEqual({ enabled: false });
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Backup schedule turned off"));
  });

  it("opens a schedule that is off on its kept settings", async () => {
    renderEditor(
      createMockDatabase({
        id: "spoke-a/pds.orders",
        backupSchedule: {
          name: "daily",
          enabled: false,
          cron: "45 * * * *",
          period: "hourly",
          minute: 45,
          retentionCopies: 12,
        },
      }),
    );

    expect(screen.getByLabelText("Frequency")).toHaveValue("off");
    await userEvent.selectOptions(screen.getByLabelText("Frequency"), "hourly");
    expect(screen.getByLabelText("Minutes past the hour")).toHaveValue("45");
    expect(screen.getByLabelText("Copies to keep")).toHaveValue("12");
  });

  it("opens a cron set elsewhere on custom and can turn it off", async () => {
    const fetchMock = stubSave(jsonResponse(daily));
    renderEditor(
      createMockDatabase({
        id: "spoke-a/pds.orders",
        backupSchedule: { name: "nightly", enabled: true, cron: "0 3 */2 * *" },
      }),
    );

    expect(screen.getByLabelText("Frequency")).toHaveValue("custom");
    expect(saveButton()).toBeDisabled();

    await userEvent.selectOptions(screen.getByLabelText("Frequency"), "off");
    await waitFor(() => expect(saveButton()).toBeEnabled());
    await userEvent.click(saveButton());

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(sentBody(fetchMock)).toEqual({ enabled: false });
  });

  it("refuses a minute out of range before sending", async () => {
    const fetchMock = stubSave(jsonResponse(daily));
    renderEditor();

    await userEvent.selectOptions(screen.getByLabelText("Frequency"), "hourly");
    await userEvent.clear(screen.getByLabelText("Minutes past the hour"));
    await userEvent.type(screen.getByLabelText("Minutes past the hour"), "60");
    await userEvent.tab();

    expect(await screen.findByText("Minutes must be between 0 and 59.")).toBeInTheDocument();
    await waitFor(() => expect(saveButton()).toBeDisabled());
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("refuses copies out of range before sending", async () => {
    renderEditor();

    await userEvent.clear(screen.getByLabelText("Copies to keep"));
    await userEvent.type(screen.getByLabelText("Copies to keep"), "31");
    await userEvent.tab();

    expect(await screen.findByText("Copies must be between 1 and 30.")).toBeInTheDocument();
    await waitFor(() => expect(saveButton()).toBeDisabled());
  });

  it("shows the reason a save was refused", async () => {
    stubSave(
      jsonResponse(
        { code: "ErrNotImplemented", message: "Backups are not set up for this environment." },
        501,
      ),
    );
    renderEditor();

    await userEvent.selectOptions(screen.getByLabelText("Frequency"), "weekly");
    await userEvent.click(saveButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Backups are not set up for this environment.",
    );
  });

  it("cannot save while the environment cannot take the change", () => {
    renderEditor(daily, false);

    expect(screen.getByLabelText("Frequency")).toBeDisabled();
    expect(saveButton()).toBeDisabled();
  });
});
