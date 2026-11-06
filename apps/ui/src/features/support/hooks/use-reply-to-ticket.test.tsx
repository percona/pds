import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useReplyToTicket } from "./use-reply-to-ticket";

function wrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useReplyToTicket", () => {
  it("does not retry a failed reply when mutations default to retries", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({ code: "ErrDownstream", message: "Percona support did not answer." }),
        {
          status: 502,
          headers: { "Content-Type": "application/json" },
        },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const client = new QueryClient({
      defaultOptions: { mutations: { retry: 3, retryDelay: 0 } },
    });
    const { result } = renderHook(() => useReplyToTicket("CS0061234"), {
      wrapper: wrapper(client),
    });

    await act(async () => {
      await expect(
        result.current.mutateAsync({ body: "The operator events are attached." }),
      ).rejects.toThrow("Percona support did not answer.");
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
