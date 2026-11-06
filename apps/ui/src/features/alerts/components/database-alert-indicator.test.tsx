import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { DatabaseAlertIndicator } from "./database-alert-indicator";

describe("DatabaseAlertIndicator", () => {
  it("shows the count and the highest severity as text", () => {
    renderWithProviders(
      <DatabaseAlertIndicator summary={{ total: 1, worst: "critical", worstCount: 1 }} />,
    );

    expect(screen.getByText("1 critical")).toBeInTheDocument();
  });

  it("says how many other alerts are firing", () => {
    renderWithProviders(
      <DatabaseAlertIndicator summary={{ total: 4, worst: "warning", worstCount: 2 }} />,
    );

    expect(screen.getByText("2 warning, 2 more")).toBeInTheDocument();
  });

  it("renders nothing when no alert is firing", () => {
    const { container } = renderWithProviders(<DatabaseAlertIndicator summary={undefined} />);

    expect(container).toBeEmptyDOMElement();
  });
});
