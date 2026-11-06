import { describe, expect, it } from "vitest";
import { agentSignInReturn, safeRedirect } from "./return-path";

describe("agentSignInReturn", () => {
  it("follows an authorization request and nothing else", () => {
    const path = "/oauth/authorize?client_id=https%3A%2F%2Fagent.example%2Fc.json&state=abc";

    expect(agentSignInReturn(path)).toBe(path);
  });

  it.each([
    ["nothing", null],
    ["an empty value", ""],
    ["another site", "https://evil.example/oauth/authorize?x=1"],
    ["a protocol relative address", "//evil.example/oauth/authorize?x=1"],
    ["a backslash host", "/\\evil.example/oauth/authorize?x=1"],
    ["the authorize path with no query", "/oauth/authorize"],
    ["another path of the server", "/oauth/token?x=1"],
    ["a console page", "/app/databases"],
    ["a path that only starts the same", "/oauth/authorizeX?x=1"],
    ["the prefix after a traversal", "/oauth/../api/auth/logout?/oauth/authorize?"],
    ["a script", "javascript:alert(1)"],
    ["an encoded break out", "/oauth/authorize?x=1\r\nLocation: https://evil.example"],
    ["a tab inside the prefix", "/oauth/\tauthorize?x=1"],
  ])("refuses %s", (_name, value) => {
    expect(agentSignInReturn(value)).toBeNull();
  });
});

describe("safeRedirect", () => {
  it("allows http and https addresses", () => {
    expect(safeRedirect("http://127.0.0.1:43211/callback?code=a")).toBe(
      "http://127.0.0.1:43211/callback?code=a",
    );
    expect(safeRedirect("https://agent.example/cb?code=a")).toBe("https://agent.example/cb?code=a");
  });

  it.each([
    "javascript:alert(1)",
    "data:text/html,<p>x</p>",
    "vscode://pds/callback?code=a",
    "/relative",
    "not a url",
    "",
  ])("refuses %s", (value) => {
    expect(safeRedirect(value)).toBeNull();
  });
});
