import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ThemeToggle } from "@/components/layout/theme-toggle";
import { renderWithProviders } from "@/test/utils";

describe("ThemeToggle", () => {
  it("adds the dark class to the html element", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ThemeToggle />);

    expect(document.documentElement.classList.contains("dark")).toBe(false);

    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));

    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("removes the dark class when toggled back to light", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ThemeToggle />);

    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));
    await user.click(screen.getByRole("button", { name: "Switch to light theme" }));

    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("persists the chosen theme", async () => {
    const user = userEvent.setup();
    renderWithProviders(<ThemeToggle />);

    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));

    expect(window.localStorage.getItem("pds-theme")).toBe("dark");
  });
});
