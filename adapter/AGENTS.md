# Adapter rules (`adapter/`)

Read the root [AGENTS.md](../AGENTS.md) first: the workflow, the gates and the
cross-cutting rules live there. This file covers the Go adapter. How the
adapter talks to everything else is in
[docs/architecture.md](../docs/architecture.md).

## Layout

`cmd/` plus `internal/`, which the compiler keeps private. No `pkg/` until
another module needs to import something. Tests sit next to the code.

| Package | Owns |
|---------|------|
| `cmd/adapter` | The binary: configuration, wiring, roles, and the `adapter organization` command |
| `internal/api` | HTTP: the chi router in `server.go`, handlers, `AppError` and `Respond`, the session guard, and the embedded join scripts |
| `internal/service` | Business logic. Services decide, call clients and return domain errors |
| `internal/client/*` | One package per downstream: `everest`, `pmm`, `servicenow`, `github`, `claude`, `cursor`, `ses`, and `kube`, the only package that talks to the Kubernetes API, and `clientmeta`, which fetches an OAuth client's metadata document |
| `internal/engine` | The engine plugin contract, the registry, and engine errors. `oe` is the shared OpenEverest implementation; `mysql` and `postgresql` are its two flavors |
| `internal/hub` | Hub state: environment inventory, join tokens, the work queue, and the two Git stores (GitHub, or a local directory) |
| `internal/agent` | The `agent` role: the heartbeat loop and the work it runs locally |
| `internal/store` | The adapter's own PostgreSQL: GORM models, migration, sealing |
| `internal/auth` | Password hashing, invite and session tokens, the signed cookie, request identity |
| `internal/config` | Environment configuration: every setting is read and validated here |
| `internal/features` | Which surfaces this release serves. A deleted surface's flag is ignored for one release, with one warning, then refused |
| `internal/oauth` | The pure rules of the agent authorization server: PKCE, redirect URI matching, scopes, resource identifiers, names clients give themselves, and which addresses a fetch may reach |
| `internal/mcp` | The registry of MCP tools, instructions and prompts for the Chat and `/mcp`. A tool serves the API's own request through the router in process, as the caller |
| `internal/log` | The `slog` logger and the handler that strips credentials |
| `internal/platform` | Kubernetes event facts the heartbeat and timelines read |

Libraries: chi, go-playground/validator, `log/slog`, GORM with the PostgreSQL
driver, client-go, OpenFeature, `golang.org/x/crypto`, the MCP Go SDK, testify. Use them;
do not add a second library for the same job.

## Handlers stay thin

A handler decodes, validates, calls a service and responds. Nothing else.

1. Decode the JSON body into a typed struct.
2. Validate with go-playground/validator tags on that struct.
3. Call the service with the request's `context.Context` first.
4. Respond through `Respond(w, r, result, err)`.

No business logic, spec mapping or decision in a handler. A downstream call
goes through a service.

## Errors

```go
type AppError struct {
    Code    string `json:"code"`    // ErrValidation, ErrNotFound, ErrConflict, ErrDownstream, ErrNotImplemented, ErrForbidden
    Message string `json:"message"` // a sentence the customer can read
    Status  int    `json:"-"`
    Cause   error  `json:"-"`       // logged with the request id, never serialized
}
```

- The six codes above are the contract the UI switches on. `ErrInternal` is
  the fallback for an unexpected failure and carries no detail.
- A downstream failure (OpenEverest, PMM, ServiceNow, GitHub) is 502 with a
  safe message. The cause goes to the log with the request id.
- Messages follow the customer copy rules in the root file.
- An id outside the caller's scope, another Team's or another Organization's,
  answers 404, never 403. 403 `ErrForbidden` is for a route the caller's role
  refuses: a User on an admin-only route.
- Services return domain errors (sentinels or typed errors) wrapped with
  `%w`, and handlers map them with `errors.Is` and `errors.As`. A lower level
  cause is formatted with `%v` beside the sentinel when callers should match
  only the sentinel.

## Context, logging, secrets

- Every handler passes `r.Context()` down. Clients build requests with
  `http.NewRequestWithContext`, so a cancelled browser request cancels its
  downstream calls. Lint fails on `context.Background()` in `internal/api` and
  `internal/service`, and on a request without a context.
