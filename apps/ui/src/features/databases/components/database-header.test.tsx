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
  it("opens the connect dialog", async () => {
    const onConnect = vi.fn();

    renderWithProviders(<DatabaseHeader database={ready} onConnect={onConnect} {...actions} />);

    await userEvent.click(screen.getByRole("button", { name: "Connect" }));

    expect(onConnect).toHaveBeenCalledTimes(1);
  });

  // The detail page sends a database with no connection to the provisioning
  // screen before this renders, but the header cannot know that, and opening
  // a dialog onto nothing is worse than a control that is plainly off. The
  // sentence saying why lives on the connect card, because a disabled button
  // takes neither hover nor focus and could not carry one.
  it("disables Connect when there is no connection to open", () => {
    const coming = createMockDatabase({ name: "orders-dev", status: "ready" });

    renderWithProviders(<DatabaseHeader database={coming} onConnect={() => {}} {...actions} />);

    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
  });
});
