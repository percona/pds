import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Operations } from "./operations";

describe("Operations", () => {
  it("lists the operations as tabs, one line each", () => {
    render(<Operations />);

    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "Scale",
      "Backups",
      "Connect",
      "Alerts",
      "AI agents",
      "Support",
    ]);
    expect(screen.getByRole("tabpanel")).toHaveTextContent("Change CPU, memory, storage");
  });

  it("moves between tabs with the arrow keys and shows that tab's line", async () => {
    const user = userEvent.setup();
    render(<Operations />);

    await user.click(screen.getByRole("tab", { name: "Scale" }));
    await user.keyboard("{ArrowRight}");

    expect(screen.getByRole("tab", { name: "Backups" })).toHaveFocus();
    expect(screen.getByRole("tabpanel")).toHaveTextContent("A restore makes a new database");
  });

  it("keeps credentials off screen", async () => {
    const user = userEvent.setup();
    render(<Operations />);

    await user.click(screen.getByRole("tab", { name: "Connect" }));

    expect(screen.getByRole("tabpanel")).toHaveTextContent("Credentials stay off screen");
  });

  it("sends support with the context attached and secrets removed", async () => {
    const user = userEvent.setup();
    render(<Operations />);

    await user.click(screen.getByRole("tab", { name: "Support" }));

    expect(screen.getByRole("tabpanel")).toHaveTextContent(
      "with the context attached and secrets removed",
    );
  });
});
