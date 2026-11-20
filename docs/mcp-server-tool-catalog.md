# PDS MCP server: tool catalog

The tools live in the Go registry in `adapter/internal/mcp`, which the control plane serves at `/mcp` to every MCP client alike, limited to the tier of the caller's token. `pds-mcp` in `apps/mcp` is a stdio bridge to that endpoint and adds none of its own; the sign in, the bridge, asynchronous operations, errors and the safety model are in [mcp-server-design.md](mcp-server-design.md).

## 1. Conventions

- **Names.** Tools are `verb_noun` in snake_case. Inputs are camelCase and use the same vocabulary the API returns (`memoryGb`, `storageGb`, `members`). The registry maps them to the API's request names (`memory`, `storage`, `replicas`).
- **Database id.** An opaque string copied exactly from `list_databases` or a create result. It is `environment/namespace.name`. The server checks it against `^[a-z0-9-]+/[a-z0-9-]+\.[a-z0-9-]+$` before building any URL, rejects anything else as `invalid_input`, including a one-segment id, encodes each path segment separately and keeps the slash. The id comes from a model, so it must never be able to reach another route.
- **Results.** Every result carries the same compact JSON twice: as `structuredContent`, validated by an `outputSchema`, and as a single text block (no pretty printing). An error is the same shape with `isError: true`.
- **Requests.** Every error carries a `requestId` that a customer can quote to Percona support. A request times out after 30 seconds. Writes are never retried after a timeout.
- **Copy.** Descriptions use customer vocabulary only (control plane, monitoring, database platform, environment). No hub, spoke, adapter, PMM, Everest, Flux, or operator as narrator. No em or en dashes, no exclamation marks in errors, sentence case. The adapter's customer copy test and the contract test of `apps/mcp` over `tools/list` hold it.

## 2. Registration

| Group               | Tools                                                                                                                                                                                                                                                                                                                                                   | Listed at                  |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------- |
| Read                | `list_environments`, `get_environment`, `list_cloud_connections`, `get_cloud_connection`, `list_databases`, `get_database`, `list_backups`, `get_metrics`, `list_activity`, `get_activity`, `list_alerts`, `get_database_alerts`, `get_database_history`, `get_database_usage`, `get_environment_history`, `list_support_tickets`, `get_support_ticket` | every tier                 |
| Wait                | `wait_for_database`, `wait_for_backup`, `wait_for_environment`                                                                                                                                                                                                                                                                                          | every tier                 |
| Changes             | `create_database`, `scale_database`, `backup_database`, `set_backup_schedule`, `restore_database`, `create_support_ticket`, `reply_to_support_ticket`, `create_environment`, `start_cloud_connection`, `create_managed_environment`, `retry_environment_run`, `upgrade_environment`                                                                     | the change tier and above  |
| Destructive changes | `delete_database`, `delete_managed_environment`, `rebuild_environment`, `remove_cloud_connection`                                                                                                                                                                                                                                                       | the change and delete tier |

- A feature the control plane has switched off or not configured answers 501, and the tool returns `not_available` with the control plane's customer sentence. This holds for the support tools and for environments.
- Counts: 17 read tools, 25 at the change tier, 26 at the change and delete tier. `get_logs` and `get_platform_status` are not registered.
- **Access tiers.** The caller's access tier decides: `read`, `change` (the changes and `reply_to_support_ticket`) or `change_delete` (which adds `delete_database`). The tier is the lowest of the Organization's limit, the sign-in choice and what the person's role allows, and the caller is built with it. `tools/list` returns only the tools the tier allows, and a call to a tool above it is refused as `not_allowed` before any request is made. A caller whose client kind is not `chat` or `mcp` is served the read tier whatever its access says, because every change is recorded with the client that made it. The console Chat calls the registry as client `chat` and is served the read tier, so it is offered the read and wait tools and no change tool.

## 3. Annotations

| Tool                               | readOnly | destructive | idempotent | openWorld |
| ---------------------------------- | -------- | ----------- | ---------- | --------- |
| all read tools and both wait tools | true     | false       | true       | false     |
| `create_database`                  | false    | false       | false      | false     |
| `scale_database`                   | false    | false       | true       | false     |
| `backup_database`                  | false    | false       | false      | false     |
| `set_backup_schedule`              | false    | false       | true       | false     |
| `restore_database`                 | false    | false       | false      | false     |
| `create_support_ticket`            | false    | false       | false      | false     |
| `reply_to_support_ticket`          | false    | false       | false      | false     |
| `create_environment`               | false    | false       | false      | false     |
| `start_cloud_connection`           | false    | false       | true       | false     |
| `create_managed_environment`       | false    | false       | false      | false     |
| `retry_environment_run`            | false    | false       | false      | false     |
| `upgrade_environment`              | false    | false       | false      | false     |
| `delete_database`                  | false    | true        | false      | false     |
| `delete_managed_environment`       | false    | true        | false      | false     |
| `rebuild_environment`              | false    | true        | false      | false     |
| `remove_cloud_connection`          | false    | true        | false      | false     |

Annotations are hints. The real gates are the registration rules and access tiers above, the `confirm` check, and the open range acknowledgement.

## 4. Read tools

### list_databases

