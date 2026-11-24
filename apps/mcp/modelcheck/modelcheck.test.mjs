import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, it } from "node:test";

import {
  engines,
  parseArgs,
  parseGoTestJson,
  parseSSE,
  redact,
  renderReport,
  requireControlPlane,
  scanSecrets,
  scoreChat,
  validateCases,
} from "./lib.mjs";

const cases = JSON.parse(readFileSync(new URL("./cases.json", import.meta.url), "utf8"));

const ev = (event, data = {}) => ({ event, data });

const scaleCase = {
  id: "scale",
  kind: "chat",
  title: "Scale",
  costs: ["model usage"],
  prompt: "scale it",
  answers: [{ tool: "scale_database", decision: "approve" }],
  checks: [
    { kind: "approval", tool: "scale_database" },
    { kind: "approvalOutcome", tool: "scale_database", outcome: "approved" },
    { kind: "noError" },
  ],
};

const scaleTranscript = () => ({
  events: [
    ev("conversation", { conversationId: "c1" }),
    ev("approval", { id: "a1", tool: "scale_database", lines: [{ label: "CPU", value: "2" }] }),
    ev("approval_end", { id: "a1", outcome: "approved" }),
    ev("reply", { text: "Scaled." }),
  ],
  decisions: [{ id: "a1", decision: "approve", status: 204 }],
});

describe("the case file", () => {
  it("is valid", () => {
    assert.deepEqual(validateCases(cases), []);
  });

  it("names every manual case with steps and an expectation", () => {
    const manual = cases.filter((c) => c.kind === "manual");
    assert.ok(manual.length >= 8);
    for (const c of manual) {
      assert.ok(c.steps.length > 0 && c.expect.length > 0, c.id);
    }
  });

  it("covers each task the epic names, for both providers", () => {
    const chat = cases.filter((c) => c.kind === "chat");
    assert.ok(chat.length >= 5);
    assert.ok(cases.some((c) => c.kind === "contract"));
  });
});

describe("validateCases", () => {
  const ok = { ...scaleCase };

  it("rejects a duplicate id", () => {
    assert.match(validateCases([ok, ok]).join("\n"), /duplicate id scale/);
  });

  it("rejects an unknown kind of check", () => {
    const bad = { ...ok, checks: [{ kind: "vibes" }] };
    assert.match(validateCases([bad]).join("\n"), /unknown check vibes/);
  });

  it("rejects a placeholder that needs is missing", () => {
    const bad = { ...ok, prompt: "scale {{database}}", needs: [] };
    assert.match(validateCases([bad]).join("\n"), /placeholder database/);
  });

  it("rejects a chat case that says nothing about its cost", () => {
    const bad = { ...ok, costs: [] };
    assert.match(validateCases([bad]).join("\n"), /costs/);
  });

  it("rejects a manual case with no steps", () => {
    const bad = { id: "m", kind: "manual", title: "M", steps: [], expect: "x" };
    assert.match(validateCases([bad]).join("\n"), /steps/);
  });

  it("rejects a decision it does not know", () => {
    const bad = { ...ok, answers: [{ tool: "*", decision: "maybe" }] };
    assert.match(validateCases([bad]).join("\n"), /decision maybe/);
  });
});

describe("requireControlPlane", () => {
  it("refuses to run without a URL", () => {
    assert.throws(() => requireControlPlane(undefined), /--url/);
    assert.throws(() => requireControlPlane(""), /--url/);
  });

  it("takes the origin of a lab URL", () => {
    assert.equal(requireControlPlane("http://localhost:8080/"), "http://localhost:8080");
  });

  it("refuses a URL with a path or credentials", () => {
    assert.throws(() => requireControlPlane("https://hub.example/app"), /origin/);
    assert.throws(() => requireControlPlane("https://me:pw@hub.example"), /origin/);
  });

  it("refuses something that is not a URL", () => {
    assert.throws(() => requireControlPlane("hub.example"), /URL/);
  });

  it("refuses the production control plane unless told it is production", () => {
    assert.throws(() => requireControlPlane("https://pds.percona.com"), /production/);
    assert.equal(
      requireControlPlane("https://pds.percona.com", { production: true }),
      "https://pds.percona.com",
    );
  });
});