- Log with `slog` only: JSON in production, text in development. The handler
  in `internal/log` redacts anything that looks like a credential, and an
  error path is no exception.
- `DATABASE_URL`, `STATE_ENCRYPTION_KEY` and every token come from Secrets.
  None is logged, put in an error, or written into chart values. Driver errors
  pass through `log.RedactText` before they are rendered.

## The adapter database (`internal/store`)

This is the adapter's state, not a customer database.

- The models in `models.go` are the schema, and `Migrate` runs `AutoMigrate`
  at every start. A new model goes in `Models()` and nowhere else. There is no
  separate migration step and no down migration.
- Replicas migrate under a PostgreSQL advisory lock so pods that start
  together take turns. The lock key is fixed and must not change.
- Readiness waits on this database and nothing else.
- Connection secrets are sealed with `STATE_ENCRYPTION_KEY`. Every query is
  scoped by Team, and connections by the Organization the Team belongs to.
- `internal/store/legacy_tenant.go` is the whole upgrade from the release before
  Organizations, which keyed every row by a tenant id. It runs before the models
  are reconciled, in one transaction, and needs `STATE_ENCRYPTION_KEY` to
  re-seal values against their new owner. Deleting the file and its call in
  `Migrate` ends the window. It is the one place the old word may appear.

## Legacy names

An environment was called a cluster in an earlier release, and state and
clients outlive an upgrade. For one release the adapter accepts the old names and writes only
the new ones. Each piece sits in one file so removing it ends the window:

- `internal/api/legacy_names.go`: the `/api/clusters` and
  `/api/internal/clusters` routes, the `cluster` query parameter and body
  field, and a `cluster` field beside every `environment` in a JSON response.
- `internal/hub/legacy_names.go`: persisted heartbeats and join grants that say
  `cluster`. The `pds.percona.com/cluster` label is read in `manifests.go` and
  replaced by `pds.percona.com/environment` the next time a manifest is
  rewritten.
- `internal/store/legacy_names.go`: renames the `hub_clusters` table and the
  `cluster` columns before `AutoMigrate`, which never renames.
- `config.lookupRenamed`: `ENVIRONMENT_NAME` and `HUB_AGENT_ENVIRONMENTS` fall
  back to `CLUSTER_NAME` and `HUB_AGENT_CLUSTERS`.

A line that exists for a window carries `allow-legacy-name`.

New code says environment. Cluster stays only for the Kubernetes cluster under
an environment, for OpenEverest's own cluster (`EVEREST_CLUSTER`), for PMM's
`cluster` label (the database name), and for the `clusters/<name>/` paths in
the hub's Git repositories, which Flux on connected environments reads.

## Scope and roles

The session resolves to an `auth.Scope`: user, organization, team and role,
read from the membership on every request. Stores read rows by `team_id`, so
the Team the request acts in is the only thing that moves them.

- `requireAdmin` guards what only an Organization admin may do. It is the one
  guard for those routes: do not test the role in a handler.
- `actingTeam` reads `?team=`. A User may name only their own Team. Another
  Team, and any Team of another Organization, is 404.
- `objectTeam` finds the Team an environment belongs to. An admin acts in it, a
  User is told 404.
- `team=all` is answered by the lists named in `listsAcrossTeams` and refused
  elsewhere, so no handler quietly answers for one Team.
- Services take their Team from the context the router set. Never accept a Team
  from a request body or path.

## Customer login

`internal/auth` holds the pieces with no response writer in them,
`internal/service/identity.go` the rules, `internal/api/auth.go` the HTTP.
Rules that are easy to break:

- One message for every refused sign-in. A wrong password, an unknown username
  and a malformed name answer the same sentence, or the form enumerates
  customers.
- The password never leaves the handler that reads it: no log line, no
  response field, no context value.
- Nothing on the HTTP surface creates a password login. `adapter organization`
  does, and it is the only thing that may.
