# PDS MCP server: design

This is the design of `pds-mcp`, the program that connects an AI agent on a person's computer to the control plane, and of the tools the agent finds there. The program lives in `apps/mcp`; the tools live in the Go registry in `adapter/internal/mcp`, which the control plane serves at `/mcp`. Per-tool inputs, results and descriptions are in [mcp-server-tool-catalog.md](mcp-server-tool-catalog.md) and are not repeated.

## 1. Purpose and scope

An AI agent, driven by a person who has chosen to give it access, reads and changes the databases of one Organization on the control plane: list and inspect them, read metrics, create, scale, back up, restore and delete them.

In scope:

- `pds-mcp login` and `pds-mcp logout`: the person signs in in their browser, as a native OAuth client, and the program keeps one refresh token for each control plane.
- `pds-mcp`, a stdio bridge to the control plane's `/mcp` for clients that run a local command. It holds no tool code.
- The tools, instructions and prompts the registry serves to every client alike: this bridge, a remote MCP client, the `pds` plugin and the console Chat.

Out of scope (each has a reason in section 14):

- A password, an operator token, or any setting that chooses what the agent may do: the person chooses on the consent screen.
- A tool that returns a database password.
- Windows.

## 2. Architecture

```text
agent (stdio)  <->  pds-mcp  <->  https://<control plane>/mcp   (streamable HTTP, bearer token)
                      |
                      +-- pds-mcp login: browser, loopback redirect, authorization code + PKCE
                          (the authorization server is the control plane itself)
```

One process per client, started by the MCP client (an editor or agent app) with a command, arguments and environment variables from the client's settings. It speaks MCP over stdin and stdout and HTTPS to one control plane. It is written on the Go MCP SDK: the SDK's stdio transport reads and writes the agent's messages, its streamable HTTP client transport carries them to `/mcp`, and the program pumps messages between the two. Nothing is parsed beyond what the bridge needs (section 4), so every method, both protocol eras and every notification the control plane sends in a response reach the agent as they were sent.

Startup: read `PDS_URL` and refuse the settings of earlier versions (section 3), open the login file, say on stderr if there is no sign in, and start forwarding. Nothing contacts the control plane until the agent sends its first message, so a control plane that is down when the client starts is picked up when it comes back. Shutdown: stdin closing ends the bridge once the calls in flight are answered. Nothing is written to disk.

The control plane is its own authorization server (`/oauth/authorize`, `/oauth/token`, `/oauth/register`, `/oauth/revoke` and `/.well-known/oauth-authorization-server`), with PKCE, Client ID Metadata Documents and Dynamic Client Registration, a consent screen after the console sign in, and opaque tokens backed by an agent session that lasts like a console session. `/mcp` serves the registry over stateless streamable HTTP with one server per request, built for the caller the access token resolves to, so a read token lists no change tool. A request with no valid token is a 401 whose `WWW-Authenticate` points at `/.well-known/oauth-protected-resource/mcp`. How it works is in [architecture.md](architecture.md).

## 3. Configuration

| Setting | Meaning |
| --- | --- |
| `PDS_URL` | Address of the control plane, with no path. `https` is required unless the host is loopback. Without it, `https://pds.percona.com`. It is also the OAuth issuer, so a control plane whose metadata names another issuer is refused. |
| `XDG_CONFIG_HOME` | Where the login file is kept. `~/.config` without it. |
| `pds-mcp login --access read\|change\|delete` | The most the agent may ask for. The default is `read`, so an agent can change nothing until you ask. The consent screen can allow less, never more. |
| `pds-mcp login --no-browser` | Prints the address instead of opening it. |

`PDS_USERNAME`, `PDS_PASSWORD`, `PDS_OPERATOR_TOKEN`, `PDS_ORGANIZATION`, `PDS_TEAM`, `PDS_ALLOW_WRITES` and `PDS_ALLOW_DESTRUCTIVE` are the settings of the TypeScript server this program replaced. A non-empty one stops every command that signs in, with a message that names the setting and `pds-mcp login` and never repeats the value. Silent acceptance would leave a person believing a password or an opt-in still worked. The Organization, the Team and what the agent may do are chosen on the consent screen, and the Organization's own limit applies on top.

There is no switch to skip certificate verification, because the program sends a token and an unverified host could capture it. A private certificate authority is trusted through the system: `SSL_CERT_FILE` on Linux, the keychain on macOS.