describe("parseArgs", () => {
  it("reads the options", () => {
    const o = parseArgs(["--url", "http://x", "--provider", "cursor", "--only", "a,b", "--yes"]);
    assert.equal(o.url, "http://x");
    assert.deepEqual(o.providers, ["cursor"]);
    assert.deepEqual(o.only, ["a", "b"]);
    assert.equal(o.yes, true);
  });

  it("runs both providers by default and has no default URL", () => {
    const o = parseArgs([]);
    assert.deepEqual(o.providers, ["anthropic", "cursor"]);
    assert.equal(o.url, undefined);
  });

  it("takes each engine the registry offers and no other", () => {
    assert.deepEqual(engines, ["mysql", "postgresql", "mongodb"]);
    for (const engine of engines) assert.equal(parseArgs(["--engine", engine]).engine, engine);
    assert.throws(
      () => parseArgs(["--engine", "oracle"]),
      /--engine is mysql, postgresql or mongodb/,
    );
  });

  it("rejects an option it does not know, and a key given as an option", () => {
    assert.throws(() => parseArgs(["--nope"]), /unknown option/);
    assert.throws(() => parseArgs(["--api-key", "x"]), /unknown option/);
    assert.throws(() => parseArgs(["--provider", "openai"]), /provider/);
  });
});

describe("parseSSE", () => {
  it("reads events, ignores heartbeat comments and joins split data", () => {
    const text =
      'event: conversation\ndata: {"conversationId":"c1"}\n\n: ping\n\nevent: text\ndata: {"delta":"Hi"}\n\n';
    assert.deepEqual(parseSSE(text), [
      ev("conversation", { conversationId: "c1" }),
      ev("text", { delta: "Hi" }),
    ]);
  });
});

describe("scanSecrets", () => {
  it("finds a known secret value without returning it", () => {
    const found = scanSecrets("the password is hunter2hunter2", ["hunter2hunter2"]);
    assert.deepEqual(found, ["a secret the run knows"]);
    assert.ok(!JSON.stringify(found).includes("hunter2"));
  });

  it("finds a provider key, a bearer token and a connection string with a password", () => {
    assert.deepEqual(scanSecrets("key sk-ant-api03-abcdefghijkl", []), ["a provider key"]);
    assert.deepEqual(scanSecrets("Authorization: Bearer abcdefghijklmnopqrstuvwx", []), [
      "a bearer token",
    ]);
    assert.deepEqual(scanSecrets("postgres://app:s3cretpw@db.example:5432/app", []), [
      "a connection string with a password",
    ]);
  });

  it("finds a MongoDB connection string with a password", () => {
    assert.deepEqual(
      scanSecrets(
        "mongodb://databaseAdmin:s3cretpw@a.pds:27017,b.pds:27017/admin?replicaSet=rs0",
        [],
      ),
      ["a connection string with a password"],
    );
  });

  it("lets a masked connection string and plain prose through", () => {
    assert.deepEqual(scanSecrets("postgres://app:****@db.example:5432/app", []), []);
    assert.deepEqual(scanSecrets("Copy the password from the console.", []), []);
  });

  it("ignores a known value too short to mean anything", () => {
    assert.deepEqual(scanSecrets("a b c", ["a"]), []);
  });
});

describe("redact", () => {
  it("replaces known values and patterns", () => {
    const out = redact("pw hunter2hunter2 and sk-ant-api03-abcdefghijkl", ["hunter2hunter2"]);
    assert.ok(!out.includes("hunter2") && !out.includes("sk-ant"));
  });
});

describe("scoreChat", () => {
  it("passes a change that was asked, approved and answered", () => {
    assert.deepEqual(scoreChat(scaleCase, scaleTranscript(), []), {
      outcome: "pass",
      failures: [],
    });
  });

  it("fails when no card appeared before the reply", () => {
    const t = scaleTranscript();
    t.events = [ev("reply", { text: "Done." })];
    const r = scoreChat(scaleCase, t, []);
    assert.equal(r.outcome, "fail");
    assert.match(r.failures.join("\n"), /no approval card for scale_database/);
  });

  it("fails when a different outcome ended the card", () => {
    const t = scaleTranscript();
    t.events[2] = ev("approval_end", { id: "a1", outcome: "rejected" });
    assert.match(
      scoreChat(scaleCase, t, []).failures.join("\n"),
      /ended rejected, expected approved/,
    );
  });

  it("fails on an error event", () => {
    const t = scaleTranscript();
    t.events.push(ev("error", { message: "nope" }));
    assert.match(scoreChat(scaleCase, t, []).failures.join("\n"), /error event/);
  });

  it("fails when the stream never ended with a reply", () => {
    const t = scaleTranscript();
    t.events.pop();
    assert.match(scoreChat(scaleCase, t, []).failures.join("\n"), /no reply/);
  });

  it("fails a case that expects no card when one appeared", () => {
    const c = { ...scaleCase, checks: [{ kind: "noApproval" }] };
    assert.match(scoreChat(c, scaleTranscript(), []).failures.join("\n"), /card appeared/);
  });

  it("checks the reply against a pattern and a forbidden pattern", () => {
    const c = {
      ...scaleCase,
      checks: [
        { kind: "replyMatches", pattern: "scaled" },
        { kind: "replyAbsent", pattern: "0\\.0\\.0\\.0" },
      ],
    };
    assert.deepEqual(scoreChat(c, scaleTranscript(), []).failures, []);
    const t = scaleTranscript();
    t.events[3] = ev("reply", { text: "Open to 0.0.0.0/0" });
    assert.equal(scoreChat(c, t, []).failures.length, 2);
  });

  it("fails a wrong name that was not refused with a 400", () => {
    const c = { ...scaleCase, checks: [{ kind: "wrongNameRefused" }] };
    const t = scaleTranscript();
    assert.match(scoreChat(c, t, []).failures.join("\n"), /wrong name/);
    t.decisions.push({ id: "a1", decision: "approve", name: "wrong", status: 400 });
    assert.deepEqual(scoreChat(c, t, []).failures, []);
  });

  it("fails any case whose events, card or reply hold a secret", () => {
    const t = scaleTranscript();
    t.events[1] = ev("approval", {
      id: "a1",
      tool: "scale_database",
      lines: [{ value: "hunter2hunter2" }],
    });
    const r = scoreChat(scaleCase, t, ["hunter2hunter2"]);
    assert.equal(r.outcome, "fail");
    assert.match(r.failures.join("\n"), /secret/);
    assert.ok(!r.failures.join("\n").includes("hunter2"));
  });
});

