import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Button } from "@/components/ui/button";

describe("Button", () => {
  it.each([
    ["default", "bg-primary"],
    ["outline", "border"],
    ["ghost", "hover:bg-accent"],
    ["destructive", "bg-destructive"],
  ] as const)("renders the %s variant", (variant, expectedClass) => {
    render(<Button variant={variant}>Create database</Button>);

    const button = screen.getByRole("button", { name: "Create database" });
    expect(button).toBeInTheDocument();
    expect(button.className).toContain(expectedClass);
  });

  it("uses rounded-md and the pressed scale on every variant", () => {
    render(<Button>Create database</Button>);

    const button = screen.getByRole("button", { name: "Create database" });
    expect(button.className).toContain("rounded-md");
    expect(button.className).toContain("active:scale-[0.98]");
  });

  it("keeps the label and blocks clicks while loading", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <Button loading onClick={onClick}>
        Creating...
      </Button>,
    );

    const button = screen.getByRole("button", { name: /Creating\.\.\./ });
    expect(button).toBeDisabled();
    await user.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });

  it("renders as a child element when asChild is set", () => {
    render(
      <Button asChild>
        <a href="/databases">Databases</a>
      </Button>,
    );

    expect(screen.getByRole("link", { name: "Databases" })).toHaveAttribute("href", "/databases");
  });
});
