---
name: open-pull-request
description: Open a pull request on percona/pds. Runs the gates for the trees you touched, reviews the branch for security, branches off main, commits, pushes, and opens the PR with gh using the repository template. Use when asked to open, raise, or submit a PR.
---

# Open a pull request

Target repository `percona/pds`, base branch `main`.

Every rule this skill applies lives in the root `AGENTS.md`: branch and title
format, commit rules, the gates, and what each PR body section must hold. Read
it, plus `adapter/AGENTS.md` or `apps/ui/AGENTS.md` for each tree the diff
touches, before running anything below. When this file and `AGENTS.md`
disagree, `AGENTS.md` wins.

## 1. Scope the change

Run `git status` and `git diff --stat`. Pick the scope from the paths, using the
scope list in `AGENTS.md`. If the diff spans unrelated changes, say so and offer
to split it.

## 2. Run the gates

Run the commands under Gates in `AGENTS.md` for every touched tree. Keep the
real output for the PR body. On a red gate, show the failure to the user;
never open a PR on red without saying so.

## 3. Review the branch for security

Run `/security-review` on the branch before anything is pushed, once per pull
request, not per commit. It must cover every change on the branch against
`origin/main`, committed or not. Fix each finding, or keep it and record why.
Stop here until every finding has one or the other; nothing else enforces it.
Re-run the gates a fix touched.

A docs-only diff may skip the review. Say so in the PR body.

Keep the outcome for the Verification section: no findings, or each finding
with what was done about it.

## 4. Branch, commit, push

If you are on `main`, branch first:

```bash
git switch -c <scope>/<short-kebab-summary>
```

Stage only this change and confirm it with `git diff --cached`. Then:

```bash
git commit -m "<type>(<scope>): <summary>"
git push -u origin HEAD
```

## 5. Open the PR

Write the body from `.github/pull_request_template.md`, filling every section
with what was actually run and seen, including the security review's outcome
under Verification, then:

```bash
gh pr create --base main --title "<type>(<scope>): <summary>" --body-file <body.md>
```

The `pr` workflow checks the title and the body. Fix them with `gh pr edit`
rather than a new push.

## 6. Report back

Give the PR URL and a one-line summary of the gate results.

## Guardrails

- Pushing and opening a PR are outward facing. If the user asked only for a
  commit, stop at the commit and ask.
- If the branch already has an open PR, update it instead of opening another.
- If `gh auth status` fails, ask the user to run `gh auth login`.
