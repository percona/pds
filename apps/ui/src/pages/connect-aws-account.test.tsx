import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

function stubApi() {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input).split("?")[0];
      const body = (value: unknown, status = 200) =>
        Promise.resolve(
          new Response(JSON.stringify(value), {
            status,
            headers: { "Content-Type": "application/json" },
          }),
        );
      if (path === "/api/environments") {
        return body({ hub: true, environments: [] });
      }
      if (path === "/api/cloud-connection-tokens" && init?.method === "POST") {
        return body(
          {
            id: "tok",
            command: "curl -fsSL x | bash -s -- --token ccn_1",
            expiresAt: "",
            allowedRegions: [],
          },
          201,
        );
      }
      if (path === "/api/cloud-connections") {
        return body({ cloudConnections: [] });
      }

      return body({});
    }),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("Connect an AWS account page", () => {
  it("is where the start_cloud_connection link lands, and it makes the command", async () => {
    stubApi();
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments/aws-accounts/new" });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Connect an AWS account" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create command" }));
    expect(await screen.findByText(/--token ccn_1/)).toBeInTheDocument();
  });
});
