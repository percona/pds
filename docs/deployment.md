# Deployment

Every way to install Percona Database Service (PDS), what each one needs, and
the procedures that go with it. The [README](../README.md) has the short form
of each path. [Architecture](architecture.md) explains what these commands
install and how the pieces talk.

A self-hosted hub is a customer install: the chart on Kubernetes, or Compose
on one host. A connected environment joins whichever hub the customer uses.

| You want | Use |
|----------|-----|
| The lab on a laptop: a hub and one connected environment | [Laptop](#laptop) |
| A self-hosted hub on Kubernetes | [Hub on Kubernetes](#hub-on-kubernetes) |
| A self-hosted hub on one host | [Hub on one host](#hub-on-one-host) |
| An environment joined to that hub | [Create an environment](#create-an-environment) |
| The public landing page | [Landing page](#landing-page) |

Every Kubernetes install uses `--namespace everest-system --create-namespace`,
because OpenEverest refuses any other release namespace. See
[Sign in with an identity provider](#sign-in-with-an-identity-provider) to
turn on OIDC.

## Values files

| File | Shape | Notes |
|------|-------|-------|
| `values.yaml` | Hub | Defaults: stack off, UI on, adapter database and its PostgreSQL operator on. Requires `adapter.hub.publicURL` and a Git backend |
| `values-hub-lab.yaml` | Hub in the lab | An overlay on the chart defaults that adds a host path for `gitDir`. It turns nothing off by itself |
| `values-spoke.yaml` | Connected environment | Overlay the chart snapshot renders. Create an environment writes the same shape; the lab does not install this file |

## Laptop

```bash
scripts/up.sh
scripts/up.sh --no-build
scripts/up.sh --down
```

`scripts/up.sh` with no cluster flags is the lab. It creates two kind clusters.
`hub` runs the chart with `values-hub-lab.yaml`: the UI,
the adapter, the adapter database, and a host directory of Git served by Gitea.
`spoke-a` is joined by minting a token at `POST /api/connect/tokens` and running
the bootstrap script from `/api/internal/connect/bootstrap.sh` with `--token`
and `--local`. The UI is forwarded to http://127.0.0.1:18080/app/.

Sign in as `dev` with `percona-dev-login`. Those values are throwaway. Anyone
who can reach the UI can sign in with them. `BOOTSTRAP_ORGANIZATION` and
`BOOTSTRAP_PASSWORD` change them. The password needs at least 12 characters
and lands in `helm get values`.

The lab starts with no Anthropic or Cursor key, so the Chat shows its paste a
key state. To start with one, set `LAB_ANTHROPIC_API_KEY` (and
`LAB_ANTHROPIC_BASE_URL` for a proxy) or `LAB_CURSOR_API_KEY` before
`scripts/up.sh`. The script signs in as the lab's Organization and stores the
key through `PUT /api/connections/assistant` or `/cursor`, the routes the
console uses, so the key is checked and sealed the way a pasted one is. The key
never reaches Helm values or the process list. A rerun with the variable still
set stores that key again, over one pasted in the console since.

The lab starts `cloud-provider-kind` in Docker, which gives each database's
`LoadBalancer` Service an address. Without one a database never reaches
ready. One controller serves every kind cluster on the host, and `--down`
removes it once no kind cluster is left. A three-member database needs three
schedulable nodes, and once kind adds workers its control plane takes no
databases; the hub smoke sets `LAB_SPOKE_WORKERS=4` for that and the platform.

| Option | Effect |
|--------|--------|
| `--no-build` | Reuse the images already loaded into the kind nodes |
| `--down` | Delete both kind clusters, the Gitea container, and the port-forward |

It needs Docker, kind, kubectl, helm, flux, git, curl, and python3. The first
image build also needs Node and npm. Docker must have about 8 GB of memory;
below that the script stops and tells you to raise the limit. Give the daemon
that much before the first run. The first run pulls platform images and takes
a while.

On Apple Silicon the connected environment runs without monitoring, because that
image is amd64 only. The script says so and continues. An amd64 machine gets
monitoring through the join script's platform Kustomization.

Gitea is at http://127.0.0.1:3000, user `hub`, password `lab-gitea`. That
password is throwaway too. Pods reach the hub and Gitea as
`host.docker.internal`, pinned to the Docker host's IPv4 gateway. The Connect
command runs on the laptop, and that process sends the same name to
127.0.0.1 so it hits the port-forward. On Linux, where the name is missing
inside kind, the script writes the Docker bridge gateway into the node.

A blank UI tab means the port-forward died. Restart it (its log is
`.local/ui-forward.log`):

```bash
kubectl --context kind-hub -n everest-system port-forward --address 0.0.0.0 svc/pds-ui 18080:8080
```

The forward listens on every interface so the connected environment can reach it.
`scripts/up.sh --down` stops it.

## Hub on Kubernetes

Requirements:

- Kubernetes 1.27 or newer, a default RWO StorageClass, and room for the
  adapter, the UI and PostgreSQL with a 10 GiB data and a 10 GiB pgBackRest
  volume.
- A stable DNS name with TLS and inbound 443. `adapter.hub.publicURL` is baked
  into every join command, so browsers and connected environments must both reach
  it.
- Egress to `api.github.com`, `ghcr.io`, ServiceNow and any AI provider you
  turn on.
- A GitHub org and either a token that can create private repositories or a
  GitHub App installed on the org (Administration write, Contents write,
  Metadata read). The Compose host reads `HUB_GITHUB_APP_ID`,
  `HUB_GITHUB_APP_INSTALLATION_ID` and `HUB_GITHUB_APP_PRIVATE_KEY`; the chart reads the same three from
  the Secret, named by `adapter.hub.github.appIdKey`, `appInstallationIdKey`
  and `appPrivateKeyKey`, in place of `HUB_GITHUB_TOKEN`. With an App and
  `registryTokenKey`, the chart also needs `adapter.hub.github.registryTokenDistinct=true`,
  which says the registry token is a separate credential and not the old
  static token: enrollment can no longer tell them apart. When the agent
  image is private, provide a separate `packages:read` token; this pull
  credential is handed to the environment, but the GitHub credentials never are.

```bash
kubectl create namespace everest-system
kubectl -n everest-system create secret generic hub-github \
  --from-literal=HUB_GITHUB_TOKEN=... --from-literal=HUB_REGISTRY_TOKEN=...
helm upgrade --install pds oci://ghcr.io/percona/charts/pds --version "$PDS_VERSION" \
  --namespace everest-system \
  --set adapter.hub.publicURL=https://hub.example.com \
  --set adapter.hub.github.org=your-org \
  --set adapter.hub.github.existingSecret=hub-github \
  --set adapter.hub.github.registryTokenKey=HUB_REGISTRY_TOKEN \
  --set ingress.enabled=true --set ingress.host=hub.example.com \
  --set ingress.tls.enabled=true --set ingress.tls.secretName=hub-tls
```

To authenticate as a GitHub App instead, put `HUB_GITHUB_APP_ID`,
`HUB_GITHUB_APP_INSTALLATION_ID` and `HUB_GITHUB_APP_PRIVATE_KEY` (the PEM on
one line with literal `\n`) in the Secret, drop `HUB_GITHUB_TOKEN`, and pass
`--set adapter.hub.github.appIdKey=HUB_GITHUB_APP_ID`,
`--set adapter.hub.github.appInstallationIdKey=HUB_GITHUB_APP_INSTALLATION_ID`
and `--set adapter.hub.github.appPrivateKeyKey=HUB_GITHUB_APP_PRIVATE_KEY`, plus
`--set adapter.hub.github.registryTokenDistinct=true` when
`registryTokenKey` is set. The chart refuses a partial set, and refuses a
registry token that is not attested as distinct.

A published GitHub Release builds linux/amd64 images
`ghcr.io/percona/percona-services-adapter` and `-ui`, and the chart
`oci://ghcr.io/percona/charts/pds`, all tagged with the release version minus
the `v`. Pushes to `main` publish nothing. The GHCR packages are private for
now. Until they are public, run `helm registry login ghcr.io` with a token
that can read packages, and give the cluster a pull Secret for the images
through `imagePullSecrets`. Check the signature with
`cosign verify ghcr.io/percona/charts/pds:$PDS_VERSION`.

This checkout installs the same hub. `IMAGE_REGISTRY` is a prefix with no
scheme and no trailing slash. The script builds linux/amd64 images, pushes
them, and points the chart at them. `--no-build` uses tags that are already
pullable. GHCR packages are private by default; the script writes an image
pull Secret from `gh auth token`.

```bash
export IMAGE_REGISTRY=ghcr.io/your-org
scripts/up.sh --kubeconfig /path/to/kubeconfig \
  --hub --hub-public-url https://hub.example.com
scripts/up.sh --kubeconfig /path/to/kubeconfig --hub --down
```

`--down` uninstalls the release and stops the tunnel. The cluster stays. A
kubeconfig without `--hub` is rejected, and so is `--hub` without
`--kubeconfig`. With no cluster flags the same script is the lab.

`scripts/up.sh --help` also lists `--kube-context`, `--hub-github-org`,
`--hub-github-secret`, `--hub-git-url`, `--hub-region`, and
`--ingress-host`. With no GitHub org the script sets
`adapter.hub.gitDir=/var/lib/hub-git` and turns persistence on. It seeds
organization `dev` with password `percona-dev-login` and prints those on its
summary. That organization is throwaway, the same as the lab, and is not how a
customer hub signs people in. The summary also says the database platform
and the backup store are off. Use Connect an environment after it is up.

- Without GitHub, the hub writes to `adapter.hub.gitDir` (keep it on a volume
  with `adapter.hub.persistence`) and environments clone `adapter.hub.gitURL`.
  Pushing that directory to that remote is yours to run; the lab does it with
  a commit and push loop. Never set GitHub and `gitDir` together.
- The adapter database is an in-cluster Percona PostgreSQL by default. For an
  external one, put `DATABASE_URL` in a Secret, set
  `adapter.database.existingSecret` to it and
  `adapter.database.operator.enabled=false`. The in-cluster database is created
  by a hook, so `helm rollback` does not roll it back, and it has no
  NetworkPolicy.
- A hub serves databases (MySQL and PostgreSQL), environments, backups, connections,
  support, chat with the Claude and Cursor keys an Organization pastes, activity
  and metrics, which come
  from the readings each agent posts.
- Keep `adapter.replicaCount` at 1.
- `HUB_OPERATOR_TOKEN` opens everything a signed-in session does, database
  credentials included. It is minted fresh on every start when unset; set a
  persistent one, kept like an administrator password, if anything calls the
  hub without a browser.

## Deploy a hub with sign-in, in order

The order matters because the public origin is baked into the issuer's
settings, every join command and the MCP login. Each step links to the section
that has the detail.

1. **Public origin.** A stable DNS name with TLS. A quick tunnel works for a
   trial, but its address changes when the tunnel pod restarts, and then the
   issuer's callback and logout URLs, and every join command already handed
   out, point at the old one.
2. **GitHub credential** for the org that holds environment repositories (see
   [Hub on Kubernetes](#hub-on-kubernetes)). A fine-grained token needs
   Administration and Contents read and write and Metadata read, is scoped to
   that org, and may need an org owner's approval. Give it an expiry you will
   notice: when it lapses, Create command answers "The control plane could not
   be reached" and the cause is only in the adapter log.
3. **Issuer** (see [Sign in with an identity provider](#sign-in-with-an-identity-provider)):
   a client for the adapter with the callback `<origin>/api/auth/oidc/callback`
   and the logout return `<origin>/`; the Google and GitHub connections turned
   on for that client, or sign in ends at "Signing in did not finish".
4. **Secrets** in `everest-system`: the GitHub Secret and `pds-oidc` with
   `OIDC_CLIENT_SECRET`.
5. **Images and chart.** A release pulls them. From a checkout, run
   `scripts/helm-deps.sh`, build and push the images to a registry the Kubernetes
   cluster can pull from, and give it a pull Secret.
6. **Install or upgrade the release** with `adapter.hub.publicURL`, the GitHub
   org and Secret, `adapter.oidc.issuer`, `clientId` and
   `existingSecret`, `adapter.oidc.signup.enabled=true`
   for open signup, and `--set-string adapter.state.organization='*'`. A first
   start after the release before Organizations migrates the database in place
   (see [Upgrades](#from-the-release-before-organizations)).
7. **Check it.** `GET <origin>/api/auth/methods` lists `oidc`. Sign in with each provider, confirm the Organization and its
   `default` Team exist, and rename the Organization in Settings.
8. **Connect an environment** from the console and run the command it prints
   (see [Create an environment](#create-an-environment)). Check the
   environment shows under its Team, and under All teams for an admin.
9. **Sign the MCP server in.** `PDS_URL=<origin> npx -y @gas-lab/pds-mcp@next login`
   opens the browser at the consent page, where the person picks the Organization, the Team
   and what the agent may do (see [Agent sign in](#agent-sign-in-oauth)). The address
   `<origin>` has to be `adapter.hub.publicURL`, because it is the issuer.

## Create an environment

In the console open Environments, then Create an environment, and create the command.
`POST /api/connect/tokens` with the operator token does the same. Run it on a
machine with `kubectl` and `helm`, plus `aws` and `eksctl` unless you pass
`--local`:

```bash
curl -fsSL 'https://hub.example.com/api/internal/connect/bootstrap.sh' | bash -s -- --token 'jnt_...'
curl -fsSL 'https://hub.example.com/api/internal/connect/bootstrap.sh' | bash -s -- --token 'jnt_...' --local
```

- Without `--local` it creates or reuses EKS (four `t3.2xlarge` nodes, which
  fit one Dev, one Staging and one Prod database together, and one NAT gateway,
  or public nodes and no NAT with `--public-nodes`), the EBS CSI add-on, a
  default StorageClass, a bucket, an IAM user and its Secret. That is a few
  hundred USD a month per cluster. `NODES` and `NODE_TYPE` change the node
  group.
- `--local` joins whatever cluster kubectl points at and creates nothing in
  AWS. The cluster needs a default StorageClass, and its backups go to the
  chart's MinIO.
- Then it installs the chart as `pds` in `everest-system` with the agent role
  and no UI, and Flux 2.5.1.
- It also installs, once, what a support access window needs, both on a join
  and with `--update`: the `pds-support` namespace, the roles
  `pds-support-read` and `pds-support-admin`, a role on Secrets in that
  namespace, the access proxy's permission to impersonate engineers, and a
  grant that lets the agent bind those two roles and no others. The agent is not cluster-admin. Running it again changes nothing, and
  it gives an engineer no access until a window opens.
- Everything the join creates in AWS carries the tag `percona.com/pds` with the
  cluster name as its value: the EKS cluster and node group, the EBS CSI role
  stack, the backup bucket and the backup IAM user. A bucket or node group that
  was already there is not tagged. A backup user from an earlier join gets the
  tag the next time the command runs. Tagging the bucket is best effort: if it
  fails the join warns and carries on, and `--delete` then does not find the
  bucket.
- `--status` prints what a run that did not finish created and the `--delete`
  command that removes it. `--dry-run` prints what a run would create. Both
  leave the token unspent. A run that finished keeps no record, so `--status`
  has nothing to read; `--delete` covers it. A failed run keeps what it made
  and prints how to resume; Ctrl-C is a pause. Running the command again
  reconciles to the same state.
- `--delete` deletes what a join created in AWS. It takes the environment name
  and the region, never a token, and does not call the control plane:

  ```bash
  curl -fsSL 'https://hub.example.com/api/internal/connect/bootstrap.sh' | bash -s -- --delete --name spoke-a --region us-west-2
  ```

  It needs `aws` and `eksctl`, signed in to the account the join used, and uses
  `kubectl` when it is installed. It finds everything by the `percona.com/pds`
  tag with the environment name as its value: the EKS cluster and its node
  group, the backup bucket, the backup IAM user with its access keys and
  policy, and the EBS CSI role stack. It refuses an EKS cluster that does not
  carry the tag for that environment and deletes nothing, which covers an EKS
  cluster you ran before the join. What a join added to such an EKS cluster
  (its node group, bucket, backup user and role stack) is left for you to
  delete; `--status` prints `--delete` for what this machine recorded creating
  and says what the command does when the EKS cluster was not made here. A bucket the
  join could not tag is not found, and a bucket that was already there or a
  user you supplied with `BACKUP_ACCESS_KEY_ID` is not tagged and stays. The IAM user is found by tag
  in any region, so it warns when that is all it found. It lists what it
  found with the `aws s3 sync` command that copies each bucket first, and
  deletes nothing until the environment name is typed on the terminal; a run
  with no terminal refuses. When `kubectl` reaches the EKS cluster it
  deletes the Flux `instances` Kustomization, every database and every Service
  of type LoadBalancer, waiting for each load balancer to go, all in a
  kubeconfig of its own. Before it asks, it reads the EBS volumes the EBS CSI
  driver provisioned with reclaim policy `Delete`, lists them, and deletes the
  ones left available once the nodes are gone, because monitoring's claim stays
  in use until then; a volume that stays is printed with the command that
  deletes it, since a rerun finds nothing tagged. When `kubectl` cannot reach
  the EKS cluster it warns that its volumes may stay and carries on. Then it runs `eksctl delete cluster
  --wait`, reads the EKS cluster and its eksctl stack back, and only when both
  are gone deletes the bucket, the IAM user and any role stack left. A rerun
  after a half deleted EKS cluster finds its leftover eksctl stack by the same
  tag. A failure stops with what was kept, and running the same command again
  finds what is left. It ends with a tag search of the region, listing
  anything still tagged (the search can lag a few minutes behind a delete),
  and says to disconnect the environment in the console. It deletes AWS
  resources only. It reads with `sts:GetCallerIdentity`,
  `eks:DescribeCluster`, `cloudformation:DescribeStacks`, `tag:GetResources`,
  `iam:ListUsers`, `iam:ListUserTags` and `ec2:DescribeVolumes`, which is more
  than the join needs, and deletes with the rights `eksctl delete cluster`,
  `s3:DeleteObject` and `s3:DeleteBucket`, `iam:DeleteAccessKey`,
  `iam:DeleteUserPolicy` and `iam:DeleteUser`, `cloudformation:DeleteStack` and
  `ec2:DeleteVolume` give.
- The chart comes from the hub (`/api/internal/connect/pds.tgz`) unless
  `PDS_CHART` names a checkout or `PDS_CHART_OCI` and `PDS_CHART_VERSION` name
  a release.
- A join token is valid for `adapter.hub.joinTokenTTL`, 24 hours by default.

Disconnect, on the environment's page, revokes its tokens and deploy key. Its
databases keep running, its Git files stay, and nothing in AWS is deleted: the
UI links a page with the `--delete` command for what the join created. It also
links a guide, Continue with OpenEverest, for a customer who keeps the databases and
runs OpenEverest themselves: it detaches the cluster from Git without deleting
a database, stops the agent, and leaves the backup user and bucket in place.

## Hub on one host

The adapter and the UI run in Docker Compose on one host, with state in an
external PostgreSQL and no Kubernetes access at all.
Connected environments reach it inbound over `/api/internal`, so the host needs
no route into any customer network.

```text
Browser ─────────┐
                 ├── TLS edge ── :80 ──▶ host ──┬── pds-ui       nginx: landing page at /, console at /app/, proxies /api
Environments ────┘                              └── pds-adapter  :8080 ──┬── PostgreSQL 17
                                                                         ├── GitHub
                                                                         └── ServiceNow
```

| Need | Why |
|------|-----|
| An x86_64 host with Docker Engine and the Compose v2 plugin | Images are built for linux/amd64 only, and `docker compose` runs the stack |
| Egress to `ghcr.io`, `api.github.com`, ServiceNow | Images, cluster state, tickets |
| Inbound 443 at an edge that forwards to `:80` | Browsers and joining environments share the origin |
| PostgreSQL 17, TLS on, a role with `CREATE` on its schema | The schema is reconciled on every start. About 10 connections per adapter |
| A GitHub org token that creates private repositories, and a `packages:read` token | Environment repositories, and the agent image pull |

| File, next to `compose.yaml` | Holds |
|------------------------------|-------|
| `compose.yaml`, `nginx/default.conf` | Copied from `deploy/compose/` of the release you run |
| `version.env` | `PDS_IMAGE_TAG=<release tag>`, the only file an upgrade changes |
| `pds.env` | Configuration and secrets. Whoever owns secrets |

First time:

1. Provision the host and the database. Confirm the host reaches the database.
2. Copy `deploy/compose/` from the release you run to a directory on the host.
   The commands below run from that directory.
3. Log in to the registry with a `packages:read` token for the private images:
   `docker login ghcr.io -u <user> --password-stdin`, reading the token from
   standard input.
4. Copy `pds.env.example` to `pds.env`, mode 600, and fill it. The template
   documents every key. When you move a host from `HUB_GITHUB_TOKEN` to the
   GitHub App, empty `HUB_REGISTRY_TOKEN` or give it a separate `packages:read`
   token before you empty `HUB_GITHUB_TOKEN`: a registry token equal to the old
   one is no longer hidden from enrollment and would reach every environment.
   Generate the two secrets it cannot supply and keep copies wherever your
   secrets live: losing `STATE_ENCRYPTION_KEY` makes every sealed row unreadable.

   ```bash
   openssl rand -base64 32   # STATE_ENCRYPTION_KEY
   openssl rand -hex 32      # HUB_OPERATOR_TOKEN
   ```

5. Write the release tag and start the stack:

   ```bash
   echo 'PDS_IMAGE_TAG=<release tag>' > version.env
   docker compose --env-file version.env pull
   docker compose --env-file version.env up -d
   ```

6. Point DNS at the edge.

To upgrade or roll back, copy `compose.yaml` and `nginx/default.conf` from the
target release, write its tag to `version.env`, pull, and recreate with
`docker compose --env-file version.env up -d --force-recreate`. The nginx
config is a bind mount, so a changed file alone leaves the running container on
the config it loaded at start.

The edge must serve `/` and `/api` on one origin, allow a 180 second read and
send timeout, and leave `/api/internal` reachable. `HUB_PUBLIC_URL` must start
with `https://`, because it decides the cookie's `Secure` flag.

The edge must also forward `/oauth/`, `/mcp`, `/.well-known/oauth-authorization-server` and
`/.well-known/oauth-protected-resource` to the UI
container, whose nginx sends them to the adapter, or agents cannot sign in.

Things that bite:

- Never set `KUBERNETES_SERVICE_HOST`. It tells the adapter it is in a
  cluster, and a hub with a database and no cluster client then refuses to
  start.
- `DATABASE_URL` present but empty is fatal on purpose: it is a broken Secret,
  not a request to run without a database.
- Editing `pds.env` does not reach a running container, and neither does
  `docker compose restart`. Recreate:
  `docker compose --env-file version.env up -d --force-recreate`.
- Compose expands `$` in `pds.env`. Single quote such a value or write `$$`.
- `sslmode=require` does not verify the database certificate.
- Container logs are not shipped anywhere. They rotate at five 10 MB files.

The `compose` workflow brings this stack up with a throwaway database
(`--profile local-db`) on every change under `deploy/`, which is also the way
to try it on a workstation. A second job there runs the MCP binary's
contract tests against the adapter and that database, signed in through the
authorize and consent routes as a user the job creates. The workflow also
runs on changes under `apps/mcp/`.

## Support access network

Support access reaches an environment over a private network the hub manages
through two OAuth clients. What the hub does not do is write the network's
access policy, so an administrator merges
[deploy/tailnet-policy.example.hujson](../deploy/tailnet-policy.example.hujson)
into it once, before the first window opens. The file is HuJSON, the policy's own
format, and it holds:

| Entry                                                                                 | Why                                                                                                                                                                                                         |
| ------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `tag:pds-environment` in `tagOwners`                                                  | The tag on each window's device. The key client mints single-use keys for it.                                                                                                                               |
| `tag:pds-support` in `tagOwners`                                                      | The tag on each environment's private address. The service client creates addresses with it and with `tcp:443` as the only port. The hub refuses to open a window on an address with any other tag or port. |
| `autoApprovers.services`                                                              | Approves a window's device as the host of its address without a click in the admin console. The key is the address tag, so no per-address entry or `svc:*` wildcard is needed.                              |
| One grant from `group:pds-support-engineers` to `tag:pds-support` on `tcp:443`        | The only path in. Environments are the destination of no grant, so they cannot reach each other.                                                                                                            |
| One `tailscale.com/cap/kubernetes` grant from the same group to `tag:pds-environment` | Makes an engineer arrive in Kubernetes as the group `pds.percona.com:support`. The destination is the window's device, which is the proxy that impersonates the engineer, not the address.                  |

Set up, in order:

1. Enable HTTPS and MagicDNS on the network: the access workload issues
   certificates.
2. Merge the example, set the group's members, and save the policy.
3. Create the two OAuth clients. The key client needs the scopes for auth keys
   and for devices, with `tag:pds-environment`. The service client needs the
   scope for Services, with `tag:pds-support`. Put the IDs and secrets in the
   hub's `SUPPORT_TAILNET_*` settings, the secrets from Secrets.
4. Open a window on a connected environment. A refusal that says "Contact
   Percona support." with a 502 is the hub's tag and port check failing, and its
   cause is in the hub's error log with the request id.

`SUPPORT_GUIDE_URL` is optional. Set it to an https address and the case note
the hub writes when a window opens, and the text the console offers to share,
end with "Steps for the engineer" and that address. Unset or empty, the line is
left out. The hub refuses to start with a value that is not an https address.

The example grants network reach and the group name only. What an engineer may
do inside an environment is decided by the Kubernetes roles the join script
installs and by the binding of a role to that group, which the agent creates
with each window and removes when it ends.

## Connected environments left running

The laptop command is the lab. See [Laptop](#laptop).

`--down` on that command touches nothing in AWS. An environment joined with the
Create an environment command without `--local`, and its bucket, keep billing until you
delete them. `--delete` on the join script covers a join that finished and one
that did not, and the Environments page shows the command.

## Organizations and sign-in

Every route needs a signed-in user. The laptop scripts seed `dev`. Anywhere
else, Percona onboards a customer from the command line:

```bash
kubectl -n everest-system exec deploy/pds-adapter -- adapter organization create acme
docker compose --env-file version.env run --rm --no-deps pds-adapter organization create acme
```

It prints a single use invite link the customer sets their own password
through. `organization invite <id>` mints another, which is also how a password is
reset, and `organization list` shows who has claimed theirs. An organization id is 3 to 63
lower case letters, digits and hyphens, starting with a letter.

A User has one Team in each Organization. The console acts in the person's own
Team, and an admin can switch to any Team of the Organization or read the
environment, database and activity lists across all of them. A create flow asks
for a Team while All teams is chosen. A Team named `all`, in any case, is
refused on create and rename, because `team=all` asks for every Team.

`adapter.state.organization` names the one Organization a release serves
(`default`), or `*` for every Organization with a row. Under `*`, environment
repositories are `<organization>-<environment>`.

### Sign in with an identity provider

`OIDC_ISSUER` turns on sign-in through an OpenID Connect issuer beside the
password login. Empty leaves only the password login. The adapter is a
confidential client, so the issuer needs:

- discovery at `<issuer>/.well-known/openid-configuration`;
- the authorization code flow with a client secret;
- PKCE with `S256`;
- ID tokens signed with RS256, carrying `sub`, `email` with `email_verified`,
  and `name`.

Register the adapter with the redirect URL
`<adapter.hub.publicURL>/api/auth/oidc/callback` and the sign-out return URL
`<adapter.hub.publicURL>/`.

```bash
kubectl -n everest-system create secret generic pds-oidc \
  --from-literal=OIDC_CLIENT_SECRET=<client secret>
helm upgrade --install pds charts/pds --reuse-values \
  --set adapter.oidc.issuer=https://login.example.com/ \
  --set adapter.oidc.clientId=<client id> \
  --set adapter.oidc.existingSecret=pds-oidc
```

`adapter.oidc.scopes` defaults to `openid email profile`, and
`adapter.oidc.secretKey` to `OIDC_CLIENT_SECRET`. The secret comes only from
an existing Secret. The other settings are `OIDC_ISSUER`, `OIDC_CLIENT_ID`,
`OIDC_CLIENT_SECRET` and `OIDC_SCOPES`.

The issuer only proves who someone is. The adapter database decides what they
may do, so an identity that is not yet a member of an Organization is sent
back to the sign-in page (`/app/login`) with `?error=no_membership` unless signup is on (see
below). Any other failure returns with `?error=sign_in_failed`. Sessions are
the same cookie as the password login, so `SESSION_LIFETIME` applies, and
`POST /api/auth/oidc/logout` ends the issuer's session too. `GET /api/auth/methods`
lists the methods on offer, `["password"]` or `["oidc", "password"]`.

#### Removed: the MCP device flow

Only the TypeScript releases of `@gas-lab/pds-mcp` use this. The Go release, which replaces them
under the same name, signs in through the browser instead (see
[Agent sign in](#agent-sign-in-oauth)), and the exchange is removed after the first Go release.
Installed TypeScript versions stop working against a control plane once the exchange is gone, until
they update.

`OIDC_AUDIENCE` and `OIDC_DEVICE_CLIENT_ID` (`adapter.oidc.audience` and
`adapter.oidc.deviceClientId`) are gone, together with `POST /api/auth/exchange`
and the `deviceFlow` object on `GET /api/auth/methods`. `@percona/pds-mcp` signs
in through the browser instead (see [Agent sign in](#agent-sign-in-oauth)), and
`@gas-lab/pds-mcp` installs that still use the device flow stop working: move
them to `@percona/pds-mcp` and run `pds-mcp login`. The adapter logs one warning
for each of the two variables that is still set, and ignores it. The chart
refuses to render while either value is set. Remove `adapter.oidc.audience` and
`adapter.oidc.deviceClientId` from your values, and the API and the public
client for the device code grant at the issuer when nothing else uses them.

#### Agent sign in (OAuth)

Remote MCP clients sign in to the control plane over OAuth, whatever its sign in
method. `adapter.hub.oauthIssuer` is the issuer, or `adapter.hub.publicURL` when it is not set, so it must be an origin with no path: `https://` for a
deployment, or `http://localhost` for a laptop. Any other value leaves the OAuth endpoints off with one
warning at start. Whatever sits in front of the UI must forward these paths:

| Path                                          | To                                          |
| --------------------------------------------- | ------------------------------------------- |
| `/oauth/` (authorize, token, register, revoke) | the adapter, through the UI's nginx or the chart's ingress |
| `/.well-known/oauth-authorization-server`     | the same                                    |
| `/.well-known/oauth-protected-resource` (and below it) | the same                                    |
| `/mcp`                                        | the same, unbuffered, with the `Authorization` header passed through |

The issuer must be the origin people reach the console at, not a tunnel that
forwards only `/api/internal`. `HUB_PUBLIC_URL` is that origin on almost every hub, and `HUB_OAUTH_ISSUER` stays empty. Set it
only when the address connected environments use is not one a browser or an agent can use as an issuer, as in the kind lab: there
`HUB_PUBLIC_URL` is `http://host.docker.internal:18080`, which is not `https` or loopback, and `HUB_OAUTH_ISSUER` is
`http://127.0.0.1:18080`, the port-forward on the machine of the person signing in. An agent is then pointed at the issuer
address, and its `PDS_URL` has to be that address. The per-address rate limits read the first `X-Forwarded-For` entry, which a caller can
set, so they slow honest clients and do not stop a determined one; registration also has an overall cap.
Browsers cannot call the token, registration or revocation endpoints from another origin: they send no CORS
headers, and the metadata document is the only one that does.

Claude's hosted connectors reach these paths from Anthropic's network, so they must be publicly
reachable. Claude Code, Cursor Desktop and VS Code reach them from the person's machine. A client that publishes a
Client ID Metadata Document needs the control plane to reach that address over HTTPS; a control plane with no
outbound access still serves clients that register themselves. Turning the `mcp` surface off
(`adapter.features.mcp=false`) removes every one of these endpoints: they answer 404.

OIDC also needs `adapter.hub.publicURL` (`HUB_PUBLIC_URL`): the redirect
addresses are built from it, never from the request's Host header. Without it
the adapter treats OIDC as off.

#### Signup

With `adapter.oidc.signup.enabled=true` (`SIGNUP_ENABLED`), an identity with no
membership creates an Organization on its first sign in: the Organization,
a `default` Team and an Organization admin, in one transaction. The
Organization is named from the token's `name`, else the part of the email before
the at sign, and the new admin acts in it. It is off by default, so a
self-hosted install stays closed. It also needs the adapter to serve every
Organization (`adapter.state.organization: "*"`), and is ignored on a deployment
pinned to one organization.

A finished sign in lands on the console, `/app/`; a refused one lands on its sign-in page, `/app/login?error=<code>`. Signup refuses with:

| Code | Why |
|------|-----|
| `email_unverified` | The token has no verified email |
| `signup_rate_limited` | Five Organizations an hour were made from this address |
| `signup_unavailable` | The deployment holds `adapter.oidc.signup.organizationCeiling` Organizations (default 1000) |
| `signup_limit_reached` | The person already created three Organizations |
| `organization_name_invalid` | The `organization` the start address carried is over 200 characters |

`/api/auth/oidc/start` takes two optional parameters. `provider` (`google` or
`github`) asks the issuer for its `google-oauth2` or `github` connection and
is 404 for anything else. `organization` names the Organization a first sign in
creates, in place of the name made from the person's identity; it is ignored for
a person who already has one and for an invitation.

A person signed in through the issuer can create another Organization with
`POST /api/auth/organizations` (`{"name": "..."}`, the same limits) and switch
with `POST /api/auth/organization` (`{"organizationId": 3}`). Either moves the
session into that Organization and records it as the person's last used, which
is where their next sign in lands. `GET /api/auth/session` lists their
Organizations. A pending invitation for the verified email is meant to win over
signup, which arrives with invitations.

A person who signs in with Google and with GitHub using the same email is
two Users, because a User is keyed by the issuer and its subject. Each has its
own memberships.

If discovery fails at start, the adapter logs
`OIDC sign in is off, only the password login is available` with the issuer
and the error, and keeps serving the password login. Fix the issuer and
restart the adapter.

## Landing page

The console lives under `/app/` in every install. With the landing page on, as
in the Compose install, the public landing page (`landing/`) is served at `/` by
the same nginx, from the same UI image: `publish.yml` runs `scripts/build-landing.sh
apps/ui/dist/landing` beside the console build, and
`deploy/compose/nginx/default.conf` serves it. That file is the chart's nginx
block rendered with `ui.landingPage=true` (the `template.hosted-ui` snapshot),
and the `compose` workflow fails when they differ. By default the chart serves
no landing page: `/` redirects to `/app/` and anything outside `/app/` and
`/api/` is a 404. In both, the console's old paths (`/login`, `/claim`, `/databases`
and the rest) redirect to the same path under `/app/` with the query kept, so
bookmarks and invite links sent before the move still work.

Sign in on the landing page goes to the console. `VITE_CONSOLE_URL` at build
time overrides that, and the default is `/app/`, where the first sign in can
create an organization.

nginx hides its version and sends `X-Frame-Options: DENY`,
`Content-Security-Policy: frame-ancestors 'none'`, `X-Content-Type-Options:
nosniff` and `Referrer-Policy: strict-origin-when-cross-origin` on every
response. `Strict-Transport-Security` is added only when the host the browser
used is `pds.percona.com`. A TLS edge in front of the host should set the same
headers, including HSTS, on that name, and pass the original Host through so
nginx can see it. A location that sets its own `add_header` repeats the set,
because nginx does not inherit them.

## Upgrades

Only a fresh install of the pinned versions is supported; the pin is
OpenEverest 2.0.0-dev.4 with its providers and operators. There is no
supported in-place upgrade of OpenEverest, a provider, an operator or a CRD: tear down
and reinstall. A connected environment can be moved in place at your own risk
with `--update`, described in [compatibility](compatibility.md). The pins and
the evidence a bump needs are there too.

### Removed Chat providers

The Chat offers Anthropic and Cursor only. The OpenAI, Gemini and OpenRouter
providers are gone, so a deployment that used one stops answering for it:
a chat that names one is refused, and the chart no longer renders their Secrets,
environment variables or grants. The first start of this release deletes every
key pasted for them from the adapter database. Remove `openai`, `gemini` and
`openrouter` from your values, and delete any Secret you created for them with
`kubectl delete secret`.

A deployment that still sets `ADAPTER_FEATURE_ASSISTANT_OPENAI`,
`ADAPTER_FEATURE_ASSISTANT_GEMINI` or `ADAPTER_FEATURE_ASSISTANT_OPENROUTER`
starts with one warning per variable. The next release refuses them. Chats
already saved with one of these providers stay readable and cannot continue.

### Removed operator keys for the Chat

A control plane no longer carries one Anthropic or Cursor key for every
Organization. Only a key an Organization pastes in the console, under
Connections, is used, and each Organization keeps its own sealed in the adapter
database. A deployment that relied on the operator key now shows every
Organization the Chat's paste a key state until someone pastes one.

The chart refuses to render while any key under `assistant` or `cursor` holds a
value other than an empty string or `false`, and the error names the key.
Remove both blocks from your values, and
delete the Secrets they created (`<release>-assistant`, `<release>-cursor`)
with `kubectl delete secret` once the keys are pasted. The adapter no longer
reads `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL` or `CURSOR_API_KEY`. A
deployment that still sets one starts with one warning naming it, and the value
is ignored. A key the Organization already pasted is not affected.

### Alert rules on connected environments

A hub that carries the alert rules adds them to the PMM of each connected
environment at start, with no change to settings or the PMM chart version.
Flux then rolls PMM once, so monitoring is unavailable for a short time and
readings show pending meanwhile. Restarting the hub again commits nothing.
A hub release that changes the rules' content, a rule, a title or an
expression, rewrites them on start in the same way and rolls PMM once on every
environment that carries them.
An environment that connects again after the hub started gets them on its
first beat from the current agent, in the background so the beat is answered
at once. Each environment is bounded at 30 seconds, and a failed try waits
five minutes.

The environment's own join command must run once first: it installs the
`hub-platform` Role that lets Flux apply the rules, and no hub action can.
Run the command from Create an environment again for that environment. Until
then the hub logs that it is waiting for the join command and writes
nothing for it. A change to its monitoring settings in the meantime leaves
the rules out too, so Flux is never given a ConfigMap it cannot apply. An
environment with no amd64 node runs no monitoring and is
skipped.

The scripts handle Helm 4: they pass `--wait=legacy`, because a bare `--wait`
never resolves CRDs, and `--force-conflicts` where Helm supports it.

### Renamed chart values

A connected Kubernetes cluster is an environment, and the chart now says so.
For one release the old keys still work, and `NOTES.txt` names each one a
release sets:

| Old value | Use instead | Adapter setting it renders |
|-----------|-------------|----------------------------|
| `adapter.hub.clusterName` | `adapter.hub.environmentName` | `ENVIRONMENT_NAME` |
| `adapter.hub.agentClusters` | `adapter.hub.agentEnvironments` | `HUB_AGENT_ENVIRONMENTS` |

The new key wins when both are set. The chart writes only the new settings,
so an adapter image older than this release does not read them: upgrade the
chart and the image together. Connect rerun on an existing environment keeps
the stored old key (`--reuse-values`) next to the new one, so the notes keep
naming it until the release is reinstalled. `adapter.hub.clusterRegion` keeps
its name: it is the region of the EKS cluster.

### From the release before Organizations

The first start of this release upgrades the adapter database in place, in one
transaction, and the upgrade cannot be undone. Back the database up first: the
release before this one cannot run against it afterwards.

The upgrade needs `STATE_ENCRYPTION_KEY`, the same key the database was sealed
with, because every sealed value is opened and sealed again against its new
owner. Without it the start fails and changes nothing.

What it does: each owner the old release keyed rows by becomes an Organization
with a default Team and an admin User that keeps its login. Environments,
tokens, connections, history, chats, signed in browsers and unspent invite
links move under them, and the repository each environment already uses is
written onto its record. A deployment that serves one Organization refuses a
database that holds rows of any other owner.

The names that the Organization model replaced no longer work, and nothing
warns about them: a release still setting one serves `default`, and a login
client still sending one is refused. Use `HUB_ORGANIZATION` and
`adapter.state.organization` for the Organization a hub serves,
`PDS_USERNAME` and `PDS_PASSWORD` for the MCP server, `username` in the login
request, `adapter organization` on the command line, and
`BOOTSTRAP_ORGANIZATION` and `adapter.auth.bootstrapOrganization` for the
development login. The import of the old join-token Secret, hub ConfigMaps and
connection Secrets is gone along with its chart value, so state that still
lives only in those objects has to be recreated.

A laptop hub that serves every Organization keeps each Team's desired state in a
directory named by the Team's id. Rename an existing directory by hand.

The chart's bootstrap hook Secret is now `<release>-bootstrap-organization`. The
Secret the release before made for the same hook is not deleted. A deployment
env file has to set `HUB_ORGANIZATION` before the next deploy: the setting it
replaced is ignored, and the hub then serves `default`.

Connected environments keep the chart they joined with. A hub upgrade does not
change them. To move one to this release, use Update on its row in Environments:
it mints a fresh token and prints the join script with `--update`, which you run
where kubectl points at the environment's Kubernetes cluster. It is not a tested
upgrade path, so back up the databases first, and it refuses a downgrade, an
installed version below the floor in the pin, and a busy database or backup. See
[compatibility](compatibility.md#updating-an-environment-in-place-at-your-own-risk).
The join command from Create an environment still repairs a connection, and
installs the release it carries without the checks.

## Troubleshooting

| Symptom | Cause |
|---------|-------|
| Metrics pending on a new database | The pmm-client sidecar on the connected environment starts scraping about a minute after the pod is Ready. A database created before MonitoringConfig `pmm` existed never gets one; create a new database. The hub draws charts from the readings the agent posts, so a cluster that has not reported yet is pending too |
| Metrics stay pending | The hub does not query monitoring itself. A missing token, an unset monitoring URL, or monitoring with no target for the database is posted as pending, not as a failed request. The token is in Secret `<release>-pmm` on the connected environment. The hook that mints it logs to `kubectl -n everest-system logs -f job/pds-monitoring-config` while that Job exists; the Job is deleted when it succeeds |
| Create command answers "The control plane could not be reached" | The UI hides the cause of a 502. Read the adapter log: a GitHub 401 means the hub's token expired or lost its permissions. Replace it in the GitHub Secret and restart the adapter |
| Sign in ends at "Signing in did not finish" | The issuer's Google or GitHub connection is not turned on for the adapter's client, or the callback URL does not match `adapter.hub.publicURL`. A quick tunnel changes address when its pod restarts |
| The MCP server lists no environments for an admin | Without `PDS_TEAM` it acts in the person's own Team. Set `PDS_TEAM` to the Team that holds them |
| Staging or Prod stuck in Creating | Fewer than three schedulable nodes on the connected environment |
| Scale refused | A backup is Pending or Running. One stuck in Pending blocks scaling until it is deleted with kubectl; the API has no backup delete |
| Support actions missing | The ServiceNow URL (`SN_URL`) or keys are absent, so the support routes answer 501. Paste the client token on the Connections panel. There is no platform status page |