- **API:** `GET /api/databases` (`?environment=` when an environment is named).
- **Input:** `environment` (string, optional filter).
- **Result:** `{databases: [{id, name, engine, profile, status, members, environment?}]}`. A short row per database; `get_database` has the rest.
- **Description:** List the databases on every connected environment, or only on one environment. Each row is short: use get_database for details. Environment state can be a little behind the real environment.

### get_database

- **API:** `GET /api/databases/{id}`.
- **Input:** `id`.
- **Result:** the database as the API returns it: `id`, `name`, `engine`, `profile`, `namespace`, `environment?`, `status`, `message?`, `members`, `cpu`, `memoryGb`, `storageGb`, `createdAt?`, `lastBackupAt?`, `backupSchedule?`, `connection?`, `access`. `connection` has no password and is absent until it is published.
- **Description:** Get one database: status, size, backup schedule, who can connect, and connection details without the password. A database is usable when status is ready and connection is present. If message names a blocking problem, tell the user.

### list_backups

- **API:** `GET /api/databases/{id}/backups`.
- **Input:** `id`.
- **Result:** `{backups: [{id, status, sizeBytes, scheduled, createdAt?, finishedAt?, message?}]}`, newest first. Status is pending, running, succeeded, failed, deleting or unknown.
- **Description:** List the backups of a database, newest first. Fails with not available when backups are not set up for the environment.

### get_metrics

- **API:** `GET /api/databases/{id}/metrics?window=&metrics=`.
- **Input:** `id`; `window` (`1h` default, `24h`); `metrics` (array of `cpu`, `memory`, `connections`, `replication-lag`, `disk`; default all); `includePoints` (boolean, default false).
- **Result:** `{databaseId, window, start, end, stepSeconds, pending, pendingReason?, metrics: [{kind, unit, pending, current, min, max, average, trend, points?}]}`. Points appear only with `includePoints: true`.
- **Description:** Get resource metrics for a database over the last hour or day. Returns current, minimum, maximum and average values and a trend for each metric. Values are zero while pending is true, so read pending before trusting a number. Ask for points only when you need the series. On a control plane with several environments, metrics come from usage samples sent by each environment and may be pending.

### list_activity

- **API:** `GET /api/activity?databaseId=&since=&limit=`.
- **Input:** `databaseId` (optional); `limit` (positive integer, default 50); `since` (cursor from an earlier call).
- **Result:** `{entries: [{id, at, actor, requestId, action, databaseId, target, outcome, detail, error, resourceKind, resourceName, client?, agent?}], cursor, truncated}`. `client` is `mcp` for an external agent and `chat` for the console Chat, and `agent` names the program, such as `Cursor` or `Claude Code` for an agent and `Anthropic` for the Chat. A person's change in the console carries neither.
- **Description:** List recent changes made through the control plane, newest first. Entries are written when a change is requested and do not update later, so an accepted entry does not mean the change finished. Use wait_for_database or wait_for_backup to follow progress.

### get_database_history

- **API:** `GET /api/databases/{id}/history?kind=&since=&until=&cursor=&limit=`.
- **Input:** `id`; `kind` (`action`, `transition`, `backup`, `restore` or `event`); `since` and `until` (RFC 3339 times); `cursor` (from an earlier call); `limit` (1 to 100, default 20).
- **Result:** `{entries: [{id, at, kind, actor?, client?, agent?, action?, target?, outcome?, detail?, error?, fromPhase?, toPhase?, message?, reason?, eventType?, count?, objectId?, sizeBytes?, durationMs?}], cursor?, truncated}`, newest first. A row carries only the fields its kind sets. `cursor` is present only when `truncated` is true.
- **Description:** Get what happened to one database, newest first: changes made through the control plane, status changes, backups, restores and events. Filter by kind, since and until (RFC 3339 times). limit is 1 to 100 (default 20). When truncated is true, pass the cursor to get older entries. Use it to explain why a database changed status or when a backup failed.

### get_database_usage

- **API:** `GET /api/databases/{id}/usage?since=&until=&limit=`.
- **Input:** `id`; `since` and `until` (RFC 3339 times); `limit` (1 to 100, default 20).
- **Result:** `{samples: [{at, cpuAvg?, cpuMax?, memoryAvg?, memoryMax?, storageBytes?, metricsReason?}]}`, newest first. CPU is in cores, memory and storage in bytes.
- **Description:** Get CPU, memory and storage samples for one database, newest first. Samples are taken about every five minutes and kept for seven days, and they work when monitoring is off. CPU is in cores and memory and storage are in bytes. A sample with a metricsReason has no CPU or memory figures. limit is 1 to 100 (default 20): narrow with since and until (RFC 3339 times) for a longer span.

### get_environment_history

- **API:** `GET /api/environments/{name}/history`.
- **Input:** `name`, checked against the shape of an environment name before any request. A name the control plane does not know answers `not_found` with a hint to call `list_environments`.
- **Result:** `{runs: [{runId, phases: [{phase, outcome, startedAt?, finishedAt?, durationMs, output?}]}]}`, oldest run first. `output` is the last 500 characters of the step's log.
- **Description:** Get the steps of each attempt to connect an environment, with how each step ended and the end of its log. Use it to find out why an environment did not connect. name is an environment name from list_environments.