## 4. Authentication and session handling

How MCP clients sign in:

- **Sign in is a command.** `pds-mcp login` opens the system browser and runs the authorization code flow with S256 PKCE and a random `state`, as a native client (RFC 8252). No stdio session opens a browser: a program started by an agent has no business taking over the person's screen.
- **Identity.** The client id is the HTTPS address `pds-mcp`. The control plane has it built in as the PDS local client, accepts loopback redirect addresses only for it, and never fetches it. It is the only client that may later fetch database passwords or write with SQL, if the Organization allows it.
- **Discovery.** From `PDS_URL`: the protected resource metadata at `/.well-known/oauth-protected-resource/mcp`, then the authorization server metadata. Discovery refuses a document whose `resource` is not `<PDS_URL>/mcp`, whose issuer is not `PDS_URL`, that does not offer S256, or that names an endpoint on another origin. A control plane that answers 404 has the MCP surface turned off, and the person is told that it does not serve AI agents.
- **The listener.** `127.0.0.1` and an ephemeral port, open only while the sign in is pending. It serves `/callback` and nothing else, refuses a request whose `Host` is not its own address (DNS rebinding), answers a request with the wrong `state` with a 400 and keeps waiting, and closes after the first answer with the right one. The response must carry `iss` equal to the issuer (RFC 9207). The code is exchanged with the verifier and the `resource`.
- **Storage.** `pds-mcp/credentials.json` under the config directory, mode 0600 in a directory of mode 0700, holding one refresh token for each issuer. It is written to a staging file, synced and renamed, under an exclusive `flock`. A file that cannot be read is left alone and refused to a writer. An access token is never written.
- **Refresh and rotation.** The access token lives in memory and is renewed a little before it expires, and on a 401. A refresh token is used once: the new one is saved before the access token it bought is used, and the rotation runs under the same lock, so two bridges and a data server that share the login never spend one token twice. A 401 refreshes once and retries once; a second 401 fails that message.
- **A refused refresh** (`invalid_grant`: the person signed out, the session was revoked or it expired) does not stop the bridge. The message that needed the token is answered with an error that names `pds-mcp login`: a tool error for `tools/call`, a JSON-RPC error for anything else. The login file is kept, so a later `pds-mcp login` replaces it, and the same bridge works again without a restart.
- **`403 insufficient_scope`** becomes the same kind of error, naming the scope and `pds-mcp login`. The bridge never asks for more by itself.
- **Where a token goes.** `internal/oauth.NewHTTPClient` is the only HTTP client that carries one. It refuses a request whose scheme, host and port are not the control plane's before anything is sent, and it follows no redirect, so a redirect or a metadata document cannot aim a bearer token elsewhere. It names the program in `User-Agent` (`pds-mcp/<version>`).
- **Messages.** Every sentence a person or an agent reads is in `internal/messages`. A failure reaches the agent as one of those sentences and never as the error's own text, so a token inside an error cannot leak. Logs go to stderr and name no token, address or body.
- **Logout.** `pds-mcp logout` removes the saved sign in first, then asks the revocation endpoint to end the session, best effort. When the control plane cannot be reached it says so and points at the AI agents page of the console.

## 5. Attribution of changes

The registry records every change with the client that made it. An OAuth session carries the client kind the control plane assigns it (`mcp` for an agent that signed in through `/oauth`, `chat` for the console Chat, which only the in-process caller sets), and a caller that names none of the kinds the control plane knows is served the read tier whatever its access says. The bridge adds nothing here: the sign in the person completed is the identity. It passes on who the agent is in two ways that the control plane may read and must treat as unverified: `User-Agent: pds-mcp/<version>`, and, for an agent of the older protocol era, the agent's own name and version from its `initialize` in the `X-PDS-MCP-Client` header. An agent of the 2026-07-28 era sends its `clientInfo` in each request's `_meta`, which the bridge forwards untouched.

The adapter names the kind `client` (`mcp` for an agent that signed in through OAuth, and `chat` for the console Chat, which only the in-process caller sets) and the program `agent` (up to 64 characters of letters, digits, inner spaces, `.`, `_`, `+` and `-`). Activity entries carry both. A login form declares neither, and the session route returns neither.

## 6. Ids, environments and limits

