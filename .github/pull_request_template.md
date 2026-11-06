<!--
Title: a Conventional Commit, for example fix(adapter): keep unknown spec fields.
The repository squash merges, so the title is the commit that lands on main.
Fill every section. HTML comments like this one are ignored by the pr check.
-->

## Summary

<!-- The problem and the fix in two or three sentences. Name the issue by its key alone: #123 for a GitHub issue, or BC-123 for a maintainer's Jira issue. Never link Jira. -->

## What changed

<!-- One bullet per meaningful change, in the order a reviewer should read them. -->

## Verification

<!--
How to check this by hand, then what you actually ran.

1. Setup: the cluster, flags or data the check needs.
2. Steps: exact commands or clicks.
3. Expected: what a pass looks like.

Then the evidence: each gate you ran with its result, and the manual run with
its date and what you observed. Then the security review: no findings, each
finding with its outcome, or skipped for a docs-only diff. UI changes attach
light and dark screenshots.
Never write a result you did not see.
-->

## Not verified

<!-- What you could not check, and why. Write "Nothing" only when that is true. -->

## Notes

<!-- Optional. Risk, migration steps, follow-ups left out on purpose, deviations from the issue. -->
