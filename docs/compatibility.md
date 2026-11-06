# OpenEverest compatibility contract

The Percona Database Service chart, OpenEverest, the engine providers, and the operators are
one tested unit. Do not mix independently selected component versions. The
machine-readable pin is
[charts/pds/compatibility.yaml](../charts/pds/compatibility.yaml).
`scripts/check-compatibility.sh` and the chart fail when Chart.yaml,
`values.yaml` `platform.*`, or this page drift from that file.

## Fresh install and upgrade

| Path | Status |
|------|--------|
| Fresh install of this pin on an empty cluster | supported |
| In-place upgrade of OpenEverest, a provider, an operator, or the CRDs | unsupported |

This design-partner preview qualifies a fresh `helm upgrade --install` of the
pinned set. It does not qualify moving a running control plane or a running
database from one pin to another. Tear the cluster (or the release and its
CRDs) down and install the new pin if you need a newer unit.

There is no supported path from OpenEverest 1.16.2 onto this pin. Create a
fresh cluster.

### Updating an environment in place, at your own risk

An environment that has already joined can move to the control plane's own
release with the join script's `--update`, offered from Update on its row in
Environments. It stays outside the compatibility guarantee above: nothing
qualifies a move between pins, and `hub-smoke` only proves the mechanism on one
pin. Back up the databases first.

`--update` runs where kubectl points at the environment's Kubernetes cluster,
takes `--token` and nothing else, and creates, changes or deletes nothing in
AWS. It moves the agent, OpenEverest, the providers, the operators, their CRDs
and Git reconciliation in one Helm upgrade to the chart and agent image the
control plane carries. There is no version picker, and no acceptance step. The
upgrade keeps every value the release was given and takes the new chart's own
defaults, and it writes only the agent image and the agent token over them. It
needs Helm 3.14 or newer, which `--update` checks before it uses the token.

It refuses, with no override, and before it uses the token:

- a Kubernetes cluster with no Percona Database Service release, a release
  that belongs to another environment than the token's, one that reports to
  another control plane than the one the command comes from, or one that applies
  its databases from another Git repository than the control plane names for the
  environment (which separates two Teams that each own an environment of the
  same name where each has its own repository);
- an installed OpenEverest or provider below its floor in
  `support.update.from` in the pin, which is `>= 2.0.0-dev.4` for OpenEverest
  because dev.3 has no upgrade path;
- an installed OpenEverest or provider newer than the control plane's chart,
  because an update never downgrades;
- a release that keeps its databases in another namespace than the control
  plane applies them in;
- a database in any phase but Ready (Suspended and Failed included), or a
  backup or restore that has not finished.

A failed update stops, names the phase, and prints the commands to look at the
release and to run `helm rollback`. Nothing rolls back on its own. A new command
from the Update page tries again.

A release a failed update left in the `failed` state can be updated again. Its
versions are read from the last revision that was running, and the update
refuses when the failed revision tried other versions than those, because the
cluster may then hold some of each. Run `helm rollback` first in that case. A
release that failed with no earlier running revision is repaired with the join
command instead. With `PDS_CHART_OCI` the released chart is pulled and unpacked
before its pin is read.

## Supported matrix

Values come from the pin. Chart.yaml dependencies and `platform.*` values
must match.

| Component | Version | Notes |
|-----------|---------|-------|
| Percona Database Service chart | 0.1.0 | Source Chart.yaml. A GitHub Release publishes the OCI tag with the release version |
| Kubernetes | >= 1.27.0-0 | Chart `kubeVersion` |
| OpenEverest | 2.0.0-dev.4 | Chart `openeverest` from https://openeverest.github.io/helm-charts/ |
| PXC provider | 0.3.1 | `provider-percona-xtradb-cluster` (`provider-pxc`) |
| PXC operator | 1.20.0 | Installed by the PXC provider chart |
| PostgreSQL provider | 0.5.0 | `provider-percona-postgresql` (`provider-postgresql`) |
| PostgreSQL operator | 3.0.0 | Installed by the PostgreSQL provider chart |
| PMM chart | 1.9.1 | Optional; app 3.9.1 |
| Instance CRD | core.openeverest.io/v1alpha1 | Adapter client constant |
| Backup and Restore CRDs | backup.openeverest.io/v1alpha1 | Adapter client constant |
| MySQL (Percona XtraDB Cluster) | operator-default | The adapter does not set an engine image |
| PostgreSQL (Percona PostgreSQL) | operator-default | The adapter does not set an engine image |

