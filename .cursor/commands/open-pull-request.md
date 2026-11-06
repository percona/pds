# Open a pull request

Read and follow `.claude/skills/open-pull-request/SKILL.md`. The rules it
applies (branch and title format, commits, gates, the PR body) live in the
root `AGENTS.md`, which wins over anything else.

1. Read root `AGENTS.md`, plus `adapter/AGENTS.md` or `apps/ui/AGENTS.md` for
   the trees you touched.
2. Run the gates `AGENTS.md` lists for those trees. Report real output.
3. Review the branch for security before anything is pushed: read
   `git diff origin/main...HEAD` (plus any uncommitted changes) for injection,
   authorization gaps, secrets in logs, errors or responses, and unsafe shell
   or Kubernetes calls. Fix each finding, or record why it stays, and stop
   until every finding has one or the other. A docs-only diff may skip this,
   and the PR body says so.
4. Branch off `main` as `<scope>/<short-kebab-summary>`, commit,
   and push.
5. Open the PR with `gh pr create --base main`, filling every section of
   `.github/pull_request_template.md` with what was actually run and seen,
   including the security review's outcome under Verification.
6. Report the PR URL and the gate results.

Pushing a branch and opening a PR is outward facing. If the user asked only
for a commit, stop at the commit and ask.
