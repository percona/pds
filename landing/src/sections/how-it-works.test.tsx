import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { HowItWorks } from "./how-it-works";

describe("HowItWorks", () => {
  it("lists the four steps in order", () => {
    render(<HowItWorks />);

    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      expect.stringContaining("Connect an environment"),
      expect.stringContaining("Pick a profile"),
      expect.stringContaining("Create a database"),
      expect.stringContaining("Operate it"),
    ]);
  });

  it("opens on the connect step with the join token masked", () => {
    render(<HowItWorks />);

    const panel = screen.getByRole("tabpanel");
    expect(panel).toHaveTextContent("pds.percona.com/api/internal/connect/bootstrap.sh");
    expect(panel).toHaveTextContent("four t3.2xlarge nodes");
    expect(panel).toHaveTextContent("Name the environment in the console");
    expect(panel).toHaveTextContent("Kubernetes cluster you already have");
    expect(panel).toHaveTextContent("An agent on that Kubernetes cluster");
    expect(panel).toHaveTextContent("monitoring, on that Kubernetes cluster");
    expect(panel).toHaveTextContent("connects an environment to your control plane");
    expect(panel).toHaveTextContent("NAT gateway");
    expect(panel).not.toHaveTextContent("example.com");
    expect(panel).not.toHaveTextContent("PMM");
    expect(panel).not.toHaveTextContent(/--token [A-Za-z0-9]{8,}/);
  });

  it("creates the database on the environment", async () => {
    const user = userEvent.setup();
    render(<HowItWorks />);

    await user.click(screen.getByRole("tab", { name: /Create a database/ }));

    expect(screen.getByRole("tabpanel")).toHaveTextContent("create it on the environment");
  });

  it("restores into a new database, the way a hub does", async () => {
    const user = userEvent.setup();
    render(<HowItWorks />);

    await user.click(screen.getByRole("tab", { name: /Operate it/ }));

    const panel = screen.getByRole("tabpanel");
    expect(panel).toHaveTextContent("Restore");
    expect(panel).toHaveTextContent("The password is never shown");
    expect(panel).toHaveTextContent("private database");
    expect(panel).not.toHaveTextContent(/Investigate/);
  });

  it("shows the three profiles once that step is picked", async () => {
    const user = userEvent.setup();
    render(<HowItWorks />);

    await user.click(screen.getByRole("tab", { name: /Pick a profile/ }));

    const panel = screen.getByRole("tabpanel");
    for (const profile of ["Dev", "Staging", "Prod"]) {
      expect(panel).toHaveTextContent(profile);
    }
  });
});
