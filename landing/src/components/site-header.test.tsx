import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { SiteHeader } from "./site-header";

describe("SiteHeader", () => {
  it("makes sign in the main call to action, on the console", () => {
    render(<SiteHeader />);

    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/app/");
    expect(screen.queryByRole("link", { name: /request access/i })).not.toBeInTheDocument();
  });

  it("links to the public repository", () => {
    render(<SiteHeader />);

    const link = screen.getByRole("link", { name: "GitHub" });
    expect(link).toHaveAttribute("href", "https://github.com/percona/pds");
    expect(link).toHaveAttribute("rel", "noreferrer");
  });

  it("flips the theme and remembers it under the console's key", async () => {
    const user = userEvent.setup();
    render(<SiteHeader />);

    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));

    expect(document.documentElement).toHaveClass("dark");
    expect(localStorage.getItem("pds-theme")).toBe("dark");
    expect(screen.getByRole("button", { name: "Switch to light theme" })).toBeInTheDocument();
  });
});