PMM is optional. It is not part of the create path. The OpenEverest,
provider, and operator rows are one unit even when PMM is off.

The `pg-operator` 3.0.0 dependency in Chart.yaml is outside this unit. It runs
the adapter's own PostgreSQL on a hub, not customer databases, and follows the
chart rather than the pin.

## What the OpenEverest 2.0.0-dev.4 pin changed

- **Fresh install only.** dev.4 has no upgrade path from dev.3 and needs
  provider-pxc 0.3.x. Provider-percona-postgresql 0.4.0 and later need dev.4,
  so the providers move with it. Tear down a dev.3 environment and install
  the pin again.
- **Retention.** A schedule keeps its newest copies through
  `retention: {type: count, count: N}`. dev.4 prunes the old `retentionCopies`
  field, which would have turned retention into keep everything. A schedule
  written outside PDS with `type: time` reads as keeping no fixed number, and
  saving a schedule in PDS replaces it with count retention.
- **Status.** The adapter reads the engine's ready and total pods from
  `status.components` entries named `engine`, with `replicas` and
  `readyReplicas`.
- **Member placement.** provider-pxc 0.3.x puts each MySQL member on its own
  node unless the Instance sets `schedulingPolicy.affinity`. PDS keeps that
  default for staging and prod, so a three-member database needs three
  schedulable nodes, on a laptop lab as much as anywhere else.
- **Connection Secret.** dev.4 gates reading a connection Secret behind the
  `read-connection` RBAC action. PDS runs OpenEverest with RBAC off, so no
  action is checked. An installation that turns RBAC on must grant that action
  to the account the adapter signs in as.

## MCP server releases

The MCP server (`apps/mcp`) is released on its own, from a GitHub Release tagged `mcp-v` and
its version, such as `mcp-v1.2.3`. Its version is stamped from that tag, so it does not appear
in [charts/pds/compatibility.yaml](../charts/pds/compatibility.yaml), and that file is
unchanged by a server release.

## Pin-bump evidence

A pull request that changes the pin, Chart.yaml dependency versions, or
`platform.*` versions is a pin-bump PR. It must pass the real-cluster
lifecycle contract before merge:

1. Update `charts/pds/compatibility.yaml` and every field that
   must match it (Chart.yaml, `values.yaml` `platform.*`, this page).
2. Run `scripts/check-compatibility.sh`, then `scripts/helm-deps.sh`,
   `helm lint charts/pds --set adapter.hub.publicURL=https://hub.example --set adapter.hub.gitDir=/var/lib/hub-git --set adapter.hub.persistence.enabled=true`,
   and `helm template pds charts/pds --namespace everest-system -f charts/pds/values-spoke.yaml --set adapter.hub.agentToken=lab-token`.
   The default install is a hub. The connected environment values file is what
   renders the pinned platform.
