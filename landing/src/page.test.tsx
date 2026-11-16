import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

function words(text: string | null) {
  return (text ?? "").trim().split(/\s+/).filter(Boolean).length;
}

function reduceMotion(on: boolean) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: on && query.includes("prefers-reduced-motion"),
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  }));
}

describe("Landing page text and motion", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("keeps visible body copy at 350 words, pricing and FAQ answers aside", () => {
    const { container } = render(<App />);
    const page = container.cloneNode(true) as HTMLElement;
    page.querySelector("#pricing")?.remove();
    page.querySelectorAll("details > :not(summary)").forEach((answer) => answer.remove());
    page.querySelectorAll("header, footer, script, style").forEach((el) => el.remove());

    expect(words(page.textContent)).toBeLessThanOrEqual(350);
  });

  it("starts every FAQ answer collapsed", () => {
    const { container } = render(<App />);
    const questions = container.querySelectorAll("#faq details");

    expect(questions.length).toBeGreaterThan(0);
    for (const question of questions) {
      expect(question).not.toHaveAttribute("open");
    }
  });

  it("shows the finished frames when the visitor prefers reduced motion", () => {
    reduceMotion(true);
    const { container } = render(<App />);

    const answer = container.querySelectorAll(".fx-answer .ss-stream__w");
    expect(answer.length).toBeGreaterThan(0);
    for (const word of answer) {
      expect(word).toHaveAttribute("data-on", "true");
    }
    expect(screen.getAllByText("Ready").length).toBeGreaterThan(0);
  });

  it("keeps every demo out of reach of the keyboard and assistive tech", () => {
    const { container } = render(<App />);
    const stages = container.querySelectorAll(".ss-stage");

    expect(stages.length).toBeGreaterThan(1);
    for (const stage of stages) {
      expect(stage.closest("[inert]")).not.toBeNull();
      expect(stage.closest('[aria-hidden="true"]')).not.toBeNull();
    }
  });

  it("hides the replay button when the visitor prefers reduced motion", () => {
    reduceMotion(true);
    render(<App />);

    expect(screen.queryByRole("button", { name: "Replay the preview" })).not.toBeInTheDocument();
  });

  it("offers the replay button otherwise", () => {
    reduceMotion(false);
    render(<App />);

    expect(screen.getByRole("button", { name: "Replay the preview" })).toBeInTheDocument();
  });
});