- A session in an Organization this deployment does not serve reads as signed out.
- OIDC sign in (`internal/auth/oidc_client.go`, `internal/api/oidc.go`) ends
  in the same opaque cookie session. The tokens are used once at the callback
  and dropped. The flow (state, nonce, PKCE verifier) rides in a short lived
  signed cookie, not in the database. A failure redirects to `/?error=<code>`
  and never says more than `no_membership`, `sign_in_failed` or the signup gate
  that refused (`email_unverified`, `signup_rate_limited`, `signup_unavailable`,
  `signup_limit_reached`). Signup (`internal/service/signup.go`) is off unless
  `SIGNUP_ENABLED`, and creating an Organization takes the advisory lock in
  `internal/store/signup.go` so the cap and the ceiling hold under concurrent
  sign ins. Only an OIDC session creates or switches Organizations. Discovery
  failure at start leaves only the password login.
- An invitation (`internal/service/invitations.go`, `internal/store/invitations.go`)
  is claimed only by the verified email it names, at the OIDC callback: the
  link's token rides in the sealed flow, never a session. A refusal leaves the
  invitation unspent and shows the address masked, and a link is shown once, in
  the response that made it. Resend revokes the old row. A password claims one
  only where no issuer is configured and the deployment serves every Organization.
- `bcrypt` reads 72 bytes, so a longer password is refused rather than
  truncated.
- The bcrypt cost is a parameter. Tests pass `bcrypt.MinCost`.

## The agent authorization server

`internal/service/oauth.go` is the flow, `internal/api/oauth.go` the HTTP, and
[docs/architecture.md](../docs/architecture.md) says what it does. Rules that are easy to break:

- A refusal before the redirect URI is proven against the client is an error page, never a
  redirect. Nothing in a page or an error is copied from the request.
- The client's document is fetched only for a signed in person, only through
  `clientmeta`, and never for any URL inside the document.
- A code is spent before anything else about it is checked, and a refresh token is spent in the
  step that issues the next. Reuse of either ends the session.
- A token is stored as its SHA-256 and is never logged, put in an error or a URL. The token
  and revocation endpoints read the form body and nothing from the query.
- An access token is no session: the session authenticator never reads it, and
  `OAuth.Authenticate` refuses it on every path but `/mcp` and the credential route.
- The issuer is `HUB_PUBLIC_URL`. Never build an address from the request's Host.
- Consent is answered only from the console session cookie, by the person the request was parked for.
  The redirect the person is sent to comes from the parked request, never from the body, and a refused
  choice leaves the request waiting. Passwords and SQL writes are grantable only to the built in local
  client (`oauth.LocalClientID`).
- The credential route takes a token through `agentCredentialAuthenticator` and nowhere else. It
  pins the request to the token's Team, refuses in-process callers, and every read goes to Activity
  as `database.credential-read`. No MCP tool may reach it.
- The list and revoke routes (`/api/agent-sessions`) read the console session cookie and nothing else. They read
  sessions through `OAuth.AgentSessions` and `OAuth.RevokeAgentSession`, which scope by the caller's Organization
  and, for a User, by their own id, so a session outside that scope is not found, never forbidden. They return no
  token, hash or client id.
- Grants are checked three times, at consent against the request, the Organization and the
  role, and again on every use against the Organization's limit as it stands.

## Engine plugins

The registry maps an engine name to a plugin. The agent builds it. `oe`
implements the contract once against the OpenEverest Instance API; `mysql`
and `postgresql` select a flavor. The agent registers both. The create form
offers PostgreSQL only when `apps/ui/src/features/databases/engines.ts`
enables it. The HTTP role does not call the registry: create, scale, schedule
and delete are Git commits, and backup is work the agent runs. Restore is a
Git commit that creates a second database. For PostgreSQL the provider refuses
that, so the hub queues a `seed` work item carrying the Instance manifest and
commits nothing yet. The agent creates the `PerconaPGCluster` with
`spec.dataSource.pgbackrest` set from the source's backup (`postgresql.Seed`),
and the hub commits the Instance when it receives that result, so Flux cannot
apply it first. That manifest carries `pds.percona.com/restored-from`, and
`Delete` refuses it with `ErrRestoring` until a heartbeat lists the Instance,
so a delete cannot orphan the seeded database. A failed seed commits nothing and is reported on the pending
database. The agent's result carries a typed `reason` (`hub.Seed*`) that the
hub turns into the failed copy's sentence; the error text goes to the log only,
and a result with no reason keeps the original sentence.
A create that errors after the API server made the cluster is
followed by deleting that cluster when it restores the same backup, so a failed
copy leaves nothing running.

