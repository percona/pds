import { describe, expect, it } from "vitest";
import {
  deleteEnvironmentCommand,
  joinEnvironmentCommand,
  isEnvironmentName,
  reservedEnvironmentName,
} from "./connect-commands";

describe("isEnvironmentName", () => {
  it("accepts a DNS label", () => {
    expect(isEnvironmentName("eks-testing")).toBe(true);
    expect(isEnvironmentName("a")).toBe(true);
  });

  it("rejects uppercase, spaces, and leading dashes", () => {
    expect(isEnvironmentName("EKS")).toBe(false);
    expect(isEnvironmentName("my environment")).toBe(false);
    expect(isEnvironmentName("-spoke")).toBe(false);
    expect(isEnvironmentName("")).toBe(false);
  });
});

describe("joinEnvironmentCommand", () => {
  it("prints one curl that creates the environment, installs the platform, and joins", () => {
    expect(
      joinEnvironmentCommand({
        hubUrl: "https://hub.example",
        token: "jnt_abc",
      }),
    ).toBe(
      "curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --token 'jnt_abc'",
    );
  });

  it("quotes a hub URL that carries shell syntax", () => {
    expect(
      joinEnvironmentCommand({
        hubUrl: "https://hub.example/; curl evil",
        token: "jnt_abc",
      }),
    ).toBe(
      "curl -fsSL 'https://hub.example/; curl evil/api/internal/connect/bootstrap.sh' | bash -s -- --token 'jnt_abc'",
    );
  });
});

describe("joinEnvironmentCommand for an update", () => {
  it("adds --update before the token and nothing else", () => {
    expect(
      joinEnvironmentCommand({ hubUrl: "https://hub.example/", token: "jnt_abc", update: true }),
    ).toBe(
      "curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --update --token 'jnt_abc'",
    );
  });
});

describe("deleteEnvironmentCommand", () => {
  it("prints one curl that deletes what the join created, with no token", () => {
    expect(deleteEnvironmentCommand({ hubUrl: "https://hub.example/", name: "prod-east" })).toBe(
      `curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --delete --name prod-east --region "$REGION"`,
    );
  });

  it("quotes a hub URL that carries shell syntax", () => {
    expect(
      deleteEnvironmentCommand({ hubUrl: "https://hub.example/; curl evil", name: "prod-east" }),
    ).toContain("'https://hub.example/; curl evil/api/internal/connect/bootstrap.sh'");
  });
});

describe("reservedEnvironmentName", () => {
  it.each(["connect", "new", "aws-accounts"])("keeps %s for the router", (name) => {
    expect(reservedEnvironmentName(name)).toBe(true);
  });

  it("lets an ordinary name through", () => {
    expect(reservedEnvironmentName("orders")).toBe(false);
  });
});
