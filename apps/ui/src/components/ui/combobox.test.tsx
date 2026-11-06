import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Combobox, panelPlacement } from "./combobox";

const options = [
  { value: "claude-opus-5", label: "Claude Opus 5" },
  { value: "claude-sonnet-5", label: "Claude Sonnet 5" },
  { value: "claude-haiku-4-5", label: "Claude Haiku 4.5" },
];

function renderCombobox(overrides: Partial<Parameters<typeof Combobox>[0]> = {}) {
  const onChange = vi.fn();
  render(
    <Combobox
      id="model"
      ariaLabel="Model"
      options={options}
      value="claude-opus-5"
      onChange={onChange}
      searchLabel="Search models"
      emptyText="No models match that search."
      {...overrides}
    />,
  );

  return { onChange, trigger: screen.getByRole("combobox", { name: "Model" }) };
}

describe("panelPlacement", () => {
  it("never lets the panel run past the height of the window", () => {
    const { maxHeight } = panelPlacement({ top: 40, bottom: 72, left: 0, width: 200 }, 400);

    expect(maxHeight).toBeLessThanOrEqual(400);
  });

  it("opens downward when the space below fits the panel", () => {
    const placement = panelPlacement({ top: 100, bottom: 132, left: 0, width: 200 }, 900);

    expect(placement.direction).toBe("down");
    expect(placement.top).toBeGreaterThan(132);
  });

  it("flips upward when the trigger sits near the bottom of the window", () => {
    const placement = panelPlacement({ top: 820, bottom: 852, left: 0, width: 200 }, 900);

    expect(placement.direction).toBe("up");
    expect(placement.top + placement.maxHeight).toBeLessThanOrEqual(820);
  });

  it("keeps the panel on screen when the trigger is wider than the window", () => {
    const placement = panelPlacement({ top: 10, bottom: 42, left: 900, width: 400 }, 900, 1000);

    expect(placement.left).toBeGreaterThanOrEqual(0);
    expect(placement.left + placement.width).toBeLessThanOrEqual(1000);
  });
});

describe("Combobox", () => {
  it("shows the selected label on the trigger", () => {
    const { trigger } = renderCombobox();

    expect(trigger).toHaveTextContent("Claude Opus 5");
  });

  it("shows the placeholder when nothing is selected", () => {
    renderCombobox({ value: "", placeholder: "Choose a model" });

    expect(screen.getByRole("combobox", { name: "Model" })).toHaveTextContent("Choose a model");
  });

  it("opens a scrollable list with a bounded height", async () => {
    const { trigger } = renderCombobox();

    await userEvent.click(trigger);

    const list = screen.getByRole("listbox");
    const panel = screen.getByTestId("combobox-panel");
    const maxHeight = Number.parseFloat(panel.style.maxHeight);

    expect(maxHeight).toBeGreaterThan(0);
    expect(maxHeight).toBeLessThanOrEqual(window.innerHeight);
    expect(list.className).toContain("overflow-y-auto");
    expect(screen.getAllByRole("option")).toHaveLength(3);
  });

  it("focuses the search box on open so a long list can be narrowed by typing", async () => {
    const { trigger } = renderCombobox();

    await userEvent.click(trigger);

    expect(screen.getByLabelText("Search models")).toHaveFocus();
  });

  it("filters by label and by model id", async () => {
    const { trigger } = renderCombobox();

    await userEvent.click(trigger);
    await userEvent.type(screen.getByLabelText("Search models"), "haiku");

    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option")).toHaveTextContent("Claude Haiku 4.5");

    await userEvent.clear(screen.getByLabelText("Search models"));
    await userEvent.type(screen.getByLabelText("Search models"), "sonnet-5");

    expect(screen.getByRole("option")).toHaveTextContent("Claude Sonnet 5");
  });

  it("says so when the search matches nothing", async () => {
    const { trigger } = renderCombobox();

    await userEvent.click(trigger);
    await userEvent.type(screen.getByLabelText("Search models"), "gpt");

    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText("No models match that search.")).toBeInTheDocument();
  });

  it("picks an option with the mouse and closes", async () => {
    const { trigger, onChange } = renderCombobox();

    await userEvent.click(trigger);
    await userEvent.click(screen.getByRole("option", { name: /Claude Sonnet 5/ }));

    expect(onChange).toHaveBeenCalledWith("claude-sonnet-5");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("picks an option with the arrow keys and enter", async () => {
    const { trigger, onChange } = renderCombobox();

    await userEvent.click(trigger);
    await userEvent.keyboard("{ArrowDown}{Enter}");

    expect(onChange).toHaveBeenCalledWith("claude-sonnet-5");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("starts the arrow keys on the selected option", async () => {
    const { trigger, onChange } = renderCombobox({ value: "claude-haiku-4-5" });

    await userEvent.click(trigger);
    await userEvent.keyboard("{Enter}");

    expect(onChange).toHaveBeenCalledWith("claude-haiku-4-5");
  });

  it("closes on escape without changing the selection", async () => {
    const { trigger, onChange } = renderCombobox();

    await userEvent.click(trigger);
    await userEvent.keyboard("{Escape}");

    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
    expect(trigger).toHaveFocus();
  });

  it("closes when the pointer goes somewhere else", async () => {
    renderCombobox();

    await userEvent.click(screen.getByRole("combobox", { name: "Model" }));
    await userEvent.click(document.body);

    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("does not open while disabled", async () => {
    const { trigger } = renderCombobox({ disabled: true });

    await userEvent.click(trigger);

    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });
});
