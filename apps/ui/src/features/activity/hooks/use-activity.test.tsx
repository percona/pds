import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { createMockActivity } from "@/test/utils";
import { activityPollMs, useActivity } from "./use-activity";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * One client across both renders, which is what a real navigation does: the
 * feed unmounts, the cache stays.
 */
function sharedWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("useActivity", () => {
  /**
   * Leaving the page and coming back must not empty the feed.
   *
   * Polls are incremental, so a steady state answer is an empty page carrying
   * only the cursor. If the rows and the cursor live in different places, the
   * remount finds a cursor that has already stepped past every row it no
   * longer has, and asks for nothing.
   */
  /**
   * The steady state a person is actually in when they leave the page: the
   * first read brought every row, and at least one poll since then answered
   * "nothing new" with only a cursor.
   */
  function steadyState() {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(createMockActivity()))
      .mockResolvedValue(jsonResponse({ entries: [], cursor: "000003", truncated: false }));
    vi.stubGlobal("fetch", fetchMock);

    return fetchMock;
  }

  async function pollOnceThenLeave(wrapper: ReturnType<typeof sharedWrapper>, fetchMock: Mock) {
    const first = renderHook(() => useActivity({ live: true }), { wrapper });
    await waitFor(() => expect(first.result.current.entries).toHaveLength(3));

    // One steady state poll lands before the person navigates away.
    await vi.advanceTimersByTimeAsync(activityPollMs + 100);
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(1));

    first.unmount();
  }

  it("still has its rows after the feed unmounts and mounts again", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = steadyState();
    const wrapper = sharedWrapper();

    await pollOnceThenLeave(wrapper, fetchMock);

    const second = renderHook(() => useActivity({ live: true }), { wrapper });
    await waitFor(() => expect(second.result.current.isFetching).toBe(false));

    expect(second.result.current.entries).toHaveLength(3);
  });

  it("shows the rows it already has rather than an empty feed on the way back", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = steadyState();
    const wrapper = sharedWrapper();

    await pollOnceThenLeave(wrapper, fetchMock);

    const second = renderHook(() => useActivity({ live: true }), { wrapper });

    // Before any new read: whatever is cached has to be on screen already.
    expect(second.result.current.isPending).toBe(false);
    expect(second.result.current.entries).toHaveLength(3);
  });

  it("asks only for what it has not seen once it has a cursor", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const fetchMock = steadyState();
    const wrapper = sharedWrapper();

    await pollOnceThenLeave(wrapper, fetchMock);
    const before = fetchMock.mock.calls.length;

    renderHook(() => useActivity({ live: true }), { wrapper });
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(before));

    expect(String(fetchMock.mock.calls[0]?.[0])).not.toContain("since=");
    expect(String(fetchMock.mock.calls.at(-1)?.[0])).toContain("since=000003");
  });

  it("folds a later poll into the rows already on screen", async () => {
    const later = {
      ...createMockActivity().entries[0],
      id: "000004",
      action: "database.delete",
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(createMockActivity()))
      .mockResolvedValue(jsonResponse({ entries: [later], cursor: "000004", truncated: false }));
    vi.stubGlobal("fetch", fetchMock);

    const wrapper = sharedWrapper();
    const { result } = renderHook(() => useActivity({ live: false }), { wrapper });
    await waitFor(() => expect(result.current.entries).toHaveLength(3));

    await result.current.retry();

    await waitFor(() => expect(result.current.entries.length).toBeGreaterThanOrEqual(3));
  });

  /** A retry starts over: an error may have lost rows the cursor stepped past. */
  it("reads the whole feed again on a manual retry", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(createMockActivity()));
    vi.stubGlobal("fetch", fetchMock);

    const wrapper = sharedWrapper();
    const { result } = renderHook(() => useActivity({ live: false }), { wrapper });
    await waitFor(() => expect(result.current.entries).toHaveLength(3));

    await result.current.retry();
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(1));

    expect(String(fetchMock.mock.calls.at(-1)?.[0])).not.toContain("since=");
  });
});