```go
// adapter/internal/engine/engine.go
type Plugin interface {
    Engine() string
    Scale(ctx context.Context, id string, in ScaleInput) error
    Backup(ctx context.Context, id string) (*Backup, error)
    Delete(ctx context.Context, id string) error
}
```

A plugin the agent can route to is a `ReadWritePlugin`: `Plugin` plus
`Reader` (list, backups). `BackupNamed` and `NamespaceBackups` live on the
OpenEverest plugin; the database service calls them when the concrete type
has them. Read `engine.go` before adding to either interface.

The OpenEverest boundary:

- Profile sizes exist twice, in `oe/profiles.go` (with the bounds) and in the
  chart's `instance-presets.yaml`: change both, and `oe.PresetVersion` with
  the chart's preset version, together. The hub writes Instance manifests
  from that catalog.
- Specs are Go structs, never YAML strings.
- The plugin only touches Instances in its namespace that carry its
  `app.kubernetes.io/managed-by` label.
- An update reads the Instance, changes the fields PDS owns and `PUT`s the
  whole object. Fields the adapter does not model survive.
- Delete is one `DELETE`; OpenEverest cascades.
- The OpenEverest token is minted from the `everest-accounts` Secret and
  minted again when it expires. With no Kubernetes access the process uses
  `EVEREST_TOKEN` as is and does not renew it. The HTTP role does not call
  OpenEverest.

## Hub and agent

- On a hub, create, scale, schedule, delete, and restore into a new database
  are Git commits through a `hub` store. Backup is a work item the agent
  collects on its heartbeat. Neither path calls OpenEverest from the hub.
- What the agent posts is stripped of credentials before it leaves the
  cluster. Never in Git: backups, restores, passwords, tokens.
- The hub runs one replica. The work queue and the inventory cache are per
  process.

## ServiceNow

The instance comes from `SN_URL`, which has no default: without it support
is off. HTTPS only. Header `x-sn-apikey` plus a `client_token` body field.
`ticket_type` is always `CASE`, and `u_main_technology` follows the engine
(`MySQL` or `PostgreSQL`). Payloads are sanitized before they leave: no
connection strings, passwords or secrets. Without the URL and both keys the
support routes answer 501. Every live check creates a real case, so test against
a non-production ServiceNow instance.

The support access case note links to the engineer's connection steps only when
`SUPPORT_GUIDE_URL` is set, to an https address. There is no default, because
the steps are not kept in this repository.

## Tests

Table-driven `testing` with testify, `httptest`, and fakes that implement the
same interface as the real client. Envtest is not used.

```go
// internal/api/hub_http_test.go
func TestOneSegmentDatabaseIDIsRefused(t *testing.T) {
    srv := newHubTestServer(t, "secret")

    rec := do(t, srv, http.MethodGet, "/api/databases/pds.orders", nil)

    assert.Equal(t, http.StatusNotFound, rec.Code)
    assert.Equal(t, api.CodeNotFound, decodeBody(t, rec)["code"])
}
```

- `newHubTestServer` in `helpers_test.go` builds the router with a hub.
  Reuse it and the other helpers there. The agent tests build a
  `service.Database` on the engine registry directly.
- Store tests need `ADAPTER_TEST_DATABASE_URL` and skip without it. CI runs
  them against PostgreSQL 17.
- `scripts/go-coverage.sh` gates the module at 78% with `-coverpkg=./...`, so
  a package covered by another package's tests still counts.
- `govulncheck` scans your local Go toolchain's standard library. An old Go
  shows advisories CI does not; upgrade Go rather than chase them.
- The join script (`internal/api/connect_bootstrap.sh`) is embedded and piped
  into customers' shells. Run `bash internal/api/connect_bootstrap_test.sh`
  and shellcheck after any change to it.
