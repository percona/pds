import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useModels } from "@/features/chat/hooks/use-models";
import { renderHookWithProviders } from "@/test/utils";

afterEach(() => {
  vi.unstubAllGlobals();
});

function stubModels() {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ models: [{ id: "claude-opus-5", label: "Opus 5" }] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

describe("useModels", () => {
  it("asks the adapter when the assistant surface is on", async () => {
    const fetchMock = stubModels();

    renderHookWithProviders(() => useModels("anthropic"));

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(String(fetchMock.mock.calls[0][0])).toContain("/api/assistant/models");
  });

  // The route behind this belongs to the assistant surface, so a request with
  // that surface off could only ever come back 501.
  it("asks for nothing when the assistant surface is off", async () => {
    const fetchMock = stubModels();

    const { result } = renderHookWithProviders(() => useModels("anthropic"), {
      flags: { assistant: false },
    });

    await waitFor(() => expect(result.current.isPending).toBe(true));
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
