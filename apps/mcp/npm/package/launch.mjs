import { join } from "node:path";

import { binaryFolder } from "./platform.mjs";

// root is the directory of the installed package, passed in so a test can point at a built one.
export function binaryPath(os, cpu, root) {
  return join(root, binaryFolder(os, cpu), "pds-mcp");
}
