import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Proof } from "./proof";

const sources = [
  ["OpenEverest", "https://github.com/openeverest/openeverest"],
  ["Operator for MySQL", "https://github.com/percona/percona-xtradb-cluster-operator"],
  ["Operator for PostgreSQL", "https://github.com/percona/percona-postgresql-operator"],
];

describe("Proof", () => {
  it("links each integration to its source, and shows the OpenEverest logo with alt text", () => {
    render(<Proof />);

    const links = screen
      .getAllByRole("link")
      .filter((link) => link.getAttribute("href")?.includes("github.com"));
    expect(links.map((link) => link.getAttribute("href"))).toEqual(sources.map(([, href]) => href));
    for (const link of links) {
      expect(link).toHaveAttribute("rel", "noreferrer");
    }
    const logo = screen.getByAltText("OpenEverest logo");
    expect(logo).toHaveAttribute("src");
    expect(logo).toHaveAttribute("width", "1222");
    expect(logo).toHaveAttribute("height", "425");
    expect(logo.getAttribute("src")).toMatch(/\.png/);
    expect(screen.getAllByRole("img")).toHaveLength(1);
  });

  it("puts OpenEverest first and largest, before the Operators", () => {
    render(<Proof />);

    const names = ["OpenEverest", "Percona Operator for MySQL", "Percona Operator for PostgreSQL"];
    const cards = screen.getByRole("list", { name: "Open source projects" });
    const text = cards.textContent ?? "";
    const positions = names.map((name) => text.indexOf(name));
    expect(positions).toEqual([...positions].sort((a, b) => a - b));
    expect(within(cards).getByText("OpenEverest").className).toMatch(/text-2xl/);
  });

  it("states only sourced figures, and no license it did not confirm", () => {
    render(<Proof />);

    expect(screen.getByText("+900 GitHub Stars")).toBeInTheDocument();
    expect(screen.getByText("+600 GitHub Stars")).toBeInTheDocument();
    expect(screen.getByText("+300 GitHub Stars")).toBeInTheDocument();
    expect(screen.getAllByText("Apache-2.0")).toHaveLength(2);
  });

  it("writes the track record and customer count as the page words them", () => {
    render(<Proof />);

    expect(screen.getByText("Since 2006 working with open source databases")).toBeInTheDocument();
    expect(screen.getByText("+3,000 consulting and support customers")).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(/more than/i);
  });

  it("highlights AI agents with an agent using the real tools, and no install command", () => {
    const { container } = render(<Proof />);

    expect(screen.getByRole("heading", { level: 3 })).toHaveTextContent("AI agent");
    const demo = container.querySelector("#ai-agents div[aria-hidden='true']")!;
    expect(demo).toHaveAttribute("inert");
    expect(demo.querySelector("a, button, input, [tabindex]")).toBeNull();
    const text = container.querySelector("#ai-agents")!.textContent ?? "";
    expect(text).toContain("list_databases");
    expect(text).toContain("list_alerts");
    expect(text).not.toMatch(/claude plugin|marketplace add|npx /);
    expect(screen.getByText(/calls list_databases and list_alerts/)).toBeInTheDocument();
  });
});
