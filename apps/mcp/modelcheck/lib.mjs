// The pure parts of the evaluation script: checking the case file, reading the options, reading
// a stream, scoring a transcript, scanning for secrets and writing the report. Nothing here
// touches the network, the disk or the environment, so the tests need no control plane.

export const providers = ["anthropic", "cursor"];
export const variables = ["database", "engine"];

const productionHosts = new Set(["pds.percona.com"]);
const decisions = ["approve", "reject", "wrong-name-then-reject"];
const kinds = ["chat", "contract", "manual"];
const checkKinds = new Set([
  "approval",
  "approvalOutcome",
  "approvalsOneAtATime",
  "noApproval",
  "noError",
  "replyAbsent",
  "replyMatches",
  "wrongNameRefused",
]);

export function validateCases(cases) {
  const problems = [];
  const seen = new Set();
  for (const c of cases) {
    const where = `case ${c.id}`;
    if (!c.id || seen.has(c.id)) problems.push(`${where}: duplicate id ${c.id}`);
    seen.add(c.id);
    if (!kinds.includes(c.kind)) problems.push(`${where}: unknown kind ${c.kind}`);
    if (!c.title) problems.push(`${where}: no title`);
    if (c.kind === "manual") {
      if (!Array.isArray(c.steps) || c.steps.length === 0) problems.push(`${where}: no steps`);
      if (!c.expect) problems.push(`${where}: no expect`);
      continue;
    }
    if (!Array.isArray(c.costs) || c.costs.length === 0) {
      problems.push(`${where}: costs must say what the case spends or changes`);
    }
    if (c.kind !== "chat") continue;
    const needs = c.needs ?? [];
    for (const n of needs) if (!variables.includes(n)) problems.push(`${where}: unknown need ${n}`);
    const texts = [c.prompt ?? "", ...(c.checks ?? []).map((k) => k.pattern ?? "")];
    for (const text of texts) {
      for (const [, name] of text.matchAll(/\{\{(\w+)\}\}/g)) {
        if (!needs.includes(name)) problems.push(`${where}: placeholder ${name} is not in needs`);
      }
    }
    if (!c.prompt) problems.push(`${where}: no prompt`);
    for (const a of c.answers ?? []) {
      if (!decisions.includes(a.decision))
        problems.push(`${where}: unknown decision ${a.decision}`);
    }
    for (const k of c.checks ?? []) {
      if (!checkKinds.has(k.kind)) problems.push(`${where}: unknown check ${k.kind}`);
    }
  }

  return problems;
}

export function fill(text, vars, escape = false) {
  return text.replace(/\{\{(\w+)\}\}/g, (_, name) => {
    const value = vars[name] ?? "";

    return escape ? value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") : value;
  });
}

export function requireControlPlane(url, { production = false } = {}) {
  if (!url) {
    throw new Error(
      "--url is required: say which control plane to run against. There is no default.",
    );
  }
  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    throw new Error("--url must be a URL such as https://hub.example.com");
  }
  if (!["http:", "https:"].includes(parsed.protocol))
    throw new Error("--url must be a URL such as https://hub.example.com");
  if (parsed.username || parsed.password || !["", "/"].includes(parsed.pathname) || parsed.search) {
    throw new Error("--url must be the origin only, with no path, query or credentials");
  }
  if (productionHosts.has(parsed.hostname) && !production) {
    throw new Error(
      "--url is the production control plane: add --production to run there on purpose",
    );
  }

  return parsed.origin;
}

