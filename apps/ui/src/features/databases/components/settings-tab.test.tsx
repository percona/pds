import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SettingsTab } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

const database = createMockDatabase({
  id: "pds.orders-prod",
  name: "orders-prod",
  profile: "prod",
  members: 3,
});

describe("SettingsTab", () => {
  // Delete sits at the bottom of Settings, behind its own heading, so it is
  // never the button next to the one someone meant to press.
  it("puts delete in a danger zone of its own", () => {
    renderWithProviders(<SettingsTab database={database} onDelete={vi.fn()} />);

    const zone = within(screen.getByTestId("danger-zone"));

    expect(zone.getByRole("heading", { name: "Danger zone" })).toBeInTheDocument();
    expect(screen.getByTestId("danger-zone")).toHaveTextContent(
      "Deleting orders-prod takes its data and its backups with it.",
    );
    expect(zone.getByRole("button", { name: "Delete" })).toBeEnabled();
  });

  it("says PostgreSQL backups stay in the backup storage", () => {
    renderWithProviders(
      <SettingsTab
        database={createMockDatabase({ ...database, engine: "postgresql" })}
        onDelete={vi.fn()}
      />,
    );

    const zone = screen.getByTestId("danger-zone");
    expect(zone).toHaveTextContent("PostgreSQL backups stay in the backup storage");
    expect(zone).not.toHaveTextContent("its data and its backups");
  });

  it("hands the delete to the page, which owns the dialog", async () => {
    const onDelete = vi.fn();
    renderWithProviders(<SettingsTab database={database} onDelete={onDelete} />);

    await userEvent.click(within(screen.getByTestId("danger-zone")).getByRole("button"));

    expect(onDelete).toHaveBeenCalledTimes(1);
  });

  // The rest of Settings is not built, and a tab that says so is better than one
  // that offers a field which cannot be saved.
  it("says which settings are not editable yet", () => {
    renderWithProviders(<SettingsTab database={database} onDelete={vi.fn()} />);

    expect(screen.getByTestId("settings-not-editable")).toHaveTextContent(
      "Name, profile, and backup destination are not editable yet.",
    );
  });
});
