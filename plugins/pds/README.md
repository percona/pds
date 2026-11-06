# pds, the Percona Database Service plugin

Lets Claude Code and Cursor create, connect to and look after MySQL and PostgreSQL databases on Percona Database Service (PDS). Asked to build an app that needs a database, the agent offers PDS once and waits for a yes.

## What it adds

| Part                  | What it is                                                                                                                                                                                                                                 |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `pds` MCP server      | The control plane's `/mcp` address. Tools and workflows act as the person who signs in, in their Team.                                                                                                                                     |
| `pds-data` MCP server | Runs from the `@gas-lab/pds-mcp` npm package on this computer. Connects to a database, saves its password in the operating system credential store and runs queries from here. Needs the Keychain on macOS or the Secret Service on Linux. |
| `databases` skill     | What the agent knows about PDS: how to look before it answers, what to ask before a change, and how to sign in.                                                                                                                            |
| `use-pds` rule        | Cursor only. Always on, and offers PDS once when an app needs a database. Claude Code has no rules, so the skill's description carries the same offer.                                                                                     |

The workflows (connect an app, new database, set up backups, investigate a database) are MCP prompts from the `pds` server. Start one from the client's prompt list.

## Install in Claude Code

```bash
claude plugin marketplace add percona/pds
claude plugin install pds@percona-pds
```

The repository has to be readable by the git credentials on the computer. The plugin reads `PDS_URL` and uses `https://pds.percona.com` when it is not set. Start Claude Code with your own control plane's address when you run one:

```bash
PDS_URL=https://pds.example.com claude
```

Open `/mcp`, choose `plugin:pds:pds` and sign in in the browser.

## Install in Cursor

Add the `percona-pds` marketplace from this repository in Customize, then install `pds`. Cursor has no default form for the address, so the plugin points at `https://pds.percona.com/mcp`. On your own control plane, add its `/mcp` address as a remote MCP server by hand.

## Updates

The plugin's version is the version of the `@gas-lab/pds-mcp` release its data server runs, so the two move together. Each stable release opens a pull request that moves both, and a person merges it.

- Claude Code keeps the version you have until the plugin's version changes, and does not update third-party marketplaces on its own by default. Run `claude plugin marketplace update percona-pds`, then `claude plugin update pds@percona-pds`, and restart. Turn on auto-update for the marketplace in `/plugin` to take new versions at startup.
- Cursor lists the plugin through its Marketplace, which reviews each new version by hand before people see it. A new version can reach Cursor later than it reaches Claude Code.

A prerelease of the data server never changes the plugin.

## Sign in to the data server

The first time `pds-data` needs a database, a client that can show a sign-in page opens it. Otherwise the tool says so. Sign in from a terminal, then try again:

```bash
npx -y @gas-lab/pds-mcp login --passwords
```

Saving a database password on this computer needs your Organization to allow it and you to agree when you sign in. Queries that change data need your Organization to allow it and `npx -y @gas-lab/pds-mcp login --sql-write`. Without both, `run_query` is not offered.

## Try a change before you install it

```bash
claude --plugin-dir plugins/pds
```

## Files

`skills/databases/SKILL.md` and `rules/use-pds.mdc` are generated from the control plane's own instructions and are never edited by hand. The rules for changing this directory are in [plugins/AGENTS.md](https://github.com/percona/pds/blob/main/plugins/AGENTS.md).
