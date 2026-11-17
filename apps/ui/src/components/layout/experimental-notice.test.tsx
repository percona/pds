import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ExperimentalNotice } from "@/components/layout/experimental-notice";
import { renderWithProviders } from "@/test/utils";

const disclaimer = [
  "Percona Database Service is an experimental project.",
  "PDS is not a generally available product, is not covered by Percona support commitments, and may change or be discontinued.",
  "Please use it for testing and evaluation, not production workloads.",
];

describe("ExperimentalNotice", () => {
  it("shows an Experimental label that opens the disclaimer, with no popup until then", () => {
    renderWithProviders(<ExperimentalNotice />);

    expect(screen.getByRole("button", { name: "Experimental, open the disclaimer" })).toBeVisible();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("opens the popup on click and says the disclaimer word for word, one line each", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ExperimentalNotice />);

    await user.click(screen.getByRole("button", { name: /Experimental/ }));

    const popup = await screen.findByRole("dialog");
    const lines = within(popup)
      .getAllByText(/./, { selector: "p" })
      .map((line) => line.textContent);
    expect(lines).toEqual(disclaimer);
  });

  it.each([["{Enter}"], [" "]])("opens the popup from the keyboard with %j", async (key) => {
    const user = userEvent.setup();
    renderWithProviders(<ExperimentalNotice />);

    await user.tab();
    expect(screen.getByRole("button", { name: /Experimental/ })).toHaveFocus();
    await user.keyboard(key);

    expect(await screen.findByRole("dialog")).toBeVisible();
  });

  it("closes on Escape and returns focus to the label", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ExperimentalNotice />);
    const label = screen.getByRole("button", { name: /Experimental/ });

    await user.click(label);
    await screen.findByRole("dialog");
    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(label).toHaveFocus();
  });

  it("closes with the Close button", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ExperimentalNotice />);
    const label = screen.getByRole("button", { name: /Experimental/ });

    await user.click(label);
    await user.click(await screen.findByRole("button", { name: "Close" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(label).toHaveFocus();
  });
});