### list_alerts

- **API:** `GET /api/alerts`.
- **Input:** `limit` (1 to 100, default 25). The route takes none, so the tool cuts the list and keeps the route's order.
- **Result:** `{environments: [{name, state, reason?, readAt?, total, shown, note?}], alerts: [{id, environment, databaseId?, rule, severity, summary, startsAt}], criticals, total, truncated}`. Alerts are critical first, then oldest first. `state` is `live`, `stale` or `off`. `criticals` and `total` count the alerts the route listed, before the limit.
- **Description:** List the alerts firing on every environment, critical first, with the state of each environment's monitoring. An empty list means nothing is firing only for an environment whose state is live: read environments before saying all is well. limit is 1 to 100 (default 25). Each alert names its database when it maps to one: use get_database for details.

### get_database_alerts

- **API:** `GET /api/databases/{id}/alerts`.
- **Input:** `id`.
- **Result:** `{databaseId, environment: {name, state, reason?, readAt?, total, shown, note?}, alerts: [...]}`, with the alert shape of `list_alerts`.
- **Description:** Get the alerts firing on one database, critical first, with the state of the environment it runs in. An empty list means nothing is firing only when the environment state is live.

### get_activity

- **API:** `GET /api/activity/{id}`.
- **Input:** `id` (an entry id from `list_activity`: letters, digits, dashes or underscores, at most 64).
- **Result:** `{event, resource?, timeline: [{at, source, type, reason?, message?, count?}], notes?, truncated}`. `event` is the `list_activity` entry shape. `resource` is what the change made (`kind`, `name`, `namespace?`, `state`, `message?`, `startedAt?`, `endedAt?`, `sizeBytes?`) as it is now, so it can differ from `event`. The timeline is oldest first.
- **Description:** Get one change from the activity log with what is known about the work it started: the object's state now and a timeline of events. id is an entry id from list_activity. The log keeps only the most recent changes, so an older entry may be gone.

### list_environments

- **API:** `GET /api/environments`.
- **Input:** none.
- **Result:** `{environments: [{name, namespace?, status, lastSeen?, origin, state, connectionId?, region?, provisioning?}]}`. `origin` is `connected` or `managed`; `state` is `creating`, `ready`, `degraded`, `failed` or `deleting`. `provisioning` is set on a managed environment, see "Managed environments" below.
- **Description:** List the environments of this control plane, with their status and when each was last seen. origin says whether the user connected the Kubernetes cluster or PDS built the environment, and state says how it is. A managed environment also carries provisioning: the four phases of its latest run and, when a run failed, what to do, and lifecycle: a newer PDS version, the maintenance window, changes made outside PDS and whether its infrastructure is lost. Use an environment name when creating a database.

### get_environment

- **API:** `GET /api/environments/{name}`.
- **Input:** `name`.
- **Result:** one entry of `list_environments`. A managed environment has `state` `ready`, `degraded`, `creating`, `failed`, `deleting` or `lost`, `provisioning` and `lifecycle`. `lifecycle` holds `chartVersion`, `pendingChartVersion`, `skippedChartVersion`, `deferred` and `deferredUntil`, `maintenanceWindow` (`day` 0 for Sunday to 6, `hour`, `timeZone`, `nextStart`, `nextEnd`), `templateRevision`, `latestTemplateRevision`, `templateOutOfDate` and `templateSummary` (for example "Template revision 1, latest 2"), `kubernetesVersion` and `kubernetesUpgradeAvailable`, `drift` (each `resource` and `detail`, from the hourly check), `driftCheckedAt`, and `lost`, `lostReason` ("Infrastructure missing") and `lostAt`. The Kubernetes version and template revision are what the hourly check last read, and are empty or zero until it has.
- **Description:** Get one environment with its status, origin and state. A managed environment carries provisioning: its latest run, the four phases (Creating network and Kubernetes cluster, Connecting to the environment, Installing PDS, Finishing) and, when a run failed, failure with the sentence to tell the user and the one action. When AWS refused a step, failure.refusal says which quota or permission and where. lifecycle names a pending PDS version, its deferral, the maintenance window, templateSummary when the infrastructure template is old, kubernetesUpgradeAvailable, drift by resource (changed outside PDS: report it, PDS never overwrites it) and lostReason when the Kubernetes cluster is gone. When state is lost, ask the user whether to rebuild it with rebuild_environment. Removing it from PDS only is for the user in the console.

### list_cloud_connections

- **API:** `GET /api/cloud-connections`.
- **Input:** none.
- **Result:** `{cloudConnections: [{id, accountId, alias?, state, reason?, allowedRegions, environments, checkedAt?, readyAt?}]}`. `state` is `pending`, `ready`, `unreachable` or `out_of_date`. `environments` names the managed environments that run through the AWS account. The result has no ExternalId, role or connect command.
- **Description:** List the AWS accounts connected to this team, with their state and the managed environments that run through each. State is pending, ready, unreachable or out_of_date. Only a ready AWS account can host a new environment. reason says in a sentence why an account is not ready.

### get_cloud_connection

