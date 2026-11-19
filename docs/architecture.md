# Architecture

How Percona Database Service (PDS) is put together: the processes, who calls
whom with which credential, where state lives, and why a hub with connected
environments is the shape. The [README](../README.md) has the summary and the ways
to run it; [deployment](deployment.md) has the install procedures.

## Components

| Component                 | Runs                                                  | Does                                                                                                                                                                                                    |
| ------------------------- | ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| UI (`apps/ui`)            | nginx serving a static React bundle                   | Every product call is `/api` on the same origin. It never calls OpenEverest, Kubernetes, GitHub, PMM or ServiceNow itself.                                                                              |
| Adapter (`adapter`)       | One Go binary, two roles                              | The only backend. Validates, maps profiles to OpenEverest specs, holds login and hub state.                                                                                                             |
| Adapter database          | PostgreSQL, in cluster or external                    | Organizations, password hashes, sessions, environment inventory, join tokens, sealed connection secrets, database history, saved chats.                                                                 |
| OpenEverest and providers | `everest-system` on every cluster that runs databases | Reconciles `Instance` objects into Percona Operator for MySQL (PXC) and PostgreSQL clusters in namespace `pds`. Pinned at 2.0.0-dev.4 with the rest of the platform in `charts/pds/compatibility.yaml`. |
| PMM                       | Connected environment, optional                       | Metrics (VictoriaMetrics behind PMM). A pmm-client sidecar on each database does the scraping. The agent reads PMM. The hub charts the readings the agent posts and does not call PMM itself.           |
| Flux                      | Connected environments only                           | Applies the Instance manifests the hub commits to Git.                                                                                                                                                  |
| GitHub                    | Hub only                                              | One private repository per connected environment holds its desired state.                                                                                                                               |
| ServiceNow                | Percona SaaS, optional                                | Support cases through the Percona Connector API.                                                                                                                                                        |
| AI providers              | External, optional                                    | Chat, with a key the customer pastes in the UI.                                                                                                                                                         |

### Adapter roles

`ADAPTER_ROLE` picks the process at start. HTTP and agent are the only roles.
An unknown value stops the process. Do not set the variable through
`extraEnv`: the chart already sets it per Deployment.

| Role         | Serves                                 | Calls                                                                             | Database                                                                            |
| ------------ | -------------------------------------- | --------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| HTTP (empty) | The `/api` surface and `/api/internal` | Git, ServiceNow, AI providers                                                     | Required. Login lives there, so the process refuses to start without `DATABASE_URL` |
| `agent`      | `/healthz`, `/readyz`                  | The hub heartbeat, and the local OpenEverest and PMM for the work the hub returns | None                                                                                |

## Topologies

Three shapes. One chart, `charts/pds`, installed as a hub or a connected
cluster, plus a hub that is not Kubernetes at all. The chart defaults are
the hub. `scripts/up.sh` with no cluster flags runs a hub and one connected
cluster as two kind clusters.

A hub and a connected environment cannot share a Kubernetes cluster. Both
releases use namespace `everest-system`, and the hub's PostgreSQL operator
and the connected environment's database platform install the same cluster-wide
CRDs. A hub has no OpenEverest. A connected environment has no UI.

| Shape                 | Installed by                             | Runs                                                                                    | Does not run                  |
| --------------------- | ---------------------------------------- | --------------------------------------------------------------------------------------- | ----------------------------- |
| Hub                   | Chart defaults                           | UI, adapter, adapter database                                                           | OpenEverest, PMM, MinIO, Flux |
| Connected environment | The Connect command, `values-spoke.yaml` | OpenEverest, providers, PMM, adapter as `agent`, Flux                                   | UI, adapter database          |
| Hub on one host       | `deploy/compose`                         | UI and adapter in Docker Compose, PostgreSQL outside                                     | Kubernetes access of any kind |

Namespaces do not follow the shape:

| Namespace        | Holds                                                                                                                                                                     |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `everest-system` | The Helm release. OpenEverest refuses any other release namespace.                                                                                                        |
| `pds`            | Database Instances, and the only namespace the adapter creates in. Helm must not own it: a Helm-owned namespace is labelled `managed-by=Helm` and OpenEverest ignores it. |
| `flux-system`    | Flux, on connected environments only.                                                                                                                                     |

```text
Hub and connected environments

  Browser ── /api ──▶ hub UI ──▶ hub adapter ──┬── adapter database
                                               ├── Git (repo or directory)       ◀── clone ──┐
                                               ├── ServiceNow, AI provider                   │
                                               ▲                                             │
                                               └── heartbeat every 15s ──── agent       Flux ┘
                                                                              │           │
                                                       connected environment:     ▼           ▼
                                                                         OpenEverest ◀── Instances in pds
                                                                         PMM
```

## Browser to adapter

The UI is a static bundle mounted at `/app/`; the root of the host belongs to
the landing page when it is on and redirects to `/app/` everywhere else.
In a cluster, the UI's nginx proxies `/api` to the
adapter Service with 180 second timeouts, because chat waits on a model for
up to 165. It sends `/oauth/`, `/mcp`, `/.well-known/oauth-authorization-server` and `/.well-known/oauth-protected-resource`
to the adapter as well, and so does the chart's ingress. Locally, Vite proxies `/api` to `ADAPTER_URL` and does not proxy the OAuth paths, default
`http://localhost:8080`. The adapter has no CORS handling on `/api` and the session
cookie is host only, so the UI and `/api` must share one origin.

`adapter/internal/api/server.go` is the route table. The groups, by what a
request has to carry:

| Credential                             | Routes                                                                                                                                                                                                                                                                   |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| None                                   | `/healthz`, `/readyz`, the `/api/auth` routes (login, logout, session, invite, and the identity provider), the Connect script `/api/internal/connect/bootstrap.sh` and `pds.tgz`, and the authorization server for agents: `/.well-known/oauth-authorization-server`, `/oauth/token`, `/oauth/register` and `/oauth/revoke`. `GET /oauth/authorize` needs a session cookie and sends a person without one to sign in |
| Join token                             | `GET /api/internal/connect/enrollment`                                                                                                                                                                                                                                   |
| Join or agent token                    | `POST /api/internal/environments/{name}/join`, `POST /api/internal/environments/{name}/heartbeat`                                                                                                                                                                        |
| Session cookie, or the operator bearer | Everything else under `/api`: features, environments and Connect, alerts, connections, assistant, chat, activity, support, and databases with their credential, scale, backups, schedule, restore, metrics, history, usage and alerts                                    |

`Authorization: Bearer $HUB_OPERATOR_TOKEN` stands in for a session wherever
that token is set, which is always on a hub: it is minted at start when none is
given. It opens every route a session does, database credentials included, so
it is an administrator password. On a hub, the writes (create, scale, backup,
schedule, restore and delete a database, change a connection, mint a join
token, disconnect an environment) need a signed-in login or that token.

A session carries a scope: the User, the Organization, the Team and the role
(`user` or `admin`). Membership is read on every request, so a login that was
removed is signed out on its next one. A User acts in their own Team with every
capability they had before Organizations. Changing a connection is the one write
only an admin may make, and a User on it gets 403 `ErrForbidden`. The operator
token is an admin of the sole Organization when the deployment serves one, and
gets 401 on every route when it serves many, because it has no Organization.

An admin manages the Organization they act in through `PATCH /api/organization`,
`GET`/`POST /api/teams`,
`PATCH`/`DELETE /api/teams/{name}`, `GET /api/users` and
`PATCH`/`DELETE /api/users/{id}`; a User gets 403 on all of them. A Team is
deleted only when it has no Users and no environments. `PATCH /api/users/{id}`
takes `team` and `role`, and either may be left out. The last admin cannot be
demoted or removed. Removing a User deletes their membership, so their session
reads as signed out on its next request. Another Organization's User or Team is 404. `PATCH /api/organization` renames the Organization with the same rules as
creation and records `organization.rename` in Activity under the acting User.

An Organization sets the most agents and the Chat may do, and two switches for
local agents, through `GET`/`PUT /api/organization/ai`. Every member reads it
and only an admin writes it; a User gets 403 on the `PUT`. The body is
`{"access":"read"|"change"|"change_delete","passwords":bool,"sqlWrites":bool}`
and an unknown or missing tier is 400. They are columns on
`hub_organizations`: new and existing Organizations start at `read` with both
switches off, and there is no off tier, because the deployment flags stay the
operator's switch. A change is recorded as `organization.ai` under the acting
User. The Chat serves its tools at `service.CallerAccess`: the lowest of that
limit and what the person's role allows, read again on every turn, so a lowered
limit applies to the next question. A limit that cannot be read leaves the Chat
at read. `passwords` and `sqlWrites` are stored for the agent server that reads
them; the Chat does not consult them.

