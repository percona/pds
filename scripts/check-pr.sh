#!/usr/bin/env bash
#
# Fails when a pull request title or body breaks the contract in AGENTS.md.
# The repository squash merges, so the title becomes the commit on main, and
# the body is the only record of how the change was verified.
#
#   PR_TITLE="fix(ui): ..." PR_BODY="$(cat body.md)" scripts/check-pr.sh
#
# Title: a Conventional Commit. One of the types below, an optional scope from
# the list below, a summary that starts in lower case and has no trailing
# period, 100 characters at most. The title GitHub writes for a revert,
# Revert "<original title>", passes as it is.
#
# Body: the sections of .github/pull_request_template.md, each with something
# in it once the template's comments are removed. An issue is named by its key
# alone (#123, or a Jira key for maintainers), never linked: Jira is not public.
set -euo pipefail

command -v python3 >/dev/null || {
  echo "python3 is required to read the pull request" >&2
  exit 2
}

python3 - <<'PY'
import os
import re
import sys

TYPES = ("feat", "fix", "chore", "docs", "test", "refactor", "perf", "ci")
SCOPES = ("ui", "adapter", "mcp", "plugins", "chart", "deploy", "docs")
SECTIONS = ("Summary", "What changed", "Verification", "Not verified")
MAX_TITLE = 100

title = os.environ.get("PR_TITLE", "").strip()
body = os.environ.get("PR_BODY", "")
problems = []

head = re.match(r"^([a-z]+)(?:\(([a-z-]+)\))?!?: (.*)$", title)
if re.match(r'^Revert ".+"$', title):
    pass
elif head is None:
    problems.append(
        "the title is not a Conventional Commit. Write <type>(<scope>): <summary>, "
        "for example fix(adapter): keep unknown spec fields"
    )
else:
    kind, scope, summary = head.groups()
    if kind not in TYPES:
        problems.append(f"the title type {kind!r} is not one of {', '.join(TYPES)}")
    if scope is not None and scope not in SCOPES:
        problems.append(f"the title scope {scope!r} is not one of {', '.join(SCOPES)}")
    if not summary.strip():
        problems.append("the title has no summary after the colon")
    elif summary[0].isupper():
        problems.append("the title summary starts in upper case. Start it in lower case")
    if summary.rstrip().endswith("."):
        problems.append("the title ends with a period")
if len(title) > MAX_TITLE:
    problems.append(f"the title is {len(title)} characters. Keep it to {MAX_TITLE}")

text = re.sub(r"<!--.*?-->", "", body, flags=re.S)
found = {}
current = None
for line in text.splitlines():
    heading = re.match(r"^##\s+(.+?)\s*#*\s*$", line)
    if heading:
        current = heading.group(1).strip().lower()
        found.setdefault(current, [])
        continue
    if current is not None:
        found[current].append(line)

for name in SECTIONS:
    lines = found.get(name.lower())
    if lines is None:
        problems.append(f"the body has no '## {name}' section")
    elif not "".join(lines).strip():
        problems.append(f"the '## {name}' section is empty")

if re.search(r"https?://[^\s)]*atlassian\.net", text):
    problems.append("the body links the issue tracker. Name the issue by its key alone, for example #123")

if problems:
    for problem in problems:
        print(problem)
    print(
        "\nSee 'Pull requests' in AGENTS.md and .github/pull_request_template.md.",
        file=sys.stderr,
    )
    sys.exit(1)

print("pull request title and body follow AGENTS.md")
PY
