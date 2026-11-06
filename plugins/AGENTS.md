# Plugin rules (`plugins/`)

Read the root [AGENTS.md](../AGENTS.md) first: the workflow, the gates and the
customer copy rules live there. This file covers `plugins/pds`, the plugin for
Cursor and Claude Code, and the two marketplace files at the repository root.
What a person sees is in [plugins/pds/README.md](pds/README.md).

## Layout

| Path                                                                   | Holds                                                                                       |
| ---------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `plugins/pds/.claude-plugin/plugin.json`, `.cursor-plugin/plugin.json` | One manifest per client. The same keys and the same version                                 |
| `plugins/pds/.mcp.json`, `mcp.json`                                    | The MCP servers, one file per client: the `pds` remote entry and the `pds-data` local entry |
| `plugins/pds/skills/databases/SKILL.md`                                | The skill. Generated                                                                        |
| `plugins/pds/rules/use-pds.mdc`                                        | The Cursor rule. Generated                                                                  |
| `.claude-plugin/marketplace.json`, `.cursor-plugin/marketplace.json`   | One marketplace file per client, listing the plugin                                         |
| `plugins/check.mjs`, `check.test.mjs`                                  | The checks CI runs, and their tests                                                         |
| `plugins/release.mjs`, `release.test.mjs`                              | Moves the plugin to an MCP server release, and checks that its pinned version is on npm     |
| `adapter/cmd/plugin-guidance`                                          | The generator, with the words only the plugin says                                          |

## Rules

- **The words live once, in the registry.** The skill and the rule come from
  `adapter/internal/mcp/guidance`. Change the text there, run
  `cd adapter && go run ./cmd/plugin-guidance ../plugins/pds`, and commit the
  result. The words only the plugin says (signing in, the data server) are in
  the generator's templates. Nobody edits a generated file.
- **One plugin, no commands.** The workflows are the registry's MCP prompts.
  Never add a `commands/` folder or a second skill for a workflow.
- **The version moves with the directory.** Any change under `plugins/pds`,
  a generated file included, bumps `version` in both manifests to the same new
  value. A client keeps the plugin it has until that string changes.
- **The data server is at an exact version** in both MCP files: `npx -y
@gas-lab/pds-mcp@<version> data`, the same in both, with no `env` and no
  setting to fill in. Never a range or a tag.
- **Each client keeps its own spelling.** Claude Code needs `type: http`, the
  `${PDS_URL:-https://pds.percona.com}` default and a `./` in the marketplace
  source. Cursor has no default form, so its remote entry is the fixed
  address, and its marketplace entry holds `name`, `source` and `description`
  only.
- **A git install copies only `plugins/pds`.** Nothing in it refers to a path
  outside, and a symbolic link stays inside.
- **Every string is customer copy**, including the manifests and this
  directory's README.
- **No secrets, no `node_modules`, no logo that is not Percona's.**

## Release

The plugin ships with each stable `@gas-lab/pds-mcp` release, so the plugin and the data server it runs
never drift. The `plugin` job of `.github/workflows/publish-mcp.yml` runs after `verify`:

1. `node plugins/release.mjs stamp mcp-v<version>` waits until npm serves
   `@gas-lab/pds-mcp@<version>`, then writes that version into both manifests and that exact
   `@gas-lab/pds-mcp@<version>` into both MCP files, and runs the plugin checks on the result. It uses
   `versionFromTag` and `distTag` from `apps/mcp/npm/release.mjs`.
2. It opens a pull request `plugins/release-<version>` against the default branch with the built-in
   token and starts the `plugins` workflow on that branch, because a pull request opened with the built-in
   token starts no `pull_request` workflow. Nothing merges it but a person, and nothing pushes to `main`.

A prerelease (`mcp-v1.2.3-rc.1`) changes nothing: a client keeps the plugin until its version changes, and the
stable plugin must not point at a build npm serves only under `next`. A release older than the plugin's
version fails the job: the plugin is ahead because its own files changed, so cut a higher release.
Re-running the job after the branch exists does nothing; close the pull request and delete the branch first.

The `plugins` workflow also runs `node plugins/release.mjs published <base>` on a pull request, which fails
when the pinned version differs from the base's and npm does not serve it. A rename of the package at the same version does not count as a change.

Once, by a person:

- Settings, Actions, General: allow GitHub Actions to create pull requests.
- Submit the plugin to the Cursor Marketplace at cursor.com/marketplace/publish, and record the date and the
  review result in the pull request. Cursor reviews each new version by hand and does not update from this
  repository, so submit a new version after a release PR merges. That review never blocks an npm release.
- Claude Code people add `/plugin marketplace add percona/pds`, or `claude plugin marketplace add percona/pds`;
  the marketplace is `percona-pds` and the plugin `pds@percona-pds`.

## Gates

```bash
node --test plugins/check.test.mjs plugins/release.test.mjs
node plugins/check.mjs --base origin/main
(cd adapter && go test ./cmd/plugin-guidance ./internal/mcp -race)
claude plugin validate plugins/pds && claude plugin validate .
```

`check.mjs` checks both manifests, both marketplace files, the MCP files, the
skill against the Agent Skills limits (a name of 64 characters or fewer that
equals its folder, a description of 1024 or fewer, a body under 500 lines),
the rule, paths that leave the plugin, the copy rules, and with `--base` the
version bump. The generator's test fails when a generated file differs from the
registry, in the adapter's CI as well as in `plugins`.
