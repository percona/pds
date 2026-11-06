import { afterEach, describe, expect, it, vi } from "vitest";
import { issueJoinToken } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("issueJoinToken", () => {
  function stubFetch() {
    const fetchMock = vi.fn((_input: RequestInfo | URL) =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    return fetchMock;
  }

  it("mints the token in the Team it is given", async () => {
    const fetchMock = stubFetch();

    await issueJoinToken("eks-1", "Payments");

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/connect/tokens?team=Payments");
  });

  it("follows the acting Team when it is given none", async () => {
    const fetchMock = stubFetch();

    await issueJoinToken("eks-1");

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/connect/tokens");
  });
});
