import { screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubHome(bodies: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: string) => {
      const path = String(input).split("?")[0];
      if (path in bodies) {
        return Promise.resolve(jsonResponse(bodies[path]));
      }
      if (path === "/api/auth/session") {
        return Promise.resolve(jsonResponse({ username: "ada", role: "admin" }));
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Home", () => {
  it("is a setup checklist until a database exists", async () => {
    stubHome({
      "/api/environments": { environments: [] },
      "/api/databases": { databases: [] },
      "/api/cloud-connections": { cloudConnections: [] },
      "/api/connections": {
        servicenow: { configured: false },
        assistant: { configured: false },
        cursor: { configured: false },
      },
    });

    renderWithProviders(<AppRoutes />, { route: "/" });

    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "Setup progress" })).toHaveAttribute(
      "aria-valuenow",
      "0",
    );
    expect(
      screen.getByRole("link", { name: /Connect an AWS account or create an environment/ }),
    ).toHaveAttribute("href", "/environments/new");
    expect(screen.queryByRole("heading", { name: "Needs attention" })).not.toBeInTheDocument();
  });

  it("counts environments, databases and alerts once a database exists", async () => {
    stubHome({
      "/api/environments": {
        environments: [
          { name: "spoke-a", status: "unreachable", lastSeen: "2026-09-08T12:00:00Z" },
        ],
      },
      "/api/databases": {
        databases: [
          { id: "spoke-a/pds.orders", name: "orders", environment: "spoke-a", status: "ready" },
        ],
      },
      "/api/alerts": {
        criticals: 2,
        alerts: [
          { id: "a", severity: "critical", environment: "spoke-a", summary: "Disk full" },
          { id: "b", severity: "critical", environment: "spoke-a", summary: "Down" },
        ],
        environments: [],
      },
      "/api/activity": { entries: [], cursor: "", truncated: false },
    });

    renderWithProviders(<AppRoutes />, { route: "/" });

    expect(await screen.findByText("Needs attention")).toBeInTheDocument();
    const main = screen.getByRole("main");
    expect(within(main).getByText("Environments").closest("a")).toHaveTextContent("1");
    expect(within(main).getByText("Databases").closest("a")).toHaveTextContent("1");
    expect(within(main).getByText("Open alerts").closest("a")).toHaveTextContent("2");
    expect(screen.getByText("2 critical alerts firing")).toBeInTheDocument();
    expect(screen.getByText("spoke-a")).toBeInTheDocument();
    expect(screen.getByText("is unreachable")).toBeInTheDocument();
  });
});
