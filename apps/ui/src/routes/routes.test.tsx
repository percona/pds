import { screen } from "@testing-library/react";
import { useLocation, useNavigationType } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AppRoutes", () => {
  it("shows Home at the root path", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/environments") {
          return Promise.resolve(jsonResponse({ hub: false, environments: [] }));
        }
        if (path === "/api/connections") {
          return Promise.resolve(
            jsonResponse({
              servicenow: { configured: false },
              assistant: { configured: false },
              cursor: { configured: false },
            }),
          );
        }
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }
        if (path.startsWith("/api/databases")) {
          return Promise.resolve(jsonResponse({ databases: [] }));
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/" });

    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1, name: "Databases" })).not.toBeInTheDocument();
  });

  it("answers not found for a database url that names no environment", () => {
    renderWithProviders(<AppRoutes />, { route: "/databases/pds.orders" });

    expect(screen.getByRole("heading", { level: 1, name: "Page not found" })).toBeInTheDocument();
  });

  it("answers not found for the old platform status path", () => {
    renderWithProviders(<AppRoutes />, { route: "/health" });

    expect(screen.getByRole("heading", { level: 1, name: "Page not found" })).toBeInTheDocument();
  });

  /** The link a person pastes into a support case has to survive a reload. */
  it("renders one change at /activity/:id", () => {
    renderWithProviders(<AppRoutes />, { route: "/activity/00000000000000000042" });

    expect(screen.getByRole("heading", { level: 1, name: "Change" })).toBeInTheDocument();
  });

  it("renders a not found page for unknown paths", () => {
    renderWithProviders(<AppRoutes />, { route: "/nope" });

    expect(screen.getByRole("heading", { level: 1, name: "Page not found" })).toBeInTheDocument();
  });
});

function LocationProbe() {
  const location = useLocation();
  const navigation = useNavigationType();
  return (
    <span data-testid="location">
      {navigation} {location.pathname}
      {location.search}
    </span>
  );
}

describe("legacy cluster URLs", () => {
  it.each([
    ["/clusters", "REPLACE /environments"],
    ["/clusters?x=1", "REPLACE /environments?x=1"],
    ["/clusters/connect", "REPLACE /environments/new"],
    ["/clusters/connect?x=1", "REPLACE /environments/new?x=1"],
    ["/clusters/spoke-a", "REPLACE /environments/spoke-a"],
    ["/clusters/spoke-a/new", "REPLACE /environments/spoke-a/new"],
    ["/clusters/spoke-a/new?x=1", "REPLACE /environments/spoke-a/new?x=1"],
    [
      "/clusters/spoke-a/aws-cleanup?namespace=databases",
      "REPLACE /environments/spoke-a/aws-cleanup?namespace=databases",
    ],
    [
      "/clusters/spoke-a/openeverest?namespace=databases",
      "REPLACE /environments/spoke-a/openeverest?namespace=databases",
    ],
    ["/clusters/spoke-a/extra?q=1", "REPLACE /environments/spoke-a/extra?q=1"],
  ])("redirects %s to %s", async (route, expected) => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(jsonResponse({ hub: true, environments: [] }))),
    );

    renderWithProviders(
      <>
        <AppRoutes />
        <LocationProbe />
      </>,
      { route },
    );

    expect(await screen.findByTestId("location")).toHaveTextContent(expected);
  });
});