An admin invites through `GET`/`POST /api/invitations`,
`POST /api/invitations/{id}/resend` and `DELETE /api/invitations/{id}`. An
invitation carries an email, a Team and a role. The token is single use and
only its hash is stored, so the link (`<public URL>/claim?token=...`) is
returned once, by the create and by a resend, and is emailed when
`SES_REGION` and `INVITATION_FROM` are both set (Amazon SES, with the AWS default credential chain) (`emailed` says whether it
went). An Organization has one pending invitation per email, and a resend
revokes the old row and mints a new link. It lives `INVITATION_TTL` (7 days). An
`anyEmail` invitation has no address, only the User role and a 48 hour life.
Following a link starts the sign in with `/api/auth/oidc/start?invite=<token>`,
and the claim is made at the callback with the verified email, which must equal
the invited one (trimmed, lower case). A mismatch, an unverified address and a
missing one are refused, leave the invitation unspent, and redirect to
`/app/login?error=invite_email_mismatch` or `invite_email_unverified` with the invited
address masked in `invited`. A link that is gone, spent or expired redirects with
`invite_invalid`, `invite_spent` or `invite_expired`, and a person already in the
Organization with `invite_already_member`. A first sign in with no link claims
the oldest invitation waiting for its verified email before it would create an
Organization. `GET /api/auth/invite?token=` previews a link without spending it
and shows the masked address. With no issuer configured, on a deployment that
serves every organization, `POST /api/auth/invite` takes `login` and `password` and
creates that password login in the Organization; with an issuer it answers 409,
because a password cannot verify the address. Activity records
`invitation.create`, `.resend`, `.revoke`, `.claim` and `.refuse`.

`team` names the Team a request acts in. It defaults to the caller's own. A
User naming another Team gets 404, as does anyone naming a Team of another
Organization. An admin may name any Team of theirs, may ask `team=all` of
`GET /api/environments`, `GET /api/databases` and `GET /api/activity` (each row
then carries its `team`), and acts in the Team an environment belongs to when the route names
one and no `team` is given. A create names its Team with `team` as well.

The console shows the Team in the sidebar: a label for a User, a list of every
Team for an admin. The admin's choice is kept in the browser for each
Organization, and the console sends it as `team` on every route that acts in a
Team, but not on `/api/auth`, `/api/features` or the Organization routes
(`/api/teams`, `/api/users`, `/api/invitations`, `/api/organization`). A
remembered Team that no longer exists is dropped for the admin's own.

The admin's list also offers All teams. It is sent as `team=all` on the three
lists above and nowhere else, because any other route refuses it; a write or an
object route then stays in the admin's own Team, or in the Team of the object.
The rows that come back show their Team. Connect asks for a Team while All
teams is chosen, and sends it as `team` on the write. Create
database goes to the Team of the environment it is for and does not ask, because
any other Team would not find the environment; Connect starts from the admin's
own.

Every activity entry the control plane can attribute to a program carries
`client` and `agent` beside `actor`. `client` is `mcp` for an agent that signed
in through OAuth, and `chat` for the console Chat. `agent` is a short name (up to
64 letters, digits, inner spaces, `.`, `_`, `+` and `-`) for the program behind
the client: the OAuth client's name for an external agent, such as `Cursor` or
`Claude Code`, and the provider for the Chat, such as `Anthropic`. The agent is
what the caller declares, and nothing verifies it, so it labels an entry and
proves nothing about who made it. Only the process itself sets `chat`, and the
router refuses a caller whose agent name does not validate. A sign in with a
password or an identity provider declares nothing, and a person's change in the
console has neither field, so the list and detail responses omit them then. An
MCP session acts as the User who authorized it. The session route returns
`controlPlaneVersion`, the control plane release stamped into the image from the
release tag (`dev` for any other build); `/healthz` does not. A request
authenticated by the operator token declares none.

An access token an identity provider issued opens nothing: no route reads one as
a bearer, and there is no route that trades one for a session. The identity
provider is used only at the console's authorization code sign in.

### The authorization server for agents

Remote MCP clients (Claude, Cursor, VS Code, Claude Code) sign in over OAuth. The control plane is its
own authorization server on every deployment, whatever its sign in method, on the same host as
`/api`. The code is `internal/oauth` (the pure rules), `internal/client/clientmeta` (the metadata
document fetch), `internal/service/oauth.go` (the flow), `internal/store/oauth.go` (the rows) and
`internal/api/oauth.go` (the HTTP). The issuer is `HUB_OAUTH_ISSUER`, or `HUB_PUBLIC_URL` when that is not set, and must be an origin; the request's Host is
never used. Everything the flow builds from the issuer (both metadata documents, the `WWW-Authenticate` pointer, the `/mcp` Origin check) reads the one value. Without a usable issuer, or with the `mcp` flag off, the endpoints answer 404.

| Endpoint                                      | What it does                                                                                                                                                                                       |
| --------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /.well-known/oauth-authorization-server` | The RFC 8414 metadata: S256 only, public clients (`none`), `client_id_metadata_document_supported`, `authorization_response_iss_parameter_supported`, and the scopes                              |
| `POST /oauth/register`                        | Dynamic Client Registration for public clients: 1 to 10 redirect URIs, https or http on a loopback address. 10 an hour per address and 300 an hour in all. A registration that never produced a session is dropped after a day |
| `GET /oauth/authorize`                        | Checks the request and parks it for consent                                                                                                                                                        |
| `POST /oauth/token`                           | `authorization_code` and `refresh_token`, form encoded, 60 a minute per address with the revocation endpoint                                                                                         |
| `POST /oauth/revoke`                          | RFC 7009: any token of a session ends the whole session. An unknown token is a 200                                                                                                                  |

Authorize without a console session answers 302 to `/app/login?next=<this request>`, after
checking only the shape of the parameters, and fetches nothing. With a session it identifies the
client, checks the redirect URI, parks the request, and answers 302 to
`/app/authorize-agent?request=<token>`. The token is random, stored as its SHA-256, bound to the
User who signed in and valid for 10 minutes. The consent screen is a console page that reads the request through
`OAuth.ConsentScreen` (which wraps `OAuth.Pending`) and ends it through `OAuth.Approve` or `OAuth.Deny`, which return
the address to send the browser to.

The page calls three routes, mounted under `/api` outside the session guard and answering 404 while the `mcp` flag is off:

| Route                           | What it does                                                                                                                                                  |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `POST /api/agent-consent/lookup`  | Describes the parked request: the client's name, host, kind and redirect host, whether the redirect is a loopback address, and for each Organization the person belongs to its Teams (a User's own, or all for an admin), the highest tier offered and which boxes are offered |
| `POST /api/agent-consent/approve` | Takes `request`, `organizationId`, `teamId`, `access`, `passwords` and `sqlWrites`, checks them and answers `{redirectUrl}` with the code |
| `POST /api/agent-consent/deny`    | Answers `{redirectUrl}` with `error=access_denied`, `state` and `iss`                                                                                         |

They read the console session cookie only: the operator token and in process callers are not a person at a browser, so they get 401.
Each is a POST with a JSON body, refused when the browser marks it as coming from another site (`Sec-Fetch-Site` other than
`same-origin`), and the session cookie is `SameSite=Lax`. The request token is in the body, never in a URL that is logged. The
address in the answer is built from the client's redirect address that was checked when the request was parked, never from the
body. A request that is not the person's, is expired or was answered reads as 404; a Team or Organization the person cannot use is 404;
grants above the limit, the request or the client are 400, and a refused choice leaves the request waiting.

The AI agents page lists and revokes the agents signed in, through two more console routes, mounted
beside the consent calls, answering 404 while the `mcp` flag is off, and reading the console session
cookie only (the operator token, an agent's own token and in process callers get 401):

| Route                           | What it does                                                                                                                                                                                                                      |
| ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /api/agent-sessions`       | `{sessions}`: the live agent sessions of the caller, or of every member of their Organization for an admin. Each has `id`, `name`, `host`, `kind`, `member`, `mine`, `team`, `access`, `passwords`, `sqlWrites`, `createdAt`, `lastUsedAt` and `expiresAt`. It never carries a token, a hash, a client id or the resource |
| `DELETE /api/agent-sessions/{id}` | Ends the session and all its tokens (`DeleteAgentSession`), so the agent's next `/mcp` call is 401, and records `agent.revoke` in Activity for the caller. Answers 204. A session of another member, of another Organization, one already revoked and an id that never existed all answer 404 |

The DELETE is refused when the browser marks it as coming from another site (`Sec-Fetch-Site` other than
`same-origin`). A session that is expired or idle past `SESSION_IDLE_TIMEOUT` is not listed. `POST /oauth/revoke` is the
client's own route and is separate: it takes a token, not a console session.

