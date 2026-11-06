// The platforms pds-mcp ships for: the names Node reports, and the names Go builds them under.
export const platforms = [
  { os: "darwin", cpu: "arm64", goos: "darwin", goarch: "arm64" },
  { os: "darwin", cpu: "x64", goos: "darwin", goarch: "amd64" },
  { os: "linux", cpu: "arm64", goos: "linux", goarch: "arm64" },
  { os: "linux", cpu: "x64", goos: "linux", goarch: "amd64" },
];

export const packageName = "@gas-lab/pds-mcp";

// Where a platform's binary sits inside the package, relative to its root.
export function binaryFolder(os, cpu) {
  const platform = platforms.find((candidate) => candidate.os === os && candidate.cpu === cpu);
  if (platform === undefined) {
    throw new Error(
      `pds-mcp does not run on ${os} ${cpu}. It runs on macOS and Linux, on arm64 and x64.`,
    );
  }
  return `bin/${platform.os}-${platform.cpu}`;
}