3. Dispatch [`.github/workflows/smoke.yml`](../.github/workflows/smoke.yml)
   (`hub-smoke`) against the branch and keep the run URL on the PR. A pin
   change needs all three suites green: `mysql`, `postgresql` and `mcp`, which
   run in parallel, each on its own lab. The `mysql` and `postgresql` suites
   are the lifecycle contract. Each brings up a hub and one connected
   kind cluster. The `mysql` suite creates a MySQL Dev database and waits until Ready,
   waits until monitoring reads MySQL connections for it, makes the hub's alerts route
   list `service_down` firing for it and then cleared, scales CPU and members, keeps an unmodeled Instance field through the
   next scale, takes a backup that succeeds, restores it, and deletes it
   and waits for cleanup. The `postgresql` suite runs
   `scripts/hub-smoke.sh --engine postgresql` on a lab of its own: a PostgreSQL
   Dev database through create, Ready, scale, schedule, backup, restore
   (which seeds a new cluster first) and delete. The alert and unmodeled
   field steps are MySQL only. Both suites then update the connected environment
   in place with `--update` on the same pin and check the databases they made
   are still Ready. The `mcp` suite brings up a lab with
   `scripts/hub-smoke.sh --lab-only`, creates no lifecycle, and runs the MCP
   binary's contract tests against its hub. Per-PR `kind-platform` brings the same lab up
   and checks the connected environment, the agent Deployment, and Flux
   Kustomizations `instances` and `platform`. It does not create a
   database. It is not pin-bump evidence.
4. Record the engine image tags the Ready Instance actually ran, if the
   operator default changed. Read them with kubectl on the connected
   cluster.
5. Set a floor in `support.update.from` for each component whose transition
   from an older version is known to break, so `--update` refuses it. A bump
   with no known break adds none. `scripts/check-compatibility.sh` checks the
   field: each key is OpenEverest or a provider the pin has, and each value is
   a `>=` constraint.

The nightly `hub-smoke` workflow runs the same three suites. Do not treat a
green lint job as enough. `hub-smoke` never gates a pull request.

This repository has no required status checks. The reviewer is the gate:
do not merge a pin-bump PR without a green `hub-smoke` run for that pin.

## N to N+1 qualification

Use this procedure when the next pin is ready. Qualify control-plane, provider, operator, CRD, and database
compatibility as one unit. Unsupported combinations must fail with a diagnostic that
names the pin (chart render). On a connected environment, compare what is installed
with the pin using kubectl.

1. **Control-plane.** Install OpenEverest N+1 on a fresh hub with the
   N+1 Percona Database Service chart, and connect one environment. Confirm the API serves Instance, Backup, and Restore
   on the CRD versions in the pin. Confirm the adapter still talks to that
   API.
2. **Provider.** Confirm the N+1 PXC and PostgreSQL provider charts install
   on the connected environment and reconcile Instance objects in `pds`.
3. **Operator.** Point kubectl at the connected environment and confirm the operator versions the
   providers deploy match the pin:

   ```bash
   kubectl get deploy -A -l app.kubernetes.io/name=operator -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.spec.template.spec.containers[0].image}{"\n"}{end}'
   kubectl get deploy -A -l app.kubernetes.io/name=pg-operator -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.spec.template.spec.containers[0].image}{"\n"}{end}'
   ```

   OpenEverest and the providers are in `everest-system`:

   ```bash
   kubectl -n everest-system get deploy -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.template.spec.containers[0].image}{"\n"}{end}'
   ```
4. **CRD.** Diff the Instance, Backup, and Restore CRDs from N to N+1.
   Storage versions, served versions, and conversion must remain usable by
   the adapter client constants. A removed or renamed field the adapter
   writes is a failed qualification.
5. **Database.** Create a MySQL Dev instance and a PostgreSQL Dev instance through the hub. Record the engine image the operator selected
   on the connected environment. Exercise the `hub-smoke` lifecycle. Engine versions are those operator
   defaults, not independently chosen tags.

Do not publish a pin that passes only a subset of those five. Do not offer
a matrix of mix-and-match rows.

## Diagnostics

| When | What fails | What to do |
|------|------------|------------|
| Pin, Chart.yaml, values, or this page disagree | `scripts/check-compatibility.sh` names both versions and the pin | Edit the pin and the matching files in one PR |
| `helm template` / install with `platform.*` off the pin | Chart fails and names `compatibility.yaml` | Drop the override or bump the whole unit |
| Installed OpenEverest, provider, or operator version is not the pin | `kubectl get deploy` in `everest-system`, and the operator image from the commands above, names a version other than this page | Install the pinned component, or bump the pin after qualification |

Hand-picking an OpenEverest tag, a provider tag, and an operator tag that
are not this unit is not supported, even if Helm will fetch them.