The control plane is a hub, so a database id carries its environment.

- **Ids.** A database id is `<environment>/<namespace>.<name>`, and the URL carries the slash literally. A one-segment id is refused as invalid input before any request. Tools treat the id as an opaque string copied from a previous result. Because it comes from a model, the registry checks it against `^[a-z0-9-]+/[a-z0-9-]+\.[a-z0-9-]+$` before building any URL, and rejects anything else, so an id like `../environments/x` can never reach another route. Each path segment is encoded separately and the slash is kept.
- **Environments.** A `list_environments` tool; an optional `environment` filter on `list_databases`; a required, never defaulted `environment` on `create_database`.
- **Not registered.** `get_alerts`, `get_logs` and `get_platform_status` are not registered. Those routes are not on this control plane. `get_metrics` stays: it reads usage samples from each environment and may be pending.
- **Create.** MySQL or PostgreSQL through the `engine` input, `mysql` by default. A new database starts as `pending` until the environment picks it up.
- **Restore.** Restores into a new database on the same environment and leaves the source alone. It needs a new `name` (lowercase letters, digits and dashes, at most 19 characters), returns the new `id`, and is not destructive, so it sits in the changes tier and needs no `confirm`.
- **Limits stated, not prechecked.** Reads can be a little behind the real environment, a new database starts `pending`, `set_backup_schedule` works only on databases created through the control plane, and an unreachable environment answers 409. The registry adds no checks of its own and passes the API's messages through.
- **Schemas.** Enums and the name pattern only. Numeric bounds are written in the descriptions and enforced by the API.

## 7. Tools and registration

The tool list, inputs, results, annotations and exact descriptions are in the catalog. The registry decides which tools a caller sees: the caller's access tier is `read`, `change` or `change_delete`, the lowest of the Organization's limit, the sign in choice and what the person's role allows. `tools/list` returns only the tools the tier allows (17 at the read tier, 25 at the change tier, 26 with delete), and a call to a tool above the tier is refused as `not_allowed` before any request is made. A caller that names no client kind (`chat` or `mcp`) is served the read tier whatever its access says.

Decisions that shape the rest of the design:

- One tool per action, so descriptions and annotations are specific. No generic action tool.
- Inputs use the response vocabulary (`memoryGb`, `storageGb`, `members`, `cpu`).  The registry maps them to the API's request names (`memory`, `storage`, `replicas`).
- `confirm` for delete is the database's short name, exact match. The registry reads the database first and compares its stored name; it never trusts the model's claim. A mismatch refuses without calling the API. Restore makes a new database and does not take `confirm`. A client that declared form mode elicitation is asked to type the name instead, and `confirm` is ignored for it (catalog section 6, "Confirmations").
- Public databases are allowed. `sourceRanges` is required for `access: public` and the registry never fills one in. A range of `0.0.0.0/0` or `::/0` is refused with `confirmation_required` until the agent retries with `acknowledgeOpenToInternet: true` after asking the user; the result repeats the warning. A client that declared form mode elicitation is asked directly, and the acknowledgement is ignored for it.
- `set_backup_schedule` requires `enabled`, so a call can never turn a schedule on by accident.
- Output size is controlled: `list_databases` returns short rows, `get_metrics` returns summaries unless `includePoints` is true, `list_activity` returns 50.
- Every result is compact JSON as `structuredContent` with an `outputSchema`, and the same JSON as one text block.
- There is no tool for `GET /api/databases/{id}/credential`. It is the only route that returns a password, and putting one in a model's context and logs is the worst outcome in this design. The Go registry has none at any tier, and the route refuses a request the registry or the Chat builds in process. An agent reaches the route only over HTTP with a token that holds the passwords grant, which the local client uses to put a connection string in the operating system keychain.

### 7.0 The local data server

`pds-mcp data` is a second stdio MCP server in the same binary (`internal/data`). It runs SQL on the person's own computer and never sends a statement or a row to the control plane; the only control plane request it makes is the credential route, for one database's connection string, with the agent's token.