- **API:** `GET /api/cloud-connections/{id}`.
- **Input:** `id` (twelve lowercase hex digits, from `list_cloud_connections`).
- **Result:** one entry of `list_cloud_connections` with `steps?` (`{step, status, at?}`, what the connect command reported).
- **Description:** Get one connected AWS account with its state, the regions it allows, its managed environments and the steps the connect command reported. id is copied from list_cloud_connections. Nothing here lets you repair an account: tell the user what reason says and link them to the console.

### list_support_tickets

- **API:** `GET /api/support/tickets`.
- **Input:** `limit` (1 to 100, default 25); `offset` (0 or more). Both pass straight through to the control plane.
- **Result:** `{tickets: [{number, state?, priority?, subject?, openedAt?, updatedAt?, resolvedAt?}], portalUrl?}`.
- **Description:** List the support tickets of this account, newest first, and the link to the support portal. limit is 1 to 100 (default 25) and offset skips that many tickets. Fails with not available when support is not set up.

### get_support_ticket

- **API:** `GET /api/support/tickets/{number}`.
- **Input:** `number` (3 to 32 letters, digits, dashes or underscores, such as `CS0061234`).
- **Result:** the ticket with `description?`, `comments?` (`{at?, author?, label?, body}`, public comments only) and `portalUrl?`.
- **Description:** Get one support ticket with its state and public comments. number is the ticket number from list_support_tickets or create_support_ticket, such as CS0061234. Fails with not available when support is not set up.

## 5. Wait tools

All three poll every 5000 ms. The overall deadline is enforced with an abort signal, so one slow poll cannot exceed it. There is no restore grace window.

The Go registry in `adapter/internal/mcp` serves all three (`wait.go`). Its deadline rides on the request context: each poll is cut off at the deadline, with at least one second left for the poll that runs at it. Time goes through an injected `Clock`, so tests wait on a manual clock and never sleep.

### wait_for_database

- **Input:** `id`; `until` (`ready` or `deleted`); `expect` (optional object with `cpu`, `memoryGb`, `storageGb`, `members`, mirroring `scale_database`); `timeoutSeconds` (1 to 45, default 30).
- **Done when:**
  - `ready`: status is ready and `connection` is present, and every field in `expect` matches. Create, scale and restore all wait this way. A restore is a new database.
  - `deleted`: the database is no longer found.
  - A status of failed gives the outcome `failed` while waiting for ready.
- **Result:** `{outcome, id, status?, message?, waitedSeconds, database?}` where `outcome` is `succeeded`, `failed`, `in_progress` or `unknown`. `database` has the get_database shape unless the database is gone. A stalled database stays `in_progress` and the blocking reason arrives in `message`. A reached `failed` is an observation, not a tool error.
- **Description:** Wait for a database to reach a state, up to timeoutSeconds (default 30, at most 45). Use until ready after create_database, scale_database or restore_database, and deleted after delete_database. Pass expect with the sizes you asked for when waiting on a scale. On in_progress a database can still take several minutes: call again. If message names a blocking problem, tell the user.

### wait_for_backup

- **Input:** `id`; `backupId`; `timeoutSeconds` (1 to 45, default 30).
- **Result:** `{outcome, id, backupId, backup?, waitedSeconds}`. `succeeded` when the backup is succeeded, `failed` when it is failed, `in_progress` while pending or running, `unknown` when the backup never appears in the list during the wait.
- **Description:** Wait for a backup to finish, up to timeoutSeconds (default 30, at most 45). Returns succeeded, failed, in_progress or unknown. A large backup can take many minutes: call again on in_progress.

### wait_for_environment

- **API:** `GET /api/environments`.
- **Input:** `name`; `until` (`connected` default, or `deleted`); `timeoutSeconds` (1 to 45, default 30).
- **Result:** `{outcome, name, status?, state?, provisioning?, waitedSeconds}`. `connected` when the environment reports in and no run is building it; `failed` or `blocked` when its latest run failed or a delete left something behind (both end the wait); `lost` when its Kubernetes cluster is gone from the AWS account; `waiting` when a run is paused until the AWS account answers; `in_progress` while listed and not done; `deleted` when `until` is `deleted` and the environment is gone; `not_found` when it never appears.
- **Description:** Wait for an environment to connect, up to timeoutSeconds (default 30, at most 45). Use it after create_environment, once the user has run the command from the console. Returns connected, in_progress or not_found. On in_progress, call it again: connecting can take several minutes.

## 6. Change tools

Each returns the envelope in section 7. All return before the work is done, and say so. On the Go registry a change is sent once and carries the caller's client kind and agent name, which the activity log records beside the actor.

### create_database (changes)

