# Contributing to Percona Database Service

Thank you for helping. Percona Database Service is licensed under the Apache
License 2.0, and contributions are accepted under the same license.

## Before you start

- [README.md](README.md) explains what the project is and how to run it,
  including a development loop that runs the control plane as a hub with no
  Kubernetes. Database screens need the lab.
- [AGENTS.md](AGENTS.md) is the rulebook every change follows, whether a
  person or a coding agent writes it: the workflow, the pull request contract,
  the quality gates, and the code, copy and design rules.
- For a bug or a feature, open a GitHub issue first so the problem is agreed
  before the code.

## Set up

```bash
npm install
lefthook install
```

[lefthook](https://lefthook.dev) is installed separately (for example
`brew install lefthook`). `lefthook install` adds the pre-commit hook that
formats and lints staged Go and TypeScript files.

## Sign the CLA

Everyone who contributes, Percona staff included, signs the Percona
Contributor License Agreement once. The CLA bot comments on your first pull
request with a link; sign there and the check turns green for this and every
later pull request. Commits need no sign-off trailer.

Commit with an email address that is added to your GitHub account, so the bot
can match your commits to the signature.

## Open a pull request

Fork the repository, branch from `main`, and open the pull request against
`main`. The template asks for a summary, what changed, how you verified it,
and what you could not verify; the `pr` check fails while a section is empty.
AGENTS.md has the title format and what counts as verification.

## Conduct

Participation follows the [Code of Conduct](CODE_OF_CONDUCT.md).
