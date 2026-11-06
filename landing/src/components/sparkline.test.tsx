import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Sparkline } from "./sparkline";

describe("Sparkline", () => {
  it("draws nothing with fewer than two points, instead of NaN coordinates", () => {
    for (const points of [[], [42]]) {
      const { container, unmount } = render(<Sparkline points={points} />);
      expect(container.querySelector("svg")).toBeNull();
      unmount();
    }
  });

  it("draws one finite coordinate per point", () => {
    const { container } = render(<Sparkline points={[10, 20, 15]} />);
    const coordinates = container.querySelector("polyline")!.getAttribute("points")!.split(" ");

    expect(coordinates).toHaveLength(3);
    expect(coordinates.join(" ")).not.toMatch(/NaN|Infinity/);
  });
});
