# Percona Database Service MCP server

`pds-mcp` lets an AI agent in your editor or chat client use Percona Database
Service (PDS). It runs on your machine and connects the agent to your control
plane, so the agent can list environments and databases, read metrics, wait for a
database to be ready and, if you allow it, create, scale, back up, restore and
delete databases.

You sign in once, in your browser, with `pds-mcp login`. No password or token goes
in your client's settings.

It supports macOS and Linux, on arm64 and x64. Windows is untested and
unsupported. It needs Node 22.23.4 or later 22.x, 24.22.1 or later 24.x, or
26.12.1 or later, only to run `npx`: the server itself is one program that the
package installs for your machine.

## What changed

This Go release replaces the TypeScript `@gas-lab/pds-mcp` under the same name. The TypeScript
versions asked for your username and password in your client's settings, and stop working against a
control plane once it drops that sign in. Update to the first Go release to keep going.

- Sign in with `pds-mcp login`, once, in your browser. Delete `PDS_USERNAME` and
  `PDS_PASSWORD` from your client's settings.
- `PDS_OPERATOR_TOKEN`, `PDS_ORGANIZATION`, `PDS_TEAM`, `PDS_ALLOW_WRITES` and
  `PDS_ALLOW_DESTRUCTIVE` are gone, and the server stops and says so if one is set.
  You choose the Organization, the Team and what the agent may do on the page
  your browser opens, and you can allow less than you asked for.
- The tools are the control plane's own, so a tool can change with the control plane
  and the server holds no tool code. `list_clusters` is `list_environments`.

## Before you start

You need the address of your control plane, for example `https://pds.example.com`,
and a login to its console. Without `PDS_URL`, the server uses
`https://pds.percona.com`.

Releases are tagged `mcp-v` and the version, such as `mcp-v1.2.3`, and are numbered
separately from the control plane. The snippets follow the `next` tag for early
access, so a new release reaches you the next time your client starts the server.
Restart your MCP client to pick up a new release. It can take a few minutes after a
release before it is available. To stay on one build, replace `next` with a version
number, for example `1.2.3`, without the `mcp-v` prefix. Then an update needs a
config edit and a restart. `pds-mcp version` prints the version.

If your client still runs an old build, check that the machine reaches
`registry.npmjs.org`, and that `npm config get prefer-offline` and
`npm config get offline` both print `false`. As a last resort, remove
`~/.npm/_npx` and restart the client.

## Find it in the MCP Registry

PDS is listed in the official MCP Registry as `io.github.percona/pds-mcp`, with
two ways to connect. A client that reads the registry can start the `@gas-lab/pds-mcp`
package on your machine, or connect to the remote server at `https://<host>/mcp`.
The listing fills in `pds.percona.com` as the host. Replace it with the host of
your own control plane.

## Sign in

Run this once in a terminal on the machine where the agent runs:

```bash
PDS_URL=https://pds.example.com npx -y @gas-lab/pds-mcp@next login
```

It opens your browser at the control plane. Sign in to the console if you are not
already, check the app's name, pick the Organization and Team, choose what the agent
may do, and allow it. The command then saves the sign in and exits.

- `--access read`, `--access change` or `--access delete` sets the most the agent may
  ask for. The default is `read`, so an agent can change nothing until you ask. The consent page can allow less.
- `--passwords` and `--sql-write` ask for the two permissions the data server needs, below. The
  consent page offers them only when an Organization admin has allowed them, and the command
  tells you when the sign in did not include one. `--sql-write` includes `--passwords`.
- `--no-browser` prints the address instead of opening it. Open it in a browser on
  the same machine: the answer returns to a port that only that machine listens on,
  so a browser on another computer cannot finish the sign in.

The sign in is kept in `pds-mcp/credentials.json` under `$XDG_CONFIG_HOME` (`~/.config`
when it is not set). The directory is readable only by you, and so is the file. It
holds one refresh token for each control plane, and nothing else: no access token is
ever written to disk. Several agents on the same machine share it.

`pds-mcp logout` removes it and ends the session on the control plane. You can also
revoke an agent on the AI agents page of the console. When a sign in expires or is
revoked, the agent is told to ask you to run `pds-mcp login` again. The server never
opens a browser by itself.

