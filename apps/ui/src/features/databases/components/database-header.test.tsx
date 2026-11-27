import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DatabaseHeader } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

const actions = {
  onScale: () => {},
  onBackUpNow: () => {},
  backingUp: false,
  onDelete: () => {},
};

const ready = createMockDatabase({
  name: "orders-dev",
  status: "ready",
  connection: { host: "orders-dev.pds", port: 3306, username: "app" },
});

describe("DatabaseHeader", () => {
  it("opens the scale dialog", async () => {
    const onScale = vi.fn();

    renderWithProviders(<DatabaseHeader database={ready} {...actions} onScale={onScale} />);

    await userEvent.click(screen.getByRole("button", { name: "Scale" }));

    expect(onScale).toHaveBeenCalledTimes(1);
  });

  // Connect leads the Overview, so a second way in up here would be two
  // places for the same thing.
  it("has no Connect button", () => {
    renderWithProviders(<DatabaseHeader database={ready} {...actions} />);

    expect(screen.queryByRole("button", { name: "Connect" })).not.toBeInTheDocument();
  });
});
