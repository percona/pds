import { afterEach, describe, expect, it, vi } from "vitest";
import { onSignedOut } from "@/lib/signed-out";
import { ALL_TEAMS, setActingTeam } from "@/lib/acting-team";
import { ApiError, requestJson, toApiError } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

describe("toApiError", () => {
  it("uses a timeout message when nginx returns 504 without a JSON body", () => {
    const error = toApiError(null, 504);

    expect(error).toBeInstanceOf(ApiError);
    expect(error.status).toBe(504);
    expect(error.message).toBe("The request took too long. Try again.");
  });

  it("keeps the adapter message when a 504 body still has one", () => {
    const error = toApiError(
      { code: "ErrDownstream", message: "The assistant did not answer. Try again in a moment." },
      504,
    );

    expect(error.message).toBe("The assistant did not answer. Try again in a moment.");
  });
});

describe("requestJson", () => {
  it("reports the session ending when a guarded route answers 401", async () => {
    const seen = vi.fn();
    const unsubscribe = onSignedOut(seen);
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ code: "ErrValidation", message: "Sign in to continue." }), {
            status: 401,
          }),
        ),
      ),
    );

    await expect(requestJson("/api/databases")).rejects.toThrow();

    expect(seen).toHaveBeenCalledOnce();
    unsubscribe();
  });

  it("does not report the session ending when a sign in is refused", async () => {
    const seen = vi.fn();
    const unsubscribe = onSignedOut(seen);
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              code: "ErrValidation",
              message: "That username and password do not match.",
            }),
            { status: 401 },
          ),
        ),
      ),
    );

    await expect(
      requestJson("/api/auth/login", {
        method: "POST",
        body: { username: "acme", password: "no" },
      }),
    ).rejects.toThrow();

    expect(seen).not.toHaveBeenCalled();
    unsubscribe();
  });

  it("sends no credential of its own, because the session rides in a cookie", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response(JSON.stringify({ ok: true }), { status: 200 }))),
    );

    await requestJson("/api/databases", { method: "POST", body: { name: "orders" } });

    const calls = vi.mocked(fetch).mock.calls;
    expect(calls).toHaveLength(1);
    expect(calls[0]?.[1]?.headers).not.toHaveProperty("Authorization");
  });

  it("maps a network failure to the unreachable control plane message", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );

    await expect(requestJson("/api/chat")).rejects.toMatchObject({
      name: "ApiError",
      message: "The control plane could not be reached.",
      status: 0,
    });
  });
});

describe("requestJson acting Team", () => {
  function stubFetch() {
    const fetchMock = vi.fn((_input: RequestInfo | URL) =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    return fetchMock;
  }

  const calledPaths = (fetchMock: ReturnType<typeof stubFetch>) =>
    fetchMock.mock.calls.map(([input]) => String(input));

  afterEach(() => {
    setActingTeam(null);
  });

  it("names the Team on a Team's route", async () => {
    const fetchMock = stubFetch();
    setActingTeam("Payments");

    await requestJson("/api/environments");

    expect(calledPaths(fetchMock)).toEqual(["/api/environments?team=Payments"]);
  });

  it("appends to a query string already there and encodes the name", async () => {
    const fetchMock = stubFetch();
    setActingTeam("R&D team");

    await requestJson("/api/activity?limit=20");

    expect(calledPaths(fetchMock)).toEqual(["/api/activity?limit=20&team=R%26D%20team"]);
  });

  it("sends nothing while no Team is chosen", async () => {
    const fetchMock = stubFetch();

    await requestJson("/api/environments");

    expect(calledPaths(fetchMock)).toEqual(["/api/environments"]);
  });

  it.each([
    "/api/auth/session",
    "/api/features",
    "/api/teams",
    "/api/teams/Payments",
    "/api/users",
    "/api/users/3",
    "/api/invitations",
    "/api/organization",
  ])("leaves %s alone, because it belongs to the Organization", async (path) => {
    const fetchMock = stubFetch();
    setActingTeam("Payments");

    await requestJson(path);

    expect(calledPaths(fetchMock)).toEqual([path]);
  });
});

describe("requestJson with every Team", () => {
  function stubFetch() {
    const fetchMock = vi.fn((_input: RequestInfo | URL) =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    return fetchMock;
  }

  const calledPaths = (fetchMock: ReturnType<typeof stubFetch>) =>
    fetchMock.mock.calls.map(([input]) => String(input));

  afterEach(() => {
    setActingTeam(null);
  });

  it.each([
    ["/api/environments", "/api/environments?team=all"],
    ["/api/databases", "/api/databases?team=all"],
    ["/api/databases?environment=eks-1", "/api/databases?environment=eks-1&team=all"],
    ["/api/activity?limit=20", "/api/activity?limit=20&team=all"],
  ])("asks %s for every Team", async (path, expected) => {
    const fetchMock = stubFetch();
    setActingTeam(ALL_TEAMS);

    await requestJson(path);

    expect(calledPaths(fetchMock)).toEqual([expected]);
  });

  it.each([
    "/api/environments/eks-1",
    "/api/environments/eks-1/history",
    "/api/databases/db-1/eks-1",
    "/api/activity/0001",
    "/api/connect",
    "/api/chat/conversations",
  ])("leaves %s in the person's own Team, because it is not a list", async (path) => {
    const fetchMock = stubFetch();
    setActingTeam(ALL_TEAMS);

    await requestJson(path);

    expect(calledPaths(fetchMock)).toEqual([path]);
  });

  it.each([
    ["/api/databases", "POST"],
    ["/api/connect/tokens", "POST"],
  ] as const)("does not send a write on %s to every Team", async (path, method) => {
    const fetchMock = stubFetch();
    setActingTeam(ALL_TEAMS);

    await requestJson(path, { method, body: {} });

    expect(calledPaths(fetchMock)).toEqual([path]);
  });

  it("names the Team a write is for, whichever Team the console is acting in", async () => {
    const fetchMock = stubFetch();
    setActingTeam(ALL_TEAMS);

    await requestJson("/api/databases", { method: "POST", body: {}, team: "Payments" });
    setActingTeam("Search");
    await requestJson("/api/connect/tokens", { method: "POST", body: {}, team: "Payments" });

    expect(calledPaths(fetchMock)).toEqual([
      "/api/databases?team=Payments",
      "/api/connect/tokens?team=Payments",
    ]);
  });
});
