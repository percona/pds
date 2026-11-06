import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatTimestamp } from "@/lib/format";
import { useEnvironmentHistory, useDatabaseHistory } from "./hooks";

const kindLabel: Record<string, string> = {
  action: "Action",
  transition: "Status",
  backup: "Backup",
  restore: "Restore",
  event: "Event",
};

export function HistoryTab({ databaseId }: { databaseId: string }) {
  const history = useDatabaseHistory(databaseId);
  const entries = history.data?.entries ?? [];

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>History</CardTitle>
        </CardHeader>
        <CardContent>
          {history.isPending ? (
            <p className="text-sm text-muted-foreground">Loading history.</p>
          ) : null}
          {history.isError ? (
            <p className="text-sm text-destructive">{history.error.message}</p>
          ) : null}
          {!history.isPending && entries.length === 0 ? (
            <p className="text-sm text-muted-foreground">No history yet.</p>
          ) : null}
          {entries.length > 0 ? (
            <ul className="divide-y divide-border">
              {entries.map((entry) => (
                <li key={entry.id} className="flex items-baseline justify-between gap-4 py-3">
                  <div className="min-w-0">
                    <p className="text-sm">
                      {kindLabel[entry.kind] ?? entry.kind}
                      {entry.action ? ` ${entry.action}` : ""}
                      {entry.toPhase ? ` ${entry.fromPhase || "new"} to ${entry.toPhase}` : ""}
                      {entry.outcome ? ` ${entry.outcome}` : ""}
                    </p>
                    {entry.message || entry.detail || entry.error ? (
                      <p className="truncate text-xs text-muted-foreground">
                        {entry.message || entry.detail || entry.error}
                      </p>
                    ) : null}
                    {entry.kind === "backup" || entry.kind === "restore" ? (
                      entry.sizeBytes != null || entry.durationMs != null ? (
                        <p className="font-mono text-xs text-muted-foreground">
                          {entry.sizeBytes != null ? `${entry.sizeBytes} bytes` : null}
                          {entry.sizeBytes != null && entry.durationMs != null ? " " : null}
                          {entry.durationMs != null ? `${entry.durationMs} ms` : null}
                        </p>
                      ) : null
                    ) : null}
                  </div>
                  <p className="shrink-0 font-mono text-xs text-muted-foreground">
                    {formatTimestamp(entry.at)}
                  </p>
                </li>
              ))}
            </ul>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}

export function JoinHistory({ environment }: { environment: string }) {
  const history = useEnvironmentHistory(environment);
  const runs = history.data?.runs ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>Join history</CardTitle>
      </CardHeader>
      <CardContent>
        {history.isPending ? (
          <p className="text-sm text-muted-foreground">Loading join history.</p>
        ) : null}
        {history.isError ? (
          <p className="text-sm text-destructive">{history.error.message}</p>
        ) : null}
        {!history.isPending && runs.length === 0 ? (
          <p className="text-sm text-muted-foreground">No join has been recorded.</p>
        ) : null}
        {runs.map((run) => (
          <div key={run.runId} className="space-y-2 py-2">
            <p className="font-mono text-xs text-muted-foreground">{run.runId}</p>
            <ul className="divide-y divide-border">
              {run.phases.map((phase) => (
                <li key={`${run.runId}-${phase.phase}`} className="py-2">
                  <p className="text-sm">
                    {phase.phase} {phase.outcome}
                    {phase.durationMs ? (
                      <span className="font-mono text-xs text-muted-foreground">
                        {" "}
                        {phase.durationMs} ms
                      </span>
                    ) : null}
                  </p>
                  {phase.output ? (
                    <pre className="mt-1 overflow-x-auto font-mono text-xs text-muted-foreground">
                      {phase.output}
                    </pre>
                  ) : null}
                </li>
              ))}
            </ul>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}
