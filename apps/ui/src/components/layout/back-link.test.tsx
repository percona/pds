import { screen } from "@testing-library/react";
import { createRoutesFromElements, generatePath, type RouteObject } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";
import { parentPage, topLevelPaths } from "./parent-page";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(hub: boolean) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input).split("?")[0];
      if (path === "/api/environments") {
        return Promise.resolve(
          jsonResponse({
            hub,
            environments: hub
              ? [{ name: "spoke-a", status: "connected", lastSeen: "2026-09-28T10:00:00Z" }]
              : [],
          }),
        );
      }
      if (path.startsWith("/api/databases")) {
        return Promise.resolve(jsonResponse({ databases: [] }));
      }
      return Promise.resolve(jsonResponse({}));
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

/** Every path the shell routes, flattened out of the route tree. */
function routedPaths(routes: RouteObject[], prefix = ""): string[] {
  return routes.flatMap((route) => {
    const path = route.path ? `${prefix}/${route.path}`.replace(/\/+/g, "/") : prefix || "/";
    const own = route.path || route.index ? [path] : [];
    return [...own, ...routedPaths(route.children ?? [], route.path ? path : prefix)];
  });
}

const allOn = () => true;

function without(off: string) {
  return (surface: string): boolean => surface !== off;
}

describe("parentPage", () => {
  it("gives every routed page a parent unless it is a top level page", () => {
    const routes = createRoutesFromElements(AppRoutes().props.children);
    const paths = [...new Set(routedPaths(routes))];

    expect(paths.length).toBeGreaterThan(10);
    for (const path of paths) {
      if (topLevelPaths.includes(path)) {
        expect(parentPage(path, { enabled: allOn }), path).toBeNull();
        continue;
      }
      // /clusters is a redirect. It is not a page with a back link.
      if (path === "/clusters" || path.startsWith("/clusters/")) continue;
      const sample = generatePath(path, {
        environment: "spoke-a",
        id: "x",
        name: "spoke-a",
        number: "1",
      });
      expect(parentPage(sample, { enabled: allOn }), path).not.toBeNull();
    }
  });

  it.each([
    ["/databases/spoke%20a/pds.orders", "/environments/spoke%20a", "databases"],
    ["/environments/spoke-a", "/environments", "environments"],
    ["/environments/spoke-a/aws-cleanup", "/environments", "environments"],
    ["/environments/spoke-a/openeverest", "/environments", "environments"],
    ["/environments/spoke-a/update", "/environments", "environments"],
    ["/environments/spoke-a/new", "/environments/spoke-a", "databases"],
    ["/environments/new", "/environments", "environments"],
    ["/environments/aws-accounts/new", "/environments", "environments"],
    ["/activity/42", "/activity", "activity"],
    ["/support/CS0001", "/support", "support"],
  ])("sends %s to %s", (pathname, to, label) => {
    expect(parentPage(pathname, { enabled: allOn })).toMatchObject({ to, label });
  });

  it("has no parent for a database url that names no environment", () => {
    expect(parentPage("/databases/pds.orders", { enabled: allOn })).toBeNull();
    expect(parentPage("/databases/new", { enabled: allOn })).toBeNull();
    expect(parentPage("/databases", { enabled: allOn })).toBeNull();
  });

  it("has no parent when this page or the parent is turned off", () => {
    expect(parentPage("/activity/42", { enabled: without("activity") })).toBeNull();
    expect(parentPage("/support/1", { enabled: without("support") })).toBeNull();
    // /environments/:name is a databases page whose parent is behind environments.
    expect(parentPage("/environments/spoke-a", { enabled: without("clusters") })).toBeNull();
    expect(parentPage("/environments/spoke-a/new", { enabled: without("clusters") })).toMatchObject(
      {
        to: "/environments/spoke-a",
      },
    );
  });
});

describe("BackLink", () => {
  it.each([
    ["/databases/spoke-a/pds.orders", "Back to databases", "/environments/spoke-a"],
    ["/environments/spoke-a/new", "Back to databases", "/environments/spoke-a"],
    ["/environments/spoke-a", "Back to environments", "/environments"],
    ["/environments/new", "Back to environments", "/environments"],
    ["/environments/aws-accounts/new", "Back to environments", "/environments"],
    ["/environments/spoke-a/aws-cleanup", "Back to environments", "/environments"],
    ["/environments/spoke-a/openeverest", "Back to environments", "/environments"],
    ["/environments/spoke-a/update", "Back to environments", "/environments"],
    ["/activity/42", "Back to activity", "/activity"],
    ["/support/CS0001", "Back to support", "/support"],
  ])("on %s links %s to %s", async (route, name, href) => {
    stubApi(true);
    renderWithProviders(<AppRoutes />, { route });

    expect(await screen.findByRole("link", { name })).toHaveAttribute("href", href);
  });

  it("points at the parent even when the page arrived with no history", async () => {
    stubApi(false);
    renderWithProviders(<AppRoutes />, { route: "/activity/42?tab=events" });

    expect(await screen.findByRole("link", { name: "Back to activity" })).toHaveAttribute(
      "href",
      "/activity",
    );
  });

  it("keeps the back link when the environment read fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/environments") {
          return Promise.resolve(jsonResponse({ code: "ErrDownstream", message: "down" }, 502));
        }
        return Promise.resolve(jsonResponse({}));
      }),
    );
    renderWithProviders(<AppRoutes />, { route: "/support/CS0001" });

    expect(await screen.findByRole("link", { name: "Back to support" })).toHaveAttribute(
      "href",
      "/support",
    );
  });

  it("renders no back link on a surface that is turned off", async () => {
    stubApi(false);
    renderWithProviders(<AppRoutes />, { route: "/activity/42", flags: { activity: false } });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Page not found" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /^Back to/ })).not.toBeInTheDocument();
  });

  it.each([
    ["/environments", "Environments"],
    ["/activity", "Activity"],
    ["/support", "Support"],
  ])("renders no back link on the top level page %s", async (route, heading) => {
    stubApi(true);
    renderWithProviders(<AppRoutes />, { route });

    expect(await screen.findByRole("heading", { level: 1, name: heading })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /^Back to/ })).not.toBeInTheDocument();
  });
});
