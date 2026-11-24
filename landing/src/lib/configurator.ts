import mysqlMark from "@/assets/integrations/mysql-mark.png";
import mongodbMark from "@/assets/integrations/mongodb-mark.svg";
import postgresqlMark from "@/assets/integrations/postgresql-mark.png";

export type EngineId = "mysql" | "postgresql" | "mongodb";
export type ProfileId = "dev" | "staging" | "prod";

export type Engine = {
  id: EngineId;
  label: string;
  /** The engine's mark, as Percona's documentation draws it. */
  logo: string;
  /** The database the demo creates. */
  database: string;
};

export type Profile = {
  id: ProfileId;
  label: string;
  /** The line the console's profile card shows under the name. */
  consequence: string;
  members: number;
};

export const environmentName = "prod-us-west-2";

export const engines: Engine[] = [
  {
    id: "mysql",
    label: "MySQL",
    logo: mysqlMark,
    database: "orders",
  },
  {
    id: "postgresql",
    label: "PostgreSQL",
    logo: postgresqlMark,
    database: "payments",
  },
  {
    id: "mongodb",
    label: "MongoDB",
    logo: mongodbMark,
    database: "events",
  },
];

/** The engines as prose, from the one list the demo offers: "MySQL, PostgreSQL, and MongoDB". */
export function engineNames(conjunction: "and" | "or"): string {
  const labels = engines.map((engine) => engine.label);

  return `${labels.slice(0, -1).join(", ")}, ${conjunction} ${labels[labels.length - 1]}`;
}

// The profile names and consequence lines are the console's own.
export const profiles: Profile[] = [
  { id: "dev", label: "Dev", consequence: "1 member, on-demand backups", members: 1 },
  { id: "staging", label: "Staging", consequence: "3 members, HA, daily backups", members: 3 },
  { id: "prod", label: "Prod", consequence: "3 members, HA, daily backups", members: 3 },
];

/** The join command the console shows after "Create command", with the token masked. */
export const joinCommand =
  "curl -fsSL 'https://pds.percona.com/api/internal/connect/bootstrap.sh' | bash -s -- --token '••••••••••••'";

export type Selection = { engine: Engine; profile: Profile };

/** What the demo says in words, for readers who cannot see it. */
export function describeSelection({ engine, profile }: Selection): string {
  return `Preview: ${engine.label} database ${engine.database}, ${profile.label} profile, goes from Creating to Ready on ${environmentName}.`;
}