If the control plane has agent access turned off, the server says the control plane
does not serve AI agents.

## Configuration

| Variable          | Meaning                                                                                                                                               |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PDS_URL`         | Address of the control plane. It must use `https`, except for a local address such as `http://localhost:8080`. Without it, `https://pds.percona.com`. |
| `XDG_CONFIG_HOME` | Where the sign in is kept. `~/.config` when it is not set.                                                                                            |
| `SSL_CERT_FILE`   | On Linux, a certificate file for a control plane that uses a private certificate authority. On macOS, add the authority to your keychain.             |

A sign in is for one control plane. The sign in for `PDS_URL` is the one used, and
an agent with another `PDS_URL` needs its own `pds-mcp login`. The server sends the
token only to the address in `PDS_URL`, follows no redirect to another address, and
has no option to skip certificate checks.

## Install

### Claude Code

Run this once. The default scope is local: the server is stored in your own
`~/.claude.json`, for the project directory where you ran the command. To use it
from every directory, add `--scope user`.

```sh
claude mcp add pds \
  --env PDS_URL=https://pds.example.com \
  -- npx -y @gas-lab/pds-mcp@next
```

### Claude Desktop

Open the config file from Settings, Developer, Edit config. On macOS it is
`~/Library/Application Support/Claude/claude_desktop_config.json`. Claude
Desktop for Linux is in beta, on Ubuntu and Debian, and has not been tried with
this server. Add the `mcpServers` section at the top level of the file, next to
the settings that are already there, and keep them. Then quit Claude Desktop
completely and open it again.

```json
{
  "mcpServers": {
    "pds": {
      "command": "npx",
      "args": ["-y", "@gas-lab/pds-mcp@next"],
      "env": {
        "PDS_URL": "https://pds.example.com"
      }
    }
  }
}
```

If the server does not start, and its log says "No such file or directory", Claude
Desktop did not find `npx` on its path. Replace `"npx"` with the full path that `which npx` prints
in your terminal.

### Cursor

Edit `~/.cursor/mcp.json`, which applies to all your projects.

```json
{
  "mcpServers": {
    "pds": {
      "command": "npx",
      "args": ["-y", "@gas-lab/pds-mcp@next"],
      "env": {
        "PDS_URL": "https://pds.example.com"
      }
    }
  }
}
```

### VS Code

Run MCP: Open User Configuration from the command palette and add the server
to the file it opens.

```json
{
  "servers": {
    "pds": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "@gas-lab/pds-mcp@next"],
      "env": {
        "PDS_URL": "https://pds.example.com"
      }
    }
  }
}
```

## Check that it works

Ask the agent to list your databases. A working server answers with your
databases, or with an empty list if you have none.

If the agent is not signed in, its tools answer that you need to run
`pds-mcp login`, and the server says so on stderr when it starts. Read the reason
in the client's MCP server log:

- Claude Desktop on macOS writes `~/Library/Logs/Claude/mcp-server-pds.log`.
- VS Code shows it under MCP: List Servers, Show Output. It labels every line
  from the server as a warning, including the normal start-up lines.

The log never contains a token.

## Tools

The control plane decides which tools the agent sees, by what you allowed when you
signed in: read tools always, change tools when you allowed changes, and
`delete_database` when you allowed deletes. `pds-mcp` adds none of its own and passes
every message through. The tools, their inputs and their safety rules are in the
control plane's registry.

| Group  | Tools                                                                                                                                                                                                                                                                                                                                                   |
| ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Read   | `list_environments`, `get_environment`, `list_cloud_connections`, `get_cloud_connection`, `list_databases`, `get_database`, `list_backups`, `get_metrics`, `list_activity`, `get_activity`, `list_alerts`, `get_database_alerts`, `get_database_history`, `get_database_usage`, `get_environment_history`, `list_support_tickets`, `get_support_ticket` |
| Wait   | `wait_for_database`, `wait_for_backup`, `wait_for_environment`                                                                                                                                                                                                                                                                                          |
| Change | `create_database`, `scale_database`, `backup_database`, `set_backup_schedule`, `restore_database`, `create_environment`, `create_support_ticket`, `reply_to_support_ticket`, `start_cloud_connection`, `create_managed_environment`, `retry_environment_run`, `upgrade_environment`                                                                     |
| Delete | `delete_database`, `delete_managed_environment`, `rebuild_environment`, `remove_cloud_connection`                                                                                                                                                                                                                                                       |

