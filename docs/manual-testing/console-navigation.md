# Manual testing: console navigation

The shell around every signed-in page. Covers the collapsible sidebar, the Organization group, the home mark, Ask PDS, and the phone drawer.

## Setup

```bash
npm install
npm -w @pds/ui run dev
```

Open the console (http://localhost:5173/app/ once the dev server is up) and sign in. Automated gates:

```bash
npm -w @pds/ui run lint
npm -w @pds/ui run typecheck
npm -w @pds/ui test
npm -w @pds/ui run build
```

## Actions and expected results

| Action | Expected result |
|--------|-----------------|
| Load the console | Sidebar is 256px. The Percona mark and "PDS" are a link to Home. Navigation is Environments, Alerts, AI agents, Activity, and Support when each is available. Chats is not in the sidebar. Ask PDS floats at the bottom right. |
| Click the mark or PDS | The address goes to Home, which opens Environments. |
| Click Collapse sidebar | The rail becomes 64px. Names are tooltips on hover and on keyboard focus. The current page stays highlighted. A firing critical count stays on Alerts as an icon plus a number. Reload: the rail is still collapsed. |
| Click Expand sidebar | The sidebar returns to 256px and the next visit starts expanded. |
| Sign in as an admin | Organization is closed. Open it with the chevron: Teams, Users, Settings. Collapse the rail and open Organization: the same three links sit in a menu beside the icon. |
| Sign in as a user | Organization is absent. Teams, Users, and Settings are not linked. |
| Narrow the window to 390px | The rail is gone. Open menu shows the full sidebar over a dimmed page. Escape, Close menu, or the dimmed page dismisses it and returns focus to Open menu. The page does not scroll sideways. |
| Click Ask PDS | The chat panel opens over the current page. |
| Switch theme, then repeat the collapsed rail and the phone drawer | Light and dark use the same layout. Purple stays the only accent. |

Home is the checklist until a database exists, then the counts, what needs attention, and recent activity. An environment row is a summary. Monitoring, support access, updates, and disconnect or delete are tabs on the environment. Connectors is its own page. Users leads with pending invites, and Invite people is a dialog. Activity names invitations and support access.

## Left for a later change

Brand marks for MySQL, PostgreSQL, Anthropic, Claude, Cursor, ServiceNow, and MCP stay on the generic icons. The design lock allows only Lucide icons, and each brand's usage rules have to be checked before a mark can ship. A price before creating an environment, and moving an environment to another team, are not available from the control plane.
