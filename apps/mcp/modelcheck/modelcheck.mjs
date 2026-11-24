#!/usr/bin/env node
// The manual evaluation of the AI tools, run by a person before an MCP or plugin release. It is
// not part of CI. The cases are data in cases.json; the pure logic is in lib.mjs and tested.

import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline/promises";
import { fileURLToPath } from "node:url";

import {
  fill,
  parseArgs,
  parseGoTestJson,
  parseSSE,
  redact,
  renderReport,
  requireControlPlane,
  scoreChat,
  validateCases,
} from "./lib.mjs";

const usage = `Usage: node apps/mcp/modelcheck/modelcheck.mjs --url <control plane> [options]
       node apps/mcp/modelcheck/modelcheck.mjs --list

Evaluates the AI tools of a control plane with real models: the tools served at /mcp, the console
Chat with Anthropic and Cursor, and the steps for the plugin that only a person can judge. Run it
before releasing the MCP server or the plugin, against a lab or test control plane. It is not run
in CI.

It costs model usage, and some cases change data. Each case says what it spends or changes and
asks before it runs. The run sets what agents may do in the Organization to the delete tier and
puts it back at the end, scales one database and scales it back, and lists in its report anything
it could not undo.

Options:
  --url <origin>          The control plane to run against. Required, no default.
  --production            Allow the production control plane. Refused without it.
  --database <name>       A scratch database on that control plane for the Chat cases. Cases that
                          need it are skipped without it.
  --engine <mysql|postgresql|mongodb>  The engine of that database, for the find case.
  --provider <anthropic|cursor|both>  Chat providers to run. Default both.
  --only <id,id>          Run only these cases.
  --yes                   Do not ask before each case.
  --report <file>         Where to write the report. Default modelcheck-report-<date>.md.
  --transcripts <dir>     Where to write the transcripts. Default modelcheck-transcripts-<date>.
  --list                  Print the cases and what each costs, then stop. Needs no control plane.
  --help                  This text.

Environment (read, never printed or written):
  PDS_REAL_USERNAME, PDS_REAL_PASSWORD   An admin of the Organization to sign in as. The
                                         Organization must already have the Anthropic and Cursor
                                         connections set up in the console; the script reads no key.

Needs Node 22 or later, and Go for the tools case. Paste the report into the release pull request.`;

// Ctrl-C stops the run between steps, and the cleanup after it still happens.
const stop = new AbortController();
process.once("SIGINT", () => stop.abort());

const say = (text = "") => process.stdout.write(`${text}\n`);
const here = new URL(".", import.meta.url);

function describeCase(c) {
  const costs = (c.costs ?? []).map((x) => `      costs: ${x}`);
  const needs =
    (c.needs ?? []).length > 0 ? ` (needs ${c.needs.map((n) => `--${n}`).join(", ")})` : "";

  return [`  ${c.id}  [${c.kind}]${needs}  ${c.title}`, ...costs].join("\n");
}

async function confirm(options, question) {
  if (options.yes) return true;
  if (!process.stdin.isTTY) return false;
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  const reply = await rl.question(`${question} [y/N] `);
  rl.close();

  return reply.trim().toLowerCase() === "y";
}

