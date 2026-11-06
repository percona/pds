#!/usr/bin/env node
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

import { binaryPath } from "../launch.mjs";

let binary;
try {
  binary = binaryPath(
    process.platform,
    process.arch,
    fileURLToPath(new URL("..", import.meta.url)),
  );
} catch (error) {
  process.stderr.write(`${error.message}\n`);
  process.exit(1);
}

// stdio is inherited: the agent talks to the binary directly and the launcher never touches it.
const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(signal, () => child.kill(signal));
}

child.on("error", (error) => {
  process.stderr.write(`pds-mcp could not start ${binary}: ${error.message}\n`);
  process.exit(1);
});

child.on("exit", (code, signal) => {
  if (signal !== null) {
    process.kill(process.pid, signal);
  } else {
    process.exit(code ?? 1);
  }
});
