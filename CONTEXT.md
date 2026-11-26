# Percona Database Service

A customer-hosted control plane for MySQL and PostgreSQL on Kubernetes. People and programs reach it through its HTTP API.

## Language

**Organization**:
A customer company, such as Acme. It holds teams and is the boundary no data crosses. A control plane holds one organization or many.
_Avoid_: Tenant, account, customer (unqualified)

**Team**:
A group inside an organization, such as Payments or Analytics. It owns environments, and the databases on them. Every user belongs to exactly one team.
_Avoid_: Group, project, workspace

**User**:
An individual person who signs in and acts on their team's environments and databases. A user belongs to exactly one team of one organization.
_Avoid_: Tenant, account, member

**Organization admin**:
A user who sees every team in their organization, creates teams and invites users. No other role exists yet.
_Avoid_: Admin (unqualified), owner, operator

**Environment**:
A Kubernetes cluster connected to the control plane, where a team's databases run. Code, the API and storage call it a cluster.
_Avoid_: cluster in customer copy, spoke, connected cluster

**Connected environment**:
An environment the customer brings up themselves by running the Connect command with their own cloud credentials.
_Avoid_: Spoke, self-managed environment

**Managed environment**:
An environment the control plane brings up and keeps up in the customer's own cloud, acting through a cloud connection. It ends up running the same things as a connected environment.
_Avoid_: BYOC cluster, hosted environment, push cluster

**Cloud connection**:
A team's standing permission for the control plane to act in one of the customer's cloud accounts, which the customer grants and can revoke. A managed environment is created through one.
_Avoid_: Cloud account, account, credentials, integration

**Allowed regions**:
The AWS regions a cloud connection lets the control plane act in, either a chosen list or all regions. Never empty.
_Avoid_: Region limit, region scope

**Provisioning run**:
One ordered attempt to create, upgrade, rebuild or delete a managed environment, kept as steps the control plane can resume.
_Avoid_: Job, task, workflow

**AWS refusal**:
A step of a provisioning run that AWS declines to carry out because of a quota, a missing permission or a region that is not enabled. It is not a revoked cloud connection: the control plane could still act in the account.
_Avoid_: AWS error, failure, revocation

**Access window**:
The short period in which a managed environment's Kubernetes API is reachable from the control plane's declared outgoing IP addresses. It opens for a provisioning run and closes when the run ends.
_Avoid_: Setup window, install window

**Cluster agent**:
The `pds-agent` process on an environment that reports to the hub and carries out its jobs.
_Avoid_: Agent (unqualified), spoke

**AI agent**:
An automated caller driven by a language model that uses the control plane through a tool interface.
_Avoid_: Agent (unqualified), bot, assistant

**MCP server**:
The program a customer runs on their own machine that lets an AI agent use the control plane on behalf of a user. Customer copy calls the page "AI agents" and the program "the PDS MCP server".
_Avoid_: plugin, connector, agent (unqualified)

**Assistant**:
The in-product chat that answers a user's questions about their databases.
_Avoid_: Agent, AI agent, copilot

**Hub operator token**:
A bearer secret held by whoever runs the hub. It lets a script call the API with no session and is recorded as the subject `operator`, never as a user.
_Avoid_: API key, operator (unqualified; the Kubernetes operator is a different thing)

**Hub mode**:
A deployment where one control plane manages databases on several environments. A database id then names its cluster, and databases are created on a chosen environment.
_Avoid_: Multi-cluster mode, spoke mode

**Single-cluster mode**:
A deployment where the control plane manages databases only on the cluster it runs on.
_Avoid_: Standalone mode

**Support access**:
A time-boxed window that an admin opens on one environment so a Percona support engineer can reach it over a private network. It belongs to the environment, not the Team, and records the ServiceNow case it was opened for.
_Avoid_: Remote access, VPN, break-glass, shadow access

**Access level**:
What a support engineer may do on the environment during a support access: read only, or admin. The admin who opens the window chooses it, and read only is the default. Read only lets the engineer look at the environment. Admin lets them change it.
_Avoid_: Role (a role belongs to a user in a Team), permission level

**Support engineer**:
A Percona employee who signs in with their Percona identity to use a support access.
_Avoid_: Operator (the Kubernetes operator and the hub operator token are different things), admin

**Tailnet**:
The private Tailscale network Percona runs. An environment joins it only while a support access is open, and a support engineer reaches the environment through it. Percona owns it and a customer needs no account on it.
_Avoid_: VPN, mesh, support network

**Support address**:
The private address of one environment on the Tailnet, where a support engineer reaches it during a support access. It is created the first time a support access opens on the environment, kept until the environment is removed, and never given to another environment.
_Avoid_: Service (a Kubernetes Service is a different thing), hostname, endpoint

**Client kind**:
The label a caller declares when it signs in to say what kind of program it is, such as `mcp`. It is recorded with each logged action. Absent means the adapter does not know.
_Avoid_: User agent, source, actor (the actor is the user, not the program)

**System actor**:
The recorded actor of a logged action that no user took, shown as `system`. A support engineer's own requests are not logged actions: they appear only in the customer's Kubernetes audit log. The system actor is never a user and has no user record.
_Avoid_: Bot, service account, operator