// A signed in session on the control plane. The cookie is held in memory only.
async function signIn(origin, username, password) {
  const res = await fetch(`${origin}/api/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) throw new Error(`sign in answered ${res.status}`);
  const cookie = res.headers
    .getSetCookie()
    .map((c) => c.split(";")[0])
    .join("; ");

  return (path, init = {}) =>
    fetch(`${origin}${path}`, {
      ...init,
      headers: { ...init.headers, cookie, "content-type": "application/json" },
    });
}

async function json(api, path, init) {
  const res = await api(path, init);
  if (!res.ok) throw new Error(`${path} answered ${res.status}`);

  return res.status === 204 ? null : res.json();
}

async function answer(api, approval, plan, decisions) {
  const post = async (body) => {
    const res = await api(`/api/chat/approvals/${encodeURIComponent(approval.id)}`, {
      method: "POST",
      body: JSON.stringify(body),
    });
    decisions.push({ id: approval.id, ...body, status: res.status });
  };
  // A delete is never approved by the script: a person does that on a scratch database.
  const decision = approval.destructive && plan === "approve" ? "reject" : plan;
  if (decision === "wrong-name-then-reject")
    await post({ decision: "approve", name: "not-the-name" });
  if (decision === "approve") await post({ decision: "approve" });
  else await post({ decision: "reject" });
}

// runChat fills the transcript it is given as the stream arrives, so a failure half way keeps
// what was already seen and answered.
async function runChat(api, c, provider, model, vars, { events, decisions }) {
  const plans = [...(c.answers ?? [])];
  const res = await api("/api/chat", {
    method: "POST",
    headers: { accept: "text/event-stream" },
    body: JSON.stringify({ provider, model, message: fill(c.prompt, vars) }),
    signal: AbortSignal.any([AbortSignal.timeout(5 * 60 * 1000), stop.signal]),
  });
  if (!res.ok) {
    events.push({ event: "error", data: { status: res.status } });

    return;
  }
  const decoder = new TextDecoder();
  let buffer = "";
  for await (const chunk of res.body) {
    buffer += decoder.decode(chunk, { stream: true });
    let end;
    while ((end = buffer.indexOf("\n\n")) >= 0) {
      const block = buffer.slice(0, end + 2);
      buffer = buffer.slice(end + 2);
      for (const e of parseSSE(block)) {
        events.push(e);
        if (e.event === "approval") {
          const at = plans.findIndex((p) => p.tool === "*" || p.tool === e.data.tool);
          const plan = at >= 0 ? plans.splice(at, 1)[0].decision : "reject";
          await answer(api, e.data, plan, decisions);
        }
      }
    }
  }
}

async function runContract(url, secrets) {
  const dir = fileURLToPath(new URL("..", here));
  const args = ["test", "./contract", "-count=1", "-json", "-timeout", "20m"];
  const out = await new Promise((resolve) => {
    const child = spawn("go", args, { cwd: dir, env: { ...process.env, PDS_REAL_URL: url } });
    let text = "";
    child.stdout.on("data", (d) => (text += d));
    child.stderr.on("data", (d) => (text += d));
    child.on("error", () => resolve(null));
    child.on("close", () => resolve(text));
  });
  if (out === null) {
    return [
      { id: "contract-tools", title: "tools", outcome: "skip", failures: ["Go is not installed"] },
    ];
  }
  const tests = parseGoTestJson(out);
  if (tests.length === 0) {
    const detail = redact(out, secrets).replace(/\s+/g, " ").slice(0, 300);

    return [{ id: "contract-tools", title: "tools", outcome: "fail", failures: [detail] }];
  }

  return tests.map((t) => ({
    id: `contract/${t.name}`,
    title: t.name,
    outcome: t.outcome,
    failures: t.detail ? [t.detail] : [],
  }));
}

// restoreScale puts a database back to the size it had, retrying while the control plane is
// still applying the earlier change.
async function restoreScale(api, before) {
  const now = (await json(api, "/api/databases")).databases.find((d) => d.id === before.id);
  if (!now) throw new Error("the database is gone");
  const body = {};
  if (now.members !== before.members) body.replicas = before.members;
  if (now.cpu !== before.cpu) body.cpu = before.cpu;
  if (now.memoryGb !== before.memoryGb) body.memory = before.memoryGb;
  if (Object.keys(body).length === 0) return;
  for (let attempt = 0; ; attempt++) {
    const res = await api(`/api/databases/${encodeURIComponent(before.id)}/scale`, {
      method: "POST",
      body: JSON.stringify(body),
    });
    if (res.ok) return;
    if (res.status !== 409 || attempt >= 20) throw new Error(`scale back answered ${res.status}`);
    await new Promise((r) => setTimeout(r, 15000));
  }
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) return say(usage);
  const cases = JSON.parse(readFileSync(new URL("cases.json", here), "utf8"));
  const problems = validateCases(cases);
  if (problems.length > 0) throw new Error(`cases.json is invalid:\n${problems.join("\n")}`);
  const unknown = options.only.filter((id) => !cases.some((c) => c.id === id));
  if (unknown.length > 0) throw new Error(`--only names no case: ${unknown.join(", ")}`);
  const selected = cases.filter((c) => options.only.length === 0 || options.only.includes(c.id));
  if (options.list) {
    say(selected.map(describeCase).join("\n"));
    say(`\nChat cases run once for each provider: ${options.providers.join(", ")}.`);

    return;
  }

  const origin = requireControlPlane(options.url, { production: options.production });
  const username = process.env.PDS_REAL_USERNAME;
  const password = process.env.PDS_REAL_PASSWORD;
  if (!username || !password) throw new Error("set PDS_REAL_USERNAME and PDS_REAL_PASSWORD");
  const secrets = [password];
  const vars = { database: options.database ?? "", engine: options.engine ?? "" };
  const date = new Date().toISOString().slice(0, 10);
  const transcriptDir = options.transcripts ?? `modelcheck-transcripts-${date}`;
  mkdirSync(transcriptDir, { recursive: true });

  say(`Control plane: ${origin}`);
  say("This run costs model usage, and the cases below that say so change data on it.");
  say(
    "It sets what agents may do in the Organization to the delete tier and puts it back at the end.",
  );
  if (!(await confirm(options, "Continue?"))) throw new Error("not confirmed, nothing was run");

  const api = await signIn(origin, username, password);
  const results = [];
  const cleanup = [];
  const models = {};
  const originalAI = await json(api, "/api/organization/ai");
  let before;
  try {
    for (const c of selected.filter((x) => x.kind === "contract")) {
      say(`\n${describeCase(c)}`);
      if (await confirm(options, "Run this case?"))
        results.push(...(await runContract(origin, secrets)));
      else results.push({ id: c.id, title: c.title, outcome: "skip", failures: ["not confirmed"] });
    }

    const chat = selected.filter((c) => c.kind === "chat");
    if (chat.length > 0) {
      await json(api, "/api/organization/ai", {
        method: "PUT",
        body: JSON.stringify({ ...originalAI, access: "change_delete" }),
      });
    }
    for (const provider of options.providers) {
      if (chat.length === 0 || stop.signal.aborted) break;
      try {
        const list = (await json(api, `/api/assistant/models?provider=${provider}`)).models;
        models[provider] = (list.find((m) => m.default) ?? list[0]).id;
      } catch (err) {
        for (const c of chat)
          results.push({
            id: c.id,
            provider,
            title: c.title,
            outcome: "skip",
            failures: [`no model for ${provider}: ${err.message}`],
          });
        continue;
      }
      for (const c of chat) {
        if (stop.signal.aborted) break;
        const missing = (c.needs ?? []).filter((n) => !vars[n]);
        const skip = (why) =>
          results.push({ id: c.id, provider, title: c.title, outcome: "skip", failures: [why] });
        if (missing.length > 0) {
          skip(`needs ${missing.map((n) => `--${n}`).join(", ")}`);
          continue;
        }
        say(`\n${describeCase(c)}\n      provider: ${provider}, model ${models[provider]}`);
        if (!(await confirm(options, "Run this case?"))) {
          skip("not confirmed");
          continue;
        }
        const approves = (c.answers ?? []).some((a) => a.decision === "approve");
        if (approves) {
          const row = (await json(api, "/api/databases")).databases.find(
            (d) => d.name === vars.database,
          );
          if (!row) {
            skip(`no database named ${vars.database}`);
            continue;
          }
          before = row;
        }
        const transcript = { events: [], decisions: [] };
        await runChat(api, c, provider, models[provider], vars, transcript).catch((err) =>
          transcript.events.push({ event: "error", data: { message: err.message } }),
        );
        const score = scoreChat(c, transcript, secrets, vars);
        writeFileSync(
          join(transcriptDir, `${c.id}-${provider}.json`),
          redact(
            JSON.stringify(
              { case: c.id, provider, model: models[provider], ...transcript },
              null,
              2,
            ),
            secrets,
          ),
        );
        results.push({ id: c.id, provider, title: c.title, ...score });
        say(
          `      ${score.outcome}${score.failures.length ? `: ${score.failures.join("; ")}` : ""}`,
        );
        if (approves) {
          try {
            await restoreScale(api, before);
            cleanup.push({
              what: `${vars.database} scaled back after ${c.id} on ${provider}`,
              ok: true,
            });
          } catch (err) {
            cleanup.push({
              what: `scale ${vars.database} back to ${before.members} members, ${before.cpu} CPU, ${before.memoryGb} GB`,
              ok: false,
              detail: err.message,
            });
          }
        }
      }
    }
  } finally {
    try {
      await json(api, "/api/organization/ai", { method: "PUT", body: JSON.stringify(originalAI) });
      cleanup.push({
        what: `agent access of the Organization put back to ${originalAI.access}`,
        ok: true,
      });
    } catch (err) {
      cleanup.push({
        what: `put the Organization's agent access back to ${originalAI.access}`,
        ok: false,
        detail: err.message,
      });
    }
  }

  let commit = "unknown";
  try {
    commit = execFileSync("git", ["rev-parse", "--short", "HEAD"], {
      cwd: fileURLToPath(here),
      encoding: "utf8",
    }).trim();
  } catch {
    // Not a git checkout: the report says unknown.
  }
  const report = redact(
    renderReport({
      date,
      url: origin,
      versions: { commit, node: process.version, models },
      results,
      manual: selected.filter((c) => c.kind === "manual"),
      cleanup,
    }),
    secrets,
  );
  const reportFile = options.report ?? `modelcheck-report-${date}.md`;
  writeFileSync(reportFile, report);
  say(`\n${report}`);
  say(`Report: ${reportFile}. Transcripts: ${transcriptDir}. Neither belongs in Git.`);
  if (results.some((r) => r.outcome === "fail") || cleanup.some((c) => !c.ok)) process.exitCode = 1;
}

main().catch((err) => {
  process.stderr.write(`modelcheck: ${err.message}\n`);
  process.exitCode = 2;
});
