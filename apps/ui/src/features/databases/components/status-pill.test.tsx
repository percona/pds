import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusPill } from "@/features/databases";

describe("StatusPill", () => {
  it("renders every shipped status as icon plus text", () => {
    render(
      <>
        <StatusPill kind="ready" label="Ready" />
        <StatusPill kind="creating" label="Creating" />
        <StatusPill kind="degraded" label="Degraded" />
        <StatusPill kind="failed" label="Failed" />
        <StatusPill kind="metrics-pending" label="Metrics pending" />
      </>,
    );

    for (const label of ["Ready", "Creating", "Degraded", "Failed", "Metrics pending"]) {
      const pill = screen.getByText(label);

      expect(pill).toBeInTheDocument();
      expect(pill.closest("[data-status]")?.querySelector("svg")).toBeTruthy();
    }
  });

  it("pulses only while the database is still coming up", () => {
    const { rerender } = render(<StatusPill kind="creating" label="Creating" />);

    expect(screen.getByTestId("status-pill").className).toContain("animate-soft-pulse");

    rerender(<StatusPill kind="ready" label="Ready" />);

    expect(screen.getByTestId("status-pill").className).not.toContain("animate-soft-pulse");
  });

  it("carries the kind so a test never has to read a color", () => {
    render(<StatusPill kind="failed" label="Failed" />);

    expect(screen.getByTestId("status-pill")).toHaveAttribute("data-status", "failed");
  });
});
