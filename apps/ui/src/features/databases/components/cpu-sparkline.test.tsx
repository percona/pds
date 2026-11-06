import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CpuSparkline } from "@/features/databases";

describe("CpuSparkline", () => {
  it("states the current value and the trend in text, not only in the line", () => {
    render(<CpuSparkline points={[10, 20, 30, 42]} />);

    expect(screen.getByRole("img", { name: "CPU 42%, trending up" })).toBeInTheDocument();
  });

  it("reads a falling series as trending down and a flat one as steady", () => {
    const { rerender } = render(<CpuSparkline points={[40, 30, 12]} />);

    expect(screen.getByRole("img", { name: "CPU 12%, trending down" })).toBeInTheDocument();

    rerender(<CpuSparkline points={[20, 20, 20]} />);

    expect(screen.getByRole("img", { name: "CPU 20%, steady" })).toBeInTheDocument();
  });

  it("draws nothing when there is not enough of a series to be a line", () => {
    const { container } = render(<CpuSparkline points={[42]} />);

    expect(container.querySelector("svg")).toBeNull();
  });
});
