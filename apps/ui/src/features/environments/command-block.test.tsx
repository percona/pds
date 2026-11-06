import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { renderWithProviders } from "@/test/utils";
import { CommandBlock } from "./command-block";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

let written: string[];

beforeEach(() => {
  written = [];
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: (text: string) => Promise.resolve(void written.push(text)) },
  });
});

afterEach(() => {
  Reflect.deleteProperty(navigator, "clipboard");
  vi.clearAllMocks();
});

describe("CommandBlock", () => {
  it("copies with the default button and toast", async () => {
    renderWithProviders(<CommandBlock command="echo hi" />);

    await userEvent.click(screen.getByRole("button", { name: "Copy command" }));

    expect(written).toEqual(["echo hi"]);
    expect(toast.success).toHaveBeenCalledWith("Command copied");
  });

  it("names what it copies when given a label", async () => {
    renderWithProviders(<CommandBlock command="{}" label="Copy config" copied="Config copied" />);

    expect(screen.queryByRole("button", { name: "Copy command" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Copy config" }));

    expect(toast.success).toHaveBeenCalledWith("Config copied");
  });
});
