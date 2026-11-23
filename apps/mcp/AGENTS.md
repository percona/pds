# MCP server rules (`apps/mcp/`)

Read the root [AGENTS.md](../../AGENTS.md) first: the workflow, the gates and
the customer copy rules live there. This file covers the Go module in `apps/mcp/` and its npm release.

## What it is

`pds-mcp` is the program that connects an AI agent to the control plane. It is one Go binary, shipped
on npm as `@gas-lab/pds-mcp`, and it does two things:

- `pds-mcp login` and `pds-mcp logout` sign a person in to the control plane in their browser (OAuth
  authorization code flow with PKCE, a loopback redirect, the built in client identity) and keep one
  refresh token for each control plane.
- `pds-mcp` with no command is a stdio bridge to the control plane's `/mcp` (streamable HTTP). It
  forwards every message in both directions, signs each request with a bearer token it refreshes, and
  holds no tool code: the tools, their inputs and their safety rules are the adapter's registry. The
  bridge adds no tool of its own.
- `pds-mcp data` is a second stdio server, the local data server: it runs SQL on the person's own
  computer against MySQL, PostgreSQL and MongoDB, keeps database passwords in the operating system credential
  store, and makes one control plane request per database, for its connection string. Section 7.0 of
  the design doc is its contract.

It imports nothing from `adapter/`. The adapter has the client identity built in as
`oauth.LocalClientID`, and `internal/oauth` repeats the string; the contract tests fail on a
mismatch.

## Where the design is

[README.md](README.md) is the install guide for design partners and ships in the package. It
follows the customer copy rules, and a snippet in it is tried on its real client before it
changes.

Build from these, and do not restate them here or in code comments:

- [docs/mcp-server-design.md](../../docs/mcp-server-design.md) is the consolidated design:
  configuration, sign in, the bridge, safety, packaging (13.1) and testing (13.2).
- [docs/mcp-server-tool-catalog.md](../../docs/mcp-server-tool-catalog.md) holds the tools the
  registry serves.

## Structure

The module is `github.com/percona/pds/apps/mcp`, listed in `go.work`. `.golangci.yml` at the root
lints it. Everything but `cmd/pds-mcp` is under `internal/`.

| Package                                                                                                   | Holds                                                                                                                                                                             |
| --------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `cmd/pds-mcp`                                                                                             | `main`: the version stamp, the browser opener, and nothing else                                                                                                                   |
| `internal/cli`                                                                                            | The commands. `commands` is the table the data server adds its own to. `Run` takes an `Env` (streams, environment, version), so a test runs a command with nothing real behind it |
| `internal/config`                                                                                         | `PDS_URL`, and the refusal of the settings earlier versions read                                                                                                                  |
| `internal/oauth`                                                                                          | The OAuth client: discovery, PKCE, the loopback login, the token source with refresh and rotation, revocation, and the one HTTP client that may carry a token                     |
| `internal/tokenstore`                                                                                     | The login file: one refresh token for each issuer, mode 0600 in a 0700 directory, written atomically under a file lock                                                            |
| `internal/bridge`                                                                                         | The stdio to `/mcp` bridge on the SDK's stdio and streamable HTTP transports                                                                                                      |
| `internal/data`                                                                                           | The data server: its tools, the sign in it asks the client for, the credential fetch, the `.env` and public address tools                                                         |
| `internal/sqlguard`, `internal/sqlrun`                                                                    | The statement check, and the runner: read only transactions the engine enforces, row, byte and time limits, TLS                                                                   |
| `internal/mongoguard`, `internal/mongorun`                                                                | The MongoDB command check, a deny by default list that alone separates the read tool from a write, and its runner: `maxTimeMS` on every command, row and byte limits              |
| `internal/vault`, `internal/connections`, `internal/envfile`, `internal/publicip`, `internal/privatefile` | The credential store seam, the saved connections (no password), the `.env` writer, the public address lookup, and the private file writes the login file shares                   |
| `internal/messages`                                                                                       | Every fixed sentence a person or an agent reads, and the copy test over them                                                                                                      |
| `internal/fakecp`, `internal/harness`                                                                     | Test support: a control plane for tests, and a driver for the built binary                                                                                                        |
| `blackbox`, `contract`                                                                                    | Tests that run the built binary from outside                                                                                                                                      |

`var version` in `cmd/pds-mcp/main.go` holds the placeholder `0.0.0-dev`. The release sets it with
`-X main.version=<version>`, which `.goreleaser.yaml` reads from `PDS_MCP_VERSION`.

## Code rules