No tool returns a database password.

## Run SQL on your databases

`pds-mcp data` is a second server in the same program. It runs on your machine, connects to your
MySQL and PostgreSQL databases from there, and lets the agent read them and, if you allow it, change
them. Add it beside the first one, with the same settings and `data` after the package name. For
Claude Code:

```sh
claude mcp add pds-data \
  --env PDS_URL=https://pds.example.com \
  -- npx -y @gas-lab/pds-mcp@next data
```

The statements and the rows stay on your machine: the control plane sees one request per
database, for its connection string.

Sign in with the permissions it needs, once:

```bash
PDS_URL=https://pds.example.com npx -y @gas-lab/pds-mcp@next login --passwords
```

Add `--sql-write` to let the agent change data. Both need an Organization admin to allow them first.
If you start the data server without signing in, a client that can show a link asks you to open the
sign in page, and any other client tells you to run the command above. The data server never opens a
browser itself.

| Tool               | What it does                                                                                                                                                   |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `connect_database` | Fetches the password for a database id, keeps it in this computer's credential store and saves how to reach the database. It never creates a database.         |
| `list_connections` | Lists the databases you connected: host, port, user and database name.                                                                                         |
| `run_read_query`   | Runs one `SELECT`, `WITH`, `SHOW`, `EXPLAIN`, `VALUES`, `TABLE` or `DESCRIBE` statement in a read only transaction. At most 1000 rows, 256 KiB and 30 seconds. |
| `run_query`        | Runs one statement that changes data or schema. Listed only while your sign in includes SQL writes.                                                            |
| `get_my_ip`        | Returns this computer's public address as a `/32`, for a database's allowed addresses.                                                                         |
| `write_env`        | Writes host, port, user and database name to a `.env` file in the working directory. It adds the password only when you confirm it.                            |

The `database_access_advice` prompt says which addresses to allow: the same VPC means private
access, a laptop or another host means public access with this computer's `/32`, and never
`0.0.0.0/0`.

What it keeps, and where:

- The password goes in your operating system's credential store: the Keychain on macOS, and a Secret
  Service such as GNOME Keyring or KWallet on Linux. If there is none, the data server saves nothing
  and says so. It never keeps a password in a file.
- How to reach each database (host, port, user, database name, TLS mode) is in `connections.json`
  beside the sign in, readable only by you.
- The password never appears in a tool input, a result, an error or a log. A private database has no
  password held on the control plane, and the control plane's own sentence says so.
- `write_env` creates the file readable only by you, keeps any key that already has a value unless
  told to replace it and says which it left alone. Writing the password or replacing a key needs
  you to confirm in your client, and a client that cannot ask refuses. Do not commit that file.
- `get_my_ip` asks `checkip.amazonaws.com`, over HTTPS and with no proxy, and sends it nothing but
  the request. It gives an answer only when that is one public IPv4 address.

What it protects against, and what it does not:

- A read runs in a transaction the database itself holds read only, on a connection of its own, and
  one statement at a time, so a write, a second statement, `COMMIT` or `SET TRANSACTION READ WRITE`
  is refused by the database. The data server also refuses transaction commands, `SELECT INTO`,
  `INTO OUTFILE`, `LOAD_FILE`, `COPY` and the functions that read files or run SQL held in a string
  before sending them. That list is a second net, not the limit: use a database user that has no
  file or superuser privileges.
- It does not narrow what the database user may read. A read can see every table that user can see,
  and a statement that only reads can still be slow or heavy: the 30 second limit stops it.
- `run_query` accepts any one statement that is not a transaction command, with the privileges of the
  database user, so it can drop a table. It is listed only while your sign in includes SQL writes, and
  the data server checks that grant again, with the control plane, before each write.
- Any program running as you can ask the credential store for the password, as it could for your
  other saved passwords. The data server cannot stop that.
- A connection that asks for TLS and has no certificate to check is encrypted and not authenticated,
  as with `sslmode=require`. A connection string that names `verify-full` or `tls=true` is checked
  against your system's certificate authorities.
