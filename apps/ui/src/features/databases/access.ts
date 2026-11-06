import type { Database, DatabaseVisibility } from "./types";

/**
 * Where a database can be reached from. Pending is an external database whose
 * load balancer has no address yet: the database may be running, but nothing
 * outside the cluster can reach it until the address exists.
 */
export type Reachability = "internal" | "external" | "pending";

export function reachabilityOf(database: Database): Reachability {
  const access = database.access;
  if (!access?.external) {
    return "internal";
  }

  return access.address ? "external" : "pending";
}

/**
 * Where a database accepts connections from. An adapter older than private
 * access reports no visibility, and every load balancer it made was public.
 */
export function visibilityOf(database: Database): DatabaseVisibility {
  const access = database.access;
  if (!access?.external) {
    return "cluster";
  }

  return access.visibility ?? "public";
}

/**
 * Whether the connection string can be copied from this page. Only a public
 * database's password is held here. A private one is read on its own environment,
 * like one with no load balancer.
 */
export function copiesHere(database: Database): boolean {
  if (reachabilityOf(database) !== "external") {
    return false;
  }

  return visibilityOf(database) === "public";
}

/** What a public database sends when it is open to the internet. */
export const defaultSourceRange = "0.0.0.0/0";

export function allowsAnyAddress(ranges: readonly string[]): boolean {
  return ranges.some((range) => range === "0.0.0.0/0" || range === "::/0");
}

/** The allow list cap the adapter enforces. */
export const maxSourceRanges = 32;

export type ParsedSourceRanges = {
  ranges: string[];
  /** The first problem, worded the way the adapter words it. */
  error?: string;
};

/**
 * Reads the allowed addresses field. Entries are separated by commas or
 * whitespace; each is an IP address or a CIDR range. The adapter checks the
 * list again and normalizes it, so this only saves a round trip.
 */
export function parseSourceRanges(text: string): ParsedSourceRanges {
  const entries = text.split(/[\s,]+/).filter((entry) => entry !== "");

  for (const entry of entries) {
    if (!isSourceRange(entry)) {
      return { ranges: [], error: `\`${entry}\` is not an IP address or CIDR range.` };
    }
  }

  if (entries.length > maxSourceRanges) {
    return { ranges: [], error: `Allow at most ${maxSourceRanges} ranges.` };
  }

  return { ranges: entries };
}

function isSourceRange(entry: string): boolean {
  const [address, prefix, ...rest] = entry.split("/");
  if (rest.length > 0 || address === undefined) {
    return false;
  }

  const bits = isIPv4(address) ? 32 : isIPv6(address) ? 128 : 0;
  if (bits === 0) {
    return false;
  }
  if (prefix === undefined) {
    return true;
  }
  if (!canonicalDecimal.test(prefix)) {
    return false;
  }

  return Number(prefix) <= bits;
}

// The adapter parses with Go's netip, which refuses a leading zero in an octet
// or a prefix length, because some parsers read one as octal.
const canonicalDecimal = /^(0|[1-9]\d{0,2})$/;

function isIPv4(address: string): boolean {
  const octets = address.split(".");

  return (
    octets.length === 4 &&
    octets.every((octet) => canonicalDecimal.test(octet) && Number(octet) <= 255)
  );
}

// The URL parser already knows every IPv6 spelling, including the compressed
// and embedded IPv4 forms, so it is the check rather than a hand written one.
function isIPv6(address: string): boolean {
  if (!address.includes(":") || !/^[0-9a-fA-F:.]+$/.test(address)) {
    return false;
  }

  try {
    return new URL(`http://[${address}]/`).hostname !== "";
  } catch {
    return false;
  }
}