- **API:** `POST /api/databases`.
- **Input:** `name` (lowercase letters, digits and dashes, at most 63 characters, 37 for `postgresql`); `environment` (required, from `list_environments`); `profile` (`dev`, `staging`, `prod`); `cpu` (cores, 1 to 64); `memoryGb` (1 to 512); `storageGb` (10 to 16000); `access` (`private` default, `public`); `sourceRanges` (required when `access` is `public`; at most 32 IP addresses or CIDR ranges); `acknowledgeOpenToInternet` (boolean); `engine` (`mysql` default, `postgresql`).
- **Rules:** the numeric bounds are written in the description and enforced by the API. The server never fills in `sourceRanges`. If `sourceRanges` includes `0.0.0.0/0` or `::/0`, the user's word is needed before any request is sent. A client that declared form mode elicitation is asked: the question names the database, the environment and every source range, and has one box to tick (see "Confirmations" below). Such a client's `acknowledgeOpenToInternet` is ignored. Any other client is refused with `confirmation_required` unless `acknowledgeOpenToInternet` is true. `namespace` is not exposed.
- **Result:** the envelope with `id` and `status`; `next` is `wait_for_database` with `until: ready`. With an open range, `warnings` repeats that any address on the internet can connect.
- **Description:** Create a MySQL or PostgreSQL database on one environment of this control plane. engine is mysql or postgresql and defaults to mysql: ask the user when they have not said. environment is required: call list_environments and ask the user which one. profile sets the default size: dev is 1 member, 1 CPU, 2 GB memory, 20 GB storage and no high availability; staging is 3 members, 2 CPU, 8 GB and 100 GB; prod is 3 members, 4 CPU, 16 GB and 250 GB. cpu, memoryGb and storageGb override the profile within limits: 1 to 64 CPU, 1 to 512 GB memory, 10 to 16000 GB storage. Databases are private by default, for both engines: reachable only on private address ranges, through an internal load balancer. For access public you must ask the user which addresses may connect and pass them as sourceRanges: never choose 0.0.0.0/0 yourself. If the user chooses it, warn them that any address on the internet can connect. A client that can ask the user asks them to confirm, and the call goes on only on their yes, whatever acknowledgeOpenToInternet says. For any other client, pass acknowledgeOpenToInternet true after the user confirms. The call returns before the database is ready: use wait_for_database. A new database starts as pending until the environment picks it up.

### scale_database (changes)

- **API:** `POST /api/databases/{id}/scale`.
- **Input:** `id`; at least one of `cpu`, `memoryGb`, `storageGb`, `members` (1, 3 or 5). Storage can only increase.
- **Result:** the envelope with the current object, `requested` echoing the target and a note that the sizes shown may still be the old ones; `next` is `wait_for_database` with `until: ready` and `expect` set to `requested`.
- **Description:** Change the size of a database: CPU cores, memory in GB, storage in GB (increase only) or members (1, 3 or 5). Set at least one. Members restart one at a time. Refused while a backup is running. The call returns before the change is applied: use wait_for_database with expect.

### backup_database (changes)

- **API:** `POST /api/databases/{id}/backup`.
- **Input:** `id`.
- **Result:** the envelope with `backupId`; `next` is `wait_for_backup`.
- **Description:** Start an on-demand backup of a database. Returns a backupId right away: use wait_for_backup to follow it. Fails with not available when backups are not set up for the environment.

### set_backup_schedule (changes)

- **API:** `POST /api/databases/{id}/backup-schedule`.
- **Input:** `id`; `enabled` (required); `period` (`hourly`, `daily`, `weekly`; required when enabled); `minute` (0 to 59, default 0); `hour` (0 to 23; required for daily and weekly, not allowed for hourly); `weekday` (0 for Sunday to 6; required for weekly, not allowed otherwise); `retentionCopies` (1 to 30, default 7). All times are UTC. `enabled: false` turns the schedule off and keeps its settings.
- **Result:** the envelope with `schedule`, the schedule that was written. No `next`.
- **Description:** Turn the backup schedule of a database on or off, or change it. enabled is required. When enabled, set period to hourly, daily or weekly. daily and weekly also need hour, and weekly needs weekday (0 is Sunday). All times are UTC. The result shows the schedule that was written. On a control plane with several environments this works only for databases created through it, and the change appears on the environment after a short delay: check with get_database.

### restore_database (changes)

- **API:** `POST /api/databases/{id}/restore`.
- **Input:** `id`; `backupId`; `name` (the new database name: lowercase letters, digits and dashes, at most 19 characters).
- **Result:** the envelope with the new database `id`. `next` is `wait_for_database` with `until: ready`.
- **Description:** Restore a backup into a new database on the same environment. The original database is left alone. name is the new database name. The copy has no public address. The call returns before the new database is ready: use wait_for_database on the returned id.

### delete_database (destructive changes)

- **API:** `DELETE /api/databases/{id}`.
- **Input:** `id`; `confirm` (the database name, exact match). The server reads the database first and compares its stored `name` to `confirm`. A client that declared form mode elicitation is asked to type the name, and `confirm` is ignored; the typed value is compared to the stored `name` the same way. For any other client, on a mismatch or a missing value the call is refused with `invalid_input` and nothing is sent.
- **Result:** the envelope with `id`; `next` is `wait_for_database` with `until: deleted`.
- **Description:** Delete a database. This cannot be undone. A client that can ask the user asks them to type the database name, and the call goes on only when it matches, whatever confirm says. For any other client, ask the user to confirm and pass the database name as confirm. The call returns before the database is gone: use wait_for_database with until deleted.

### create_support_ticket (changes)

