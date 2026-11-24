import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Hero } from "./hero";

function words(text: string | null) {
  return (text ?? "").trim().split(/\s+/).filter(Boolean).length;
}

describe("Hero", () => {
  it("says what PDS is in a short headline and one sentence, before any choice", () => {
    render(<Hero />);

    const title = screen.getByRole("heading", { level: 1 });
    expect(words(title.textContent)).toBeLessThanOrEqual(10);
    expect(title).toHaveTextContent("MySQL, PostgreSQL, and MongoDB");

    const sentence = screen.getByText(/creates an Amazon EKS cluster in your AWS account/);
    expect(words(sentence.textContent)).toBeLessThanOrEqual(20);
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/app/");
  });

  it("carries an Experimental stamp beside the headline, in place of Alpha", () => {
    render(<Hero />);

    expect(screen.getByText("Experimental")).toBeVisible();
    expect(screen.queryByText("Alpha")).not.toBeInTheDocument();
  });

  it("starts on a default that already shows the payoff", () => {
    render(<Hero />);

    expect(screen.getByRole("radio", { name: "MySQL" })).toBeChecked();
    expect(screen.getByRole("radio", { name: /Prod/ })).toBeChecked();
    expect(screen.getByText(/goes from Creating to Ready/)).toBeInTheDocument();
    expect(screen.getByText(/bootstrap\.sh/)).toBeInTheDocument();
  });

  it("offers three engines and three profiles as radio groups, and fixes where it runs", () => {
    render(<Hero />);

    for (const [group, names] of [
      ["Engine", ["MySQL", "PostgreSQL", "MongoDB"]],
      ["Profile", ["Dev", "Staging", "Prod"]],
    ] as const) {
      const radios = within(screen.getByRole("group", { name: group })).getAllByRole("radio");
      expect(radios.map((radio) => radio.closest("label")?.textContent)).toEqual(
        names.map((name) => expect.stringContaining(name)),
      );
    }
    expect(screen.queryByRole("group", { name: "Where it runs" })).not.toBeInTheDocument();
    expect(screen.getByText("Runs on Amazon EKS in your AWS account")).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/already have|--local/);
  });

  it("moves the choice with the arrow keys", async () => {
    const user = userEvent.setup();
    render(<Hero />);

    await user.click(screen.getByRole("radio", { name: "MySQL" }));
    await user.keyboard("{ArrowRight}");

    expect(screen.getByRole("radio", { name: "PostgreSQL" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "PostgreSQL" })).toHaveFocus();
  });

  it("describes the choice for readers who cannot see the preview", async () => {
    const user = userEvent.setup();
    render(<Hero />);

    await user.click(screen.getByRole("radio", { name: "PostgreSQL" }));
    await user.click(screen.getByRole("radio", { name: /Dev/ }));

    expect(screen.getByText(/PostgreSQL database payments, Dev profile/)).toBeInTheDocument();
  });

  it("shows the console's join command with the token masked and never a real one", () => {
    render(<Hero />);

    const command = screen.getByText(/bootstrap\.sh/);
    expect(command).toHaveTextContent("pds.percona.com/api/internal/connect/bootstrap.sh");
    expect(command).toHaveTextContent("bash -s -- --token");
    expect(command).not.toHaveTextContent(/--token '[A-Za-z0-9]{8,}/);
  });

  it("keeps the faux console out of reach: hidden from assistive tech and inert", () => {
    const { container } = render(<Hero />);

    const demo = container.querySelector(".ss-fit")!.closest("[aria-hidden='true']");
    expect(demo).not.toBeNull();
    expect(demo).toHaveAttribute("inert");
  });

  it("shows each engine's mark from Percona's docs beside its name", () => {
    const { container } = render(<Hero />);

    const group = within(screen.getByRole("group", { name: "Engine" }));
    for (const [name, mark] of [
      ["MySQL", /mysql-mark.*\.png/],
      ["PostgreSQL", /postgresql-mark.*\.png/],
      ["MongoDB", /mongodb-mark.*\.svg|^data:image\/svg\+xml/],
    ] as const) {
      const label = group.getByRole("radio", { name }).closest("label")!;
      expect(label.textContent).toBe(name);
      const logo = label.querySelector("img")!;
      expect(logo.getAttribute("src")).toMatch(mark);
      expect(logo).toHaveAttribute("alt", "");
      expect(logo).toHaveAttribute("width", "24");
      expect(logo).toHaveAttribute("height", "24");
    }
    expect(container.querySelectorAll("fieldset img")).toHaveLength(3);
  });
});