export function parseArgs(argv) {
  const o = {
    url: undefined,
    database: undefined,
    engine: undefined,
    providers: [...providers],
    only: [],
    list: false,
    yes: false,
    production: false,
    help: false,
    report: undefined,
    transcripts: undefined,
  };
  const value = (i, flag) => {
    if (i + 1 >= argv.length) throw new Error(`${flag} needs a value`);

    return argv[i + 1];
  };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    switch (flag) {
      case "--url":
        o.url = value(i++, flag);
        break;
      case "--database":
        o.database = value(i++, flag);
        break;
      case "--engine":
        o.engine = value(i++, flag);
        break;
      case "--provider": {
        const p = value(i++, flag);
        if (!["both", ...providers].includes(p))
          throw new Error("--provider is anthropic, cursor or both");
        o.providers = p === "both" ? [...providers] : [p];
        break;
      }
      case "--only":
        o.only = value(i++, flag).split(",").filter(Boolean);
        break;
      case "--report":
        o.report = value(i++, flag);
        break;
      case "--transcripts":
        o.transcripts = value(i++, flag);
        break;
      case "--list":
        o.list = true;
        break;
      case "--yes":
        o.yes = true;
        break;
      case "--production":
        o.production = true;
        break;
      case "--help":
      case "-h":
        o.help = true;
        break;
      default:
        throw new Error(`unknown option ${flag}`);
    }
  }

  return o;
}

// parseSSE reads a whole text/event-stream body. Comment lines, the heartbeat, are dropped.
export function parseSSE(text) {
  const events = [];
  for (const block of text.split("\n\n")) {
    let event = "message";
    const data = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("event:")) event = line.slice(6).trim();
      else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
    }
    if (data.length > 0) events.push({ event, data: JSON.parse(data.join("\n")) });
  }

  return events;
}

const secretPatterns = [
  [/sk-ant-[A-Za-z0-9_-]{8,}/, "a provider key"],
  [/Bearer\s+[A-Za-z0-9._~+/-]{16,}/, "a bearer token"],
  [/:\/\/[^/\s:@]+:(?![*•x<]+@)[^/\s@]{3,}@/, "a connection string with a password"],
];

// scanSecrets names the kinds of secret found in text and never returns a value. A known value
// shorter than four characters would match ordinary words, so it is not searched for.
export function scanSecrets(text, known) {
  const found = [];
  if (known.some((s) => s.length >= 4 && text.includes(s))) found.push("a secret the run knows");
  for (const [pattern, name] of secretPatterns) if (pattern.test(text)) found.push(name);

  return found;
}

export function redact(text, known) {
  let out = text;
  for (const s of known) if (s.length >= 4) out = out.split(s).join("[redacted]");
  for (const [pattern] of secretPatterns) out = out.replace(new RegExp(pattern, "g"), "[redacted]");

  return out;
}

// scoreChat judges one transcript: { events: [{event, data}], decisions: [{id, decision, name?, status}] }.
export function scoreChat(testCase, transcript, secrets, vars = {}) {
  const failures = [];
  const { events, decisions: given = [] } = transcript;
  const approvals = events.filter((e) => e.event === "approval").map((e) => e.data);
  const ended = new Map(
    events.filter((e) => e.event === "approval_end").map((e) => [e.data.id, e.data.outcome]),
  );
  const reply = events.findLast((e) => e.event === "reply")?.data.text ?? "";

  if (!events.some((e) => e.event === "reply") && !events.some((e) => e.event === "error")) {
    failures.push("the stream ended with no reply");
  }
  for (const check of testCase.checks ?? []) {
    switch (check.kind) {
      case "approval":
        if (!approvals.some((a) => a.tool === check.tool))
          failures.push(`no approval card for ${check.tool}`);
        break;
      case "noApproval":
        if (approvals.length > 0)
          failures.push(`an approval card appeared for ${approvals[0].tool}`);
        break;
      case "approvalOutcome": {
        for (const a of approvals.filter((x) => x.tool === check.tool)) {
          const outcome = ended.get(a.id);
          if (outcome !== check.outcome) {
            failures.push(
              `the card for ${check.tool} ended ${outcome ?? "never"}, expected ${check.outcome}`,
            );
          }
        }
        break;
      }
      case "approvalsOneAtATime": {
        let open = 0;
        for (const e of events) {
          if (e.event === "approval") open++;
          if (e.event === "approval_end") open--;
          if (open > 1) {
            failures.push("a second card was shown before the first was answered");
            break;
          }
        }
        break;
      }
      case "wrongNameRefused":
        if (!given.some((d) => d.decision === "approve" && d.name && d.status === 400)) {
          failures.push("the wrong name was not refused with a 400");
        }
        break;
      case "replyMatches":
        if (!new RegExp(fill(check.pattern, vars, true), "i").test(reply)) {
          failures.push(`the reply does not match ${check.pattern}`);
        }
        break;
      case "replyAbsent":
        if (new RegExp(fill(check.pattern, vars, true), "i").test(reply)) {
          failures.push(`the reply matches ${check.pattern}`);
        }
        break;
      case "noError":
        if (events.some((e) => e.event === "error"))
          failures.push("the stream carried an error event");
        break;
      default:
        failures.push(`unknown check ${check.kind}`);
    }
  }
  const leaks = scanSecrets(JSON.stringify(events), secrets);
  for (const leak of leaks) failures.push(`${leak} appears in the transcript`);

  return { outcome: failures.length === 0 ? "pass" : "fail", failures };
}