- Standard output carries protocol messages when the bridge runs, and nothing else. Every
  diagnostic goes to standard error, through `log/slog` or an `io.Writer` the caller passes.
- A refresh token, an access token or a password never reaches a log line, an error, standard
  output or a tool result. Errors built from a token endpoint answer carry its status and never its
  body, and `bridge.describe` turns any failure into a fixed sentence before an agent sees it.
- A token goes only to the control plane's origin. `oauth.NewHTTPClient` is the only client that
  sends one: it refuses another origin before sending and follows no redirect. Discovery refuses
  metadata whose issuer or endpoints are elsewhere.
- No program started by a stdio session opens a browser. `login` does, because a person ran it.
  A refused refresh or a 403 `insufficient_scope` is a tool error that names `pds-mcp login`.
- `PDS_USERNAME`, `PDS_PASSWORD`, `PDS_OPERATOR_TOKEN`, `PDS_ORGANIZATION`, `PDS_TEAM`,
  `PDS_ALLOW_WRITES` and `PDS_ALLOW_DESTRUCTIVE` stop the binary with a message that names the
  setting and `pds-mcp login`, and never repeat the value. Do not read them for anything else.
- A sentence a person or an agent can read is in `internal/messages` and follows the customer copy
  rules in the root AGENTS.md. Say what to do, and name `pds-mcp login` when the person must act.
- A database password lives in `internal/vault` and in memory for one statement. It never reaches a
  tool input, a result, an error, a log, `connections.json` or an environment variable the program
  sets, and a test with a sentinel password searches every output for it. There is no file fallback
  for it: a computer with no credential store gets a sentence and nothing is saved.
- A tool of the data server returns only `toolError` values, which are fixed sentences, or an
  engine's own message, which `sqlrun` has cleaned. Never return an `err` from a driver.
- A MongoDB command goes through `mongoguard.Check` and `mongorun.Run` and nothing else, and what is sent is the
  checked tree written out again, never the text the agent typed. A new command, field or aggregation stage
  is added to the lists in `mongoguard` with a test that the read tool refuses it or runs it, and every
  bypass a test tries stays in the test file.
- `run_query` is registered only while the saved sign in carries `pds:sql_write`, and its handler
  checks the grant again. A read goes through `sqlguard.Check` and `sqlrun.Read` and nothing else.
- macOS and Linux only: the token store uses `flock`.

## Tests

Write the failing test first, and test what a caller sees: a tool result, a line on standard error,
a request the control plane received, a file on disk. Never assert on internal structure.

- **Package tests** sit next to the code and use `internal/fakecp`, which serves the authorization
  server for the PDS local client and a stateless `/mcp` the way the adapter does, and records what
  it received. `internal/bridge` tests run the bridge between an SDK client and the fake over pipes.
- **Black box tests** (`blackbox`) build the binary, run its commands and talk to it over stdio
  through `internal/harness`, against the fake. They hold what the epic's security decisions need:
  the removed settings, a private login file, no token on any output, the version stamp.
- **Data server tests** need a real MySQL and a real PostgreSQL for what only an engine can prove: that a
  read only transaction refuses a write, a second statement and a switch to read write. They read
  `PDS_TEST_MYSQL_ADDR` and `PDS_TEST_POSTGRES_ADDR` (`host:port`) with
  `PDS_TEST_MYSQL_PASSWORD` and `PDS_TEST_POSTGRES_PASSWORD` for the `root` and `postgres` users and a
  database named `app`, and skip only when the one they need is not set. The MongoDB tests read
  `PDS_TEST_MONGODB_ADDR` and `PDS_TEST_MONGODB_PASSWORD` for the `root` user of the `admin` database. The
  `mcp` workflow starts all three as services. Everything else uses the fake runner and `vault.Memory`, because the test machine's own
  keychain is never touched.
- **Contract tests** (`contract`) run the built bridge against a real control plane: they sign in
  through the authorize and consent routes as a user whose password is known, then check what every
  agent relies on: the tools of each tier, no credential tool, hostile ids, open range
  acknowledgement, no secret in any output, and the customer copy rules over `tools/list`. They skip
  without `PDS_REAL_URL`, `PDS_REAL_USERNAME` and `PDS_REAL_PASSWORD`. The `mcp` job of the `compose`
  workflow runs them per pull request against a hub with no environments; the nightly `smoke`
  workflow runs them against the lab hub. The safety rules themselves live in the adapter registry
  and its tests; these only prove the bridge passes them through unchanged.