- **The PDS local client.** The client id `pds-mcp` is built in: nothing is fetched for it, its only
  redirect addresses are `http://127.0.0.1/callback` and `http://[::1]/callback` on any port, and its kind is `local`. It is the only
  client that may be granted `pds:credentials` (database passwords into the computer's keychain) or `pds:sql_write`; for any
  other client those grants are refused at approval, and the consent page does not offer the boxes.

- **Clients.** A client id that is an `https://` URL is a Client ID Metadata Document, fetched once the person has signed in. The fetch
  refuses a name unless every address it resolves to is globally routable (private, loopback,
  link local, CGNAT and the other special use ranges, and addresses that embed one), connects to
  the address it checked and never looks the name up again, follows no redirect, reads at most 5 KiB,
  gives up after 5 seconds, accepts only port 443 and an ASCII host, and caches the valid document for 1 to 60 minutes. A document with a secret,
  a shared secret authentication method, a redirect URI that is not https or loopback, or a `client_id` that is not its own address is refused.
  An id that starts with `dcr_` is a registered client. Any other id is refused.
- **Redirect URIs.** The match is the exact string. A loopback address (`localhost`, `127.0.0.1`, `[::1]`) may use any port, and still
  needs the same scheme, host, path and query. A request whose client or redirect URI does not check out is answered with an error
  page and never a redirect. Once the redirect URI is proven, other problems go to it as `error`, `state` and `iss`.
- **PKCE.** `code_challenge` with `S256` is required. The code is spent before the verifier is checked, so a stolen code cannot be tried against many verifiers. Reusing a code ends the session it made.
- **Resource.** `resource` may be absent, or any spelling of `<origin>/mcp`. Anything else is `invalid_target`.
- **Scopes.** `pds:read`, `pds:change`, `pds:delete`, `pds:credentials` and `pds:sql_write`, each tier including those below it, plus `offline_access`, which is accepted and means nothing because every grant gets a refresh token. What the request asks for bounds what the person may grant; the Organization's AI setting and the person's role bound it again. A token response carries the session's grants as `scope`.
- **Sessions and tokens.** Consent ends in a code (60 seconds). Exchanging it creates an agent session (`agent_sessions`) holding the User, Organization, Team, tier, the two permissions, the client's id, name and host, and its last use, and two opaque 256 bit tokens, stored as SHA-256 in `oauth_tokens`. The session lasts `SESSION_LIFETIME` (30 days) and `SESSION_IDLE_TIMEOUT` (7 days) like a console session. An access token lasts one hour and never outlives its session. A refresh token is used once: presenting a retired one ends the whole session. Deleting a session ends every token at once.
- **What a token is.** `OAuth.Authenticate(token, path)` returns an `AgentCaller`: the `auth.Caller` the registry and the API act for, with the Team the person chose, the tier now allowed, and the scopes now held. The membership, the Team and the Organization's limit are read on every use, so removing a person, moving them to another Team or lowering the limit takes effect on the next call. A token is refused on every path but `/mcp` and `/api/databases/{id}/{hubID}/credential`, which also needs the `pds:credentials` grant, and it is no session for the rest of `/api`. Two agents are told apart in the activity log by the client's name, cleaned to the characters an agent name allows.

An activity row stores the id of the User who acted and of the Team it belongs
to, never a name or an email. `actor` is read from the User when the row is
listed, so a rename shows on old rows and a removed User shows as "Removed
user". A fixed actor such as system has no User row and keeps its label. Rows
written before this are not backfilled and keep the `actor` they were stored
with. A User lists their own Team's activity, and an admin lists every Team's.

Each surface sits behind a feature flag from `adapter.features`. A surface that
is off answers 501, and `GET /api/features` tells the UI which ones to hide.
### `/mcp`: the registry served to signed in agents

`internal/api/mcp.go` serves the registry over streamable HTTP with the Go MCP SDK, on the same
host as `/api`. The handler is stateless: no MCP session outlives a request, so every call is judged
by its own token and a revoked token stops working on the next call. Each request builds the caller
from its access token with `OAuth.Authenticate(token, "/mcp")` and serves `Registry.Server(caller)`,
so `tools/list` holds only the tools the caller's tier allows, and a direct call to a tool above it is
refused as `not_allowed`. The Chat uses the same registry in process.

- **Order.** With the `mcp` flag off, no registry or no authorization server: 404 for `/mcp` and the
  metadata, whatever the method. Then the `Origin` header: a request that carries one must carry the issuer's own origin, or it is 403 before its token is
  looked at, so a web page cannot use an agent's token or probe for one. Agents that are not
  browsers send none. Then the token.
- **Token.** Only `Authorization: Bearer` is read. A cookie, a query parameter and the operator
  token are ignored. A missing token is 401 with `WWW-Authenticate: Bearer resource_metadata="<issuer>/.well-known/oauth-protected-resource/mcp", scope="pds:read"`;
  an unknown, expired, revoked or refresh token adds `error="invalid_token"`. The body is the
  API's JSON error. A token is accepted on `/mcp` and the credential route only, and only an
  access token: the audience is the one resource the server issues tokens for, `<issuer>/mcp`.
- **Scopes.** Every token holds `pds:read`, so the 401 challenge names it and there is no
  `insufficient_scope` 403: what a token may do is the tier it holds, which the tool list shows.
- **Metadata.** `/.well-known/oauth-protected-resource` and `/.well-known/oauth-protected-resource/mcp`
  answer RFC 9728 with `resource` `<issuer>/mcp`, the issuer as the one authorization server, the
  resource scopes, and header bearer only.
- The credential route is not an MCP tool. It is reached by a person's own client, over the API,
  and only with a token that holds the passwords grant.
  - It answers a token on `Authorization: Bearer` the way it answers a session, with the same
    rules: the Team, the role, `Cache-Control: no-store`, and a body no log or Activity row carries.
    Every call re-reads the session, the person's membership and the Organization's switch, so a
    revoked session is 401 at once and a switch turned off, or a token that never held the grant,
    is 403 with a sentence that says what to do.
  - A token is pinned to the Team it was approved for. An Organization admin behind it still
    cannot name another Team with `?team=`, and a database of another Team is 404.
  - A request this process built for itself, a tool call or the Chat, is refused on this route,
    so adding a tool cannot reach it by accident.
  - A refused attempt is not a read and leaves no row: the person behind a refused token is not
    known until the token is accepted.
  - Each read is recorded in Activity as `database.credential-read`, with the agent's name and
    client `mcp` when an agent made it, and with the outcome and never the connection string.

`mcp` is the exception: it hides the AI agents page (`/agents`) and its sidebar entry,
and with it off every OAuth endpoint answers 404, so a deployment that does not serve agents
shows nothing to find.
A name that is not a surface stops the adapter starting, so a typo cannot leave
a surface on. A flag whose surface has been deleted is the exception, for one
release: startup logs one warning that names the variable and says it is
ignored, the value is not applied, and the name is absent from
`GET /api/features` and from the UI flag list. The following release drops the
name, and the variable is refused like any other unknown name. Those names are
the retired list in `adapter/internal/features`.

Database ids always carry the environment (`<environment>/<namespace>.<name>`). A
one-segment id is not a route. Every per-database route is mounted with that
extra path segment. Nothing on
this surface creates a password login: `adapter organization create <id>` does, and prints
the invite link the customer sets a password through.

`GET /api/databases/:id/credential` is the only route that returns a password, to a session or to an agent token that holds the passwords grant.
The UI masks connection strings and copies the real one to the clipboard only.

### The `pds` plugin

`plugins/pds` carries the registry to Claude Code and Cursor from one directory: a manifest for each
client, a remote entry for `/mcp`, a local entry that runs `@gas-lab/pds-mcp data` at an exact
version, one skill and the Cursor rule. The skill and the rule are generated from
`adapter/internal/mcp/guidance` by `adapter/cmd/plugin-guidance`, so the words an agent reads from
the plugin are the registry's own, at its widest tier, and a test in the adapter's CI fails when they differ.
Claude Code reads `PDS_URL` with `https://pds.percona.com` as the default. Cursor has no default
form, so its remote entry is that fixed address and a self-hosted control plane is added by hand.
Rules for the directory are in [plugins/AGENTS.md](../plugins/AGENTS.md).

### The MCP tool registry

`adapter/internal/mcp` is the one registry of tools AI agents use: the Chat
calls it in process, and the `/mcp` endpoint will serve it to MCP clients. It is
not served over HTTP yet. The tools are the ones in
[mcp-server-tool-catalog.md](mcp-server-tool-catalog.md), with the same names,
inputs, results and descriptions. The read tools are `list_environments`,
`list_databases`, `get_database`, `list_backups`, `get_metrics`,
`list_activity`, `list_support_tickets` and `get_support_ticket`, and the wait
tools are `wait_for_database`, `wait_for_backup` and `wait_for_environment`. A
wait polls the same routes every five seconds, for 30 seconds unless the caller
asks for up to 45. Each poll is cut off at that deadline through the request's
context, so a slow poll cannot hold the wait open.

The registry also has six read tools the TypeScript server does not:
`get_database_history`, `get_database_usage`, `get_environment_history`,
`list_alerts`, `get_database_alerts` and `get_activity`.

The change tools are `create_database`, `scale_database`, `backup_database`,
`set_backup_schedule`, `restore_database`, `create_support_ticket`,
`reply_to_support_ticket` and `create_environment`, and `delete_database`
needs a higher tier still. The caller's access (`read`, `change` or
`change_delete`) decides which tools a server lists and runs: a call above the
tier is answered `not_allowed` before any request is built. A caller whose
client kind is not `chat` or `mcp` is served the read tier, so no change is made
without a client to record it under. A change is sent once. If its answer does
not arrive or cannot be read, the result says the outcome is unknown and names
the call that finds out. `create_environment` reads only the expiry from the
join answer, so the token and the command never reach a result or a log.
`WithConsoleURL` gives it the console address to hand to the user.