- **API:** `POST /api/support/tickets`; with `databaseId`, `GET /api/databases/{id}` first.
- **Input:** `subject` (at most 400 characters); `description` (at most 40000); `databaseId` (optional).
- **Rules:** given a database, the server reads it and sends the same `context` the console sends: database name, profile, engine, engine version, topology (`cluster` when it has more than one member, else `standalone`), namespace, members, status and the database's message as the error message. An unknown database refuses with `not_found` and no ticket is sent.
- **Result:** the envelope with `id` (the ticket number), `status` (the ticket state) and `portalUrl`.
- **Description:** Open a support ticket with Percona. Give a subject that says what stopped working and a description with what you saw and tried. Pass databaseId to attach the database's profile, engine, version, members, namespace, status and error message to the ticket. Returns the ticket number, its state and the support portal link. Fails with not available when support is not set up.

### reply_to_support_ticket (changes, registry only)

- **API:** `POST /api/support/tickets/{number}/replies`, which answers 204.
- **Input:** `number` (a ticket number from `list_support_tickets` or `create_support_ticket`, checked against `^[A-Za-z0-9_-]{3,32}$`); `body` (1 to 40000 characters).
- **Rules:** the reply is a public comment from the user's account, and the tool never writes one the agent was not given. A ticket the control plane does not know answers `not_found`.
- **Result:** the envelope with `id` (the ticket number). No `next`.
- **Description:** Add a reply to a support ticket. It is posted as a comment from the user's account, and Percona support reads it. number is the ticket number from list_support_tickets or create_support_ticket, such as CS0061234. body is the message, up to 40000 characters. Ask the user before sending anything they have not seen. Fails with not available when support is not set up.

### create_environment (changes)

- **API:** `GET /api/environments`, then `POST /api/connect/tokens`.
- **Input:** `name`, which is also the EKS cluster name.
- **Rules:** a name that is already listed refuses with `conflict` and sends no write. The join token and command the control plane returns are discarded: neither appears in a result, a log line or an error.
- **Result:** the envelope with `id` (the name), `expiresAt`, `consoleUrl` (`<PDS_URL>/environments/new`) and a `note` that tells the user to open that link, enter the same name and run the command it creates. `next` is `wait_for_environment`. On the Go registry `consoleUrl` is present only when the registry was given the console's address; without it the note sends the user to the console instead.
- **Description:** Register a new environment. name is also the name of the EKS cluster the user creates, so ask the user for it. Refused when an environment with that name already exists. This call does not connect anything: it returns consoleUrl. Ask the user to open it, enter the same name and run the command it creates. The command is never shown here. Then use wait_for_environment.

### start_cloud_connection (changes)