// parseGoTestJson turns `go test -json` output into one outcome for each top level test.
export function parseGoTestJson(text) {
  const output = new Map();
  const outcomes = new Map();
  for (const line of text.split("\n")) {
    let e;
    try {
      e = JSON.parse(line);
    } catch {
      continue;
    }
    if (!e.Test || e.Test.includes("/")) continue;
    if (e.Action === "output") output.set(e.Test, (output.get(e.Test) ?? "") + e.Output);
    if (["pass", "fail", "skip"].includes(e.Action)) outcomes.set(e.Test, e.Action);
  }

  return [...outcomes].map(([name, outcome]) => ({
    name,
    outcome,
    detail:
      outcome === "pass"
        ? ""
        : (output.get(name) ?? "")
            .split("\n")
            .map((l) => l.trim())
            .filter((l) => l && !l.startsWith("---") && !l.startsWith("=== "))
            .join(" ")
            .slice(0, 300),
  }));
}

const label = { pass: "pass", fail: "FAIL", skip: "skipped" };

export function renderReport({ date, url, versions, results, manual, cleanup }) {
  const models = Object.entries(versions.models ?? {})
    .map(([p, m]) => `${p}: ${m}`)
    .join(", ");
  const cell = (s) => String(s).replaceAll("|", "\\|").replaceAll("\n", " ");
  const lines = [
    "## AI tools evaluation",
    "",
    `- Date: ${date}`,
    `- Control plane: ${url}`,
    `- Script checkout: ${versions.commit}, node ${versions.node}`,
    `- Models: ${models || "none used"}`,
    "",
    "| Case | Provider | Result | Detail |",
    "| --- | --- | --- | --- |",
  ];
  for (const r of results) {
    lines.push(
      `| ${r.id} | ${r.provider ?? "all"} | ${label[r.outcome]} | ${cell(r.failures.join("; "))} |`,
    );
  }
  const failed = results.filter((r) => r.outcome === "fail").length;
  const passed = results.filter((r) => r.outcome === "pass").length;
  lines.push(
    "",
    `${passed} passed, ${failed} failed, ${results.length - passed - failed} skipped.`,
  );
  lines.push("", "### Cleanup", "");
  if (cleanup.length === 0) lines.push("Nothing to clean up.");
  for (const c of cleanup)
    lines.push(c.ok ? `- done: ${c.what}` : `- could not: ${c.what} (${c.detail})`);
  lines.push("", "### Manual checks (not run by the script)", "");
  for (const m of manual) {
    lines.push(`- [ ] ${m.id}: ${m.title}${m.optional ? " (optional, long)" : ""}`);
    lines.push(`  - Expect: ${m.expect}`);
  }

  return lines.join("\n") + "\n";
}