The registry also sends the server instructions and four prompts, embedded as
text files from `adapter/internal/mcp/guidance`: `connect_app`,
`investigate_database`, `new_database` and `set_up_backups`. Both follow the
caller's tier like the tools do, so neither names a tool the caller cannot use,
and a prompt above the tier is not listed. The design is in
[mcp-server-design.md](mcp-server-design.md), section 7.1, and the prompts are
in the catalog.

A tool builds the request a client of the public API would send and serves it
through the adapter's own router, in the same process, as its caller. Team
scope, feature flags, role guards and error copy are therefore the API's, and a
tool reads no store or service itself. The caller (`auth.Caller`: the signed in
person, their Organization, Team and role, the access tier, the client kind and
the agent's name, which activity entries record as `agent`) rides the request's
context. The router accepts it in place of a session for a request built in this process, and refuses one with no
Organization. A context value cannot arrive in an HTTP request, so a header,
cookie or query parameter naming a caller is only data to the session check. The
credential route has no tool.

A server is built for one caller (`Registry.Server`), and `Registry.Connect`
gives the Chat an MCP client joined to it over an in-memory pipe.

### The Chat's tools

The Chat defines no tools of its own. Each turn `internal/service/chat_tools.go`
connects to the registry as the signed in person, with client kind `chat` and
the provider as the agent (`Anthropic` or `Cursor`). The tier is
`Chat.access`, the lowest of the Organization's limit and the person's role,
read again each turn, so the model is offered exactly the tools `/mcp` lists for
that caller. The turn lists the tools once and calls each one through the
session, a few at a time, in the order the model asked.

- **System prompt.** The Chat's role and how a button works, then the registry's
  server instructions for the tier, then how the provider writes its answer.
  None of it depends on the question, the person or the round, so Anthropic
  reads it from the cache.
- **Anthropic** gets the tools as native tool definitions, with the registry's
  descriptions and input schemas.
- **Cursor** keeps the emulated protocol: the same tool list is rendered into the
  prompt of the agent when it is created, and each `tool_calls` entry the agent
  replies with is dispatched to the registry.
- **Results.** The result's text is what the model reads, after the same
  credential redaction as the rest of the Chat's text. A result with `isError`
  reaches the model as a tool error, never as a failed turn, and so does a call
  to a tool the tier does not list. A tool's panic is recovered by the registry
  into the same kind of result.
- **Progress.** The step labels the person sees while a turn runs follow the
  tool's name, such as `Reading metrics`. Calls are not shown in the reply.
- **Prompts.** The Chat reads the registry's instructions and does not use its
  prompts, which are for a person to start from a client.

The Chat still reads the database list itself, to give Cursor ahead of a
question and to check the database a button names. A button only opens
something: the database, its metrics or a support ticket. A scale, backup or
schedule button an older reply saved is not shown.

### Approvals for the Chat's changes

`chatTools.call` is the one place a registry call is made, and the one place a
change is held. A tool that is not marked read only in the registry's
annotations, and that the tier offers, is put to the person before it runs. A
call to a tool above the tier is not put to anyone: the registry refuses it as
`not_allowed`, as it does for any caller, and that check still runs after an
approval.

- **The card.** The `approval` event carries an id, the tool's title, the
  database's name and each argument in words (`approvalLines`, with credentials
  redacted like the rest of the Chat's text). Reads run without asking and are
  not shown.
- **The answer.** `POST /api/chat/approvals/{id}` takes `{decision, name}`,
  where `decision` is `approve` or `reject`, and answers 204. `Chat.Decide`
  reaches only the person the model was asking: another person, an id that is
  not open, one already answered or expired, and a stream that has closed are all
  404. What runs is the arguments the call was made with and the card showed,
  never anything from the answer. A delete is approved only with the database's
  name typed exactly, a wrong name is a 400 and leaves the approval open, and
  the typed name is what the call sends as `confirm`, so the model's own
  `confirm` counts for nothing. The registry then compares it with the stored
  name.
- **Where it lives.** A pending approval is in the memory of the adapter process
  that holds the stream. Closing the stream, reloading or leaving cancels it and
  nothing changes. It waits at most 15 minutes and then expires. A request that
  is not a stream cannot show a card, so its changes are refused. The hub runs
  one replica, so the answer reaches the process that is waiting.
- **One at a time.** Changes asked for in one round are put to the person one
  after the other, in the order asked.
- **When it ends.** A rejected call goes back to the model as a refusal, so it
  says so. An expired or cancelled one ends the turn with a fixed sentence saying
  nothing changed, without asking the model again. `approval_end` carries
  `approved`, `rejected`, `expired` or `cancelled`.
- **The budget.** The turn's 165 second budget stands still while an approval
  waits, so the wait costs the model nothing, and runs again once it is answered.
  The heartbeat comment keeps the stream open for the wait.
- **Cursor.** Its `tool_calls` become the same tool calls, so they take the same
  approvals, and Activity shows the change with client `chat` and agent `Cursor`.

## Agent to OpenEverest

The hub does not call OpenEverest. Create, scale, delete, and restore into a
new database are commits. Backup runs on the connected environment through the
heartbeat, and so does the seed that a PostgreSQL restore needs: the provider
refuses to restore into a new Instance, so the agent creates the
`PerconaPGCluster` first, with a `dataSource` that reads the source's pgBackRest
repository. The hub commits the Instance only when the agent reports the seed
done, and commits nothing if it failed. That Instance carries the annotation
`pds.percona.com/restored-from`, and the hub refuses to delete it until a
heartbeat lists the Instance, because removing the manifest earlier would leave
the seeded database running with nothing to delete it. A failed seed reports a typed reason
(backup unusable, source gone, environment cannot seed, other), and the hub
shows the copy a sentence for that reason.
When the create errors after the API
server accepted it, the agent deletes the `PerconaPGCluster` it made (the one
whose `dataSource` restores that backup) before reporting the failure.

The agent calls OpenEverest with the hand written client in
`adapter/internal/client/everest`. It scopes calls to cluster `main`
(`EVEREST_CLUSTER`) and namespace `pds` (`EVEREST_NAMESPACE`):

```text
GET             /v1/clusters/main/namespaces/{ns}/instances
GET/PUT/DELETE  /v1/clusters/main/namespaces/{ns}/instances/{name}
GET             /v1/clusters/main/namespaces/{ns}/instances/{name}/connection
GET             /v1/clusters/main/namespaces/{ns}/instances/{name}/backups
POST            /v1/clusters/main/namespaces/{ns}/backups
```

- The agent mints its own bearer token through `POST /v1/auth/token`, from the
  credentials in the `everest-accounts` Secret, and mints a new one when it
  expires. With no Kubernetes access it keeps `EVEREST_TOKEN` and does not
  renew it.
- The hub writes Instance manifests from the preset catalog. The agent does
  not copy a live preset.
- The agent only touches Instances in its namespace that carry its
  `app.kubernetes.io/managed-by` label. Anything else in the cluster is not
  PDS's to change.
- An update is a read, a change and a `PUT` of the whole object. Fields the
  adapter does not model survive the round trip.
- Every database gets a `LoadBalancer` Service: internal with private
  address ranges by default, internet facing with an allow list when the
  customer makes it public. On a cloud that is a billed load balancer.
- The agent never calls namespace or operator APIs, `kubectl exec`, or the
  database port. Namespaces, StorageClasses and BackupStorage come from the
  chart's install Jobs.

## Hub and connected environments

A connected environment dials the hub. The hub never dials it, so an environment behind
NAT or a firewall joins with outbound HTTPS only.

1. **Join.** The hub mints a single use join token and prints a command. The
   command downloads `bootstrap.sh` from the hub, which enrolls with the token
   and receives the environment name, an agent token and a read-only deploy key. It
   never receives the GitHub org token.
   Any User mints the token for their own Team, and an Organization admin for any
   Team. The agent token belongs to the environment and its Team, not to the User
   who minted it, so removing that User leaves the environment connected. Names are
   unique per Team, and minting again for a name the Team has reissues it.
2. **Install.** It creates or reuses an EKS cluster (none with `--local`),
   installs the chart with `adapter.role=agent` and no UI, then Flux.
3. **Heartbeat.** Every 15 seconds the agent posts the Instances and backups it
   reads locally, credentials stripped, with metric readings from its PMM and
   the alerts that PMM's Grafana Alertmanager has firing (see
   Alerts on the heartbeat, below). The
   response is the work queue: backup, scale, delete, and credentials. The
   agent runs them against the local OpenEverest and reports on a later beat.
   Credentials is how a new MySQL database starts: the hub writes its Instance
   to Git but leaves it out of the environment's `kustomization.yaml`, and the
   agent creates the users Secret `everest-secrets-<name>` with an alphanumeric
   password for every system user. Only when the agent answers does the hub
   list the Instance, so the Secret exists before the PXC operator would
   generate one with symbols, and a `#`, `;` or `$` in the monitor password
   would cut the login the monitoring exporter reads from its unquoted
   `my.cnf`. An answer with an error, from an agent that cannot write Secrets
   or does not know the kind, releases the Instance too and the operator
   generates the passwords. A Secret that already exists is kept, and the
   agent answers with an error when one of its passwords holds a character
   the exporter cuts, so the hub logs it. Once the index lists a database it
   stays listed even if its credentials item is still owed. A
   restore is never held, because the provider copies the source's users
   Secret for it. A PostgreSQL database is never held either: nothing reads
   that Secret for it, so the hub lists it at once. The agent also
   watches the list itself: when a PostgreSQL database it had seen is gone, it
   deletes the Secrets the PostgreSQL operator left behind on the following
   beats until a pass finds none. It deletes them by name
   (`postgresql.LeftoverSecrets`), never lists or reads Secrets, and its Role
   in the database namespace grants only `delete` and `create`. The per-member
   certificate Secrets have a random suffix and are left. A database that is listed again is never swept. The
   memory is per process, so a cleanup owed across an agent restart is not run.