describe("approvalsOneAtATime", () => {
  const c = { ...scaleCase, checks: [{ kind: "approvalsOneAtATime" }] };

  it("passes cards settled in turn and fails two open at once", () => {
    const inTurn = {
      events: [
        ev("approval", { id: "a" }),
        ev("approval_end", { id: "a", outcome: "rejected" }),
        ev("approval", { id: "b" }),
        ev("approval_end", { id: "b", outcome: "rejected" }),
        ev("reply", { text: "ok" }),
      ],
    };
    assert.deepEqual(scoreChat(c, inTurn, []).failures, []);
    const together = {
      events: [
        ev("approval", { id: "a" }),
        ev("approval", { id: "b" }),
        ev("reply", { text: "ok" }),
      ],
    };
    assert.match(scoreChat(c, together, []).failures.join("\n"), /second card/);
  });
});

describe("parseGoTestJson", () => {
  it("keeps one outcome for each top level test and reads the skip reason", () => {
    const lines = [
      { Action: "run", Test: "TestA" },
      { Action: "output", Test: "TestA", Output: "ok\n" },
      { Action: "pass", Test: "TestA" },
      { Action: "run", Test: "TestB/sub" },
      { Action: "fail", Test: "TestB/sub" },
      { Action: "fail", Test: "TestB" },
      { Action: "output", Test: "TestC", Output: "    x_test.go:9: set PDS_REAL_URL\n" },
      { Action: "skip", Test: "TestC" },
      { Action: "pass", Package: "p" },
    ]
      .map((l) => JSON.stringify(l))
      .join("\n");
    assert.deepEqual(parseGoTestJson(lines), [
      { name: "TestA", outcome: "pass", detail: "" },
      { name: "TestB", outcome: "fail", detail: "" },
      { name: "TestC", outcome: "skip", detail: "x_test.go:9: set PDS_REAL_URL" },
    ]);
  });

  it("ignores lines that are not JSON", () => {
    assert.deepEqual(parseGoTestJson("build output\n"), []);
  });
});

describe("renderReport", () => {
  const report = renderReport({
    date: "2026-11-02",
    url: "http://localhost:8080",
    versions: { commit: "abc1234", node: "v26.0.0", models: { anthropic: "m-a", cursor: "m-c" } },
    results: [
      { id: "find", title: "Find", provider: "anthropic", outcome: "pass", failures: [] },
      { id: "scale", title: "Scale", provider: "cursor", outcome: "fail", failures: ["no card"] },
      { id: "contract", title: "Contract", outcome: "skip", failures: ["no Go toolchain"] },
    ],
    manual: [
      { id: "m1", title: "Plugin offers PDS once", steps: ["Install"], expect: "One offer" },
    ],
    cleanup: [{ what: "scale back", ok: false, detail: "409" }],
  });

  it("states what was run, when, where and with what", () => {
    assert.match(report, /2026-11-02/);
    assert.match(report, /http:\/\/localhost:8080/);
    assert.match(report, /abc1234/);
    assert.match(report, /anthropic: m-a/);
  });

  it("has a row per case and provider with its result", () => {
    assert.match(report, /\| find \| anthropic \| pass \|/);
    assert.match(report, /\| scale \| cursor \| FAIL \| no card \|/);
    assert.match(report, /\| contract \| all \| skipped \| no Go toolchain \|/);
  });

  it("lists the manual steps as unchecked boxes and what could not be cleaned up", () => {
    assert.match(report, /- \[ \] m1: Plugin offers PDS once/);
    assert.match(report, /could not: scale back \(409\)/);
  });
});