- **Tools.** `connect_database`, `list_connections`, `run_read_query`, `get_my_ip`, `write_env`, and `run_query`, which is listed only while the saved sign in carries `pds:sql_write`. The server re-reads the saved scopes before every `tools/list` and `tools/call` and every few seconds, adds or removes `run_query`, and the SDK sends `notifications/tools/list_changed`. A write also renews the access token first, so a grant the Organization withdrew is read from the control plane's answer and not from the scopes saved last time. The write tool enforces the grant itself.
- **Sign in.** It shares the login file. With no login, the first tool that needs the control plane asks the client, through URL mode elicitation (a result with `InputRequests`, which the SDK also serves to a client of the older protocol), to open the sign in page on the control plane. It starts the same loopback listener `pds-mcp login` uses, with no browser opener, asking for `pds:read` and `pds:credentials`, and keeps the sign in when the person finishes. A client that did not declare URL mode gets a tool error telling the person to run `pds-mcp login --passwords`. Neither Claude Code nor Cursor was driven for this task: Claude Code documents `elicitation: {form, url}`, and Cursor's support for URL mode is unconfirmed.
- **Credential.** `connect_database` takes a database id of the registry's shape, reads the connection string, splits the password from the rest in memory, probes the database, and stores the password in the operating system credential store (`internal/vault`, the Keychain on macOS and the Secret Service on Linux, no file fallback) under the control plane and database id. Host, port, user, database name and TLS mode go to `connections.json` (mode 0600). A control plane refusal (no passwords grant, a private database, an unknown id) is passed through as the control plane worded it. A database that does not exist is an error that names it and creates nothing; one that cannot be reached is saved with a note.
- **TLS.** The connection string decides: `disable` or `tls=false` means plain text, `verify-ca`, `verify-full`, `VERIFY_*` and `tls=true` check the certificate, and anything else, including no setting on a host that is not this computer, encrypts without checking. A connection that asks for TLS never falls back.
- **Reads.** The statement is checked by `internal/sqlguard` (one statement, a first word that is a query, no `INTO`, `UPDATE`, `DELETE`, `INSERT`, `MERGE`, no file, string-executing or session control function, whether written bare, in quotes or with `U&` escapes, no executable comment), then run by `internal/sqlrun` on a fresh connection in a transaction the engine holds read only (`BEGIN READ ONLY` plus `default_transaction_read_only` on PostgreSQL, `START TRANSACTION READ ONLY` plus `transaction_read_only` on MySQL), after a priming `SELECT 1`, because PostgreSQL lets a read only transaction switch itself to read write until its first snapshot. The transaction is always rolled back. Results stop at 1000 rows and 256 KiB and carry `truncated`; a statement stops at 30 seconds.
- **Writes.** One statement that is not transaction control, in a transaction that is committed when it succeeds.
- **Files and addresses.** `write_env` writes `<prefix>_HOST`, `_PORT`, `_USER` and `_NAME` to a file named `.env*` inside the working directory (symbolic links are never followed), never overwrites a key unless told to and lists what it left alone, creates the file mode 0600, and adds the password only for `includePassword: true`. That flag and `overwrite: true` are the model's word, so they need the person: a client that declared form elicitation is asked, with the file and the key named and the answer bound to the request, and a client that did not is refused and nothing is written. `get_my_ip` makes one HTTPS request without a proxy to `checkip.amazonaws.com` and answers only with one public IPv4 address as a `/32`. The `database_access_advice` prompt carries the access advice.

What it does not protect against: the database user's own privileges (a read sees whatever that user can see, and a write can do whatever that user can do, including `DROP`); any process running as the same user, which can ask the credential store for the password; a server certificate that is not checked when the connection string asks only for encryption; a data server whose control plane is malicious, which chooses the host the password goes to; and a person who allows SQL writes to a model that then follows an instruction hidden in a row. The statement check is a second net and not the boundary: it is a list, and a function it does not name can still run. The database's read only transaction and the database user's privileges are the boundary, so the user a database hands out for this should not hold `pg_read_server_files`, `pg_write_server_files`, `pg_execute_server_program`, the FILE privilege or superuser. Anyone who can edit `connections.json` as the same user can point a saved connection at another host, which is no more than what the same user can already do to the credential store.

### 7.1 Instructions and prompts (Go registry)

The Go registry owns the words it says to an agent besides the tools: the server instructions a client receives when it connects, and the prompts a person starts from a client's prompt or command list. They are text files embedded from `adapter/internal/mcp/guidance`, so the text exists once and is not in Go strings. The plugin's skill and its Cursor rule are generated from them in a later task. The Chat is an in-process client of the registry and reads the instructions through its client: its system prompt is its own role, the instructions for its tier, and how the provider writes an answer. It does not use the prompts.