- **API:** `GET /api/cloud-connections`. Nothing is minted: the connect command is never requested.
- **Input:** none.
- **Result:** the envelope with `consoleUrl` (`<PDS_URL>/environments/aws-accounts/new`, present only when the registry was given the console's address), the AWS accounts already connected and a `note` that tells the user to open the link and run the command it shows.
- **Description:** Start connecting an AWS account so PDS can build environments in it. This call does not connect anything: it returns consoleUrl and the AWS accounts that are already connected. Ask the user to open consoleUrl and run the one command it shows in their AWS account. The command is never shown here. Follow the account with get_cloud_connection.

### create_managed_environment (changes)

- **API:** `POST /api/cloud-connections/{connectionId}/environments`, which answers 202.
- **Input:** `connectionId`; `name`; `region`. The control plane checks the AWS account is ready and allows the region, and that the name is free in the Team.
- **Result:** the envelope with `id` (the name), `status` (the state) and `provisioning`; `next` is `wait_for_environment`.
- **Description:** Build a new environment in a connected AWS account: PDS creates the network and the Kubernetes cluster and installs PDS on it. connectionId is a ready AWS account from list_cloud_connections and region is one it allows. Ask the user for the name and the region. The call returns before anything is built: use wait_for_environment, which follows the four phases. If a phase fails, tell the user what failure says, and call retry_environment_run once they have fixed it.

### retry_environment_run (changes)

- **API:** `GET /api/environments/{environment}`, then `POST /api/environments/{environment}/runs/{runId}/retry`.
- **Input:** `environment`. The tool reads the latest run and refuses with `invalid_input` when it has not failed.
- **Result:** the envelope with `provisioning`; `next` is `wait_for_environment`.
- **Description:** Retry the failed step of an environment's latest run. Only the step that failed runs again. Use it after the user has fixed what failure says, such as raising a quota in their AWS account. Refused when the environment has no failed run.

### upgrade_environment (changes)

- **API:** `POST /api/environments/{environment}/runs` with `kind` `upgrade_pds` or `upgrade_kubernetes`, and for `upgrade_kubernetes` the optional `kubernetesVersion`. The tool never sends `confirmNodeGroupReplace`. A run already in progress, nothing newer to upgrade to, and an update that would replace the network, the Kubernetes cluster or the node group answer 409.
- **Input:** `environment`; `kind` (`pds` or `kubernetes`); `kubernetesVersion` (only with `kubernetes`: the minor version one above the current one, and with none only the infrastructure template is updated).
- **Result:** the envelope with `provisioning`; `next` is `wait_for_environment`.
- **Description:** Start an upgrade of a managed environment now. kind pds upgrades PDS on it. kind kubernetes upgrades the Kubernetes version and the nodes one minor version at a time, and with no kubernetesVersion it only updates the infrastructure template (get_environment shows templateSummary when it is old). Refused while the environment has a run in progress, when nothing newer applies, and when the update would replace the network or the Kubernetes cluster or the node group: the user decides those in the console. Use wait_for_environment to follow it.

### delete_managed_environment (destructive changes)

- **API:** `GET /api/environments/{environment}`, then `DELETE /api/environments/{environment}` with the stored name as `confirm`. The tool never sends `scope`, so it never does Remove from PDS only.
- **Input:** `environment`; `confirm` (the environment name, exact match). A client that declared form mode elicitation is asked to type the name and `confirm` is ignored. An environment the user connected is refused with `invalid_input`.
- **Result:** the envelope with `provisioning`; `next` is `wait_for_environment` with `until: deleted`.
- **Description:** Delete an environment PDS built, with its databases, its Kubernetes cluster and its network in the AWS account. This cannot be undone. A client that can ask the user asks them to type the environment name, and the call goes on only when it matches, whatever confirm says. For any other client, ask the user to confirm and pass the environment name as confirm. The call returns before the environment is gone: use wait_for_environment with until deleted. If something is left behind, the environment shows delete blocked and lists what remains.

### rebuild_environment (destructive changes)

- **API:** `POST /api/environments/{environment}/runs` with `kind` `rebuild`.
- **Input:** `environment`.
- **Result:** the envelope with `provisioning`; `next` is `wait_for_environment`.
- **Description:** Rebuild a managed environment whose infrastructure is gone, so PDS builds the Kubernetes cluster again. Backups can be restored afterwards. Ask the user first. Use wait_for_environment to follow it.

### remove_cloud_connection (destructive changes)

- **API:** `GET /api/cloud-connections/{id}`, then `DELETE /api/cloud-connections/{id}` with the name as `confirm`.
- **Input:** `id`; `confirm` (the AWS account's alias, or its account number when it has none). The tool refuses with `conflict` when managed environments still run through the AWS account, before it asks anyone. Elicitation works as for `delete_database`.
- **Result:** the envelope with `id`. Nothing in the AWS account is deleted.
- **Description:** Remove a connected AWS account from PDS. Nothing is deleted in the AWS account itself. Refused while managed environments still run through it. A client that can ask the user asks them to type the AWS account name, and the call goes on only when it matches, whatever confirm says. For any other client, ask the user to confirm and pass the name as confirm: the alias, or the account number when there is none.

## Managed environments and what no tool does

A managed environment is an environment PDS builds in the user's AWS account. `provisioning` is `{run: {id, kind, status, startedAt?, finishedAt?}, progress, message?, phases: [{name, status}], failure?: {sentence, action, refusal?: {reason, service?, item?, region?}}}`. `progress` is `in_progress`, `waiting`, `failed`, `blocked` or `ready`. The phases of a create are "Creating network and Kubernetes cluster", "Connecting to the environment", "Installing PDS" and "Finishing". `failure.sentence` is a safe sentence with what to do next and never carries AWS text, a role or another account's id. `refusal.reason` is `quota`, `permission` or `region_not_enabled`.

No tool, at any tier, shows or runs the connect script, its command or its `ccn_` token; changes AllowedRegions, the control plane's principal or the ExternalId; force deletes a blocked delete; does Remove from PDS only; or repairs a revoked role. Typed-account creation (`POST /api/cloud-connections`), the connect token routes, the maintenance window and deferral routes have no tool. An agent explains and links the console.

## 7. Change result envelope

```
{
  accepted: true,
  operation: "create_database",
  id: "...",
  status?: "...",
  requested?: { ... },
  database?: { ... },
  note?: "...",
  backupId?: "...",
  schedule?: { ... },
  warnings?: ["..."],
  next?: { tool: "wait_for_database", arguments: { ... } }
}
```

`create_support_ticket` and `create_environment` add `portalUrl`, and `expiresAt` and `consoleUrl`, to the envelope.

### Confirmations

Deleting a database and opening one to the internet need the user's word, and the registry takes it in one of two ways, by what the client declared when it connected.

- **A client that declared form mode elicitation** (an empty declaration counts as form) is asked directly. The call answers with an input required result holding one form question, and the client retries the same call with the person's answer. The question is plain words with the exact database name, the environment and, for an open range, every source range; it holds no secret. For a delete the form has one text field, and the call goes on only when the typed text equals the stored name, read again on the retry. For an open range the form has one box, and the call goes on only when it is ticked. The model's `confirm` and `acknowledgeOpenToInternet` are ignored, because the person, not the model, must say yes. One question is asked for each call.
- **Any other client**, including the Chat's in-process client, whose approvals are its own, uses the arguments as described under each tool, and a refusal still sends nothing.

Rules for both: a declined or cancelled question, an answer that does not match and a client that fails the question all leave everything unchanged, and the first two say so in the result (`confirmation_required` for a decline, `invalid_input` for a wrong name). A failed question is never read as a yes. An answer counts only on the call it was asked for: the request state holds a digest of the database and, for an open range, of the name, environment and ranges the person was shown, and a time limit of five minutes. An answer sent with other arguments, or after the limit, is not taken: the first asks again, the second is refused as expired. The control plane serves `/mcp` without sessions, so a client of the 2026-07-28 protocol era can be asked, and a client of an earlier era is served as the second kind. The stdio bridge adds nothing: the question and the answer are fields of the tool call and its result, which it forwards unchanged.

A timed-out change reports the outcome as unknown and names the check to run, in `hint`. The Go registry also sets `outcome: "unknown"` on the error, and does the same when the answer cannot be read or the caller leaves before it arrives. The check is `get_database` for scale, set_backup_schedule and delete, `list_databases` for create and restore (the new id is not known), `list_backups` for a backup, `list_support_tickets` for a ticket, `get_support_ticket` for a reply and `list_environments` for an environment. Such a change is sent once and never again.

## 8. Error kinds

Every failure is an `isError` result with `{kind, message, retryable, hint?, outcome?, requestId}`. `requestId` is left out when no request was sent, as with a refused saved sign in or a rejected id.

| `kind`                  | Raised when                                                                                                                                                                    | `retryable`                |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------- |
| `invalid_input`         | the API answers 400, or the registry rejects an id, a `confirm`, or a range                                                                                                    | no                         |
| `not_found`             | 404; hint: call `list_databases` for current ids                                                                                                                               | no                         |
| `conflict`              | 409 (busy database, restored copy still being restored, name taken, environment unreachable)                                                                                   | no                         |
| `not_available`         | 501                                                                                                                                                                            | no                         |
| `unavailable`           | 502 or 500                                                                                                                                                                     | yes                        |
| `timeout`               | the request timed out                                                                                                                                                          | reads yes; changes unknown |
| `confirmation_required` | an open range was sent without `acknowledgeOpenToInternet` and the client cannot ask; or the user declined or cancelled a question; or an answer expired                       | no, ask the user           |
| `not_allowed`           | the caller's access tier does not include the tool (Go registry): `change` tools need the change tier, `delete_database` the change and delete tier. Raised before any request | no, tell the user          |

Messages: 400, 403, 404 and 409 pass through as the API wrote them, and the adapter's customer copy test keeps them clean. 502, 500 and any other status use fixed text. A sign in that is missing or ended never reaches the registry: the bridge answers it with words that name `pds-mcp login`.

## 9. Prompts (Go registry only)

The registry serves four prompts beside the tools. Each is a workflow a person starts from a client's prompt or command list (Claude Code names the command after the server and the prompt), and each answers with one user message that the agent follows. They are text files in `adapter/internal/mcp/guidance/prompts`. `prompts/list` returns them by name, and only those the caller's access tier includes. The text names only tools that tier lists. An argument that fails its check, or a required one that is missing, is refused as invalid params. The TypeScript server has no prompts.

| Prompt                 | Title                                  | Tier   | Arguments                       |
| ---------------------- | -------------------------------------- | ------ | ------------------------------- |
| `connect_app`          | Connect an app to a database           | read   | `database` (required)           |
| `investigate_database` | Investigate a slow or failing database | read   | `database` (required)           |
| `new_database`         | Create a database for an app           | change | `engine`, `environment`, `name` |
| `set_up_backups`       | Set up backups                         | change | `database` (required)           |

- **`connect_app`.** Reads the database, waits until it is ready, then checks where the app will run against the database's access. The same VPC means private. A laptop or another host means public with that machine's address as a `/32`. Never `0.0.0.0/0`. It gives the host, port and username, never the password: the user copies it from the console. When the access does not fit, it says no tool changes access after creation and, at the change tier, offers a new database.
- **`investigate_database`.** `get_database`, `get_metrics`, `get_database_usage` when monitoring is off, `get_database_history` and `get_database_alerts`, then a summary. At the change tier it offers `create_support_ticket` after the user agrees. At the read tier it tells the user a ticket needs change access.
- **`new_database`.** Engine, environment, name, access (the same advice as `connect_app`), profile, a confirmation, `create_database`, then `wait_for_database` until ready. A left out `engine`, `environment` or `name` becomes a question to the user.
- **`set_up_backups`.** Reads the schedule and backups, agrees a schedule and the copies to keep with the user, `set_backup_schedule`, a check with `get_database`, then an optional first `backup_database` followed by `wait_for_backup`.
- **Arguments.** `database` is an id as `list_databases` returns it. `engine` is `mysql` or `postgresql`. `environment` and `name` are lowercase letters, digits and dashes. Values are never repeated in a refusal.

## 10. Additions and amendments to earlier decisions

These came out of writing the catalog. Check them in review.

- `wait_for_database` `until` is `ready` or `deleted`. A restore is waited for as ready on the new id. There is no restore grace window.
- `list_activity` returns the entry fields the API returns, including `client` and `agent` when the entry has them.
- The wait tools return the get_database shape in `database` so a create wait delivers `connection` without another call.
- `get_platform_status` is not a tool. Platform status is not a route on this control plane.

## 11. Open items

- The maximum `limit` the activity route accepts was not read.
- Status values of `list_environments` were not traced and are passed through.
- The heartbeat interval was not read, so descriptions say state "can be a little behind".
- The 45 second wait cap assumes a client limit of about 60 seconds, which was not confirmed.
