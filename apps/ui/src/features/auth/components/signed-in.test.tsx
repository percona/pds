import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Feature } from "@/features/flags";
import { renderWithProviders } from "@/test/utils";
import { SignedIn } from "./signed-in";

afterEach(() => {
  vi.unstubAllGlobals();
});

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** Answers the session as signed in, and the flag read with whatever the test gives. */
function stubAdapter(features: () => Response | Promise<Response>) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/api/auth/session")) {
      return Promise.resolve(json({ username: "percona-demo" }));
    }
    if (url.includes("/api/features")) {
      return Promise.resolve(features());
    }

    return Promise.resolve(json({ code: "ErrNotImplemented", message: "Not stubbed." }, 501));
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

const featureCalls = (fetchMock: ReturnType<typeof stubAdapter>) =>
  fetchMock.mock.calls.filter(([input]) => String(input).includes("/api/features")).length;

function renderGate() {
  return renderWithProviders(
    <SignedIn>
      <Feature name="mcp" fallback={<p>MCP is off</p>}>
        <p>MCP is on</p>
      </Feature>
    </SignedIn>,
  );
}

describe("SignedIn flag read", () => {
  it("draws a surface the adapter turned off as off", async () => {
    stubAdapter(() => json({ flags: { mcp: false } }));

    renderGate();

    expect(await screen.findByText("MCP is off")).toBeInTheDocument();
  });

  it("retries a failed read instead of keeping every surface on", async () => {
    let calls = 0;
    const fetchMock = stubAdapter(() => {
      calls += 1;
      if (calls === 1) {
        throw new TypeError("network");
      }

      return json({ flags: { mcp: false } });
    });

    renderGate();

    expect(await screen.findByText("MCP is off", {}, { timeout: 4000 })).toBeInTheDocument();
    expect(screen.queryByText("MCP is on")).not.toBeInTheDocument();
    expect(featureCalls(fetchMock)).toBe(2);
  });

  it("says the read failed, and draws nothing, when it keeps failing", async () => {
    stubAdapter(() => {
      throw new TypeError("network");
    });

    renderGate();

    expect(
      await screen.findByText("The console could not load its settings", {}, { timeout: 6000 }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    expect(screen.queryByText("MCP is on")).not.toBeInTheDocument();
    expect(screen.queryByText("MCP is off")).not.toBeInTheDocument();
  });

  it("reads everything as on when the adapter has no flag route", async () => {
    stubAdapter(() => json({ code: "ErrNotFound", message: "gone" }, 404));

    renderGate();

    await waitFor(() => expect(screen.getByText("MCP is on")).toBeInTheDocument());
  });

  it("reloads the page from the failure screen", async () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload });
    stubAdapter(() => {
      throw new TypeError("network");
    });

    renderGate();
    await userEvent.click(await screen.findByRole("button", { name: "Reload" }, { timeout: 6000 }));

    expect(reload).toHaveBeenCalledOnce();
  });
});