- **Instructions.** `instructions.md` is a template over the caller's tier, rendered when `Server` builds the server for that caller. It names only tools the tier lists: the section on changes appears at the change tier, the delete sentence at the change and delete tier, and a read caller is told that changes are not allowed in this session. It carries the rules every agent needs: copy ids exactly, never invent numbers, say when monitoring is pending, never show or ask for a password, ask before changing, never choose `0.0.0.0/0`. It stays under 4,096 characters, where Claude Code cuts a server's instructions, and a test holds it there.
- **The nudge.** `nudge.md` is part of the instructions and its own file: offer PDS once when an app needs a database, wait for a yes, leave an existing database alone. The plugin's Cursor rule and the skill's description will be generated from that file. It is sent at the change tier and above, where the agent can create a database, and left out for a read caller.
- **Prompts.** One file per prompt, with front matter (name, title, description, the lowest access tier, arguments) and a template as its text. A prompt is listed only when the caller's tier includes it, and a prompt above the tier is unknown to the SDK, which answers as it does for any name it does not have, so a read caller sees `connect_app` and `investigate_database` and no workflow it could not finish. The SDK lists prompts by name, which is the fixed order. The prompts are `connect_app`, `investigate_database`, `new_database` and `set_up_backups`, and the catalog lists them.
- **Arguments.** Each argument has a check by name: a database id has the shape `list_databases` returns, an engine is `mysql` or `postgresql`, an environment and a database name are Kubernetes names. A value is placed in text the agent follows, so one that fails its check is refused as invalid params, naming the argument and not repeating the value. A missing required argument is refused the same way. An optional argument left out turns its step into a question for the user.
- **Copy.** The customer copy test in `adapter/internal/api` connects at every tier and reads the instructions, each prompt's title, description and argument descriptions, and the text each prompt answers with, filled in and left empty.

## 8. Asynchronous operations

No change route returns an operation id or status URL. Create, backup, restore and delete are accepted and finish later; scale and backup-schedule return a database object that may still show the old values. The activity log is not a completion signal: create is recorded `succeeded` at request time and backup stays `accepted`.

How changes and waiting work:

- Change tools return immediately with an envelope `{accepted, operation, id, status?, requested?, backupId?, schedule?, warnings?, next?}`. `next` names the follow-up call with ready-to-use arguments.
- Two bounded read-only tools do the waiting: `wait_for_database`, `wait_for_backup` and `wait_for_environment`. `timeoutSeconds` defaults to 30 with a hard cap of 45, so one call ends well inside a typical client limit. They poll every 5000 ms, and an abort signal enforces the overall deadline. There is no restore grace window.
- Done means: create is `ready` with `connection` present; scale has every requested value matching and is `ready` (the call takes an `expect` object that mirrors the scale inputs, because the phase can read `ready` before the change starts); backup is `succeeded`; delete is gone (404); a restore is `ready` on the new id. `until` is `ready` or `deleted`.
- A reached `failed` is an observation, not a tool error. Wait tools return a normal result with `outcome` (`succeeded`, `failed`, `in_progress`, `unknown`) plus the last status and message. `isError` is for problems with the call itself.
- A create that cannot place a member or attach a volume never becomes `failed`. It stays in `pending`, `provisioning` or `initializing` while `message` is replaced by the newest blocking Kubernetes event, with no separate flag. The server does not classify it: the outcome stays `in_progress` and the description tells the agent to read the message and ask the user.
- A change call that times out reports the outcome as unknown with the check to run (`get_database`, or `list_backups` for a backup).

## 9. Errors and customer copy

The agent's output reaches a customer, so the banned vocabulary in `AGENTS.md` applies to everything the registry and the bridge say.

- **Error result.** Every failure of a tool is an `isError` result with `{kind, message, retryable, hint?, requestId}`. Kinds: `invalid_input`, `not_found`, `conflict`, `not_available`, `unavailable`, `timeout`, `confirmation_required` and `not_allowed`. The catalog lists when each is raised.
- **Copy.** 400, 403, 404, 409 and 501 carry the API's customer copy; 500, 502 and any other status carry fixed sentences. The adapter's customer copy test keeps its messages clean, so they pass through as they are, and the registry has no banned word guard and no rewrite table.
- **The bridge's own words** are fixed sentences that say what to do and name `pds-mcp login` when only the person can fix it. A copy test over all of them in `apps/mcp/internal/messages` and a contract test over `tools/list` through the built binary hold both to the rules.
- **Request ids.** The registry puts a request id in every error so a customer can quote it to Percona support.

