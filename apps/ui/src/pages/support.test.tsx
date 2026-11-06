import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

describe("SupportPage", () => {
  it("opens the ticket dialog from the page header", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ tickets: [], portalUrl: "https://example.service-now.com/percona" }),
        ),
    );

    renderWithProviders(<AppRoutes />, { route: "/support" });

    await userEvent.click(await screen.findByRole("button", { name: "Contact support" }));

    expect(await screen.findByRole("dialog", { name: "Contact support" })).toBeInTheDocument();
  });
});