4. **Staleness.** An environment with no beat inside `adapter.hub.heartbeatStale`
   (90 seconds) reads as unreachable. Create, scale, schedule and delete are
   commits, so they are accepted while it is away and Flux applies them when it
   is back. Backup rides the heartbeat, so it needs it connected.
   An environment that has never sent a beat accepts nothing.

`/readyz` on an agent fails until the first successful beat and again after
consecutive failures. Upgrade the hub before its agents: an agent older than
the hub may not report what the hub expects.

#### Alerts on the heartbeat

Every beat carries an `alerts` object read from the environment's own PMM, with
the agent's `PMM_TOKEN`, at `/graph/api/alertmanager/grafana/api/v2/alerts`
with `active=true&silenced=false&inhibited=false`. The control plane never
calls PMM. The read runs beside the metric readings inside the same monitoring
budget, and a failed read never fails the beat.

| Field            | Meaning                                                                                                                                                                  |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `alerts` absent  | The agent predates the field. No reading, not "no alerts"                                                                                                                |
| `alerts.at`      | When the agent read PMM                                                                                                                                                  |
| `alerts.pending` | Why PMM could not be read: `not-configured` (no PMM), `unreachable`, `unavailable`. `items` is then empty and `total` is 0                                               |
| `alerts.total`   | Every firing alert PMM held, before the cap                                                                                                                              |
| `alerts.items`   | At most 200 and at most 400 KiB encoded, critical first, then warning, then info, each longest firing first. A `total` above the number of items means the rest were cut |

Each item has `fingerprint`, `rule` (`pds_rule`, else `alertname`), `severity`
(`critical` or `error` map to `critical`, `info` or `notice` to `info`, anything
else or none to `warning`), `summary` (the `summary` annotation, else the alert
name), `startsAt`, and `labels` limited to `alertname`, `pds_rule`, `severity`,
`cluster`, `namespace`, `node_name`, `service_name` and `instance`. Label values
are cut at 200 bytes and the summary at 300. Items are added until the next one
would take the encoded list past 400 KiB, where `&`, `<` and `>` count as six
bytes, so the `alerts` object stays well inside the heartbeat's 1 MiB body
limit. The agent reads the PMM list as a stream and holds the 2000 best ranked
alerts, critical first, while it counts every other one, so a critical late in a
long list is kept and `total` stays whole however many alerts fire. A null
element is skipped and not counted, a body that ends early is read as an
unreachable PMM, and bytes after the closing bracket are ignored.

#### Serving alerts

The hub keeps the newest heartbeat per environment, `alerts` included, and
stores nothing else: no table and no history. Two routes read it, both organization
scoped like every hub route:

| Route                                                        | Answers                                                                                                                                        |
| ------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /api/alerts`                                            | `environments`, `alerts` and `criticals` for every environment. The page and the sidebar count read this one response                          |
| `GET /api/databases/{environment}/{namespace}.{name}/alerts` | `databaseId`, that database's `environment` entry and its `alerts`. An unknown database is 404, including an id whose namespace does not match |

Each `environments` entry has `name`, `state`, `readAt`, `total`, `shown`, and
for a state that is not `live` a `reason` and one `action`. `state` is `live`
(PMM was read on the newest beat), `off` (`pending` says why: `not-configured`,
`unreachable`, `unavailable`, or `outdated` for an agent that sent no `alerts`)
or `stale` (the heartbeat is older than `HUB_HEARTBEAT_STALE`, or there never
was one). An environment that is not `live` has no PMM alerts, so an empty list
there is unknown, never healthy. A stale one has no alerts at all. When `total`
is above `shown`, `note` says how many were cut.

Each `alerts` entry has `id`, `environment`, `databaseId` (absent when the alert
maps to no database), `rule`, `severity`, `summary`, `startsAt`, `labels` and an
`action` of `kind` and `label`. An alert maps to a database when its `cluster`
label is a database name in that environment and its `namespace` label matches,
or is absent and only one database has that name. The list is ordered by
severity, then longest firing. Rules and their action kinds: `service_down`
`open_database`, `node_down` `view_environment`, `disk_used` `scale_storage`,
`replication_lag` `view_metrics`, `connections_used` `scale_resources`,
`backup_failed` `retry_backup`, any other rule `open_database`. An alert that
maps to no database offers `view_environment` instead. An unavailable
environment's action is `view_environment`, `set_up_monitoring`,
`open_monitoring` or `update_connection`.

`backup_failed` is the one alert the control plane computes: a Warning for each
database whose newest finished backup in the heartbeat failed. It is present
whenever the heartbeat is current, whatever state PMM is in. A backup names its
database but not its namespace, so when databases in one environment share a
name, any failed finished backup of that name raises the alert on each of them.
`criticals` counts
the Critical alerts in the list.

#### The Alerts page

The console's Alerts page (`/alerts`) and the sidebar count read `GET /api/alerts`
through one query, every 30 seconds. The sidebar shows the number of Critical
alerts as an icon plus the number, and nothing while the read is pending or has
failed. Rows keep the order the control plane sends. The severity and
environment filters live in the URL (`?severity=` and `?environment=`). Each
environment that is not `live` gets one banner line with its `reason` and its
one `action`, and an environment's `note` is shown when alerts were cut. Each
row's action opens the database page on the control that does the work (scale
dialog, replication lag chart, or the Backups tab), the same way a chat action
does, and falls back to the environment page when an alert names no database.
An environment's own action opens that environment's page. Following a link
never writes: a failed backup lands on the Backups tab and the customer presses
Back up now. Relative times ("Firing since", "Last read") move every 30 seconds
between reads. Filtering to an environment with no current reading says so
rather than reporting no match, and a `?environment=` naming an environment the
report does not hold is ignored. The page is read only: it has no silence or
acknowledge control.

### Support access windows

An environment has one support access record, kept per Team in
`hub_support_access`: the current window (its id, level, expiry, case reference
and key id) and the support address, which is created once and kept for the
environment's life. A new window replaces the last, so history is the activity
log, never this row.

`GET`, `POST`, `PATCH` and `DELETE /api/environments/{name}/support-access` read,
open, extend and end the window. Reading is open to any member of the
environment's Team. Opening, extending and ending go through `requireSupportAccessWriter`, the one place that decides
who may write, which today is the gate monitoring settings use. An environment of
another Team answers 404.

- Open takes `level` (`readOnly` or `admin`), `hours` (1, 4, 24 or 72) and an
  optional `caseReference`, trimmed and at most 64 characters. A value outside
  those answers 422. An environment that is not connected, or one with a window
  already opening or open, answers 409. The window starts `opening`.
- A read answers `{"window": null}` or the window with `state` (`opening`, `open`
  or `closed`), `level`, `expiresAt`, `caseReference` and, when closed,
  `closedReason` (`ended`, `expired` or `failed`). The state is computed from `expiresAt` on
  every read, so a window past its expiry reads closed whether or not anything
  has swept it.
- Extend takes `hours` and nothing else, and means "until N hours from now". It
  answers 200 with the window. Hours outside 1 to 72, or an end no later than
  the window's current one, answer 422. A window that is not open, still opening
  included, or none at all, answers 409. Level and case reference never change:
  a body that names either is refused with 400.
- The window carries `extendingTo` from an extend until the agent confirms it.
  `expiresAt` stays what the environment holds until then, so the countdown never
  promises time the environment lacks. Asking again before the confirmation
  replaces the pending `extendingTo`.
- End answers 204. Ending a window that is already closed, or was never opened,
  succeeds and writes no entry.
- Each row of `GET /api/environments` carries `supportAccess` (`state`, `level`,
  `expiresAt`) while a window is opening or open, and nothing otherwise.
- Opening writes `support_access.open`, extending `support_access.extend` and
  ending `support_access.end` to the activity log, with the environment on the entry. The agent's
  confirmations write `support_access.install` and `support_access.uninstall`,
  a window that never started writes `support_access.fail`, and one that ran
  out writes `support_access.expire`. Removing the environment writes
  `support_access.remove`. They appear in
  `GET /api/activity` and in the `entries` of
  `GET /api/environments/{name}/history`, beside the join `runs`.
- When a window has a case reference, the control plane posts a comment to that
  ServiceNow case when the agent confirms the install and when access ends
  (by an admin, or by expiry). The open note says the environment, level, expiry,
  the address's DNS name and where the engineer's steps are. Neither note carries a key, a key
  id or a connection string, and both end with the ServiceNow marker. A window
  with no case reference posts nothing, and a failed post is logged and never
  holds up opening or ending (`service.Support.NoteAccessOpened`,
  `NoteAccessEndedByAdmin` and `NoteAccessExpired`).
- No response carries the key id. The open window's response carries the same
  text as the open note, in `shareNote`, with the address's DNS name in it, so
  a member can send it to an engineer when there is no case or the post failed.
  It is set only on an open window and only for a caller who may change support
  access (the same check as opening and ending it), and no other response to a customer carries the address.

Removing an environment (`DELETE /api/environments/{name}`) takes its support
access with it: an active window's devices are deleted and its key revoked, the
support address is deleted, and the record is dropped, so the address is never
reused for another environment. `support_access.remove` is written when the
environment held a record. A network that refuses does not stop the removal: the
record is dropped and the stray sweep below deletes what is left. An agent that
still reports the window as held is told to remove its workload, as for any
window the hub does not know.

Access ends on its own. Each Team's hub service sweeps its windows at boot and
every minute: a window past its expiry has its devices deleted and its key
revoked, and only then is it stored as closed and `support_access.expire`
written, so a network that refuses is retried and the entry is written once. A
Team's service is built on its first request, so a Team with no traffic since a
restart is swept from then. The agent reports the windows it holds on every beat
and is told to close any the hub holds as closed or does not know, and it removes
a workload past its expiry by itself when it cannot reach the hub.

One more sweeper runs per process, on a hub with a database and the network
credentials. The network is shared by every Team, so it reads the records of all
Teams (`Store.AllSupportAccess`) and deletes a device carrying the environment
tag whose hostname is not an active window's id, and an address named `svc:pds-`
that no record holds. A census it cannot read deletes nothing. Address creation
and this sweep share one lock, so an address is never swept before its record is
saved.

The hub reaches the private network through `internal/client/tailnet` with two
OAuth clients, loaded from Secrets into memory and never stored. The key client
mints single-use, ephemeral keys for one shared environment tag, and lists and
deletes devices. The service client creates the private addresses, each with one
service tag and the one port `tcp:443`. Neither reaches the policy, which is
edited by hand. `SUPPORT_TAILNET_DOMAIN` is the network's MagicDNS domain, which
the hub puts after an address's name to get the DNS name the proxy keeps its
certificate under. Without it, `SUPPORT_TAILNET` and the four client settings
(`SUPPORT_TAILNET_KEY_CLIENT_ID`, `_KEY_CLIENT_SECRET`, `_SERVICE_CLIENT_ID`,
`_SERVICE_CLIENT_SECRET`) the hub makes no network call and an environment
uses a key made by hand. With some of the six and not all, the hub still
starts, logs one warning naming the missing settings, and leaves support access
off as if none were set. Off means the `support-access` surface flag is off
whatever the operator set, so `/api/features` publishes it off, the console
hides it and its routes answer 501. The tags default to `tag:pds-environment` and
`tag:pds-support`, and `SUPPORT_TAILNET_URL` points the client at a fake.

- The first open on an environment creates its address, named from a random id
  and not from the environment, and keeps it. Every open then checks the
  address's tags and ports, and a mismatch refuses the open with 502 and
  "Contact Percona support." The cause is in the hub's error log with the request
  id, which is the alert. Responses never name the network vendor.
- `MintSupportKey` makes a window's key with a 5 minute life and stores only its
  id. Asking again revokes the previous id first.
- Ending a window deletes the devices named with the window id, then revokes the
  key by id, before the window reads closed. If the network is unreachable the
  end answers 502 and the window stays as it was.

How a window starts and stops. The hub never dials the environment, so both
ride the heartbeat work queue as `support_open`, `support_extend` and `support_close` items that
carry the window id, level, expiry and the environment's address.

- The hub mints the single-use key when it hands a `support_open` to the agent.
  The key is on that reply only: the queued item and the window row never hold
  it, and an item handed out again gets a fresh key and revokes the last one.
- An agent that runs support access sends `support` on every beat: the window
  ids whose workload it holds, the ones among them whose proxy can serve
  (`serving`: the Secret named for the address's DNS name, which the
  Deployment's `pds.percona.com/support-host` annotation names, holds a key and
  a certificate that has not expired), and its confirmations since the last
  beat (`installed`, `removed`, `failed`). A beat without `support` is an agent
  that predates it, and the hub leaves its windows alone.
- Confirmations count once per window and event, so a beat sent again writes no
  second entry. A `failed` goes to the hub's log with the agent's reason and
  nothing else.
- A window reads `open` only once the agent lists it as `serving` and the
  private network approves its device to host the environment's address. Until
  the proxy holds its certificate every TLS handshake to the address times out,
  and issuing it can fail, so approval alone does not open a window. On each
  beat the hub finds the device named for the window id and asks the network
  whether the policy, or a person, has approved it for the address
  (`GET .../vip-services/{name}/device/{id}/approved` answers `approved` and
  `autoApproved`), and a serving window opens when either is true. That is
  permission, not proof: the network does not say whether the device
  advertises the address, so `open` means the workload is installed, holds its
  certificate and is allowed to serve, not that a connection was made. An open
  window is not asked about again. A network that cannot answer moves nothing.
  A hub with no private network has no address and no certificate, and opens
  on the agent's `installed`.
- While a window is opening and no install is confirmed, each beat mints a new
  key (revoking the old) until 5 minutes after the open. A window that is
  installed but not serving or not approved is not handed the open again, so
  its proxy keeps trying for the certificate. A window still in `opening` after
  5 minutes has its devices and key taken away and closes as `failed`. The customer sees one sentence in the activity log.
- An extension rides as a `support_extend` item with the window id and the new
  expiry, queued on each beat while `extendingTo` is set and no item is owed. The
  agent moves the expiry on the workload's Deployment and leaves the proxy
  running, then confirms `extended` with that expiry. The hub applies a
  confirmation only when its expiry is the pending `extendingTo`, so a repeated or
  stale one changes nothing and writes no entry. An extension that fails is
  retried on the next beat until the window expires. Ending a window drops an
  extension the agent has not run.
- A workload the agent holds for a window that is closed or unknown to the hub
  gets a `support_close`. Ending a window also drops an open the agent has not
  run. The agent also removes a workload past its expiry on its own, without the
  hub.

Per window the agent creates, in `pds-support`, the ServiceAccount
`pds-support-proxy`, the config and state Secrets, an empty Secret named for the
address's DNS name (the proxy may update it but not create it, so it holds its
certificate there), the Deployment `pds-support-proxy` and a ClusterRoleBinding named for the window that
binds `pds-support-read` or `pds-support-admin` to the group
`pds.percona.com:support`, the identity the proxy impersonates engineers into. The key and the address are in the
config Secret only. Flux does not manage any of it, and removal deletes only
objects labeled with that window's id.

The certificate Secret is the exception: it belongs to the address, carries no
window label, and outlives the window. Let's Encrypt issues one name at most 5
certificates a week, and each issuance is a DNS race that can fail, so the next
window's proxy finds the certificate and asks for none, and renews it itself
near expiry. Its private key stays in `pds-support` between windows. Removing
the environment deletes the address but not that Secret; a new address has a
new name, so it is never reused, and it goes with the namespace.

What the environment holds is installed once by the join script, on a join and
with `--update` (`install_support_access` in `connect_bootstrap.sh`), so opening
a window needs no customer step:

- The `pds-support` namespace, where the access workload runs.
- `pds-support-read` and `pds-support-admin`, the two access levels as
  ClusterRoles. Read only looks at the environment. Admin adds create, update
  and patch on `instances` and `backups`, writes on `restores` and a pod
  delete, but no delete of a database or a backup. Neither reads Secrets
  or has exec, port-forward, token minting or RBAC writes, and admin writes no
  Deployment, ServiceAccount or ConfigMap, so a window cannot change the access
  workload, its binding or the agent. RBAC cannot narrow a Secret read to the
  ones labeled for support, so no level reads them.
- `pds-support-secrets`, a Role in `pds-support` bound to the ServiceAccount
  `pds-support-proxy`, which keeps the workload's state and certificate Secrets.
  It has no `create`, so the agent makes the certificate Secret empty first.
- `pds-support-proxy`, a ClusterRole bound by a standing ClusterRoleBinding to
  the same ServiceAccount: `impersonate` on users and on the one group
  `pds.percona.com:support`. The proxy sets the engineer's login as the
  impersonated user, so users cannot be limited by name, but a group can. The
  agent does not hold it.
- `pds-support-agent`, the agent's grant: manage ServiceAccounts, ConfigMaps,
  Secrets and Deployments in `pds-support`, get, list, create and delete
  ClusterRoleBindings, and `bind` on `pds-support-read` and `pds-support-admin`
  only. Kubernetes refuses any binding to another role.

Limits to know. The proxy can impersonate any user, so a compromised proxy can
act as any user that holds permissions, such as a cluster administrator's
login. Kubernetes checks service accounts as their own resource, so those are
out of reach, and so is any group but the support one, `system:masters`
included. The binding is standing and the agent may create a ServiceAccount and
a Deployment in `pds-support`, so the agent can run a pod as the proxy's account
and reach the same impersonation: it is trusted at that level, though it holds
no `impersonate` itself. The namespace enforces the `baseline` pod security level, so the
agent cannot start a privileged pod there. The agent may bind the two roles to
any subject, itself included, and its ClusterRoleBinding delete is not limited
by name because binding names are per window. Admin's pod delete is cluster
wide, so a window can restart the agent or workload pod but not change them.
Reading database credential Secrets "selected by label", which the spec asks
of admin, is not built: it needs a mechanism other than RBAC.

### What Git holds

On join the hub writes `clusters/<environment>/cluster.yaml` and
`clusters/<environment>/instances/kustomization.yaml`. The directory keeps the name
`clusters/` because Flux on each connected environment reads that path. Create adds an Instance file
and lists it in the index, scale edits it, delete removes it. A restore into a
new database is also a new file, which for PostgreSQL waits for the agent's seed.

The hub writes desired state to one of two places, never both:

| Hub setting                                                                               | Where it writes                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| ----------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `adapter.hub.github.org` with `HUB_GITHUB_TOKEN`, or with the `HUB_GITHUB_APP_*` settings | One private repository per environment. Connect creates it and stores its name on the environment's record, so a later rename of the Organization or Team never moves it. A new environment is named `<organization>-<team>-<environment>`, each of the first two as a lower case slug of at most 16 characters. Every new name is claimed in `hub_repo_claims`, one owner per name across all organizations, and a name already claimed or already a repository on GitHub gets `-2`, `-3` and so on up to `-99`, so two environments never share a repository. An environment that joined before records carried a name keeps the one it used: its own name for a release that serves one Organization, and `<organization>-<environment>` for one that serves every Organization. The upgrade writes that name onto the record. |
| `adapter.hub.gitDir`                                                                      | A local directory. Environments clone `adapter.hub.gitURL`, and getting the directory onto that remote is outside the adapter: the lab runs a commit and push loop to Gitea.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |

On the connected environment, Flux runs a `GitRepository` named `hub` and a
`Kustomization` named `instances` with `prune: true`, applying as the
ServiceAccount `hub-instances`, which may touch `instances.core.openeverest.io`
in `pds` and nothing else. The Flux install manifest is pinned by SHA-256 in
the bootstrap script, because a release asset can be replaced and the script
applies it with cluster-admin. GitHub's host key is pinned in `known_hosts`.

The monitoring definition sits beside it in `clusters/<environment>/platform/pmm`:
the PMM HelmRelease, its HelmRepository, and a ConfigMap, `pds-alert-rules`,
that holds the curated alert rules, for MySQL and PostgreSQL, as a Grafana alerting provisioning
file. Each engine has service down, data volume over 85%, replication lag
over 30 seconds and connections over 90% of `max_connections`, and one node
down rule covers both. Each is labelled
with `pds_rule` and `severity` and keeping the `cluster` and `namespace`
labels so an alert maps to a database. PMM loads them at start into a
read-only folder named PDS. The HelmRelease mounts the ConfigMap at Grafana's
alerting provisioning directory through `extraVolumes` and `extraVolumeMounts`,
and writes the chart's own `pmm-config` volume back because setting
`extraVolumes` replaces the chart's list. A digest of the rules sits on the pod
as an annotation, so a changed rule rolls PMM. The `hub-platform` Role on the
connected environment grants ConfigMaps for this. Only the join script can install
that Role: the hub holds no kubeconfig, and the platform Kustomization runs as
`hub-platform`, which cannot edit Roles. The agent tells the hub it ran a
join of this release with the `alertRules` mark on its heartbeat. The hub
rewrites the PMM tree of an environment that runs monitoring and carries the
mark, once at start and on the first beat of an environment that joined again
afterwards, keeping its settings and chart version and committing nothing when
the tree is already current. An environment without the mark is left alone,
because Flux would refuse the whole platform tree and PMM would mount a
ConfigMap that does not exist. A settings change reads the mark under the
environment's lock, and a settings change rendered without the rules, or a beat
without the mark, makes the next beat with the mark reconcile again. The
background run ends with the process.

Never in Git: Backup and Restore objects, connection strings, passwords, the
org token, join and agent tokens.

Disconnect revokes the environment's tokens and deploy key and leaves its databases
running. Its Instance files stay in Git on purpose: with `prune: true`, an
emptied index followed by a later join of the same name would delete every
database that Kustomization owned.

### Why it is built this way

- **Desired state lives in Git, actions do not.** Replaying a backup or a
  restore from Git would repeat it. Only what should converge is committed.
- **No inbound path into a customer cluster.** The hub holds no kubeconfig and
  no route to a customer network.
- **Plain Flux controllers.** Flux is Apache-2.0 like this repository. The
  Flux Operator is AGPL-3.0, so it is not shipped into customer clusters.
- **Fleet tools that were considered and rejected:**
  - Open Cluster Management: a second control plane reconciling the same
    clusters beside Flux.
  - Rancher Fleet: delivery only, with no read path and no way to send a
    backup or a restore.
  - The Argo CD agent: pre-1.0, and an Argo `Application` is heavier than a
    database needs.
  - Karmada: built for workload scheduling PDS does not need.
  - GitLab agentk and KAS: puts GitLab in the read path, and its chart grants
    cluster-admin.

## Adapter to the other services

| To           | Calls                                                                                                                                                   | Credential                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| PMM          | The agent calls `GET /prometheus/api/v1/query_range` and posts the newest reading on its heartbeat. The hub charts those samples and does not call PMM. | A Grafana service account token in Secret `<release>-pmm` on the connected environment, minted by the chart's hook. PMM 3 refuses anonymous queries. When `PMM_URL` is unset the agent logs that metrics will answer pending.                                                                                                                                                                                                                                                                                                                                                                                                 |
| ServiceNow   | `/api/pellc/percona_connector/*` and the attachment API, plus the Table API to list and reply to cases                                                  | `x-sn-apikey` header and a `client_token` body field. Without both, support answers 501 and the UI hides it.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| GitHub       | `api.github.com`: create a repository, commit through the Contents API, add a deploy key                                                                | Hub only. A GitHub App installation: the adapter signs a JWT with the App key (`HUB_GITHUB_APP_ID`, `HUB_GITHUB_APP_INSTALLATION_ID`, `HUB_GITHUB_APP_PRIVATE_KEY` or `HUB_GITHUB_APP_PRIVATE_KEY_FILE`), exchanges it for an installation token that lasts an hour, and renews it before it expires or after a 401. `HUB_GITHUB_TOKEN` is the fallback for the lab and local Compose, and setting both is a start error. Enrolled environments never receive either: their image pull Secret comes from `HUB_REGISTRY_TOKEN`, a separate credential, because an installation token would expire under a running environment. |
| AI providers | Anthropic and Cursor                                                                                                                                    | The key an Organization pastes in the chat panel, sealed in the adapter database. There is no operator key in the chart or the environment.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |

### Chat requests to Anthropic

`adapter/internal/client/claude` sends every Chat model call. A request that names no model runs on `claude-sonnet-5-5`
at effort `low`, the level Anthropic suggests for chat. The model list marks that model as the default, and a new chat
starts on it.

- **Cache.** The tool list (the registry's, for the Chat's tier) and the system prompt (the Chat's role, the registry's
  instructions and the answer format) are the stable prefix, ended by one `cache_control` breakpoint on the
  system block. Nothing that changes per request comes before it: the instruction to answer without tools, sent on the
  last round, goes after the tool results. From the second model call of a turn on, and on later turns within the
  cache's lifetime, the API reports the prefix under `cache_read_input_tokens`. The adapter logs each call's usage at
  debug level.
- **Valid for current models.** A request never ends on an assistant turn, forces a tool with `tool_choice`, or sets
  `temperature`, `top_p` or `top_k`. The final round's `tool_choice: none` stays. `max_tokens` leaves room for
  adaptive thinking.
- **Responses go back unchanged.** Within a turn, each response is sent back as the API returned it, thinking blocks
  and their signatures included. Earlier turns go back as the saved text.
- **Refusals.** Models with a server-side fallback send `fallbacks: "default"` with the beta
  `server-side-fallback-2026-07-01`. A turn that still ends in `stop_reason: refusal` answers with a fixed sentence
  instead of failing, and the partial output is dropped. Effort and fallback are sent only to models that accept them,
  so a model picked from the list that predates them still works.
- **Streaming.** A call that has a text listener sends `stream: true` and reads the server-sent events: `message_start`
  for the usage it opens with, a `content_block_start`, deltas and `content_block_stop` for each block, `message_delta`
  for the stop reason and the final token counts, and `error` for a failure after the stream opened. Each block is
  rebuilt from its deltas, a thinking block with its signature and a tool call with its parsed input, and goes back to
  the API as that raw JSON. When a fallback model takes over part way, the blocks written before the switch are kept only
  if they are text. The HTTP timeout still bounds the whole call. The adapter uses its own client rather than
  `github.com/anthropics/anthropic-sdk-go`: it already owns the raw block round trip and the beta fields, and the stream
  reader is one small file, so the SDK would add a dependency and its retry behavior for no gain.
- **Deprecated models.** A turn that names a model Anthropic has deprecated or retired runs on the default: a
  conversation saved on one continues on the default from its next turn, a new chat that picks one starts on the
  default, and the conversation list and thread report the default. The model list still shows what Anthropic lists.

### Chat requests to Cursor

A Cursor turn drives Cursor Cloud Agents with no repository attached. The agent cannot call a tool, so the answer keeps
its JSON form and the emulated tool protocol: the adapter reads each `tool_calls` reply, calls each tool through the
registry and sends the results back as the next run. The tool list in the agent's first prompt is the registry's.

- **One agent per conversation.** The first turn creates the agent, and the conversation keeps its id in
  `hub_chat_conversations.agent_id`. A later turn starts a run on that agent and sends only the new question, since the
  agent already holds the earlier turns. A question carries the current database list ahead of it, so the list is as new
  as the question and not as old as the agent. The system prompt stays the same on every round. The agent is given the
  system prompt, the tool list and the model once, when it is created, so a conversation keeps the ones its agent
  started with until a new agent takes over.
- **A new agent takes over** when the kept one refuses a run with a 4xx other than a refused key or a request limit: it
  is deleted, archived, or still running a turn that timed out. The new agent starts from the stored thread, the same
  way a conversation's first turn does, and the conversation keeps its id. The agent that refused is deleted. A 5xx, a
  request limit or a refused key leaves the agent as it is and fails the turn.
- **Deleting a conversation deletes its agent** at Cursor. It is best effort: the conversation is gone whether or not
  Cursor answers, and a failure is logged. A first turn that fails takes its conversation back and deletes the agent it
  started. A kept agent holds the conversation's history at Cursor until the conversation is deleted.
- **No faster endpoint.** The Cloud Agents API has no completion endpoint and no way to ask a model without an agent, so
  every answer is a run, polled every 500 ms until it finishes. Its run stream (`/v1/agents/{id}/runs/{runId}/stream`,
  server-sent events) is not used, which is why a Cursor answer arrives whole.

### The Chat's stream

`POST /api/chat` with `Accept: text/event-stream` answers a turn as events. A client that does not ask for a stream
gets the whole reply as one JSON object, whichever provider answers.

| Event          | Data                                    | Meaning                                                                                      |
| -------------- | --------------------------------------- | -------------------------------------------------------------------------------------------- |
| `conversation` | `{conversationId}`                      | The message is saved and the conversation exists. Sent first.                                |
| `progress`     | `{label}`                               | A step of the turn, such as `Thinking` or `Reading status`.                                  |
| `text`         | `{delta}`                               | The next piece of the answer's text. The pieces, in order, add up to the reply's text.       |
| `reply`        | `{conversationId, text, actions}`       | The end of the turn: the whole saved text and the buttons. It replaces whatever `text` showed. |
| `error`        | `{code, message}`                       | The turn failed after the stream opened. The pieces shown so far are not saved.              |
| `pending`      | `{}`                                    | A retried first message that is still being answered: wait on its conversation.              |
| `approval`     | `{id, tool, title, database, destructive, lines, expiresAt}` | A change the model asked for. The turn waits until it is answered, rejected or expired. |
| `approval_end` | `{id, outcome}`                         | How an approval ended: `approved`, `rejected`, `expired` or `cancelled`.                     |

A comment line, `: ping`, keeps the connection alive while the model works or an approval waits.

With Anthropic, `text` events follow the model. The answer is plain text. Buttons that open something are a structured part at its end, one
line `<actions>[{"label":"View status","kind":"status","id":"environment/namespace.name"}]</actions>`, which the adapter cuts out of
the text as it arrives and reads once the call ends. A button of a kind the console does not know, or one that names a
database the caller cannot see, is dropped. A tool round writes text too: it is shown, a new paragraph starts when the
next round writes, and a structured part in a tool round is discarded. Text is released at word boundaries, and a line
that could carry a credential is held until it ends, so the redaction that saved replies get applies to what is
streamed. A private key that has begun is held until it ends, and one that never ends is dropped. An answer that stops
at the token limit ends with a sentence saying it was cut off, and its structured part is discarded. A turn whose model
refuses sends a `reply` with a fixed sentence, which replaces the text already shown.

Cursor does not stream. Its answer keeps the JSON form and the emulated tool protocol, and arrives as one `text` event
followed by the `reply`.

The saved message is the text the person saw, with its buttons. A dropped connection leaves the turn running, and the
reply is in the conversation when the person comes back.

## State

The HTTP adapter's PostgreSQL is not a customer database. It holds bcrypt
password hashes, sessions, organizations, environment inventory, join token hashes,
deploy keys, the work queue, support access windows, database history, usage
samples and saved chats.
Connection secrets are sealed with AES-GCM under `STATE_ENCRYPTION_KEY`, 32
bytes kept in a Secret and never in the database. Losing that key makes every
sealed row unreadable. `STATE_ENCRYPTION_KEYS_RETIRED` lets a rotated key
still open old rows.

- The schema is GORM `AutoMigrate` on every start, under a PostgreSQL advisory
  lock. There is no down migration. The first start after the release before
  Organizations turns each owner it keyed rows by into an Organization with a
  default Team and an admin User that keeps its login, moves every row, signed
  in browser and unspent invite link under that Team and User, and drops the old
  columns and tables. It needs `STATE_ENCRYPTION_KEY` to re-seal values against
  their new owner, and the old release cannot run against the database afterwards.
- `/readyz` waits on this database and nothing else. An OpenEverest, PMM or
  ServiceNow outage keeps the UI up so it can explain the outage.
- A hub runs one replica: the work queue and the environment inventory cache are
  per process.
- A release serves one named Organization, or every Organization when
  `adapter.state.organization` is `*`. Each row is keyed by Team, and connections by Organization, and a request for
  another Organization's id answers 404.