## 10. Safety model

The model is untrusted input. A person trusts it enough to use the tools, not enough to give it unchecked authority. The controls live in the registry and the adapter; the bridge's job is to add none of its own and weaken none, which the contract tests prove through the built binary.

| Risk | Control |
| --- | --- |
| The model sends a crafted id to reach another route | strict id shape check before any URL is built |
| The model deletes or overwrites a database | the caller's access tier lists and runs only what it allows, `confirm` equal to the database name, or for a client that can be asked the name typed by the person, destructive annotations as hints |
| The model opens a database to the internet | `sourceRanges` required, never defaulted, acknowledgement for open ranges, or the person's own tick for a client that can be asked |
| The model reads a database password | no credential tool, no secret in any result or log |
| A token is sent to a wrong host | https required except loopback, the one HTTP client refuses other origins and redirects, issuer and endpoints must be the control plane's |
| A local process or a web page steals the sign in | PKCE, a random `state`, `iss` check, a loopback listener on `127.0.0.1` that checks its `Host` and closes after one answer |
| The login file is read by someone else | mode 0600 in a 0700 directory, no access token on disk, a loose file is tightened |
| The control plane leaks internal names to a customer's agent | the adapter's customer copy test, the copy test and the contract test through the binary |
| A change is made and nobody can tell it was an agent | the session's client kind, and the read tier for a caller that names none |
| A timed-out change runs twice | no retry of timed-out changes, the error says `outcome: unknown` and is not retryable |
| An old setting leaves a password or an opt-in in a client's config | the binary refuses the setting and names `pds-mcp login` |
| The agent's context fills up | short list rows, summary metrics, entry limits |

Annotations are hints only; every control above is enforced in code.

## 11. Facts about the API this design relies on

These are the behaviours the server depends on, not a replacement for reading the handlers: the code is the source of truth.

- Guarded routes accept a `percona_session` cookie (tried first) or `Authorization: Bearer <operator token>`. A 401 is `{"code":"ErrValidation","message":"Sign in to continue."}` with no `WWW-Authenticate` header. Login failures answer `That username and password do not match.` for every reason.
- Every error is `{code, message}`. Codes: `ErrValidation` (400, and also 401 and 405), `ErrNotFound`, `ErrConflict`, `ErrDownstream` (502), `ErrNotImplemented` (501), `ErrInternal` (500). Every unclassified failure becomes a 502, so a 502 does not always mean the platform is down.
- Unknown request fields are ignored, except in the backup-schedule body, which rejects them.
- Database status is one of `pending`, `provisioning`, `initializing`, `ready`, `updating`, `deleting`, `failed`, `restoring`, `suspending`, `suspended`, `resuming`, `unknown`. Backup status is `pending`, `running`, `succeeded`, `failed`, `deleting`, `unknown`. A `ready` database can still have no `connection` yet.
- A scale request can be refused with 409 while a backup is running or while a restored copy is still being restored. Storage can only increase. Members are 1, 3 or 5.
- The activity log records only changes (database create, scale, backup, backup-schedule, restore, delete, and connection changes). It is a ring of at most 500 entries.
- Hub reads can lag the environment by one heartbeat.

## 12. Prerequisites and follow-up work outside this design

- **The adapter's device flow route** (`POST /api/auth/exchange` and the `deviceFlow` object on `GET /api/auth/methods`) is removed, after the first Go release of `@gas-lab/pds-mcp`. Installed TypeScript versions of `@gas-lab/pds-mcp` still sign in with it, so they stop working against a control plane that has this change until they update to the first Go release.
- **The Go release replaces the TypeScript `@gas-lab/pds-mcp` under the same name.** The package is not deprecated and nothing moves to another name. The first Go release needs a version above the newest TypeScript one so `latest` moves forward, and a prerelease goes to `next`.
- **The AI agents page** shows install steps for this program; its sign in snippets switched to the login form here, and the rest of the page is a later task.

## 13. Building it

