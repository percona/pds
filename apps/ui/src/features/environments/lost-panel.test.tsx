import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMockLifecycle, renderWithProviders } from "@/test/utils";
import { LostPanel } from "./lost-panel";

function stubApi() {
  const fetchMock = vi.fn<(input: string, init?: RequestInit) => Promise<Response>>(() =>
    Promise.resolve(
      new Response(JSON.stringify({ run: { id: 8 }, provisioning: { phases: [] } }), {
        status: 202,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

const lifecycle = createMockLifecycle({
  lost: true,
  lostReason: "Infrastructure missing",
  lostAt: "2026-11-20T08:00:00Z",
});

afterEach(() => vi.unstubAllGlobals());

describe("LostPanel", () => {
  it("says the infrastructure is missing and offers Rebuild or Remove from PDS only", () => {
    stubApi();

    renderWithProviders(
      <LostPanel environment="orders" lifecycle={lifecycle} onRemove={vi.fn()} />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Infrastructure missing");
    expect(screen.getByRole("button", { name: "Rebuild" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove from PDS only" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  });

  it("starts a rebuild", async () => {
    const fetchMock = stubApi();
    const user = userEvent.setup();

    renderWithProviders(
      <LostPanel environment="orders" lifecycle={lifecycle} onRemove={vi.fn()} />,
    );
    await user.click(screen.getByRole("button", { name: "Rebuild" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/environments/orders/runs");
    expect(JSON.parse(String(init?.body))).toEqual({ kind: "rebuild" });
  });

  it("hands Remove from PDS only to the dialog", async () => {
    stubApi();
    const onRemove = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <LostPanel environment="orders" lifecycle={lifecycle} onRemove={onRemove} />,
    );
    await user.click(screen.getByRole("button", { name: "Remove from PDS only" }));

    expect(onRemove).toHaveBeenCalledTimes(1);
  });
});