- Time is never faked with sleeps: a test waits on a condition or a channel.
- One fake control plane serves every test. Do not add a second one or an HTTP interceptor library.

## Evaluating the AI tools

`modelcheck/` is a script a person runs before an MCP or plugin release, against a lab or test control
plane, with real models. It is not part of CI and no workflow runs it. `node modelcheck/modelcheck.mjs --help`
is its usage text, and `--list` prints the cases without a control plane.

- `cases.json` is the case table. A `contract` case runs this module's contract tests. A `chat` case drives the
  console Chat over `POST /api/chat` with Anthropic and with Cursor, answers each approval card through
  `POST /api/chat/approvals/{id}` as the case says, and scores the stream. A `manual` case is steps and an
  expectation for what a person has to judge: a GUI client, the plugin's offer, how streaming feels.
- It takes the control plane only from `--url`, refuses the production address without `--production`,
  reads the sign in from `PDS_REAL_USERNAME` and `PDS_REAL_PASSWORD`, and reads no model key: the Organization's
  Anthropic and Cursor connections are the ones set up in the console. It writes neither to a report nor
  to a transcript, and scans every transcript for them and for key, token and connection string patterns.
- Every case that spends money or changes data says so before it runs, and asks. The script never approves a
  delete: that is a manual case on a scratch database. It puts back what it changed (the Organization's agent
  access, the scratch database's size) and lists in the report what it could not.
- The pure parts are in `lib.mjs` and tested by `node --test modelcheck/modelcheck.test.mjs` in the `mcp` workflow.
  A new case is a new entry in `cases.json`, checked by `validateCases`. A new kind of check needs a test first.
- The report is Markdown for the release pull request. Do not commit the report or the transcripts.

## Publishing

A GitHub Release tagged `mcp-v1.2.3` is published by Percona's release pipeline, not by a workflow
here: `publish.yml` sends it the tag and the version, and it checks out this repository at the tag.
The control plane's `v1.2.3` releases do not start it. A tag with a prerelease part
(`mcp-v1.2.3-rc.1`) goes to the `next` dist-tag and never moves `latest`. Bad versions are
deprecated, never unpublished.

What the pipeline runs lives here: `.goreleaser.yaml`, the scripts in `npm/` (`release.mjs`,
`pack.sh`, `check-install.sh`, `install-publisher.sh`), `server.json` and the package's
`package.json`. The manifest's `repository.url` names the repository that publishes, because npm
trusted publishing accepts a publish only from it. GoReleaser does not read the `mcp-v` prefix as a
version, so the pipeline sets `GORELEASER_CURRENT_TAG=v<version>` and passes `--skip=validate`, and it
installs cosign 2, because the signing flags in `.goreleaser.yaml` are the cosign 2 ones.

The listing is `apps/mcp/server.json`: one entry with the `@gas-lab/pds-mcp` package (stdio) and a
remote at `https://{host}/mcp` (streamable HTTP), whose `host` variable defaults to
`pds.percona.com` because every deployment has its own address. Its name and the package's
`mcpName` must be the same string, and the package it names must be the one the release publishes: the release
fails when either differs. `npm/server.schema.json` is the registry's schema at the version
`server.json` names, kept in the repository so the tests need no network. To move to a newer schema,
replace that file and the `$schema` line, run `mcp-publisher validate apps/mcp/server.json`, and
rerun the tests. The committed version, `0.0.0-dev`, is a placeholder like the one in the package's
`package.json`. The registry has no publish dry run: the `validate` command is the check, and the
`mcp` workflow runs it in `release-dry-run` on every pull request, against the public registry's
validation service.

The `mcp` workflow runs the same build and pack without signing or publishing on every pull request
(`release-dry-run`), on macOS and Linux. To run it locally, install GoReleaser v2.12.0 and:

```bash
cd apps/mcp
PDS_MCP_VERSION=1.2.3-rc.1 goreleaser release --snapshot --clean --skip=sign
npm/pack.sh mcp-v1.2.3-rc.1 dist/npm-tarballs
npm/check-install.sh dist/npm-tarballs 1.2.3-rc.1
```

## Gates

Run these from the repository root:

```bash
cd apps/mcp && test -z "$(gofmt -l .)" && go vet ./... && golangci-lint run && go test ./... -race && go build ./cmd/pds-mcp
node --test apps/mcp/npm/release.test.mjs
node --test apps/mcp/modelcheck/modelcheck.test.mjs
```

To run the contract tests against a control plane of your own, see
[docs/mcp-server-design.md](../../docs/mcp-server-design.md) section 13.2.