Follow the repository rules in `AGENTS.md` and `apps/mcp/AGENTS.md`. Tests first. The gates run from `apps/mcp`: gofmt, `go vet`, `golangci-lint run`, `go test ./... -race` and `go build ./cmd/pds-mcp`, plus `node --test npm/release.test.mjs` for the release scripts. Hand verification goes in each pull request body, and trying each client's install snippet lives in the package README.

### 13.0 Where the tools live

The tools are a Go registry in `adapter/internal/mcp`, served in process through the adapter's router. The console Chat is the registry's other client: it calls the tools in process as the signed in person, at the lowest of the Organization's limit and the person's role, with client kind `chat`, and has no tools of its own. A change it makes is put to the person first with the arguments it will run with, and nothing runs without their approval. [architecture.md](architecture.md) describes how the registry is built and how the Chat uses it, and section 7.1 covers its instructions and prompts.

### 13.1 Packaging and release

- **Channel.** A public npm package, `@gas-lab/pds-mcp`, command `pds-mcp`, run with `npx -y @gas-lab/pds-mcp@next` or an exact version. Container image, Node single executable and a Claude Desktop `.mcpb` bundle are out of this release. It targets macOS and Linux; Windows is untested and unsupported.
- **Build.** One Go binary, `cmd/pds-mcp` in the module `github.com/percona/pds/apps/mcp`, built by GoReleaser for macOS and Linux on arm64 and amd64 with `CGO_ENABLED=0`. The npm package `@gas-lab/pds-mcp` is the only package: it carries the four binaries, each in `bin/<os>-<cpu>/pds-mcp`, and has no dependencies, no install script and no `os` or `cpu` restriction. Its launcher picks the folder by `process.platform` and `process.arch` and runs that binary with the arguments, standard streams and exit code passed through. On any other platform the launcher stops with a message naming the platform and the ones that work. An install downloads all four binaries, because one package has to serve every machine and the repository is private, so a download at install time would need credentials.
- **Pinning.** A person may pin an exact version in a snippet. The snippets on the AI agents page follow `next`. The binary's `version` command prints the version it was built with.
- **Own release, own version.** The MCP program is released separately from the control plane, so a UI typo does not force an MCP release and an MCP fix does not force a hub release. A GitHub Release whose tag starts with `mcp-v` (`mcp-v1.2.3`) takes its version from that tag. `publish.yml` does not build it: it sends `mcp-release-published` (tag and version) to Percona's release pipeline, which checks out this repository at the tag and publishes. The two releases stay separate so an npm failure cannot block images and the chart. `charts/pds/compatibility.yaml` is unchanged.
- **Version stamping.** The tag is validated as SemVer and its version reaches the binary (`-X main.version`, through `PDS_MCP_VERSION`), the one package's `package.json`. Prereleases publish under the npm dist-tag `next`; only a stable tag moves `latest`.
- **Supply chain.** Only Percona's release pipeline publishes, and it holds no npm token: npm trusted publishing trusts its workflow file and its `npm-publish` environment, whose required reviewer (self-review off) approves the publish. The package's `repository.url` names the repository that publishes, which npm trusted publishing requires, and npm adds no provenance, because it generates one only for a public repository. The pipeline builds on a clean checkout of the tag, runs the Go gates, builds with GoReleaser, signs the checksum file with keyless cosign (the signer identity is the pipeline's workflow, not one in this repository), attaches the binaries, checksums, signature and certificate to the GitHub Release here, installs the packed package on macOS and Linux, publishes, and ends with `npx` from the registry on both. Bad versions are deprecated, never unpublished. Supporting guards: code owners on `apps/mcp/`, and the release dry run on every pull request.
- **Registry listing.** The official MCP Registry lists one server, `io.github.percona/pds-mcp`: the npm package over stdio and the control plane's `/mcp` as a streamable HTTP remote at `https://{host}/mcp`, with `host` defaulting to `pds.percona.com` because each deployment has its own address. `apps/mcp/server.json` is stamped with the release version, and the release pipeline publishes it after the npm publish with GitHub OIDC and no token. Its `repository` stays `percona/pds`.
- **Mismatch.** The bridge never compares versions with the control plane. The control plane decides what the agent sees, so a newer or older control plane changes the tools and not the bridge.

### 13.2 Testing

The tests drive the program as an outside client would: through its commands and its stdio, against a control plane.

- **Fake control plane.** `internal/fakecp` is the authorization server for the PDS local client (discovery, authorize, token with PKCE verification and refresh rotation, revoke) and a stateless `/mcp` on the Go SDK, as the adapter builds it. It records what it receives (`Authorization`, `User-Agent`, protocol version headers, token requests) and has switches for the failures the tests need: an expired access token, a refused refresh, a 403 with a scope, the MCP surface off, a wrong issuer, a missing `iss`, a denial.
- **Package tests** cover PKCE against the RFC 7636 example, the S256 challenge matching the verifier sent later, the `state` check, the loopback listener (address, `Host` check, closed after the answer), discovery refusals, the one HTTP client refusing another origin and every redirect, the token source (reuse, a stale rejection not refreshing twice, expiry margin, a revoked session), the login file (modes, atomicity, one issuer apiece, a foreign file left alone, serialized updates), the commands, and the removal of each old setting.
- **Bridge tests** run an SDK client against the bridge over pipes: `tools/list` through the bridge equals the control plane's for the same token; a 401 refreshes once and retries once (the control plane sees 401 then 200 and one token request); a refused refresh is a tool error naming `pds-mcp login` and the bridge carries on; a 403 `insufficient_scope` names the scope and starts no browser or refresh; no sign in and a control plane with no `/mcp` are explained; no token appears on stdout, stderr or in a result (sentinels over every token the fake issued); a redirect never carries a token elsewhere; a notification inside a response stream reaches the agent; a request of the 2026-07-28 era and an `initialize` of the older era both pass.
- **Black box tests** (`blackbox`) build the binary and use it as a person and an agent do, through `internal/harness`: `login --no-browser` prints an address and the test plays the browser; the login file is 0600 in a 0700 directory; each removed setting stops the binary and nothing is sent; an old setting stops an agent's session; a revoked session; the MCP surface off; no token anywhere; a non-https address is refused; the version stamp; logout.
- **Contract tests** (`contract`) are the tool contract checks of the TypeScript server, as outside tests of the built binary against a real control plane: the exact tools of each tier, no tool that takes or returns a credential, hostile ids refused as `invalid_input`, an open range needing acknowledgement, a change tool refused at the read tier, no secret in any output, the customer copy rules over `tools/list`, and the agent told to sign in again after logout. The tests sign in the way a person does, with no browser: the user's session opens the authorize address, the consent screen's lookup and approve calls answer it, and the redirect is followed to the command's listener. They skip without `PDS_REAL_URL`, `PDS_REAL_USERNAME` and `PDS_REAL_PASSWORD`. The `mcp` job of the `compose` workflow runs them on every pull request against a hub with no environments, and the `mcp` suite of the nightly `smoke` workflow brings up a lab with `scripts/hub-smoke.sh --lab-only` and runs them against that hub. The deletion check that needs a database (a wrong `confirm`) is the registry's own test in the adapter.
- **Release gate.** The release pipeline runs the Go gates, builds with GoReleaser, packs once and installs that tarball on macOS and Linux before the publish step. The `mcp` workflow runs the same build and pack on every pull request without publishing.

## 14. Why things are out of scope

- **A password or operator token sign in.** The browser flow is the way a person signs in, to the same consent screen as every other agent, and a password in a client's config is the thing this design removes.
- **Settings that choose what the agent may do.** The person chooses on the consent screen and the Organization's limit applies. A setting in a config file is not the person.
- **A tool for the database password.** See sections 7 and 7.0. The local data server fetches it with the passwords grant, into the credential store, and never shows it.
- **`investigate_database` as a tool.** It is a prompt (section 7.1).
- **Ticket attachments, severity and list filters.** They need adapter changes.
- **The join command.** `create_environment` registers the name and discards the join token and command. The user takes the command from the console, so it never passes through an agent's context.
- **A bridge that waits for a sign in.** A stdio session cannot open a browser or block on a person. It fails the call with the words that tell the person what to run.

## 15. Not yet decided

- **Arrangements outside the design:** publish rights on the `@gas-lab` npm scope; who administers the npm org; whether keyless cosign from a private repository exposes the repository identity in the public Rekor log; whether npm trusted publishing works from a private repository.
- **Whether the control plane should read `X-PDS-MCP-Client`** and show it beside the client name. It is unverified and the adapter does not read it yet.
