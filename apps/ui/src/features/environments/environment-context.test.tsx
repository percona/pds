import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EnvironmentProvider, useEnvironments } from "@/features/environments";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function Readout() {
  const value = useEnvironments();

  return (
    <div>
      <p data-testid="error">{value.error?.message ?? ""}</p>
      <p data-testid="count">{value.environments.length}</p>
      <p data-testid="keys">{Object.keys(value).toSorted().join(",")}</p>
    </div>
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("EnvironmentProvider", () => {
  it("reports a failed environment read and does not treat it as a mode", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(jsonResponse({ code: "ErrNotImplemented", message: "Not a hub." }, 501)),
      ),
    );

    renderWithProviders(
      <EnvironmentProvider>
        <Readout />
      </EnvironmentProvider>,
    );

    expect(await screen.findByText("Not a hub.")).toBeInTheDocument();
    expect(screen.getByTestId("count")).toHaveTextContent("0");
    expect(screen.getByTestId("keys")).toHaveTextContent("environments,error,isPending,resolved");
  });
});
