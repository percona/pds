import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SupportPage } from "./support";
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

    renderWithProviders(<SupportPage />, { route: "/support" });

    await userEvent.click(await screen.findByRole("button", { name: "Contact support" }));

    expect(await screen.findByRole("dialog", { name: "Contact support" })).toBeInTheDocument();
  });

  it("says support is not connected and points to Connectors", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse(
            { code: "ErrNotImplemented", message: "Support tickets are not configured." },
            501,
          ),
        ),
    );

    renderWithProviders(<SupportPage />, { route: "/support" });

    expect(await screen.findByTestId("support-page-not-connected")).toHaveTextContent(
      "Support is not connected",
    );
    expect(screen.getByRole("link", { name: "Open Connectors" })).toHaveAttribute(
      "href",
      "/connectors",
    );
    expect(screen.queryByRole("button", { name: "Contact support" })).not.toBeInTheDocument();
  });

  it("opens an empty case for the person to describe", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ tickets: [] })));

    renderWithProviders(<SupportPage />, { route: "/support" });

    await userEvent.click(await screen.findByRole("button", { name: "Contact support" }));

    const dialog = await screen.findByRole("dialog", { name: "Contact support" });
    expect(within(dialog).getByLabelText("Subject")).toHaveValue("Support request");
    expect(within(dialog).getByLabelText("Description")).toHaveValue("");
  });
});
