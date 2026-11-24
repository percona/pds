import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Pricing } from "./pricing";

describe("Pricing", () => {
  it("offers the two plans, both pointing at sign in", () => {
    render(<Pricing />);

    expect(screen.getByRole("heading", { level: 3, name: "Yourself" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 3, name: "Us" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Get started" })).toHaveAttribute("href", "/app/");
    expect(screen.getByRole("link", { name: "Contact Percona" })).toHaveAttribute("href", "/app/");
  });

  it("compares the plans by row, with no backups row", () => {
    render(<Pricing />);
    const table = screen.getByRole("table", { name: "Compare plans" });
    const rows = within(table)
      .getAllByRole("rowheader")
      .map((cell) => cell.textContent);

    expect(rows).toEqual([
      expect.stringContaining("Database engines"),
      expect.stringContaining("Provisioning templates"),
      expect.stringContaining("Fleet policies"),
      expect.stringContaining("Monitoring"),
      expect.stringContaining("AI DBA"),
      expect.stringContaining("Channel"),
      expect.stringContaining("Coverage"),
      expect.stringContaining("Response SLA"),
      expect.stringContaining("Live sessions with Percona experts"),
      expect.stringContaining("Dedicated Service Delivery Manager"),
    ]);
    expect(rows.some((row) => /^backups/i.test(row ?? ""))).toBe(false);
  });

  it("says in words when a plan does not include something", () => {
    render(<Pricing />);
    const table = screen.getByRole("table", { name: "Compare plans" });

    expect(within(table).getAllByText("Not included").length).toBeGreaterThan(0);
  });

  it("lists the three engines without a soon badge", () => {
    render(<Pricing />);
    const engines = screen.getByRole("row", { name: /Database engines/ });

    expect(engines).toHaveTextContent("MySQL");
    expect(engines).toHaveTextContent("PostgreSQL");
    expect(engines).toHaveTextContent("MongoDB");
    expect(engines).not.toHaveTextContent("Soon");
  });
});
