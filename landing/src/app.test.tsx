import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "./App";

describe("App", () => {
  it("has one h1 and every section heading", () => {
    render(<App />);

    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    for (const name of [
      "Day two, handled",
      "No lock-in",
      "Plans built around your own infrastructure",
      "Questions",
    ]) {
      expect(screen.getByRole("heading", { level: 2, name })).toBeInTheDocument();
    }
  });

  it("only links to sections that exist", () => {
    const { container } = render(<App />);

    const targets = [...container.querySelectorAll<HTMLAnchorElement>('a[href^="#"]')].map((link) =>
      link.getAttribute("href")!.slice(1),
    );
    expect(targets.length).toBeGreaterThan(0);
    for (const id of targets) {
      expect(document.getElementById(id), `#${id}`).not.toBeNull();
    }
  });

  it("makes sign in the way in", () => {
    render(<App />);

    const signIn = screen.getAllByRole("link", { name: "Sign in" });
    expect(signIn.length).toBeGreaterThan(0);
    for (const link of signIn) {
      expect(link).toHaveAttribute("href", "/app/");
    }
    expect(screen.queryByRole("link", { name: /request access/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /request access/i })).not.toBeInTheDocument();
  });

  it("names both engines outside the pricing table", () => {
    const { container } = render(<App />);
    const pricing = container.querySelector("#pricing");
    const outsidePricing = (container.textContent ?? "").replace(pricing?.textContent ?? "", "");

    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("MySQL and PostgreSQL");
    expect(outsidePricing).toContain("MySQL or PostgreSQL on prod-us-west-2");
    expect(outsidePricing).toContain("PostgreSQL");
    expect(outsidePricing).not.toMatch(/MongoDB/);
  });

  it("no longer marks PostgreSQL as coming in the pricing table", () => {
    const { container } = render(<App />);

    expect(container.querySelector("#pricing")).not.toHaveTextContent(/PostgreSQL\s*Soon/);
  });

  it("no longer asks what it costs in the FAQ", () => {
    render(<App />);

    expect(screen.queryByText(/How much does it cost/)).not.toBeInTheDocument();
  });

  it("calls a connected cluster an environment", () => {
    const { container } = render(<App />);
    const text = container.textContent ?? "";

    expect(text).toContain("Back to environments");
    expect(text).toContain("pds.percona.com/app/environments/prod-us-west-2");
    expect(text).toContain("What happens if I disconnect an environment?");
    expect(text).toContain("aws, eksctl, kubectl, helm, and python3");
    expect(text).toContain("Experimental");
    expect(text).toContain("is in alpha");
    expect(text).toContain("What does alpha mean for my databases?");
    expect(text).toContain("There is no backward compatibility yet");
    expect(text).toContain("recreate your setup from scratch");
    expect(text).not.toMatch(/\bpreview\b/i);
    expect(text).toContain("your database context");
    expect(text).not.toContain("example.com");
    expect(text).not.toContain("Back to Clusters");
    expect(text).not.toContain("disconnect a cluster");
    expect(text).not.toContain("your environment");
    expect(text).not.toMatch(/private alpha|by invitation|single-use invite|request access/i);
  });

  it("offers Amazon EKS and no other place to run, existing Kubernetes clusters included", () => {
    const { container } = render(<App />);
    const text = container.textContent ?? "";

    expect(text).toContain("Amazon EKS");
    expect(text).not.toMatch(/already have|already run|--local|join a Kubernetes/i);
    expect(text).not.toMatch(
      /\b(GKE|AKS|Azure|Google Cloud|kind)\b|on premises|hybrid|data center/i,
    );
  });

  it("names alerts and AI agents, and leaves out surfaces that are not on the page", () => {
    const { container } = render(<App />);
    const text = container.textContent ?? "";

    expect(text).toContain("Alerts");
    expect(text).toContain("AI agents");
    expect(text).not.toMatch(/\blogs?\b|investigate|recommendation|platform status/i);
    expect(text).not.toMatch(/roadmap|where we're going/i);
  });

  it("puts the AI agents highlight above pricing", () => {
    const { container } = render(<App />);

    const agents = container.querySelector("#ai-agents")!;
    const pricing = container.querySelector("#pricing")!;
    expect(agents.compareDocumentPosition(pricing) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
