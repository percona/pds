import { describe, expect, it } from "vitest";
import {
  accessChoices,
  bridgeConfig,
  claudeCodeInstall,
  claudeCodeStart,
  claudeCodeUpdate,
  defaultOrigin,
  loginCommand,
  mcpAddress,
  mcpPackage,
  type Access,
} from "./snippets";

const origin = "https://pds.example.com";

describe("plugin commands", () => {
  it("installs the plugin from the marketplace", () => {
    expect(claudeCodeInstall).toBe(
      "claude plugin marketplace add percona/pds\nclaude plugin install pds@percona-pds",
    );
    expect(claudeCodeUpdate).toBe(
      "claude plugin marketplace update percona-pds\nclaude plugin update pds@percona-pds",
    );
  });

  it("starts Claude Code with this control plane's address", () => {
    expect(claudeCodeStart(origin)).toBe("PDS_URL=https://pds.example.com claude");
  });
});

describe("mcpAddress", () => {
  it("is the origin followed by /mcp", () => {
    expect(mcpAddress(origin)).toBe("https://pds.example.com/mcp");
    expect(mcpAddress(defaultOrigin)).toBe("https://pds.percona.com/mcp");
  });
});

describe("bridgeConfig", () => {
  it("runs the bridge with the address alone", () => {
    expect(JSON.parse(bridgeConfig(origin))).toEqual({
      mcpServers: {
        pds: { command: "npx", args: ["-y", mcpPackage], env: { PDS_URL: origin } },
      },
    });
  });

  it("holds no password, username or permission setting for any access", () => {
    for (const choice of accessChoices) {
      const text = bridgeConfig(origin) + loginCommand(origin, choice.id);

      expect(text).not.toMatch(
        /PDS_USERNAME|PDS_PASSWORD|PDS_ORGANIZATION|PDS_TEAM|PDS_ALLOW|your-password/,
      );
    }
  });
});

describe("loginCommand", () => {
  it("names the access the person chose", () => {
    expect(loginCommand(origin, "read")).toBe(
      `PDS_URL=https://pds.example.com npx -y ${mcpPackage} login --access read`,
    );
    const flag = (access: Access) => loginCommand(origin, access).split("--access ")[1];

    expect(flag("write")).toBe("change");
    expect(flag("delete")).toBe("delete");
  });
});
