#!/usr/bin/env bash
#
# Create an EKS cluster with worker nodes, install Percona Database Service and Flux, and
# join this hub. The hub URL and Git URL are baked in. Run from a machine that
# can reach AWS:
#
#   curl -fsSL __HUB_URL__/api/internal/connect/bootstrap.sh | bash -s -- --token TOKEN
#
# The region is asked on a terminal unless --region is passed. An empty
# answer uses the AWS CLI's own region, then the default baked into this
# script.
#
# An environment that has already joined moves to this control plane's release
# with --update, run where kubectl points at its Kubernetes cluster:
#
#   curl -fsSL __HUB_URL__/api/internal/connect/bootstrap.sh | bash -s -- --update --token TOKEN
#
# The AWS resources a join created are deleted with --delete. It needs no token:
#
#   curl -fsSL __HUB_URL__/api/internal/connect/bootstrap.sh | bash -s -- --delete --name NAME --region REGION
#
set -euo pipefail

HUB_URL='__HUB_URL__'
GIT_URL='__GIT_URL__'
REGION='__DEFAULT_REGION__'
# Set when --region was passed, so an unattended run is not asked.
REGION_FROM_FLAG=0
TOKEN=""
RELEASE="${RELEASE:-pds-agent}"
AGENT_NAMESPACE="${AGENT_NAMESPACE:-pds-agent}"
PLATFORM_RELEASE_NAMED=0
if [[ -n "${PLATFORM_RELEASE:-}" ]]; then
  PLATFORM_RELEASE_NAMED=1
fi
PLATFORM_RELEASE="${PLATFORM_RELEASE:-pds}"
PLATFORM_NAMESPACE="${PLATFORM_NAMESPACE:-everest-system}"
# Four nodes so one Dev, one Staging, and one Prod fit together. Staging and
# Prod each place one member on every node, and the node that also holds Dev
# does not have 2 CPU left on three t3.2xlarge nodes.
NODES="${NODES:-4}"
PUBLIC_NODES=0
LOCAL=0
UPDATE=0
DELETE=0
DELETE_NAME=""
# The flags that came next to --update or --delete, so the refusal can name them.
OTHER_FLAGS=""
NODE_TYPE="${NODE_TYPE:-t3.2xlarge}"
NODEGROUP="${NODEGROUP:-ng-1}"
FLUX_VERSION="${FLUX_VERSION:-2.5.1}"
# SHA-256 of the install.yaml for the default FLUX_VERSION above. A GitHub
# release asset is mutable, and this script pipes it straight into kubectl with
# cluster-admin, so the digest is the only thing standing between a replaced
# asset and arbitrary workloads in a customer cluster. Recompute with:
#   curl -fsSL https://github.com/fluxcd/flux2/releases/download/v<ver>/install.yaml | shasum -a 256
# Flux is Apache-2.0, the same license as this repository, which is why it
# can be installed into customer clusters.
FLUX_PINNED_VERSION="2.5.1"
FLUX_PINNED_SHA256="1ab601012b35e0a84c817f839edb2a52f3fabab2a33d5498b29cf96f9feb57b2"
FLUX_SHA256="${FLUX_SHA256:-}"
REGISTRY_PULL_SECRET_NAME="${REGISTRY_PULL_SECRET_NAME:-ghcr-pull}"
# What a failed run does with the resources it created. A control plane costs
# the same kept or deleted and takes over fifteen minutes to rebuild, and a
# dropped connection says nothing about whether it is usable, so keeping is
# the default and destroying is something you ask for.
ON_FAILURE="${ON_FAILURE:-keep}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-}"
STATUS_ONLY=0
DRY_RUN=0
# eksctl's own create timeout defaults to 25m while its delete was allowed 45m,
# so a create could time out with more time left for the teardown than for the
# thing being built. These are aligned: a create gets longer than every wait
# this script performs, and a delete keeps its longer budget because it runs
# against resources that are already in use.
CLUSTER_CREATE_TIMEOUT="${CLUSTER_CREATE_TIMEOUT:-40m}"
CLUSTER_DELETE_TIMEOUT="${CLUSTER_DELETE_TIMEOUT:-45m}"
# How long --delete waits for the databases or for one load balancer to go
# before it warns and carries on. eksctl removes what is left.
WORKLOAD_DELETE_TIMEOUT="${WORKLOAD_DELETE_TIMEOUT:-10m}"
# Wait budgets in seconds, and the gap between polls. Every loop that uses
# them prints a heartbeat, so a long wait can be told apart from a hang.
WAIT_INTERVAL="${WAIT_INTERVAL:-15}"
CLUSTER_WAIT_TIMEOUT="${CLUSTER_WAIT_TIMEOUT:-2400}"
NODEGROUP_WAIT_TIMEOUT="${NODEGROUP_WAIT_TIMEOUT:-1200}"
READY_NODE_TIMEOUT="${READY_NODE_TIMEOUT:-600}"
RETRY_DELAY="${RETRY_DELAY:-5}"

usage() {
  cat <<EOF >&2
usage: bootstrap.sh --token TOKEN [--region REGION] [--local]
                    [--public-nodes] [--on-failure keep|rollback]
                    [--kubernetes-version V] [--status] [--dry-run]
       bootstrap.sh --update --token TOKEN
       bootstrap.sh --delete --name NAME --region REGION

  --token TOKEN     Join token from the Create an environment page
  --region REGION   AWS region. Asked on a terminal when omitted.
                    An empty answer uses your AWS configuration, or
                    ${REGION} when you have not set one. A run with
                    no terminal uses that default without asking.
  --local           Join the Kubernetes cluster kubectl already points at.
                    Creates no AWS resources: no EKS cluster, no S3 bucket.
                    Use it for kind, minikube, or any Kubernetes cluster you
                    already run.
  --public-nodes    Put worker nodes on public subnets with no NAT gateway.
                    Cheaper, and the nodes get public IPs.
  --on-failure P    What a failed run does with what it created:
                    keep (default) leaves everything and prints how to
                    resume, rollback deletes it. Ctrl-C always keeps,
                    whichever policy is set, because stopping is a pause.
  --kubernetes-version V
                    Kubernetes version for a new EKS cluster. Defaults to the
                    newest version this region offers.
  --status          Print what a run that did not finish created, with the
                    command that deletes it, and exit. Changes nothing. A
                    run that finished keeps no record, so for a joined
                    environment run --delete.
  --dry-run         Print what a run would create and exit. Does not
                    spend the invitation.
  --update          Move an environment that already joined to this control
                    plane's release: the agent, OpenEverest, the providers,
                    the operators and Git reconciliation, in one Helm upgrade.
                    Run it where kubectl points at that environment's
                    Kubernetes cluster. It takes --token and nothing else,
                    creates no AWS resources, and refuses a downgrade, a
                    version too old to update from, and a database or backup
                    that is busy. Updating in place is not a tested upgrade
                    path: back up your databases first.
  --delete          Delete the AWS resources a join created for the
                    environment NAME in REGION: its EKS cluster, the backup
                    bucket, the backup IAM user and the EBS CSI role. It finds
                    them by the percona.com/pds tag with NAME as its value and
                    lists them. It asks you to type the environment name
                    first, and deletes nothing before. When kubectl reaches the
                    EKS cluster it deletes the databases and load balancers
                    first, and when it cannot it warns and carries on. It
                    refuses an EKS cluster without the tag. It takes --name and
                    --region and nothing else, needs no token, and leaves the
                    environment in the console: disconnect it there afterwards.
  --name NAME       The environment --delete deletes.

environment:
  NODES                          worker nodes (default: ${NODES})
  NODE_TYPE                      instance type (default: ${NODE_TYPE})
  NODEGROUP                      node group name (default: ${NODEGROUP})
  ROLLOUT_TIMEOUT                how long to wait for the agent to join (default: 10m)
  FLUX_VERSION                   Git reconciliation release (default: ${FLUX_VERSION})
  FLUX_SHA256                    Expected sha256 of that release install.yaml.
                                 Required when FLUX_VERSION is overridden. Use
                                 "skip" to install without verifying.
  PLATFORM_RELEASE               Helm release (default: ${PLATFORM_RELEASE})
  PLATFORM_NAMESPACE             Helm namespace (default: ${PLATFORM_NAMESPACE})
  AGENT_NAMESPACE                sidecar agent namespace (default: ${AGENT_NAMESPACE})
  PDS_CHART                      path to a charts/pds checkout
  PDS_CHART_OCI                  released chart reference
  PDS_CHART_VERSION              released chart version
  BUCKET                         S3 bucket for backups (default: pds-<account>-<environment>)
  REGISTRY_PULL_SECRET_NAME      dockerconfigjson Secret for a private GHCR agent image (default: ghcr-pull)

This creates real AWS resources: an EKS control plane, ${NODES} ${NODE_TYPE}
nodes, an S3 bucket, an IAM user and the EBS CSI role. Each one it creates
carries the tag percona.com/pds with the environment name as its value, so
--delete can tell them from your own. A bucket it could not tag is the
exception, and --delete does not find it. After a run that did not finish,
--status prints the command that deletes what it made.
EOF
  exit 2
}

# valid_on_failure guards the policy that decides whether a failed run deletes
# a control plane. A typo must not fall through to either behaviour.
valid_on_failure() {
  case "${1:-}" in
    keep | rollback) return 0 ;;
    *) return 1 ;;
  esac
}

# have_tty is true when this process has a terminal to ask. A curl pipe
# leaves stdin on the script, so the answer is read from /dev/tty. A run
# with no terminal must take the default instead of waiting for an answer
# that will never come.
have_tty() {
  : 2>/dev/null </dev/tty
}

# delete_read_cluster sets CLUSTER_STATE to absent, unreadable or present, and
# CLUSTER_TAG to the EKS cluster's percona.com/pds tag when it is present and
# carries one.
delete_read_cluster() {
  local name="$1" region="$2" value
  CLUSTER_STATE=present
  CLUSTER_TAG=""
  if value="$(aws eks describe-cluster --name "$name" --region "$region" \
    --query 'cluster.tags."percona.com/pds"' --output text 2>"${DELETE_TMP}/describe.err")"; then
    if [[ "$value" != "None" ]]; then
      CLUSTER_TAG="$value"
    fi
    return 0
  fi
  if grep -q ResourceNotFoundException "${DELETE_TMP}/describe.err"; then
    CLUSTER_STATE=absent
  else
    CLUSTER_STATE=unreadable
  fi
}

# delete_read_stack sets STACK_STATE to absent, unreadable or present, and
# STACK_OWNED to 1 when the CloudFormation stack carries this environment's
# percona.com/pds tag. A stack of that name without the tag is someone else's.
delete_read_stack() {
  local stack="$1" region="$2" name="$3" value
  STACK_STATE=present
  STACK_OWNED=0
  if ! value="$(aws cloudformation describe-stacks --stack-name "$stack" --region "$region" \
    --query "Stacks[0].Tags[?Key=='percona.com/pds'].Value | [0]" --output text 2>"${DELETE_TMP}/stack.err")"; then
    if grep -q "does not exist" "${DELETE_TMP}/stack.err"; then
      STACK_STATE=absent
    else
      STACK_STATE=unreadable
    fi
    return 0
  fi
  if [[ "$value" == "$name" ]]; then
    STACK_OWNED=1
  fi
}

# delete_tagged_buckets prints the S3 buckets in the region that carry the
# environment's tag, one a line.
delete_tagged_buckets() {
  local name="$1" region="$2" arns
  arns="$(aws resourcegroupstaggingapi get-resources --region "$region" \
    --resource-type-filters s3 --tag-filters "Key=percona.com/pds,Values=${name}" \
    --query 'ResourceTagMappingList[].ResourceARN' --output text)" || return 1
  printf '%s\n' "$arns" | tr '\t' '\n' | sed -e 's/^arn:aws:s3::://' -e '/^$/d'
}

# delete_still_tagged prints what the tag search still lists in the region, one
# ARN a line. The search can lag a few minutes behind a delete, so it informs
# the closing message and never decides whether something was deleted.
delete_still_tagged() {
  local name="$1" region="$2" arns
  arns="$(aws resourcegroupstaggingapi get-resources --region "$region" \
    --tag-filters "Key=percona.com/pds,Values=${name}" \
    --query 'ResourceTagMappingList[].ResourceARN' --output text 2>/dev/null)" || return 0
  printf '%s\n' "$arns" | tr '\t' '\n' | sed -e '/^$/d'
}

# delete_tagged_users prints the IAM users that carry the environment's tag.
# IAM is global and the tagging API reaches it through one region only, so the
# users are listed and each one's tag is read. A tag that cannot be read stops
# the run and says why: a user left out of the plan keeps a live access key.
delete_tagged_users() {
  local name="$1" users user value
  users="$(aws iam list-users --query 'Users[].UserName' --output text)" || return 1
  for user in $users; do
    if ! value="$(aws iam list-user-tags --user-name "$user" \
      --query "Tags[?Key=='percona.com/pds'].Value | [0]" --output text 2>"${DELETE_TMP}/tags.err")"; then
      # A user deleted since the list was read has no tags to read.
      grep -q NoSuchEntity "${DELETE_TMP}/tags.err" && continue
      echo "could not read the tags of IAM user ${user}: $(cat "${DELETE_TMP}/tags.err")" >&2
      return 1
    fi
    if [[ "$value" == "$name" ]]; then
      printf '%s\n' "$user"
    fi
  done
}

# delete_connect_kubectl points kubectl at the EKS cluster with a kubeconfig of
# this run's own, so no other Kubernetes cluster is reached by mistake, and sets
# KUBE_READY when it can reach it.
delete_connect_kubectl() {
  local name="$1" region="$2"
  KUBE_READY=0
  if ! command -v kubectl >/dev/null; then
    echo "kubectl is not installed, so the databases and load balancers on EKS cluster ${name} are not deleted first. Carrying on." >&2
    return 0
  fi
  export KUBECONFIG="${DELETE_TMP}/kubeconfig"
  if ! aws eks update-kubeconfig --name "$name" --region "$region" >/dev/null 2>&1 \
    || ! kubectl get namespaces --request-timeout=20s >/dev/null 2>&1; then
    echo "cannot reach EKS cluster ${name} with kubectl, so its databases and load balancers are not deleted first. Carrying on." >&2
    return 0
  fi
  KUBE_READY=1
}

# delete_workloads deletes the databases and every load balancer while the EKS
# cluster still runs. A load balancer that is left holds network interfaces in
# the VPC, which then cannot be deleted, and deleting a Service waits for its
# load balancer to go. A step that will not finish is a warning: eksctl deletes
# what is left.
delete_workloads() {
  local name="$1" services namespace service
  [[ "$KUBE_READY" == 1 ]] || return 0
  echo "deleting the databases on EKS cluster ${name}"
  kubectl delete kustomization instances -n flux-system --ignore-not-found --wait --timeout=5m \
    || echo "could not delete the Git reconciliation, so a database may come back. Carrying on." >&2
  kubectl delete instances.core.openeverest.io --all -A --wait --timeout="$WORKLOAD_DELETE_TIMEOUT" \
    || echo "could not delete every database. Carrying on." >&2
  echo "deleting the load balancers"
  services="$(kubectl get svc -A -o jsonpath='{range .items[?(@.spec.type=="LoadBalancer")]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}')" \
    || services=""
  while read -r namespace service; do
    [[ -n "$service" ]] || continue
    kubectl delete svc -n "$namespace" "$service" --wait --timeout="$WORKLOAD_DELETE_TIMEOUT" \
      || echo "load balancer ${service} in ${namespace} is still there. Carrying on." >&2
  done <<<"$services"
}

# cluster_volumes prints one line per EBS volume the EBS CSI driver tagged for
# an EKS cluster, as "id size state". The tag is the only link between a volume
# and its cluster that outlives the nodes, and it is read from AWS, so a volume
# created after anything was written down is found too. A volume tagged for
# another EKS cluster is never named by the filter.
cluster_volumes() {
  local name="$1" region="$2" volumes
  volumes="$(aws ec2 describe-volumes --region "$region" \
    --filters "Name=tag:kubernetes.io/cluster/${name},Values=owned" \
    --query 'Volumes[].[VolumeId,Size,State]' --output text)" || return 1
  awk -F'\t' '$1 ~ /^vol-[0-9a-f]+$/ {print $1, $2, $3}' <<<"$volumes"
}

# delete_cluster_volumes deletes the volumes cluster_volumes finds, once the
# nodes are gone. They are read again here because the plan can be minutes old.
# A volume that is still in use is waited on while it detaches. One that stays
# in use, or cannot be deleted, is printed with the command that deletes it,
# because a run that finds the EKS cluster gone has no other way to name it.
delete_cluster_volumes() {
  local name="$1" region="$2" volumes volume size state attempt status=0
  for attempt in 1 2 3 4 5 6; do
    volumes="$(cluster_volumes "$name" "$region")" || {
      echo "could not list the EBS volumes tagged for EKS cluster ${name}, so any it left stay in your account." >&2
      return 1
    }
    grep -q ' in-use$' <<<"$volumes" || break
    [[ "$attempt" == 6 ]] || sleep "$WAIT_INTERVAL"
  done
  while read -r volume size state; do
    [[ -n "$volume" && "$state" != deleting ]] || continue
    if [[ "$state" != available ]]; then
      echo "EBS volume ${volume} is ${state}, so it is not deleted. Delete it yourself when it is available:" >&2
      echo "  aws ec2 delete-volume --region ${region} --volume-id ${volume}" >&2
      status=1
      continue
    fi
    echo "deleting EBS volume ${volume}"
    if ! aws ec2 delete-volume --region "$region" --volume-id "$volume"; then
      echo "could not delete EBS volume ${volume}. Delete it yourself:" >&2
      echo "  aws ec2 delete-volume --region ${region} --volume-id ${volume}" >&2
      status=1
    fi
  done <<<"$volumes"
  return "$status"
}

# delete_iam_user deletes the backup user. IAM refuses a user that still has
# an access key or an inline policy, so both go first.
delete_iam_user() {
  local user="$1" keys key policies policy
  keys="$(aws iam list-access-keys --user-name "$user" --query 'AccessKeyMetadata[].AccessKeyId' --output text)" || return 1
  for key in $keys; do
    aws iam delete-access-key --user-name "$user" --access-key-id "$key" || return 1
  done
  policies="$(aws iam list-user-policies --user-name "$user" --query PolicyNames --output text)" || return 1
  for policy in $policies; do
    aws iam delete-user-policy --user-name "$user" --policy-name "$policy" || return 1
  done
  aws iam delete-user --user-name "$user"
}

# delete_bucket deletes a backup bucket. One the tag search still lists after
# it was deleted is already gone.
delete_bucket() {
  local bucket="$1"
  if aws s3 rb "s3://${bucket}" --force 2>"${DELETE_TMP}/bucket.err"; then
    return 0
  fi
  if grep -q NoSuchBucket "${DELETE_TMP}/bucket.err"; then
    return 0
  fi
  cat "${DELETE_TMP}/bucket.err" >&2
  return 1
}

# delete_environment deletes what a join created in AWS for one environment. It
# finds everything by the percona.com/pds tag the join puts on it, so a list in
# this script cannot drift from what the create side builds, and it deletes
# nothing until the name is typed on the terminal. A curl pipe leaves stdin on
# the script, so the answer comes from /dev/tty.
delete_environment() {
  local name="$1" region="$2"
  if [[ ! "$name" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]; then
    echo "environment name must be a DNS label" >&2
    return 2
  fi
  if [[ ! "$region" =~ ^[a-z]{2}(-[a-z]+)+-[0-9]+$ ]]; then
    echo "${region} is not an AWS region name, such as us-west-2." >&2
    return 2
  fi
  local tool
  for tool in aws eksctl; do
    command -v "$tool" >/dev/null || {
      echo "${tool} is required" >&2
      return 1
    }
  done
  DELETE_TMP="$(mktemp -d)"
  trap 'rm -rf "${DELETE_TMP:-}"' EXIT
  KUBE_READY=0

  local account buckets users bucket user volumes volume size state stack arn answer failed=0 leftovers
  local csi_stack="eksctl-${name}-addon-iamserviceaccount-kube-system-ebs-csi-controller-sa"
  local cluster_stack="eksctl-${name}-cluster" has_csi=0 has_stack=0
  account="$(aws sts get-caller-identity --query Account --output text)" || {
    echo "could not read the AWS account. Sign in with the AWS profile the join used, then run this command again." >&2
    return 1
  }
  echo "environment ${name}, AWS account ${account}, region ${region}"
  echo "looking for what carries the tag percona.com/pds=${name}"

  delete_read_cluster "$name" "$region"
  if [[ "$CLUSTER_STATE" == unreadable ]]; then
    echo "could not read EKS cluster ${name} in ${region}: $(cat "${DELETE_TMP}/describe.err")" >&2
    echo "Check the region and your AWS permissions. Nothing was deleted." >&2
    return 1
  fi
  if [[ "$CLUSTER_STATE" == present ]]; then
    if [[ -z "$CLUSTER_TAG" ]]; then
      echo "EKS cluster ${name} in ${region} has no percona.com/pds tag, so a join did not create it and this command does not delete it. Nothing was deleted." >&2
      return 1
    fi
    if [[ "$CLUSTER_TAG" != "$name" ]]; then
      echo "EKS cluster ${name} in ${region} is tagged percona.com/pds=${CLUSTER_TAG}, not ${name}, so it belongs to another environment. Nothing was deleted." >&2
      return 1
    fi
  fi
  buckets="$(delete_tagged_buckets "$name" "$region")" || {
    echo "could not list the S3 buckets tagged for ${name}. Nothing was deleted." >&2
    return 1
  }
  users="$(delete_tagged_users "$name")" || {
    echo "could not read the IAM users' tags. Nothing was deleted." >&2
    return 1
  }
  volumes="$(cluster_volumes "$name" "$region")" || {
    echo "could not list the EBS volumes tagged for EKS cluster ${name}. Nothing was deleted." >&2
    return 1
  }
  for stack in "$csi_stack" "$cluster_stack"; do
    delete_read_stack "$stack" "$region" "$name"
    if [[ "$STACK_STATE" == unreadable ]]; then
      echo "could not read CloudFormation stack ${stack}: $(cat "${DELETE_TMP}/stack.err")" >&2
      echo "Check your AWS permissions. Nothing was deleted." >&2
      return 1
    fi
    if [[ "$stack" == "$csi_stack" ]]; then
      has_csi="$STACK_OWNED"
    else
      has_stack="$STACK_OWNED"
    fi
  done

  if [[ "$CLUSTER_STATE" == absent && -z "$buckets" && -z "$users" && -z "$volumes" && "$has_csi" == 0 && "$has_stack" == 0 ]]; then
    echo "nothing in AWS account ${account}, region ${region} carries the tag percona.com/pds=${name}, so there is nothing to delete. Check the name and the region." >&2
    return 1
  fi
  if [[ "$CLUSTER_STATE" == present ]]; then
    delete_connect_kubectl "$name" "$region"
  fi

  echo "This deletes the following in AWS account ${account}, region ${region}:"
  if [[ "$CLUSTER_STATE" == present ]]; then
    echo "  EKS cluster ${name}, with its nodes, its VPC and every database on it"
  elif [[ "$has_stack" == 1 ]]; then
    echo "  CloudFormation stack ${cluster_stack}, what is left of EKS cluster ${name} with its nodes and its VPC"
  fi
  while read -r volume size state; do
    [[ -n "$volume" ]] || continue
    echo "  EBS volume ${volume}, ${size} GiB, tagged for EKS cluster ${name}, deleted once the nodes are gone if it is available"
  done <<<"$volumes"
  for bucket in $buckets; do
    echo "  S3 bucket ${bucket}, with every backup in it"
  done
  for user in $users; do
    echo "  IAM user ${user}, with its access keys and its policy"
  done
  if [[ "$has_csi" == 1 ]]; then
    echo "  EBS CSI role stack ${csi_stack}"
  fi
  if [[ "$CLUSTER_STATE" == absent ]]; then
    echo "There is no EKS cluster named ${name} in ${region}."
    if [[ -z "$buckets" && "$has_csi" == 0 && "$has_stack" == 0 ]]; then
      echo "Only the backup IAM user was found. If ${name} still runs in another region, stop and check the region first: the access key of that user may be the one its backups use."
    fi
  fi
  if [[ -z "$buckets" ]]; then
    echo "No S3 bucket carries the tag, so none is deleted. A bucket the join could not tag is not found: check for one named pds-<account id>-${name}, or a shortened name that ends in a hash."
  fi
  for bucket in $buckets; do
    echo "To keep a copy of the backups, stop here and run: aws s3 sync s3://${bucket} ./${bucket}"
  done
  echo "It cannot be undone."

  if ! have_tty; then
    echo "this needs a terminal to ask you to confirm, and there is none. Nothing was deleted." >&2
    return 1
  fi
  printf 'Type %s to confirm: ' "$name" >/dev/tty
  answer=""
  IFS= read -r answer </dev/tty || true
  if [[ "$answer" != "$name" ]]; then
    echo "that is not ${name}. Nothing was deleted." >&2
    return 1
  fi

  if [[ "$CLUSTER_STATE" == present || "$has_stack" == 1 ]]; then
    delete_workloads "$name"
    echo "deleting EKS cluster ${name}. This takes a while."
    if ! eksctl delete cluster --name "$name" --region "$region" --wait --force \
      --disable-nodegroup-eviction --timeout "$CLUSTER_DELETE_TIMEOUT"; then
      echo "could not delete EKS cluster ${name}. The backup bucket and the IAM user were kept. Run the same command again." >&2
      return 1
    fi
    # eksctl can report success with part of it left, and --force carries on
    # past errors, so what it deleted is read back before anything else goes.
    # A stack that cannot be read counts as still there.
    delete_read_cluster "$name" "$region"
    local cluster_gone=1
    [[ "$CLUSTER_STATE" == absent ]] || cluster_gone=0
    delete_read_stack "$cluster_stack" "$region" "$name"
    [[ "$STACK_STATE" == absent || ("$STACK_STATE" == present && "$STACK_OWNED" == 0) ]] || cluster_gone=0
    if [[ "$cluster_gone" == 0 ]]; then
      echo "EKS cluster ${name} is still there after the delete, or its stack cannot be read. The backup bucket and the IAM user were kept. Run the same command again." >&2
      return 1
    fi
  fi
  delete_cluster_volumes "$name" "$region" || failed=1
  delete_read_stack "$csi_stack" "$region" "$name"
  if [[ "$STACK_OWNED" == 1 ]]; then
    echo "deleting EBS CSI role stack ${csi_stack}"
    aws cloudformation delete-stack --stack-name "$csi_stack" --region "$region" \
      && aws cloudformation wait stack-delete-complete --stack-name "$csi_stack" --region "$region" \
      || {
        echo "could not delete the EBS CSI role stack ${csi_stack}." >&2
        failed=1
      }
  fi
  for bucket in $buckets; do
    echo "deleting S3 bucket ${bucket}"
    delete_bucket "$bucket" || {
      echo "could not delete S3 bucket ${bucket}." >&2
      failed=1
    }
  done
  for user in $users; do
    echo "deleting IAM user ${user}"
    delete_iam_user "$user" || {
      echo "could not delete IAM user ${user}." >&2
      failed=1
    }
  done
  if [[ "$failed" == 1 ]]; then
    echo "some of it was not deleted. Run the same command again, or delete what is printed above yourself." >&2
    return 1
  fi
  leftovers="$(delete_still_tagged "$name" "$region")"
  if [[ -n "$leftovers" ]]; then
    echo "These are still listed with the tag percona.com/pds=${name}:"
    while read -r arn; do
      echo "  ${arn}"
    done <<<"$leftovers"
    echo "The tag search can lag a few minutes behind a delete. Check again later:"
    echo "  aws resourcegroupstaggingapi get-resources --region ${region} --tag-filters Key=percona.com/pds,Values=${name}"
  else
    echo "nothing in region ${region} carries the tag percona.com/pds=${name} any more."
  fi
  echo "Now disconnect ${name} in the console to remove it from the control plane."
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --token)
      [[ $# -ge 2 ]] || usage
      TOKEN="$2"
      shift 2
      ;;
    --region)
      OTHER_FLAGS="${OTHER_FLAGS} $1"
      [[ $# -ge 2 ]] || usage
      REGION="$2"
      REGION_FROM_FLAG=1
      shift 2
      ;;
    --public-nodes)
      OTHER_FLAGS="${OTHER_FLAGS} $1"
      PUBLIC_NODES=1
      shift
      ;;
    --local)
      OTHER_FLAGS="${OTHER_FLAGS} $1"
      LOCAL=1
      shift
      ;;
    --on-failure)
      OTHER_FLAGS="${OTHER_FLAGS} $1"
      [[ $# -ge 2 ]] || usage
      if ! valid_on_failure "$2"; then
        echo "--on-failure takes keep or rollback, not \"$2\"." >&2
        exit 2
      fi
      ON_FAILURE="$2"
      shift 2
      ;;
    --kubernetes-version)
      OTHER_FLAGS="${OTHER_FLAGS} $1"
      [[ $# -ge 2 ]] || usage
      KUBERNETES_VERSION="$2"
      shift 2
      ;;
    --status)
      OTHER_FLAGS="${OTHER_FLAGS} $1"
      STATUS_ONLY=1
      shift
      ;;
    --dry-run)
      OTHER_FLAGS="${OTHER_FLAGS} $1"
      DRY_RUN=1
      shift
      ;;
    --update)
      UPDATE=1
      shift
      ;;
    --delete)
      DELETE=1
      shift
      ;;
    --name)
      [[ $# -ge 2 ]] || usage
      DELETE_NAME="$2"
      shift 2
      ;;
    -h | --help)
      usage
      ;;
    *)
      usage
      ;;
  esac
done

if [[ "$DELETE" != 1 && -n "$DELETE_NAME" ]]; then
  echo "--name works only with --delete." >&2
  exit 2
fi

# A delete works on what AWS holds, found by its tag, and never calls the
# control plane, so it takes no token and nothing that shapes a join. The
# region is never guessed or asked for: a delete in the wrong one finds nothing
# or, worse, something else.
if [[ "$DELETE" == 1 ]]; then
  strays="${OTHER_FLAGS// --region/}"
  [[ -z "$TOKEN" ]] || strays="${strays} --token"
  [[ "$UPDATE" != 1 ]] || strays="${strays} --update"
  if [[ -n "$strays" ]]; then
    echo "--delete takes --name and --region and nothing else, so it cannot be combined with${strays}." >&2
    exit 2
  fi
  if [[ -z "$DELETE_NAME" || "$REGION_FROM_FLAG" != 1 ]]; then
    echo "--delete needs --name and --region." >&2
    exit 2
  fi
  delete_environment "$DELETE_NAME" "$REGION"
  exit 0
fi

if [[ -z "$TOKEN" ]]; then
  usage
fi

# An update works on the Kubernetes cluster kubectl points at and never touches
# AWS, so every flag that shapes a join is meaningless next to it, and guessing
# which one the caller meant is how an environment gets moved by accident.
if [[ "$UPDATE" == 1 ]]; then
  if [[ -n "$OTHER_FLAGS" ]]; then
    echo "--update takes --token and nothing else, so it cannot be combined with${OTHER_FLAGS}." >&2
    exit 2
  fi
  LOCAL=1
fi

if [[ "$TOKEN" == *$'\n'* || "$TOKEN" == *"\""* || "$HUB_URL" == *$'\n'* || "$HUB_URL" == *"\""* || "$GIT_URL" == *$'\n'* || "$GIT_URL" == *"\""* ]]; then
  echo "token, control plane URL, and Git URL must not contain quotes or newlines" >&2
  exit 1
fi

# Enrollment exchanges this invitation for an agent token, and the spoke
# posts that on every heartbeat. Over plain http either bearer travels in
# cleartext. Reject before enrollment, which is the first request that
# sends the invitation.
require_hub_tls() {
  case "$HUB_URL" in
    https://*) ;;
    http://localhost | http://localhost:* | http://127.0.0.1 | http://127.0.0.1:* | http://host.docker.internal | http://host.docker.internal:*) ;;
    *)
      echo "this control plane is ${HUB_URL}, so the join token would travel in cleartext." >&2
      echo "Contact Percona support." >&2
      exit 1
      ;;
  esac
}
require_hub_tls

now_seconds() {
  printf '%s' "$SECONDS"
}

# elapsed is how long this run has been going, for the phase banners and the
# heartbeats. A wait that prints nothing cannot be told apart from a hang.
elapsed() {
  local total=$(($(now_seconds) - RUN_STARTED))
  printf '%dm%02ds' "$((total / 60))" "$((total % 60))"
}

# phase announces a step and records it, so a failure can say how far the run
# got and the next run can say where the last one stopped.
phase() {
  if [[ -n "${PHASE:-}" && "${PHASE_OPEN:-0}" == 1 ]]; then
    report_phase succeeded ""
  fi
  PHASE="$1"
  PHASE_OPEN=1
  PHASE_STARTED="$(now_seconds)"
  PHASE_NUMBER=$((PHASE_NUMBER + 1))
  printf '\n[%d/%d] %s (%s elapsed)\n' "$PHASE_NUMBER" "$PHASE_TOTAL" "$PHASE" "$(elapsed)"
  save_state
  report_phase started ""
}

# scrub_tail removes credentials before a failed phase leaves the machine.
# The fixtures cover an AWS access key, a secret access key, a password, a
# token, a kubeconfig key, and a PEM block.
scrub_tail() {
  sed -E \
    -e '/-----BEGIN .*PRIVATE KEY-----/,/-----END .*PRIVATE KEY-----/c\
[redacted]' \
    -e 's/(client-certificate-data|client-key-data|certificate-authority-data):[[:space:]]*[^[:space:]]+/\1: [redacted]/g' \
    -e 's/(aws_secret_access_key[[:space:]]*[=:][[:space:]]*)("[^"]*"|'\''[^'\'']*'\''|[^[:space:]]+)/\1[redacted]/Ig' \
    -e 's/(password|token|secret|api[_-]?key)([[:space:]]*[:=][[:space:]]*)("[^"]*"|'\''[^'\'']*'\''|[^[:space:]]+)/\1\2[redacted]/Ig' \
    -e 's/[Bb]earer[[:space:]]+[^[:space:]]+/Bearer [redacted]/g' \
    -e 's/eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}/[redacted]/g' \
    -e 's/(AKIA|ASIA)[0-9A-Z]{16}/[redacted]/g'
}

# report_phase tells the hub how far this join got. A missing hub is not a
# failed join: the report is best effort.
report_phase() {
  local outcome="$1" output="${2:-}"
  # The hub treats a phase report made with the join token as using the token
  # up, and an update's first phases run before its enrollment. Reporting waits
  # until the agent token has replaced it.
  if [[ "${UPDATE:-0}" == 1 && "${UPDATE_ENROLLED:-0}" != 1 ]]; then
    return 0
  fi
  [[ -n "${HUB_URL:-}" && -n "${TOKEN:-}" && -n "${NAME:-}" && -n "${PHASE:-}" ]] || return 0
  local duration=0 finished=""
  if [[ -n "${PHASE_STARTED:-}" ]]; then
    duration=$(($(now_seconds) - PHASE_STARTED))
  fi
  if [[ "$outcome" != "started" ]]; then
    finished="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  fi
  # Redact the whole tail before shortening it. Cutting first can split a
  # key name off the value that follows it, and the value would then leave
  # the machine unchanged.
  redacted="$(printf '%s' "$output" | scrub_tail)"
  output="$(printf '%s' "$redacted" | tail -c 4000)"
  RUN_ID="${RUN_ID:-local-$$}"
  local body auth_header finished_json
  if [[ -n "$finished" ]]; then
    finished_json="$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$finished")"
  else
    finished_json=null
  fi
  body="$(PHASE="$PHASE" OUTCOME="$outcome" RUN_ID="$RUN_ID" DURATION="$((duration * 1000))" FINISHED="$finished_json" python3 -c '
import json, os, sys
print(json.dumps({
    "runId": os.environ["RUN_ID"],
    "phase": os.environ["PHASE"],
    "outcome": os.environ["OUTCOME"],
    "durationMs": int(os.environ["DURATION"]),
    "finishedAt": json.loads(os.environ["FINISHED"]),
    "output": sys.stdin.read(),
}))
' <<<"$output")"
  auth_header="$(mktemp)"
  printf 'Authorization: Bearer %s\n' "$TOKEN" >"$auth_header"
  curl_once -sS -o /dev/null -X POST -H @"$auth_header" -H 'Content-Type: application/json' \
    --data "$body" "${HUB_URL}/api/internal/environments/${NAME}/join" || true
  rm -f "$auth_header"
}

# One file per cluster, region, and AWS account (or "local"), so two joins
# of the same name in different accounts do not overwrite each other's
# progress. A file from before account scoping is still read.
state_scope() {
  if [[ "${LOCAL:-0}" == 1 ]]; then
    printf 'local'
    return 0
  fi
  if [[ -n "${AWS_ACCOUNT_ID:-}" ]]; then
    printf '%s' "$AWS_ACCOUNT_ID"
    return 0
  fi
  local account
  account="$(aws sts get-caller-identity --query Account --output text 2>/dev/null || true)"
  if [[ "$account" =~ ^[0-9]{12}$ ]]; then
    AWS_ACCOUNT_ID="$account"
    printf '%s' "$AWS_ACCOUNT_ID"
    return 0
  fi
}

legacy_state_path() {
  printf '%s/pds/join-%s-%s.env' "${XDG_STATE_HOME:-$HOME/.local/state}" "$1" "$2"
}

state_path() {
  local name="$1" region="$2" scope
  scope="$(state_scope)"
  if [[ -n "$scope" ]]; then
    printf '%s/pds/join-%s-%s-%s.env' "${XDG_STATE_HOME:-$HOME/.local/state}" "$name" "$region" "$scope"
  else
    printf '%s/pds/join-%s-%s.env' "${XDG_STATE_HOME:-$HOME/.local/state}" "$name" "$region"
  fi
}

# owned_flag is 1 when this run created the resource or an earlier attempt on
# this machine already recorded that it did. CREATED_* stays this process
# only, so rollback still cannot delete something it did not make.
owned_flag() {
  if [[ "${1:-0}" == 1 || "${2:-0}" == 1 ]]; then
    printf '1'
  else
    printf '0'
  fi
}

# recorded_key_ids lists every access key that runs which did not finish
# minted, oldest first. Each such run mints its own key and keeps the one
# before it until a run finishes, so one slot would forget the earlier key
# and leave it live.
recorded_key_ids() {
  local ids="${STATE_ACCESS_KEY_IDS:-}" key="${CREATED_ACCESS_KEY_ID:-}"
  if [[ -n "$key" && " $ids " != *" $key "* ]]; then
    ids="${ids:+${ids} }${key}"
  fi
  printf '%s' "$ids"
}

# save_state records the phase reached and which resources this machine created.
# Ownership has to survive the process, or a second run cannot tell a resource
# it made itself from one that was already there. A resumed run merges the
# loaded STATE_* flags with this process's CREATED_* flags so a later --status
# still knows what the earlier attempt built.
save_state() {
  [[ "${UPDATE:-0}" != 1 ]] || return 0
  [[ -n "${NAME:-}" && -n "${REGION:-}" ]] || return 0
  local path dir legacy
  path="$(state_path "$NAME" "$REGION")"
  dir="$(dirname "$path")"
  if ! mkdir -p "$dir"; then
    echo "cannot write resume state under ${dir}" >&2
    return 1
  fi
  chmod 700 "$dir" 2>/dev/null || true
  # Deliberately not the CREATED_ names this run uses. Reading the file must
  # not be able to tell this process that it built something it did not.
  cat >"$path" <<EOF
PHASE_LAST='${PHASE}'
STATE_CLUSTER=$(owned_flag "${CREATED_CLUSTER:-0}" "${STATE_CLUSTER:-0}")
STATE_NODEGROUP=$(owned_flag "${CREATED_NODEGROUP:-0}" "${STATE_NODEGROUP:-0}")
STATE_BUCKET=$(owned_flag "${CREATED_BUCKET:-0}" "${STATE_BUCKET:-0}")
STATE_FLUX=$(owned_flag "${CREATED_FLUX:-0}" "${STATE_FLUX:-0}")
STATE_AGENT=$(owned_flag "${CREATED_AGENT:-0}" "${STATE_AGENT:-0}")
STATE_IAM_USER=$(owned_flag "${CREATED_IAM_USER:-0}" "${STATE_IAM_USER:-0}")
STATE_PLATFORM=$(owned_flag "${INSTALLED_PLATFORM:-0}" "${STATE_PLATFORM:-0}")
STATE_CSI_STACK=$(owned_flag "${CREATED_CSI_STACK:-0}" "${STATE_CSI_STACK:-0}")
STATE_ACCESS_KEY_IDS='$(recorded_key_ids)'
EOF
  chmod 600 "$path" 2>/dev/null || true
  legacy="$(legacy_state_path "$NAME" "$REGION")"
  if [[ "$legacy" != "$path" && -f "$legacy" ]]; then
    rm -f "$legacy"
  fi
}

# load_state reports what an earlier attempt left behind. It deliberately
# does not fold that into what this run may delete. A control plane an earlier
# run created can be in service by the time anyone runs the command again, and
# rollback is only ever allowed to remove what the process asking for it made.
# Deleting on inherited ownership would rebuild the bug this change removes.
load_state() {
  [[ "${UPDATE:-0}" != 1 ]] || return 0
  [[ -n "${NAME:-}" && -n "${REGION:-}" ]] || return 0
  local path legacy
  path="$(state_path "$NAME" "$REGION")"
  if [[ ! -f "$path" ]]; then
    legacy="$(legacy_state_path "$NAME" "$REGION")"
    if [[ -f "$legacy" ]]; then
      path="$legacy"
    fi
  fi
  [[ -f "$path" ]] || return 0
  # shellcheck source=/dev/null
  source "$path"
  if [[ -n "${PHASE_LAST:-}" ]]; then
    echo "the last run for ${NAME} stopped at: ${PHASE_LAST}"
    echo "Nothing it created is deleted by this run. To remove any of it, use"
    echo "the commands --status prints."
  fi
  return 0
}

clear_state() {
  [[ "${UPDATE:-0}" != 1 ]] || return 0
  [[ -n "${NAME:-}" && -n "${REGION:-}" ]] || return 0
  rm -f "$(state_path "$NAME" "$REGION")" "$(legacy_state_path "$NAME" "$REGION")"
}

# retry runs an idempotent step again with a widening gap. It is deliberately
# not used around anything that creates a new object per call: an IAM access
# key minted twice leaves a key nobody tracks.
retry() {
  local attempts="$1" label="$2"
  shift 2
  local attempt=1 delay="$RETRY_DELAY"
  while true; do
    if "$@"; then
      return 0
    fi
    if [[ "$attempt" -ge "$attempts" ]]; then
      echo "${label} failed ${attempts} times." >&2
      return 1
    fi
    echo "  ${label} failed, retrying in ${delay}s (attempt ${attempt} of ${attempts})" >&2
    sleep "$delay"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
}

# curl_retry is for idempotent downloads. Without these flags one dropped
# second exited non-zero, and that exit used to delete the cluster.
# --retry-all-errors is deliberately absent: it would also repeat a request
# the hub already answered.
curl_retry() {
  curl --retry 5 --retry-connrefused --retry-delay 2 \
    --connect-timeout 10 --max-time 300 "$@"
}

# curl_once is for enrollment. The invitation is spent on the first answer,
# so retrying a dropped 200 would 401 and leave the minted agent nowhere.
curl_once() {
  curl --connect-timeout 10 --max-time 300 "$@"
}

tools=(curl kubectl python3 helm tar awk)
if [[ "$LOCAL" != 1 ]]; then
  tools+=(aws eksctl)
fi
for tool in "${tools[@]}"; do
  command -v "$tool" >/dev/null || {
    echo "${tool} is required" >&2
    exit 1
  }
done

CREATED_CLUSTER=0
CREATED_NODEGROUP=0
CREATED_BUCKET=0
CREATED_FLUX=0
APPLIED_FLUX=0
FLUX_APPLIED_NAMES=""
CSI_ROLE_ARN=""
CREATED_AGENT=0
INSTALLED_PLATFORM=0
CREATED_IAM_USER=0
CREATED_CSI_STACK=0
CREATED_ACCESS_KEY_ID=""
KEPT_ACCESS_KEY_ID=""
CHART_DEPS_UPDATED=""
APPLIED_BUCKET_TO_EXISTING=0
APPLIED_IDENTITY_TO_EXISTING=0
BUCKET="${BUCKET:-}"
# An interrupt is a pause, not a request to destroy anything, so it is tracked
# separately from the failure policy and outranks it.
INTERRUPTED=0
PHASE=""
PHASE_LAST=""
PHASE_NUMBER=0
PHASE_TOTAL=7
if [[ "$LOCAL" == 1 ]]; then
  PHASE_TOTAL=6
fi
if [[ "$UPDATE" == 1 ]]; then
  PHASE_TOTAL=4
fi
UPDATE_STARTED=0
UPDATE_ENROLLED=0
UPDATE_ATTEMPTED_MANIFEST=""
RUN_STARTED="$(now_seconds)"

tmp="$(mktemp -d)"
PHASE_LOG="${tmp}/phase.log"
touch "$PHASE_LOG"
# Keep a copy of what this run prints so a failed phase can send the tail.
# The redirect is not a pipe, so the script's own exit status stays intact.
exec > >(tee -a "$PHASE_LOG") 2>&1

# An AWS join goes through a kubeconfig of its own: aws eks update-kubeconfig
# rewrites current-context wherever it writes, and a customer whose context
# pointed at something else should not find it moved because they joined a
# cluster. A local join is the opposite: the current context is the cluster
# the caller is asking to join, so it uses their config unchanged.
if [[ "$LOCAL" != 1 ]]; then
  export KUBECONFIG="${tmp}/kubeconfig"
fi

# run_created_something is true when this run, or an earlier one recorded in
# the state file, built anything that is still standing.
run_created_something() {
  local flag
  for flag in "$CREATED_CLUSTER" "$CREATED_NODEGROUP" "$CREATED_BUCKET" \
    "$CREATED_FLUX" "$CREATED_AGENT" "$CREATED_IAM_USER" "$INSTALLED_PLATFORM" \
    "$CREATED_CSI_STACK" "${STATE_CLUSTER:-0}" "${STATE_NODEGROUP:-0}" \
    "${STATE_BUCKET:-0}" "${STATE_FLUX:-0}" "${STATE_AGENT:-0}" \
    "${STATE_IAM_USER:-0}" "${STATE_PLATFORM:-0}" "${STATE_CSI_STACK:-0}"; do
    [[ "$flag" == 1 ]] && return 0
  done
  return 1
}

# created_this_run is true only for resources this process made. Rollback
# still keys off that, not the inherited STATE_* record.
created_this_run() {
  local flag
  for flag in "$CREATED_CLUSTER" "$CREATED_NODEGROUP" "$CREATED_BUCKET" \
    "$CREATED_FLUX" "$CREATED_AGENT" "$CREATED_IAM_USER" "$INSTALLED_PLATFORM"; do
    [[ "$flag" == 1 ]] && return 0
  done
  return 1
}

# print_delete_command is how a join's AWS resources are removed: one command
# that finds them by their percona.com/pds tag, so no list here can drift from
# what the create side builds. It prints when this machine recorded creating
# any of them, including an earlier attempt, and says what happens when the EKS
# cluster was not made here, because the command refuses an EKS cluster that
# does not carry this environment's tag. Rollback still only deletes what this
# process made.
print_delete_command() {
  if [[ "${CREATED_CLUSTER}" == 1 || "${STATE_CLUSTER:-0}" == 1 \
    || "${CREATED_NODEGROUP}" == 1 || "${STATE_NODEGROUP:-0}" == 1 \
    || "${CREATED_BUCKET}" == 1 || "${STATE_BUCKET:-0}" == 1 \
    || "${CREATED_IAM_USER:-0}" == 1 || "${STATE_IAM_USER:-0}" == 1 \
    || "${CREATED_CSI_STACK:-0}" == 1 || "${STATE_CSI_STACK:-0}" == 1 \
    || -n "$(recorded_key_ids)" ]]; then
    echo "  curl -fsSL '${HUB_URL}/api/internal/connect/bootstrap.sh' | bash -s -- --delete --name ${NAME} --region ${REGION}"
  fi
  if [[ "${CREATED_CLUSTER}" != 1 && "${STATE_CLUSTER:-0}" != 1 ]] \
    && [[ "${CREATED_NODEGROUP}" == 1 || "${STATE_NODEGROUP:-0}" == 1 \
    || "${CREATED_BUCKET}" == 1 || "${STATE_BUCKET:-0}" == 1 \
    || "${CREATED_IAM_USER:-0}" == 1 || "${STATE_IAM_USER:-0}" == 1 \
    || "${CREATED_CSI_STACK:-0}" == 1 || "${STATE_CSI_STACK:-0}" == 1 \
    || -n "$(recorded_key_ids)" ]]; then
    echo "  If EKS cluster ${NAME} was already there, --delete refuses it, because the join did not create it. What this run added beside it then stays until you delete it yourself."
  fi
}

# keep_notice explains a kept failure: how far the run got, that running the
# same command again carries on from here, and the exact commands that remove
# what is left. Being told a cluster was kept without being told its name
# leaves the customer unable to act on either choice.
keep_notice() {
  local kept=0
  if run_created_something; then
    kept=1
  fi
  if [[ "$kept" == 0 && -z "${PHASE:-}" ]]; then
    return 0
  fi
  if [[ "${INTERRUPTED:-0}" == 1 ]]; then
    echo "interrupted. Nothing was deleted." >&2
  else
    echo "the join did not finish. Nothing was deleted." >&2
  fi
  if [[ -n "${PHASE:-}" ]]; then
    echo "  it stopped at: ${PHASE}" >&2
  fi
  echo "Run the same command again to carry on from here." >&2
  if [[ "$kept" == 0 ]]; then
    return 0
  fi
  echo "To remove what it left instead:" >&2
  print_delete_command >&2
  if created_this_run; then
    echo "Or run the same command with --on-failure rollback, which removes" >&2
    echo "everything this join created, including its backup identity." >&2
  fi
}

# An interrupt is a pause. Whatever the failure policy says, stopping the
# command by hand must not destroy a control plane that takes a quarter of an
# hour to rebuild.
on_interrupt() {
  INTERRUPTED=1
  echo "" >&2
  exit 130
}

# rollback_try runs a teardown step. A failure must not abort the rest of
# rollback, but it must keep resume state so --status can still name what is
# left.
rollback_try() {
  if "$@"; then
    return 0
  fi
  ROLLBACK_FAILED=1
  return 0
}

# agent_rollout_timeout is how long to wait for an adapter in agent role to
# report Ready.
#
# That readiness is a real join, not a process start: the agent serves 503 on
# /readyz until a heartbeat has reached the hub, so a wrong hub URL cannot pass
# a rollout. It retries on a backoff that doubles from 15s to a five minute
# cap, so a spoke whose first beats fail can sit a full five minutes before it
# tries again. Waiting three minutes was shorter than one retry interval, and
# tore down clusters whose agent went on to join seconds later. Ten minutes
# clears the cap with room for the beat itself.
agent_rollout_timeout() {
  echo "${ROLLOUT_TIMEOUT:-10m}"
}

# dump_failure_state prints what the platform namespace looked like when the
# run gave up.
#
# Rollback deletes the cluster this run created, and every trace of the reason
# goes with it: the pod that never pulled, the event that named the missing
# credential. Printing it first costs a few seconds and leaves the reason in
# the operator's terminal, which is the only place it survives.
# cluster_reachable is true when kubectl has a cluster configured to talk to.
#
# An AWS join points KUBECONFIG at a file of this run's own, written once the
# cluster exists. Until then the file is not there, and kubectl falls back to
# its built in default of localhost:8080 and answers every call with a
# connection refused. That output names a port inside the cluster and reads
# like a component there is broken, when what happened is that this run never
# got as far as having a cluster to look at.
cluster_reachable() {
  kubectl config current-context >/dev/null 2>&1
}

dump_failure_state() {
  local ns="${PLATFORM_NAMESPACE:-}"
  [[ -n "$ns" ]] || return 0
  if ! cluster_reachable; then
    echo "--- no Kubernetes cluster state to show ---" >&2
    echo "This run has no Kubernetes cluster to read, so nothing here describes one." >&2
    echo "The reason it gave up is in the lines above this block." >&2
    return 0
  fi
  echo "--- state of ${ns} when the run gave up ---" >&2
  kubectl -n "$ns" get pods -o wide >&2
  kubectl -n "$ns" get deploy >&2
  kubectl -n "$ns" get events --field-selector type=Warning \
    --sort-by=.lastTimestamp >&2
  # Pods and events say a probe failed. Only the agent's own log says why it
  # could not reach the hub, which is the question a failed join turns on.
  if [[ -n "${PLATFORM_RELEASE:-}" ]]; then
    echo "--- last log lines from ${PLATFORM_RELEASE}-adapter ---" >&2
    kubectl -n "$ns" logs "deploy/${PLATFORM_RELEASE}-adapter" --tail=30 >&2
  fi
  echo "--- end of state ---" >&2
}

# keep_unremoved_state runs after a rollback that finished. It rewrites the
# state file to what is still standing: what earlier runs recorded, and what
# this run made that rollback leaves alone (a bucket or identity an existing
# platform now uses, and the CSI role stack on a cluster it did not create).
# Clearing the file would forget a key or stack that is still live, and
# --status could no longer name it.
keep_unremoved_state() {
  if [[ "${APPLIED_BUCKET_TO_EXISTING:-0}" != 1 ]]; then
    CREATED_BUCKET=0
  fi
  if [[ "${APPLIED_IDENTITY_TO_EXISTING:-0}" != 1 ]]; then
    CREATED_IAM_USER=0
    CREATED_ACCESS_KEY_ID=""
  fi
  if [[ "$CREATED_CLUSTER" == 1 ]]; then
    CREATED_CSI_STACK=0
  fi
  CREATED_CLUSTER=0
  CREATED_NODEGROUP=0
  CREATED_FLUX=0
  CREATED_AGENT=0
  INSTALLED_PLATFORM=0
  if run_created_something || [[ -n "$(recorded_key_ids)" ]]; then
    save_state
  else
    clear_state
  fi
}

# A failed run leaves what it created in place and says how to resume. With
# --on-failure rollback it instead removes the EKS cluster, node group, Helm
# release, Flux install, agent, S3 bucket, and IAM user this command created.
# A cluster or bucket that was already there stays either way, because Connect
# is allowed to attach to one.
on_exit() {
  local status=$?
  set +e
  if [[ "$status" -ne 0 && "${PHASE_OPEN:-0}" == 1 ]]; then
    report_phase failed "$(tail -n 40 "${PHASE_LOG:-/dev/null}" 2>/dev/null || true)"
    PHASE_OPEN=0
  fi
  if [[ "$status" -eq 0 ]]; then
    if [[ "${PHASE_OPEN:-0}" == 1 && "${STATUS_ONLY:-0}" != 1 && "${DRY_RUN:-0}" != 1 ]]; then
      report_phase succeeded ""
      PHASE_OPEN=0
    fi
    # --status and --dry-run exit 0 without joining anything. Clearing here
    # would spend the enrollment the next run resumes from, and the record of
    # where the last one stopped, so reading the state would destroy it.
    if [[ "${STATUS_ONLY:-0}" != 1 && "${DRY_RUN:-0}" != 1 ]]; then
      clear_state
      rm -f "${ENROLL_CACHE:-}"
    fi
    rm -f "${FLUX_APPLIED_NAMES:-}"
    rm -rf "$tmp"
    return "$status"
  fi
  if [[ "${UPDATE:-0}" == 1 ]]; then
    # Nothing an update does is undone here: it moved a running platform, and
    # putting that back is the customer's call.
    update_failure_notice
    rm -f "${FLUX_APPLIED_NAMES:-}"
    rm -rf "$tmp"
    return "$status"
  fi
  if [[ -z "${NAME:-}" || -z "${REGION:-}" ]]; then
    rm -f "${FLUX_APPLIED_NAMES:-}"
    rm -rf "$tmp"
    return "$status"
  fi
  save_state
  if [[ "${INTERRUPTED:-0}" == 1 || "${ON_FAILURE:-keep}" != "rollback" ]]; then
    keep_notice
    rm -f "${FLUX_APPLIED_NAMES:-}"
    rm -rf "$tmp"
    return "$status"
  fi
  ROLLBACK_FAILED=0
  # Rollback deletes the cluster this run created, and the reason goes with
  # it. Print the platform namespace first so the diagnosis stays in the
  # terminal.
  dump_failure_state
  echo "bootstrap failed. Deleting resources this run created." >&2
  if [[ "$CREATED_CLUSTER" == 1 ]]; then
    echo "deleting EKS cluster ${NAME}" >&2
    if eksctl delete cluster --name "$NAME" --region "$REGION" --wait --force \
      --disable-nodegroup-eviction --timeout "$CLUSTER_DELETE_TIMEOUT"; then
      # The volumes behind the databases and monitoring outlive the nodes, and
      # only an EKS cluster this run created may lose them. While the EKS
      # cluster still stands they are still its volumes.
      rollback_try delete_cluster_volumes "$NAME" "$REGION" >&2
    else
      ROLLBACK_FAILED=1
    fi
  else
    if [[ "$INSTALLED_PLATFORM" == 1 ]]; then
      echo "uninstalling Helm release ${PLATFORM_RELEASE}" >&2
      rollback_try helm uninstall "$PLATFORM_RELEASE" --namespace "$PLATFORM_NAMESPACE" --timeout 5m
    fi
    if [[ "$CREATED_AGENT" == 1 ]]; then
      echo "deleting agent namespace ${AGENT_NAMESPACE}" >&2
      rollback_try kubectl delete namespace "$AGENT_NAMESPACE" --wait=false
    fi
    if [[ "${APPLIED_FLUX:-0}" == 1 ]]; then
      echo "removing Git reconciliation this run applied" >&2
      if [[ "$CREATED_FLUX" == 1 ]]; then
        delete_applied_flux "${FLUX_APPLIED_NAMES:-}" 0
        rollback_try kubectl delete namespace flux-system --wait=false
      else
        delete_applied_flux "${FLUX_APPLIED_NAMES:-}" 1
      fi
    elif [[ "$CREATED_FLUX" == 1 ]]; then
      echo "removing Git reconciliation this run installed" >&2
      rollback_try kubectl delete namespace flux-system --wait=false
    fi
    if [[ "$CREATED_NODEGROUP" == 1 ]]; then
      echo "deleting node group ${NODEGROUP}" >&2
      rollback_try eksctl delete nodegroup --cluster "$NAME" --name "$NODEGROUP" --region "$REGION" \
        --wait --disable-eviction
    fi
  fi
  if [[ "$CREATED_BUCKET" == 1 && -n "$BUCKET" ]]; then
    if [[ "${APPLIED_BUCKET_TO_EXISTING:-0}" == 1 ]]; then
      echo "keeping S3 bucket ${BUCKET}; the existing platform now uses it." >&2
    else
      echo "deleting S3 bucket ${BUCKET}" >&2
      rollback_try aws s3 rb "s3://${BUCKET}" --force
    fi
  fi
  # A credential written into an already-installed platform is now the
  # running one. write_s3_secret replaces pds-s3 before Helm runs, so
  # deleting that key after that apply leaves the existing install
  # pointed at a dead key. Keep it once the Secret is on a pre-existing
  # release. A new install that fails before Helm still deletes the key
  # this run minted, including one minted on a user that was already
  # there.
  if [[ "${APPLIED_IDENTITY_TO_EXISTING:-0}" != 1 ]]; then
    if [[ -n "${CREATED_ACCESS_KEY_ID:-}" ]]; then
      echo "deleting IAM access key ${CREATED_ACCESS_KEY_ID}" >&2
      rollback_try aws iam delete-access-key --user-name "$(spoke_iam_user)" --access-key-id "$CREATED_ACCESS_KEY_ID"
    fi
    if [[ "${CREATED_IAM_USER:-0}" == 1 ]]; then
      echo "deleting IAM user $(spoke_iam_user)" >&2
      rollback_try aws iam delete-user-policy --user-name "$(spoke_iam_user)" --policy-name pds-backup-storage
      rollback_try aws iam delete-user --user-name "$(spoke_iam_user)"
    fi
  fi
  if [[ "${ROLLBACK_FAILED:-0}" == 1 ]]; then
    echo "rollback did not finish. Resume state was kept so --status can still name what is left." >&2
  else
    keep_unremoved_state
  fi
  rm -f "${FLUX_APPLIED_NAMES:-}"
  rm -rf "$tmp"
  return "$status"
}
trap on_exit EXIT
trap on_interrupt INT TERM

enrollment_cache_path() {
  local invitation="$1"
  local cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/pds"
  printf '%s/enroll-%s' "$cache_dir" "$(printf '%s' "$invitation" | python3 -c 'import hashlib,sys; print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())')"
}

# The invitation is consumed on the first successful enrollment, so a later
# mint cannot create a second agent credential. A failed run keeps the JSON
# (including agentToken) so the same pasted command can finish without asking
# the hub to mint again.
load_enrollment() {
  local invitation="$1"
  local hub="$2"
  local cache="${3:-$(enrollment_cache_path "$invitation")}"
  local cache_dir body code auth_header
  cache_dir="$(dirname "$cache")"
  auth_header="$(mktemp "${tmp}/enroll-header.XXXXXX")"
  printf 'Authorization: Bearer %s' "$invitation" >"$auth_header"
  chmod 600 "$auth_header"
  body="$(curl_once -sS -w "\n%{http_code}" -H @"$auth_header" "${hub}/api/internal/connect/enrollment")" || true
  rm -f "$auth_header"
  code="$(printf '%s' "$body" | tail -n 1)"
  body="$(printf '%s' "$body" | sed '$d')"
  if [[ "$code" == "200" && -n "$body" ]]; then
    mkdir -p "$cache_dir"
    chmod 700 "$cache_dir"
    printf '%s' "$body" >"$cache"
    chmod 600 "$cache"
    printf '%s' "$body"
    return 0
  fi
  if [[ -f "$cache" ]]; then
    echo "using the enrollment saved from the last attempt" >&2
    cat "$cache"
    return 0
  fi
  echo "enrollment failed (${code:-no response})" >&2
  return 1
}

# preview_enrollment names the cluster a join would create without spending
# the invitation. A later run, including one on another machine, can still
# enroll. A cache from a previous real enrollment on this machine is enough
# and is not written here, so a dry run cannot poison a later join with a
# body that has no agent token.
preview_enrollment() {
  local invitation="$1"
  local hub="$2"
  local cache="${3:-$(enrollment_cache_path "$invitation")}"
  if [[ -f "$cache" ]]; then
    echo "using the enrollment saved from the last attempt" >&2
    cat "$cache"
    return 0
  fi
  local body code auth_header
  auth_header="$(mktemp "${tmp}/enroll-header.XXXXXX")"
  printf 'Authorization: Bearer %s' "$invitation" >"$auth_header"
  chmod 600 "$auth_header"
  body="$(curl_retry -sS -w "\n%{http_code}" -H @"$auth_header" "${hub}/api/internal/connect/enrollment?preview=1")" || true
  rm -f "$auth_header"
  code="$(printf '%s' "$body" | tail -n 1)"
  body="$(printf '%s' "$body" | sed '$d')"
  if [[ "$code" == "200" && -n "$body" ]]; then
    printf '%s' "$body"
    return 0
  fi
  echo "enrollment preview failed (${code:-no response})" >&2
  return 1
}

# trim_ws strips leading and trailing whitespace from one answer.
trim_ws() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

# configured_aws_region is the region the AWS CLI on this machine would
# use. The environment wins over the config file, which is how the CLI
# itself resolves it. Empty means this machine has no region of its own.
configured_aws_region() {
  local region=""
  if [[ -n "${AWS_REGION:-}" ]]; then
    region="${AWS_REGION}"
  elif [[ -n "${AWS_DEFAULT_REGION:-}" ]]; then
    region="${AWS_DEFAULT_REGION}"
  else
    region="$(aws configure get region 2>/dev/null || true)"
  fi
  region="${region%%$'\n'*}"
  region="${region%%$'\r'*}"
  region="$(trim_ws "$region")"
  if [[ "$region" == "None" || "$region" == "null" ]]; then
    region=""
  fi
  printf '%s' "$region"
}

# account_regions lists the regions this account can use. The endpoint has
# to be a region that already resolves, so a typed answer is checked
# against the list rather than sent to the API as its own endpoint.
account_regions() {
  local endpoint="$1"
  local offered
  offered="$(aws ec2 describe-regions --region "$endpoint" \
    --query 'Regions[].RegionName' --output text 2>/dev/null)" || return 1
  if [[ -z "$offered" ]]; then
    return 1
  fi
  printf '%s' "$offered"
}

# region_offered is true when want is one of the whitespace-separated
# names in offered.
region_offered() {
  local want="$1"
  local offered="$2"
  (
    set -f
    local name
    # shellcheck disable=SC2086
    for name in $offered; do
      if [[ "$name" == "$want" ]]; then
        exit 0
      fi
    done
    exit 1
  )
}

# choose_region runs before enrollment and before any AWS resource is
# created. --region has already decided, and --local has nothing to place.
# Otherwise the default is this machine's own AWS region, then the region
# baked in when the script was served. A region the account cannot use is
# asked again, so the run does not fail halfway through a create.
choose_region() {
  local hub_default="$1"
  if [[ "${LOCAL:-0}" == 1 || "${REGION_FROM_FLAG:-0}" == 1 ]]; then
    return 0
  fi
  local configured default offered candidate answer
  configured="$(configured_aws_region)"
  if [[ -n "$configured" ]]; then
    default="$configured"
  else
    default="$hub_default"
  fi
  REGION="$default"
  if ! have_tty; then
    return 0
  fi
  offered=""
  if [[ -n "$default" ]]; then
    offered="$(account_regions "$default")" || offered=""
  fi
  if [[ -z "$offered" && -n "$hub_default" && "$hub_default" != "$default" ]]; then
    offered="$(account_regions "$hub_default")" || offered=""
  fi
  if [[ -z "$offered" ]]; then
    echo "could not list the regions this account can use." >&2
    echo "Check the AWS credentials, then run this command again." >&2
    exit 1
  fi
  while true; do
    printf 'AWS region [%s]: ' "$default" >/dev/tty
    if ! IFS= read -r answer </dev/tty; then
      REGION="$default"
      return 0
    fi
    candidate="$(trim_ws "$answer")"
    if [[ -z "$candidate" ]]; then
      candidate="$default"
    fi
    if region_offered "$candidate" "$offered"; then
      REGION="$candidate"
      return 0
    fi
    printf '%s is not a region this account can use.\n' "$candidate" >/dev/tty
  done
}

# The region selects the cluster, the node group, the bucket and the
# resume file. Asking here, before enrollment, means a region this
# account cannot use never spends the invitation or creates any of them.
choose_region "$REGION"

ENROLL_CACHE="$(enrollment_cache_path "$TOKEN")"
if [[ "$UPDATE" == 1 ]]; then
  # A preview names the environment and spends nothing. The checks run on it
  # before the enrollment that spends the token.
  enroll="$(preview_enrollment "$TOKEN" "$HUB_URL" "$ENROLL_CACHE")" || exit 1
elif [[ "$STATUS_ONLY" == 1 ]]; then
  # The invitation is spent on the first successful enrollment, so reporting
  # status reads what an earlier run cached rather than asking for a new one.
  if [[ ! -f "$ENROLL_CACHE" ]]; then
    echo "no join has been attempted from this machine with this token, so" >&2
    echo "there is no status to report. Run the join command first." >&2
    exit 1
  fi
  enroll="$(cat "$ENROLL_CACHE")"
elif [[ "$DRY_RUN" == 1 ]]; then
  enroll="$(preview_enrollment "$TOKEN" "$HUB_URL" "$ENROLL_CACHE")" || exit 1
else
  enroll="$(load_enrollment "$TOKEN" "$HUB_URL" "$ENROLL_CACHE")" || exit 1
fi
# read_enrollment turns the enrollment JSON in $enroll into the settings the
# rest of the run uses. An update reads it twice: once from the preview that
# names the environment, and again from the enrollment that spends the token.
read_enrollment() {
  # A cached enrollment from before an environment was called one names it cluster.
  NAME="$(printf '%s' "$enroll" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("environment") or d.get("cluster") or "")')"
  IMAGE="$(printf '%s' "$enroll" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("agentImage") or "")')"
  enroll_git="$(printf '%s' "$enroll" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("gitUrl") or "")')"
  enroll_git_user="$(printf '%s' "$enroll" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("gitUsername") or "")')"
  enroll_git_password="$(printf '%s' "$enroll" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("gitPassword") or "")')"
  enroll_git_identity="$(printf '%s' "$enroll" | python3 -c 'import json,sys; sys.stdout.write(json.load(sys.stdin).get("gitIdentity") or "")')"
  enroll_git_identity_pub="$(printf '%s' "$enroll" | python3 -c 'import json,sys; print((json.load(sys.stdin).get("gitIdentityPub") or "").strip())')"
  enroll_registry_server="$(printf '%s' "$enroll" | python3 -c 'import json,sys; r=json.load(sys.stdin).get("registry") or {}; print(r.get("server") or "")')"
  enroll_registry_user="$(printf '%s' "$enroll" | python3 -c 'import json,sys; r=json.load(sys.stdin).get("registry") or {}; print(r.get("username") or "")')"
  enroll_registry_password="$(printf '%s' "$enroll" | python3 -c 'import json,sys; r=json.load(sys.stdin).get("registry") or {}; print(r.get("password") or "")')"
  enroll_namespace="$(printf '%s' "$enroll" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("namespace") or "")')"
  enroll_agent_token="$(printf '%s' "$enroll" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("agentToken") or "")')"
  if [[ -n "$enroll_agent_token" ]]; then
    TOKEN="$enroll_agent_token"
  fi
  if [[ "$TOKEN" == *$'\n'* || "$TOKEN" == *"\""* ]]; then
    echo "agent token must not contain quotes or newlines" >&2
    exit 1
  fi
  if [[ -n "$enroll_namespace" ]]; then
    DB_NAMESPACE="$enroll_namespace"
  fi
  DB_NAMESPACE="${DB_NAMESPACE:-pds}"
  if [[ "$DB_NAMESPACE" == *$'\n'* || "$DB_NAMESPACE" == *"\""* ]]; then
    echo "database namespace must not contain quotes or newlines" >&2
    exit 1
  fi
  if [[ ! "$DB_NAMESPACE" =~ ^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$ ]]; then
    echo "database namespace ${DB_NAMESPACE} is not a usable Kubernetes namespace" >&2
    exit 1
  fi
  if [[ -n "$enroll_git" ]]; then
    GIT_URL="$enroll_git"
  fi
  GIT_USERNAME=""
  GIT_PASSWORD=""
  GIT_IDENTITY=""
  GIT_IDENTITY_PUB=""
  if [[ -n "$enroll_git_identity" ]]; then
    if [[ -z "$enroll_git_identity_pub" ]]; then
      echo "the control plane sent a git identity without a public key" >&2
      exit 1
    fi
    GIT_IDENTITY="$enroll_git_identity"
    GIT_IDENTITY_PUB="$enroll_git_identity_pub"
  fi
  if [[ -n "$enroll_git_password" ]]; then
    GIT_USERNAME="${enroll_git_user:-x-access-token}"
    GIT_PASSWORD="$enroll_git_password"
  fi
  REGISTRY_SERVER=""
  REGISTRY_USERNAME=""
  REGISTRY_PASSWORD=""
  if [[ -n "$enroll_registry_password" ]]; then
    REGISTRY_SERVER="${enroll_registry_server:-ghcr.io}"
    REGISTRY_USERNAME="${enroll_registry_user:-x-access-token}"
    REGISTRY_PASSWORD="$enroll_registry_password"
  elif [[ -n "$enroll_git_password" ]]; then
    # Older hubs sent the PAT only as gitPassword. That token is also how a
    # private GHCR agent image is pulled.
    REGISTRY_SERVER="ghcr.io"
    REGISTRY_USERNAME="${enroll_git_user:-x-access-token}"
    REGISTRY_PASSWORD="$enroll_git_password"
  fi
  if [[ -z "$NAME" ]]; then
    echo "the control plane did not return an environment name for that token" >&2
    exit 1
  fi

  if [[ -z "$GIT_URL" ]]; then
    echo "this control plane has no Git URL, so it cannot apply this environment's databases." >&2
    echo "Contact Percona support." >&2
    exit 1
  fi

  if [[ "$GIT_URL" == *$'\n'* || "$GIT_URL" == *"\""* ]]; then
    echo "git url must not contain quotes or newlines" >&2
    exit 1
  fi
  if [[ "${GIT_USERNAME}" == *$'\n'* || "${GIT_USERNAME}" == *"\""* || "${GIT_PASSWORD}" == *$'\n'* || "${GIT_PASSWORD}" == *"\""* ]]; then
    echo "git credentials must not contain quotes or newlines" >&2
    exit 1
  fi
  if [[ "${GIT_IDENTITY_PUB}" == *"\""* ]]; then
    echo "git identity public key must not contain quotes" >&2
    exit 1
  fi
  if [[ "${REGISTRY_USERNAME}" == *$'\n'* || "${REGISTRY_USERNAME}" == *"\""* || "${REGISTRY_PASSWORD}" == *$'\n'* || "${REGISTRY_PASSWORD}" == *"\""* || "${REGISTRY_SERVER}" == *$'\n'* || "${REGISTRY_SERVER}" == *"\""* ]]; then
    echo "registry credentials must not contain quotes or newlines" >&2
    exit 1
  fi

  if [[ ! "$NAME" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]; then
    echo "environment name must be a DNS label" >&2
    exit 1
  fi
}
read_enrollment

# S3 buckets max 63 characters and IAM user names max 64. The IAM prefix is
# pds- (4). Bucket names are global, so they also carry this account id.
# Short names keep the readable form so a re-run finds the bucket it created.
bound_aws_name() {
  local prefix="$1"
  local full="${prefix}${NAME}"
  if [[ ${#full} -le 63 ]]; then
    printf '%s\n' "$full"
    return 0
  fi
  local hash room stem
  hash="$(python3 -c 'import hashlib,sys; print(hashlib.sha256(sys.argv[1].encode()).hexdigest()[:8])' "$NAME")"
  room=$((63 - ${#prefix} - 9))
  stem="${NAME:0:room}"
  while [[ "$stem" == *- ]]; do
    stem="${stem%-}"
  done
  printf '%s\n' "${prefix}${stem}-${hash}"
}

spoke_aws_name() {
  bound_aws_name "pds-"
}

# aws_account_id is read once. Every name derived from it has to agree, and a
# transient STS failure that answered blank would build a name no release ever
# created, quietly pointing a repair at a bucket that does not exist.
AWS_ACCOUNT_ID=""
aws_account_id() {
  if [[ -n "${AWS_ACCOUNT_ID:-}" ]]; then
    printf '%s\n' "${AWS_ACCOUNT_ID}"
    return 0
  fi
  local account
  account="$(aws sts get-caller-identity --query Account --output text 2>/dev/null || true)"
  if [[ ! "$account" =~ ^[0-9]{12}$ ]]; then
    echo "could not read the AWS account id for the backup bucket name" >&2
    exit 1
  fi
  AWS_ACCOUNT_ID="$account"
  printf '%s\n' "$AWS_ACCOUNT_ID"
}

spoke_bucket_name() {
  bound_aws_name "pds-$(aws_account_id)-"
}

# Prefer the account-scoped default. A cluster that already joined under an
# earlier name keeps that bucket so a repair does not orphan its backups. Two
# earlier shapes exist: the global name that predates account scoping, and both
# names from before the product was renamed to PDS. Nothing is created under an
# old name; they are only looked up.
resolve_spoke_bucket() {
  if [[ -n "${BUCKET:-}" ]]; then
    printf '%s\n' "$BUCKET"
    return 0
  fi
  local scoped account candidate before_rename_scoped before_rename_global
  scoped="$(spoke_bucket_name)"
  account="$(aws_account_id)"
  before_rename_scoped="$(bound_aws_name "basecamp-${account}-")" # allow-legacy-name
  before_rename_global="$(bound_aws_name "basecamp-")"            # allow-legacy-name
  # Bucket names are global, so a short unscoped name can belong to a stranger.
  # --expected-bucket-owner makes a 200 mean this account's bucket and nothing
  # else, which is what the name is being used to prove.
  for candidate in \
    "$scoped" \
    "$(spoke_aws_name)" \
    "$before_rename_scoped" \
    "$before_rename_global"; do
    if aws s3api head-bucket --bucket "$candidate" --region "$REGION" \
      --expected-bucket-owner "$account" >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  printf '%s\n' "$scoped"
}

# A --local join has no S3 bucket. BUCKET overrides the derived name.
if [[ "$LOCAL" != 1 ]]; then
  BUCKET="$(resolve_spoke_bucket)"
fi

export AWS_REGION="$REGION"
export AWS_DEFAULT_REGION="$REGION"

ready_nodes() {
  kubectl get nodes --no-headers 2>/dev/null | awk '$2 == "Ready" { c++ } END { print c + 0 }'
}

# This wait used to print one line and then say nothing for ten minutes, on
# the slowest part of a join, which is indistinguishable from a hang.
wait_for_ready_nodes() {
  # ready_nodes drops kubectl's own errors so a node that is not up yet reads
  # as zero rather than as noise. With no cluster to ask, every poll is that
  # same zero, and the wait used to sit out its full timeout and then report a
  # cluster with no Ready nodes. The nodes are not the problem.
  if ! cluster_reachable; then
    echo "there is no Kubernetes cluster for this run to read, so the node wait would" >&2
    echo "time out against nothing. The kubeconfig for ${NAME} was never written." >&2
    return 1
  fi
  if [[ "$(ready_nodes)" -ge 1 ]]; then
    return 0
  fi
  echo "waiting for a Ready worker node"
  local polls=0 max_polls
  if [[ "$WAIT_INTERVAL" -gt 0 ]]; then
    max_polls=$((READY_NODE_TIMEOUT / WAIT_INTERVAL))
  else
    max_polls="$READY_NODE_TIMEOUT"
  fi
  while [[ "$polls" -lt "$max_polls" ]]; do
    sleep "$WAIT_INTERVAL"
    polls=$((polls + 1))
    if [[ "$(ready_nodes)" -ge 1 ]]; then
      echo "  a worker node is Ready ($(elapsed) elapsed)"
      return 0
    fi
    echo "  no Ready worker node yet, waiting ($(elapsed) elapsed)"
  done
  echo "this Kubernetes cluster has no Ready worker nodes." >&2
  kubectl get nodes >&2 || true
  return 1
}

# print_status only reads. It writes a kubeconfig into this run's temporary
# directory so the cluster side lines are true, and touches nothing else.
print_status() {
  if [[ "$LOCAL" == 1 ]]; then
    echo "environment ${NAME}, Kubernetes cluster ${NAME}"
  else
    echo "environment ${NAME}, EKS cluster ${NAME} in ${REGION}"
  fi
  if [[ "$LOCAL" == 1 ]]; then
    echo "  context: $(kubectl config current-context 2>/dev/null || echo "none")"
  else
    local control_plane
    control_plane="$(cluster_state "$NAME" "$REGION")"
    echo "  EKS control plane: ${control_plane}"
    echo "  node group ${NODEGROUP}: $(nodegroup_state "$NAME" "$NODEGROUP" "$REGION")"
    echo "  eksctl stack: $(cluster_stack_state "$NAME" "$REGION")"
    if [[ -n "${BUCKET:-}" ]]; then
      if aws s3api head-bucket --bucket "$BUCKET" --region "$REGION" \
        --expected-bucket-owner "$(aws_account_id)" >/dev/null 2>&1; then
        echo "  backup bucket ${BUCKET}: present"
      else
        echo "  backup bucket ${BUCKET}: absent"
      fi
    fi
    if aws iam get-user --user-name "$(spoke_iam_user)" >/dev/null 2>&1; then
      echo "  backup identity $(spoke_iam_user): present"
    else
      echo "  backup identity $(spoke_iam_user): absent"
    fi
    if [[ "$control_plane" == "ACTIVE" ]]; then
      aws eks update-kubeconfig --name "$NAME" --region "$REGION" >/dev/null 2>&1 || true
    fi
  fi
  echo "  Ready worker nodes: $(ready_nodes)"
  resolve_platform_release
  if platform_installed; then
    echo "  Percona Database Service: installed"
  else
    echo "  Percona Database Service: absent"
  fi
  if flux_install_needed; then
    echo "  Git reconciliation: absent or incomplete"
  else
    echo "  Git reconciliation: installed"
  fi
  if [[ -n "${PHASE_LAST:-}" ]]; then
    echo "  the last run stopped at: ${PHASE_LAST}"
    # What an earlier attempt built, so the operator can tell a resource this
    # machine made from one that was already here. Nothing acts on it: a
    # rollback removes only what the run asking for it created.
    local made=""
    [[ "${STATE_CLUSTER:-0}" == 1 ]] && made="${made} the EKS cluster,"
    [[ "${STATE_NODEGROUP:-0}" == 1 ]] && made="${made} the node group,"
    [[ "${STATE_BUCKET:-0}" == 1 ]] && made="${made} the backup bucket,"
    [[ "${STATE_IAM_USER:-0}" == 1 ]] && made="${made} the backup identity,"
    [[ -n "${STATE_ACCESS_KEY_IDS:-}" ]] && made="${made} a backup access key,"
    [[ "${STATE_CSI_STACK:-0}" == 1 ]] && made="${made} the EBS CSI role stack,"
    [[ "${STATE_FLUX:-0}" == 1 ]] && made="${made} Git reconciliation,"
    [[ "${STATE_AGENT:-0}" == 1 ]] && made="${made} the agent namespace,"
    [[ "${STATE_PLATFORM:-0}" == 1 ]] && made="${made} Percona Database Service,"
    if [[ -n "$made" ]]; then
      echo "  it created:${made%,}"
    fi
  fi
  if [[ "$LOCAL" != 1 ]]; then
    local cmds
    cmds="$(print_delete_command)"
    if [[ -n "$cmds" ]]; then
      echo "To remove what a join created here:"
      printf '%s\n' "$cmds"
    fi
  fi
}

# print_plan says what a run would do without doing any of it.
print_plan() {
  local cluster_status
  resolve_platform_release
  echo "this join would:"
  if [[ "$LOCAL" == 1 ]]; then
    echo "  join the Kubernetes cluster kubectl points at, creating no AWS resources"
  else
    cluster_status="$(cluster_state "$NAME" "$REGION")"
    if [[ "$cluster_status" == "ABSENT" ]]; then
      echo "  create EKS cluster ${NAME} in ${REGION} on Kubernetes $(resolve_kubernetes_version)"
      echo "  create node group ${NODEGROUP} with ${NODES} ${NODE_TYPE} nodes"
    else
      echo "  attach to EKS cluster ${NAME} in ${REGION}, which is ${cluster_status}"
    fi
    echo "  create the backup bucket ${BUCKET} if it is not already there"
    echo "  create the backup identity $(spoke_iam_user) if it is not already there"
  fi
  echo "  install Percona Database Service as Helm release ${PLATFORM_RELEASE} in ${PLATFORM_NAMESPACE}"
  echo "  install Git reconciliation (${FLUX_VERSION}) and point it at the control plane"
  echo "A failed run keeps what it created. Pass --on-failure rollback to remove it."
}

write_new_cluster_config() {
  # Databases run on these nodes. Private subnets behind a NAT gateway are the
  # default because a public node is a decision the customer should make, not
  # one a one-line install makes for them. --public-nodes trades that for the
  # NAT gateway's hourly cost.
  local nat="Single"
  local private="true"
  if [[ "$PUBLIC_NODES" == 1 ]]; then
    nat="Disable"
    private="false"
  fi
  local version
  version="$(resolve_kubernetes_version)"
  echo "the EKS cluster will run Kubernetes ${version}"
  cat >"${tmp}/cluster.yaml" <<EOF
apiVersion: eksctl.io/v1alpha5
kind: ClusterConfig
metadata:
  name: ${NAME}
  region: ${REGION}
  version: "${version}"
  tags:
    percona.com/pds: "${NAME}"
iam:
  withOIDC: true
vpc:
  nat:
    gateway: ${nat}
autoModeConfig:
  enabled: false
addons:
  - name: aws-ebs-csi-driver
    wellKnownPolicies:
      ebsCSIController: true
managedNodeGroups:
  - name: ${NODEGROUP}
    instanceType: ${NODE_TYPE}
    desiredCapacity: ${NODES}
    minSize: 1
    maxSize: ${NODES}
    privateNetworking: ${private}
    tags:
      percona.com/pds: "${NAME}"
EOF
}

write_nodegroup_config() {
  aws eks describe-cluster --name "$NAME" --region "$REGION" --output json >"${tmp}/cluster.json"
  python3 - "$tmp" "$NAME" "$REGION" "$NODEGROUP" "$NODE_TYPE" "$NODES" "$PUBLIC_NODES" <<'PY'
import json
import subprocess
import sys

tmp, name, region, nodegroup, node_type, nodes, public_nodes = sys.argv[1:]
cluster = json.load(open(f"{tmp}/cluster.json"))
vpc = cluster["cluster"]["resourcesVpcConfig"]
vpc_id = vpc["vpcId"]
sg = vpc.get("clusterSecurityGroupId") or (vpc.get("securityGroupIds") or [None])[0]
if not sg:
    print("EKS cluster security group is missing", file=sys.stderr)
    sys.exit(1)
subnets = json.loads(
    subprocess.check_output(
        ["aws", "ec2", "describe-subnets", "--subnet-ids", *vpc["subnetIds"], "--region", region, "--output", "json"],
        text=True,
    )
)["Subnets"]

public = {}
private = {}
for s in subnets:
    az = s["AvailabilityZone"]
    entry = f"      {az}: {{ id: {s['SubnetId']} }}"
    if s.get("MapPublicIpOnLaunch"):
        public[az] = entry
    else:
        private[az] = entry

lines = [
    "apiVersion: eksctl.io/v1alpha5",
    "kind: ClusterConfig",
    "metadata:",
    f"  name: {name}",
    f"  region: {region}",
    "  tags:",
    f'    percona.com/pds: "{name}"',
    "vpc:",
    f"  id: {vpc_id}",
    f"  securityGroup: {sg}",
    "  subnets:",
]
if private:
    lines.append("    private:")
    lines.extend(private.values())
if public:
    lines.append("    public:")
    lines.extend(public.values())
lines.extend(
    [
        "managedNodeGroups:",
        f"  - name: {nodegroup}",
        f"    instanceType: {node_type}",
        f"    desiredCapacity: {int(nodes)}",
        "    minSize: 1",
        f"    maxSize: {int(nodes)}",
        f"    privateNetworking: {'false' if public_nodes == '1' else 'true'}",
        "    tags:",
        f'      percona.com/pds: "{name}"',
    ]
)
open(f"{tmp}/cluster.yaml", "w").write("\n".join(lines) + "\n")
PY
}

# aws_state turns one describe call into a status word. An account this run
# cannot read answers UNKNOWN rather than ABSENT, because reading an
# unreadable account as an empty one is how a second control plane gets built
# and billed.
aws_state() {
  local out="$1" code="$2"
  if [[ "$code" -eq 0 ]]; then
    if [[ -z "$out" || "$out" == "None" ]]; then
      printf 'UNKNOWN'
    else
      printf '%s' "$out"
    fi
    return 0
  fi
  case "$out" in
    *ResourceNotFoundException* | *"No cluster found"* | *"does not exist"*)
      printf 'ABSENT'
      ;;
    *)
      printf 'UNKNOWN'
      ;;
  esac
}

# describe-cluster exits 0 for CREATING, DELETING, UPDATING and FAILED alike,
# so an exit code cannot tell a cluster that is ready to use from one that is
# still being built or already being torn down. Read the status itself.
cluster_state() {
  local out code=0
  out="$(aws eks describe-cluster --name "$1" --region "$2" \
    --query cluster.status --output text 2>&1)" || code=$?
  aws_state "$out" "$code"
}

# Same flaw, same fix: a CREATE_FAILED node group used to read as healthy, and
# the run then spent ten silent minutes waiting for a Ready node.
nodegroup_state() {
  local out code=0
  out="$(aws eks describe-nodegroup --cluster-name "$1" --nodegroup-name "$2" \
    --region "$3" --query nodegroup.status --output text 2>&1)" || code=$?
  aws_state "$out" "$code"
}

# An interrupted create can leave eksctl-<name>-cluster standing with no EKS
# cluster behind it. describe-cluster then reports nothing there, eksctl
# create cluster runs, and it fails on AlreadyExistsException with nothing the
# customer can act on.
cluster_stack_state() {
  local out code=0
  out="$(aws cloudformation describe-stacks --stack-name "eksctl-$1-cluster" \
    --region "$2" --query 'Stacks[0].StackStatus' --output text 2>&1)" || code=$?
  aws_state "$out" "$code"
}

# wait_out_status polls a status reader until the value leaves the transient
# set, printing a heartbeat so a twenty minute wait reads as a wait rather
# than a hang. The answer lands in WAIT_STATUS, because the heartbeat already
# owns stdout.
wait_out_status() {
  local label="$1" transient="$2" timeout="$3"
  shift 3
  local polls=0 max_polls
  if [[ "$WAIT_INTERVAL" -gt 0 ]]; then
    max_polls=$((timeout / WAIT_INTERVAL))
  else
    max_polls="$timeout"
  fi
  WAIT_STATUS="$("$@")"
  while [[ " ${transient} " == *" ${WAIT_STATUS} "* ]]; do
    if [[ "$polls" -ge "$max_polls" ]]; then
      echo "${label} is still ${WAIT_STATUS} after $((timeout / 60))m." >&2
      return 1
    fi
    echo "  ${label} is ${WAIT_STATUS}, waiting ($(elapsed) elapsed)"
    sleep "$WAIT_INTERVAL"
    polls=$((polls + 1))
    WAIT_STATUS="$("$@")"
  done
  return 0
}

# The version a join falls back to when the region cannot be asked what it
# offers. It is the version an unpinned eksctl produced when this was written,
# so the fallback never builds an older cluster than the bug it replaces.
kubernetes_version_fallback() {
  printf '1.34'
}

# The cluster used to come up on whatever version the operator's eksctl build
# defaulted to, which nobody chose and which changed with their toolchain. Ask
# the region what it offers and take the newest, so the version is a decision.
resolve_kubernetes_version() {
  if [[ -n "${KUBERNETES_VERSION:-}" ]]; then
    printf '%s' "$KUBERNETES_VERSION"
    return 0
  fi
  local offered newest
  offered="$(aws eks describe-cluster-versions --region "$REGION" \
    --query 'clusterVersions[].clusterVersion' --output text 2>/dev/null)" || offered=""
  # --output text separates values with tabs, and a version is two numbers, so
  # sort field by field numerically rather than as text: 1.9 must not sort
  # after 1.10.
  newest="$(printf '%s\n' "$offered" | tr '[:space:]' '\n' \
    | grep -E '^[0-9]+\.[0-9]+$' | sort -t. -k1,1n -k2,2n | tail -n 1 || true)"
  if [[ -z "$newest" ]]; then
    newest="$(kubernetes_version_fallback)"
    echo "could not ask ${REGION} which Kubernetes versions it offers." >&2
    echo "Using ${newest}. Pass --kubernetes-version to choose another." >&2
  fi
  printf '%s' "$newest"
}

# require_cluster_cni refuses to add a node group to a cluster with no CNI.
#
# A kubelet registers without one and then sits NotReady for good, so eksctl
# waits out its whole window and CloudFormation cancels the node group stack,
# which on a fresh run tears down a healthy control plane with it. One API call
# turns twenty-odd minutes of waiting into a sentence. Either the managed addon
# or a self managed aws-node daemonset counts as a CNI.
require_cluster_cni() {
  if aws eks describe-addon --cluster-name "$NAME" --addon-name vpc-cni \
    --region "$REGION" >/dev/null 2>&1; then
    return 0
  fi
  if kubectl -n kube-system get daemonset aws-node >/dev/null 2>&1; then
    return 0
  fi
  echo "EKS cluster ${NAME} has no CNI: no vpc-cni addon and no aws-node daemonset." >&2
  echo "A node group added now would never report Ready, and the stack would be" >&2
  echo "cancelled after the wait. Install the vpc-cni addon on ${NAME}, or delete" >&2
  echo "the EKS cluster and let this script create one." >&2
  return 1
}

# stack_create_failures is the CREATE_FAILED reason on one stack. eksctl
# reports a failed create as "waiter state transitioned to Failure" and
# names no resource. Cancelled dependents are the rollback, not the cause.
stack_create_failures() {
  local stack="$1"
  local events
  events="$(aws cloudformation describe-stack-events --stack-name "$stack" \
    --region "$REGION" \
    --query "StackEvents[?ResourceStatus=='CREATE_FAILED'].[LogicalResourceId,ResourceStatusReason]" \
    --output text 2>/dev/null)" || return 1
  printf '%s\n' "$events" | awk -F '\t' '
    $0 ~ /Resource creation cancelled/ { next }
    NF < 2 || $2 == "" || $2 == "None" { next }
    {
      printf "  %s: %s\n", $1, $2
      shown++
      if (shown >= 3) exit
    }
  '
}

# explain_create_failure prints the CloudFormation reason after eksctl
# gives up. The node group stack is first because that is the wait that
# runs long enough to look like a hang and then fail with no detail.
explain_create_failure() {
  local stack reason found=0
  for stack in \
    "eksctl-${NAME}-nodegroup-${NODEGROUP}" \
    "eksctl-${NAME}-cluster"; do
    reason="$(stack_create_failures "$stack")" || reason=""
    if [[ -n "$reason" ]]; then
      found=1
      echo "CloudFormation stack ${stack} failed:" >&2
      printf '%s\n' "$reason" >&2
    fi
  done
  if [[ "$found" == 0 ]]; then
    echo "eksctl did not say which resource failed." >&2
    echo "The node group stack is eksctl-${NAME}-nodegroup-${NODEGROUP} in ${REGION}." >&2
  fi
}

ensure_cluster() {
  local status stack ng_status
  status="$(cluster_state "$NAME" "$REGION")"
  if [[ "$status" == "UNKNOWN" ]]; then
    # One blip should not refuse a join, but a persistent inability to read
    # the account must never become a create.
    sleep "$RETRY_DELAY"
    status="$(cluster_state "$NAME" "$REGION")"
  fi

  if [[ "$status" == "UNKNOWN" ]]; then
    echo "cannot read the state of EKS cluster ${NAME} in ${REGION}." >&2
    echo "Check the AWS credentials and the region, then run this command again." >&2
    return 1
  fi

  if [[ "$status" == "DELETING" ]]; then
    echo "EKS cluster ${NAME} is DELETING. Waiting for that to finish before creating it again."
    wait_out_status "EKS cluster ${NAME}" "DELETING UNKNOWN" "$CLUSTER_WAIT_TIMEOUT" \
      cluster_state "$NAME" "$REGION" || return 1
    status="$WAIT_STATUS"
  fi

  if [[ "$status" == "CREATING" || "$status" == "UPDATING" ]]; then
    echo "EKS cluster ${NAME} is ${status} from an earlier run. Waiting for it rather than creating another."
    wait_out_status "EKS cluster ${NAME}" "CREATING UPDATING UNKNOWN" "$CLUSTER_WAIT_TIMEOUT" \
      cluster_state "$NAME" "$REGION" || return 1
    status="$WAIT_STATUS"
  fi

  if [[ "$status" == "FAILED" ]]; then
    echo "EKS cluster ${NAME} is FAILED, so this join cannot use it." >&2
    echo "Delete it, then run this command again:" >&2
    echo "  eksctl delete cluster --name ${NAME} --region ${REGION}" >&2
    return 1
  fi

  if [[ "$status" == "ABSENT" ]]; then
    stack="$(cluster_stack_state "$NAME" "$REGION")"
    if [[ "$stack" == "UNKNOWN" ]]; then
      # Carrying on is better than refusing a join over a read this
      # credential may simply not be allowed to make. Say so, because the
      # failure it lets through is otherwise silent.
      echo "could not check for a leftover eksctl-${NAME}-cluster stack."
      echo "If one is there, the create below fails on AlreadyExistsException."
    fi
    if [[ "$stack" != "ABSENT" && "$stack" != "UNKNOWN" ]]; then
      echo "there is no EKS cluster named ${NAME}, but the CloudFormation stack" >&2
      echo "eksctl-${NAME}-cluster is still there in ${stack}, so creating the" >&2
      echo "EKS cluster now would fail on AlreadyExistsException." >&2
      echo "Remove the leftover stack, then run this command again:" >&2
      echo "  eksctl delete cluster --name ${NAME} --region ${REGION}" >&2
      return 1
    fi
    if [[ "$PUBLIC_NODES" == 1 ]]; then
      echo "creating EKS cluster ${NAME} with ${NODES} ${NODE_TYPE} nodes in ${REGION}"
      echo "Nodes will be on public subnets with public IPs. NAT is off, so this"
      echo "consumes no Elastic IPs and no NAT gateway hours."
    else
      echo "creating EKS cluster ${NAME} with ${NODES} ${NODE_TYPE} nodes in ${REGION}"
      echo "Nodes will be on private subnets behind a NAT gateway, which bills by"
      echo "the hour. Pass --public-nodes to skip it."
    fi
    write_new_cluster_config
    CREATED_CLUSTER=1
    save_state
    if ! eksctl create cluster --config-file "${tmp}/cluster.yaml" \
      --timeout "$CLUSTER_CREATE_TIMEOUT"; then
      explain_create_failure
      return 1
    fi
    return 0
  fi

  echo "EKS cluster ${NAME} is ${status}. Writing kubeconfig."
  retry 3 "writing the kubeconfig for ${NAME}" \
    aws eks update-kubeconfig --name "$NAME" --region "$REGION" >/dev/null

  if [[ "$(ready_nodes)" -ge 1 ]]; then
    echo "EKS cluster ${NAME} already has Ready worker nodes."
    return 0
  fi

  echo "EKS control plane is up with no Ready nodes. Checking managed node group ${NODEGROUP}."
  ng_status="$(nodegroup_state "$NAME" "$NODEGROUP" "$REGION")"
  case "$ng_status" in
    ACTIVE)
      echo "node group ${NODEGROUP} is ACTIVE. Waiting for Ready workers."
      return 0
      ;;
    CREATING | UPDATING)
      echo "node group ${NODEGROUP} is ${ng_status} from an earlier run. Waiting for it rather than creating another."
      wait_out_status "node group ${NODEGROUP}" "CREATING UPDATING UNKNOWN" "$NODEGROUP_WAIT_TIMEOUT" \
        nodegroup_state "$NAME" "$NODEGROUP" "$REGION" || return 1
      if [[ "$WAIT_STATUS" != "ACTIVE" ]]; then
        echo "node group ${NODEGROUP} settled in ${WAIT_STATUS} rather than ACTIVE." >&2
        echo "Delete it, then run this command again:" >&2
        echo "  eksctl delete nodegroup --cluster ${NAME} --name ${NODEGROUP} --region ${REGION}" >&2
        return 1
      fi
      return 0
      ;;
    DELETING)
      echo "node group ${NODEGROUP} is DELETING. Waiting for that to finish before creating it again."
      wait_out_status "node group ${NODEGROUP}" "DELETING UNKNOWN" "$NODEGROUP_WAIT_TIMEOUT" \
        nodegroup_state "$NAME" "$NODEGROUP" "$REGION" || return 1
      ng_status="$WAIT_STATUS"
      case "$ng_status" in
        ABSENT) ;;
        ACTIVE)
          echo "node group ${NODEGROUP} is ACTIVE. Waiting for Ready workers."
          return 0
          ;;
        CREATE_FAILED | DELETE_FAILED | DEGRADED)
          echo "node group ${NODEGROUP} is ${ng_status}, so no worker node on it will become Ready." >&2
          echo "Delete it, then run this command again:" >&2
          echo "  eksctl delete nodegroup --cluster ${NAME} --name ${NODEGROUP} --region ${REGION}" >&2
          return 1
          ;;
        *)
          echo "node group ${NODEGROUP} settled in ${ng_status} rather than ABSENT." >&2
          echo "Delete it, then run this command again:" >&2
          echo "  eksctl delete nodegroup --cluster ${NAME} --name ${NODEGROUP} --region ${REGION}" >&2
          return 1
          ;;
      esac
      ;;
    CREATE_FAILED | DELETE_FAILED | DEGRADED)
      echo "node group ${NODEGROUP} is ${ng_status}, so no worker node on it will become Ready." >&2
      echo "Delete it, then run this command again:" >&2
      echo "  eksctl delete nodegroup --cluster ${NAME} --name ${NODEGROUP} --region ${REGION}" >&2
      return 1
      ;;
    UNKNOWN)
      echo "cannot read the state of node group ${NODEGROUP} on EKS cluster ${NAME}." >&2
      echo "Check the AWS credentials and the region, then run this command again." >&2
      return 1
      ;;
    ABSENT) ;;
  esac

  require_cluster_cni
  echo "creating managed node group ${NODEGROUP}."
  write_nodegroup_config
  CREATED_NODEGROUP=1
  save_state
  if ! eksctl create nodegroup --config-file "${tmp}/cluster.yaml"; then
    explain_create_failure
    return 1
  fi
}

ensure_ebs_csi_iam() {
  echo "granting EBS CSI volume permissions"
  CSI_ROLE_ARN=""
  eksctl utils associate-iam-oidc-provider --cluster "$NAME" --region "$REGION" --approve >/dev/null 2>&1 || true
  role_name="AmazonEKS_EBS_CSI_DriverRole_${NAME}"
  # The role lives in this stack, so it is what a cleanup deletes. A stack that
  # was already there belongs to whoever made it, and only one this call
  # brings into being is recorded as the join's.
  # Only a reply that says the stack does not exist counts as absent. A call
  # that failed for another reason, such as a throttle or an expired token,
  # proves nothing, and guessing absent would claim someone else's stack.
  local csi_stack="eksctl-${NAME}-addon-iamserviceaccount-kube-system-ebs-csi-controller-sa"
  local csi_stack_existed=1 probe
  probe="$(aws cloudformation describe-stacks --stack-name "$csi_stack" --region "$REGION" 2>&1)" || true
  if [[ "$probe" == *"does not exist"* ]]; then
    csi_stack_existed=0
  fi
  eksctl create iamserviceaccount \
    --cluster "$NAME" --region "$REGION" \
    --namespace kube-system --name ebs-csi-controller-sa \
    --role-name "$role_name" \
    --attach-policy-arn arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy \
    --tags "percona.com/pds=${NAME}" \
    --role-only --approve >/dev/null 2>&1 || true
  if [[ "$csi_stack_existed" == 0 ]] \
    && aws cloudformation describe-stacks --stack-name "$csi_stack" --region "$REGION" >/dev/null 2>&1; then
    CREATED_CSI_STACK=1
    save_state
  fi
  arn="$(aws iam get-role --role-name "$role_name" --query Role.Arn --output text 2>/dev/null || true)"
  if [[ "$arn" == arn:* ]]; then
    CSI_ROLE_ARN="$arn"
    kubectl -n kube-system annotate sa ebs-csi-controller-sa "eks.amazonaws.com/role-arn=${arn}" --overwrite >/dev/null 2>&1 || true
  fi
}

ensure_storage() {
  ensure_ebs_csi_iam
  if ! kubectl get csidriver ebs.csi.aws.com >/dev/null 2>&1; then
    echo "installing the EBS CSI driver"
    addon_args=(create addon --name aws-ebs-csi-driver --cluster "$NAME" --region "$REGION" --force)
    if [[ "${CSI_ROLE_ARN:-}" == arn:* ]]; then
      addon_args+=(--service-account-role-arn "$CSI_ROLE_ARN")
    fi
    if ! eksctl "${addon_args[@]}"; then
      echo "could not install the EBS CSI driver on ${NAME}." >&2
      echo "Databases cannot bind volumes without it." >&2
      exit 1
    fi
  fi
  if ! kubectl get csidriver ebs.csi.aws.com >/dev/null 2>&1; then
    echo "the EBS CSI driver is not installed on ${NAME}." >&2
    exit 1
  fi
  echo "waiting for the EBS CSI controller"
  if ! kubectl -n kube-system rollout status deploy/ebs-csi-controller --timeout=180s; then
    echo "the EBS CSI controller is not ready on ${NAME}." >&2
    echo "Databases cannot bind volumes until it is." >&2
    exit 1
  fi
  existing="$(kubectl get storageclass -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  if [[ -n "$existing" ]]; then
    echo "default StorageClass already set: ${existing%%$'\n'*}"
    return 0
  fi
  echo "creating default StorageClass gp3"
  kubectl apply -f - <<'EOF'
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: gp3
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
provisioner: ebs.csi.aws.com
parameters:
  type: gp3
  encrypted: "true"
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
EOF
}


ensure_spoke_bucket() {
  local bucket="$1"
  local created=0
  if aws s3api head-bucket --bucket "$bucket" --region "$REGION" >/dev/null 2>&1; then
    echo "S3 bucket ${bucket} already exists."
  else
    echo "creating S3 bucket ${bucket} in ${REGION}"
    local err
    if [[ "$REGION" == "us-east-1" ]]; then
      err="$(aws s3api create-bucket --bucket "$bucket" --region "$REGION" 2>&1 >/dev/null)" || true
    else
      err="$(aws s3api create-bucket --bucket "$bucket" --region "$REGION" \
        --create-bucket-configuration LocationConstraint="$REGION" 2>&1 >/dev/null)" || true
    fi
    if [[ -n "$err" ]]; then
      # S3 bucket names are global across every AWS account, so a cluster
      # name someone else already used fails here rather than at head-bucket,
      # which answers 403 for a bucket owned by another account.
      case "$err" in
        *BucketAlreadyOwnedByYou*)
          echo "S3 bucket ${bucket} is already yours."
          ;;
        *BucketAlreadyExists*)
          echo "S3 bucket ${bucket} is taken. S3 bucket names are global across all" >&2
          echo "AWS accounts, so this environment name is not available. Rerun with a" >&2
          echo "different environment name, or set BUCKET to a name you own." >&2
          exit 1
          ;;
        *)
          echo "$err" >&2
          exit 1
          ;;
      esac
    else
      created=1
      CREATED_BUCKET=1
      # Only a bucket this call made is tagged: put-bucket-tagging replaces the
      # whole tag set, which on a bucket that was already there is not ours to
      # change. A failure to tag leaves a bucket that cleanup identifies by the
      # name recorded in the cluster instead, so it must not fail the join.
      if ! aws s3api put-bucket-tagging --bucket "$bucket" --region "$REGION" \
        --tagging "TagSet=[{Key=percona.com/pds,Value=${NAME}}]" >/dev/null 2>&1; then
        echo "could not tag S3 bucket ${bucket} with percona.com/pds=${NAME}." >&2
        echo "It still works. Without the tag, cleanup finds it by the name saved for this environment." >&2
      fi
    fi
  fi

  # No bucket policy is written. Access is granted by the cluster's own IAM
  # user policy in ensure_spoke_identity, which is enough for same-account
  # access and is the single place the grant lives. A bucket policy would have
  # to name that user, which does not exist when the bucket is created, and
  # duplicating the grant in two documents is how one of them goes stale.
  if [[ "$created" == 1 ]]; then
    echo "created ${bucket}. Access is granted by the $(spoke_iam_user) IAM policy."
  fi
}

# spoke_iam_user is the IAM identity that owns one cluster's backup storage.
# One user per cluster, so a leaked key reaches one bucket and revoking it
# touches one cluster.
#
# Resolved once and remembered. The cleanup path reads it after a failure, and
# a second lookup that disagreed with the first would delete a user this run
# did not create.
# A cluster that joined before the product was renamed to PDS has its user
# under the old name, holding the key its backups authenticate with. A repair
# keeps using it rather than minting a second user with a second live key.
# Nothing is created under the old name.
SPOKE_IAM_USER=""
resolve_spoke_iam_user() {
  if [[ -n "${SPOKE_IAM_USER:-}" ]]; then
    return 0
  fi

  local current before_rename
  current="$(spoke_aws_name)"
  SPOKE_IAM_USER="$current"
  if aws iam get-user --user-name "$current" >/dev/null 2>&1; then
    return 0
  fi
  before_rename="$(bound_aws_name "basecamp-")" # allow-legacy-name
  if aws iam get-user --user-name "$before_rename" >/dev/null 2>&1 \
    && spoke_iam_user_owned "$before_rename"; then
    SPOKE_IAM_USER="$before_rename"
    echo "this environment's backup identity predates the rename. Reusing ${SPOKE_IAM_USER}." >&2
  fi
}

spoke_iam_user() {
  resolve_spoke_iam_user
  printf '%s\n' "${SPOKE_IAM_USER}"
}

# Connect owns users it created (managed-by=pds) and users that already
# carry the backup policy from an earlier Connect run. Both marks have an
# older spelling, from before the product was renamed to PDS.
spoke_iam_user_owned() {
  local user="$1"
  local managed
  managed="$(aws iam list-user-tags --user-name "$user" \
    --query "Tags[?Key=='managed-by'].Value | [0]" --output text 2>/dev/null || true)"
  if [[ "$managed" == "pds" || "$managed" == "basecamp" ]]; then # allow-legacy-name
    return 0
  fi
  aws iam get-user-policy --user-name "$user" --policy-name pds-backup-storage >/dev/null 2>&1 \
    || aws iam get-user-policy --user-name "$user" --policy-name basecamp-backup-storage >/dev/null 2>&1 # allow-legacy-name
}

# ensure_spoke_identity mints the credential the spoke uses for backups.
#
# It exists because the alternative is copying whoever ran this command into
# the customer's cluster. That fails two ways: an SSO or assumed-role session
# is temporary, so backups start failing with InvalidAccessKeyId within hours,
# and a long lived personal key would put an operator's own IAM identity, here
# often account administrator, inside a Secret any namespace reader can pull.
#
# The identity is scoped to this cluster's own bucket.
#
# Idempotent on purpose. Re-running this command against a cluster that already
# joined is how an existing cluster gets repaired, so it must converge rather
# than fail on anything that already exists.
ensure_spoke_identity() {
  local bucket="$1"
  local user
  user="$(spoke_iam_user)"

  if [[ -n "${BACKUP_ACCESS_KEY_ID:-}" && -n "${BACKUP_SECRET_ACCESS_KEY:-}" ]]; then
    if [[ "$BACKUP_ACCESS_KEY_ID" == ASIA* ]]; then
      echo "BACKUP_ACCESS_KEY_ID is a temporary key. Those expire and cannot be stored without a session token." >&2
      echo "Supply a long-lived IAM access key, or let this command create one." >&2
      exit 1
    fi
    echo "using the backup access key supplied in the environment"
    write_s3_secret "$BACKUP_ACCESS_KEY_ID" "$BACKUP_SECRET_ACCESS_KEY"

    return 0
  fi

  if ! aws iam get-user --user-name "$user" >/dev/null 2>&1; then
    echo "creating IAM user ${user} for backup storage"
    if ! aws iam create-user --user-name "$user" \
      --tags "Key=managed-by,Value=pds" "Key=pds-cluster,Value=${NAME}" \
      "Key=percona.com/pds,Value=${NAME}" >/dev/null 2>&1; then
      echo "could not create the IAM user ${user} for backup storage." >&2
      echo "This command needs iam:CreateUser, iam:PutUserPolicy and" >&2
      echo "iam:CreateAccessKey, or a key supplied as BACKUP_ACCESS_KEY_ID and" >&2
      echo "BACKUP_SECRET_ACCESS_KEY that can read and write s3://${bucket}." >&2
      exit 1
    fi
    CREATED_IAM_USER=1
  elif ! spoke_iam_user_owned "$user"; then
    echo "IAM user ${user} already exists and is not a Connect backup user." >&2
    echo "Set BACKUP_ACCESS_KEY_ID and BACKUP_SECRET_ACCESS_KEY, or pick another environment name." >&2
    exit 1
  else
    aws iam tag-user --user-name "$user" \
      --tags "Key=managed-by,Value=pds" "Key=pds-cluster,Value=${NAME}" \
      "Key=percona.com/pds,Value=${NAME}" >/dev/null 2>&1 || true
  fi

  local resources
  resources="\"arn:aws:s3:::${bucket}\", \"arn:aws:s3:::${bucket}/*\""

  cat >"${tmp}/spoke-policy.json" <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "PDSBackupStorage",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket", "s3:GetBucketLocation", "s3:AbortMultipartUpload", "s3:ListBucketMultipartUploads", "s3:ListMultipartUploadParts"],
      "Resource": [${resources}]
    }
  ]
}
EOF
  aws iam put-user-policy --user-name "$user" --policy-name pds-backup-storage \
    --policy-document "file://${tmp}/spoke-policy.json" >/dev/null

  # IAM allows two access keys per user. Keep one existing key until this
  # run finishes, then retire it. Two keys means a slot must be freed first
  # or create-access-key fails with LimitExceeded.
  local existing kept=""
  existing="$(aws iam list-access-keys --user-name "$user" \
    --query 'AccessKeyMetadata[].AccessKeyId' --output text 2>/dev/null || true)"
  local key
  for key in $existing; do
    [[ -z "$key" ]] && continue
    if [[ -n "$kept" ]]; then
      aws iam delete-access-key --user-name "$user" --access-key-id "$kept" >/dev/null 2>&1 || true
    fi
    kept="$key"
  done

  local created access secret
  created="$(aws iam create-access-key --user-name "$user" --output json)"
  access="$(printf '%s' "$created" | python3 -c 'import json,sys; print(json.load(sys.stdin)["AccessKey"]["AccessKeyId"])')"
  secret="$(printf '%s' "$created" | python3 -c 'import json,sys; print(json.load(sys.stdin)["AccessKey"]["SecretAccessKey"])')"
  if [[ -z "$access" || -z "$secret" ]]; then
    echo "IAM did not return an access key for ${user}." >&2
    exit 1
  fi
  CREATED_ACCESS_KEY_ID="$access"

  write_s3_secret "$access" "$secret"
  KEPT_ACCESS_KEY_ID="$kept"
}

# retire_previous_backup_key drops the access key this run replaced, after
# Helm and Flux have finished. Doing it earlier leaves a joined cluster with
# no valid key when a later step fails and on_exit also deletes the key this
# run minted.
retire_previous_backup_key() {
  if [[ -z "${KEPT_ACCESS_KEY_ID:-}" ]]; then
    return 0
  fi
  aws iam delete-access-key --user-name "$(spoke_iam_user)" --access-key-id "$KEPT_ACCESS_KEY_ID" >/dev/null 2>&1 || true
  KEPT_ACCESS_KEY_ID=""
}

# write_s3_secret stores the backup credential in the spoke. It takes the key
# rather than reading the environment, because the environment holds the
# operator's own credentials and those must never reach a customer cluster.
write_s3_secret() {
  local access="$1"
  local secret="$2"
  if [[ -z "$access" || -z "$secret" ]]; then
    echo "AWS access keys are required to register S3 backup storage." >&2
    exit 1
  fi
  if [[ "$access" == ASIA* ]]; then
    echo "Temporary backup keys cannot be stored. Supply a long-lived IAM access key, or let this command create one." >&2
    exit 1
  fi

  kubectl create namespace "$PLATFORM_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
  mkdir -p "$tmp"
  local access_file secret_file
  access_file="${tmp}/s3-access"
  secret_file="${tmp}/s3-secret"
  printf '%s' "$access" >"$access_file"
  printf '%s' "$secret" >"$secret_file"
  chmod 600 "$access_file" "$secret_file"
  kubectl -n "$PLATFORM_NAMESPACE" create secret generic pds-s3 \
    --from-file=AWS_ACCESS_KEY_ID="$access_file" \
    --from-file=AWS_SECRET_ACCESS_KEY="$secret_file" \
    --dry-run=client -o yaml | kubectl apply -f -
  rm -f "$access_file" "$secret_file"
  # The Secret is already on the cluster. If this is an existing release
  # and this run minted a credential, deleting that key on a later Helm
  # failure leaves the install pointed at a key IAM no longer has.
  if { [[ "${CREATED_IAM_USER:-0}" == 1 ]] || [[ -n "${CREATED_ACCESS_KEY_ID:-}" ]]; } && platform_installed; then
    APPLIED_IDENTITY_TO_EXISTING=1
  fi
}

# mirror_s3_secret copies the backup credential into the database namespace.
#
# The credential is written to the Helm namespace, and a BackupStorage lives in
# the database namespace with a credentialsSecretRef that carries no namespace,
# so it resolves there. Without this the operator reads a Secret that is not
# present and every backup fails.
#
# This used to sit inside a helper only a restoring join called, so an ordinary
# join left the credential in one namespace and the BackupStorage looking in
# the other. It runs on every join now, and re-running is what carries a
# rotated key across on an existing cluster.
mirror_s3_secret() {
  local dbns="${DB_NAMESPACE:-pds}"
  if [[ "$PLATFORM_NAMESPACE" == "$dbns" ]]; then
    return 0
  fi
  if ! kubectl get namespace "$dbns" >/dev/null 2>&1; then
    echo "namespace ${dbns} is not there yet, skipping the S3 secret mirror" >&2

    return 0
  fi
  if ! kubectl -n "$PLATFORM_NAMESPACE" get secret pds-s3 >/dev/null 2>&1; then
    return 0
  fi

  local access_key secret_key access_file secret_file
  access_key="$(kubectl -n "$PLATFORM_NAMESPACE" get secret pds-s3 -o jsonpath='{.data.AWS_ACCESS_KEY_ID}' | python3 -c 'import base64,sys; sys.stdout.write(base64.b64decode(sys.stdin.read()).decode())')"
  secret_key="$(kubectl -n "$PLATFORM_NAMESPACE" get secret pds-s3 -o jsonpath='{.data.AWS_SECRET_ACCESS_KEY}' | python3 -c 'import base64,sys; sys.stdout.write(base64.b64decode(sys.stdin.read()).decode())')"
  access_file="${tmp}/mirror-s3-access"
  secret_file="${tmp}/mirror-s3-secret"
  mkdir -p "$tmp"
  printf '%s' "$access_key" >"$access_file"
  printf '%s' "$secret_key" >"$secret_file"
  chmod 600 "$access_file" "$secret_file"
  kubectl -n "$dbns" create secret generic pds-s3 \
    --from-file=AWS_ACCESS_KEY_ID="$access_file" \
    --from-file=AWS_SECRET_ACCESS_KEY="$secret_file" \
    --dry-run=client -o yaml | kubectl apply -f -
  rm -f "$access_file" "$secret_file"
}

resolve_image() {
  IMAGE_REPOSITORY=""
  IMAGE_TAG=""
  if [[ -n "$IMAGE" ]]; then
    parse_image "$IMAGE"
    return 0
  fi
  # There used to be a fallback that scanned every Deployment in the cluster
  # for an adapter image, preferring a lab tag. On a cluster already running
  # an older install that picked a build with no agent role, which then crash
  # looped with no explanation. The hub says which image to run, or nothing does.
  echo "the control plane did not provide an agent image." >&2
  echo "Contact Percona support." >&2
  exit 1
}

# parse_image splits IMAGE into IMAGE_REPOSITORY and IMAGE_TAG. A registry
# host:port is not a tag; only a colon in the last path component is. Digest
# refs keep "@sha256" on the repository so Helm's repository:tag join rebuilds
# name@sha256:hex.
parse_image() {
  local image="$1"
  local last="${image##*/}"
  if [[ "$last" == *:* ]]; then
    IMAGE_REPOSITORY="${image%:*}"
    IMAGE_TAG="${image##*:}"
    return 0
  fi
  IMAGE_REPOSITORY="$image"
  IMAGE_TAG="latest"
}

# Private GHCR packages need the hub token on the spoke. Public images and
# non-GHCR registries pull without it.
need_registry_pull_secret() {
  [[ -n "${REGISTRY_PASSWORD:-}" ]] || return 1
  local image="${IMAGE_REPOSITORY:-}"
  if [[ -z "$image" && -n "${IMAGE:-}" ]]; then
    image="${IMAGE%:*}"
  fi
  case "$image" in
    ghcr.io/*) return 0 ;;
  esac
  return 1
}

ensure_registry_pull_secret() {
  local ns="$1"
  local secret="${REGISTRY_PULL_SECRET_NAME:-ghcr-pull}"
  need_registry_pull_secret || return 0
  [[ -n "$ns" ]] || return 0
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
  mkdir -p "$tmp"
  local dockerconfig="${tmp}/dockerconfigjson"
  local password_file="${tmp}/registry-password"
  printf '%s' "$REGISTRY_PASSWORD" >"$password_file"
  chmod 600 "$password_file"
  python3 - "${REGISTRY_SERVER:-ghcr.io}" "${REGISTRY_USERNAME:-x-access-token}" "$password_file" "$dockerconfig" <<'PY'
import base64, json, os, sys

server, user, password_file, dest = sys.argv[1:]
with open(password_file, encoding="utf-8") as f:
    password = f.read()
auth = base64.b64encode(f"{user}:{password}".encode()).decode()
fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    json.dump({"auths": {server: {"auth": auth}}}, f)
PY
  kubectl -n "$ns" create secret generic "$secret" \
    --type=kubernetes.io/dockerconfigjson \
    --from-file=.dockerconfigjson="$dockerconfig" \
    --dry-run=client -o yaml | kubectl apply -f -
  rm -f "$dockerconfig" "$password_file"
}

write_spoke_registry_pull_secret() {
  local dest="$1"
  local secret="${REGISTRY_PULL_SECRET_NAME:-ghcr-pull}"
  need_registry_pull_secret || return 0
  cat >>"$dest" <<EOF
imagePullSecrets:
  - name: ${secret}
EOF
}

find_local_chart() {
  if [[ -n "${PDS_CHART:-}" && -f "${PDS_CHART}/Chart.yaml" ]]; then
    printf '%s' "$PDS_CHART"
    return 0
  fi
  # An update moves to the control plane's own release. A checkout that happens
  # to be under the current directory is not that, so only PDS_CHART counts.
  if [[ "${UPDATE:-0}" == 1 ]]; then
    return 1
  fi
  # Only the checkout this command ran from. Another clone on disk can be a
  # different branch whose chart still deploys the UI, and Helm --wait then
  # dies on ghcr.io/percona/percona-services-ui.
  if root="$(git rev-parse --show-toplevel 2>/dev/null)" && [[ -f "${root}/charts/pds/Chart.yaml" ]]; then
    printf '%s' "${root}/charts/pds"
    return 0
  fi
  if [[ -f "${PWD}/charts/pds/Chart.yaml" ]]; then
    printf '%s' "${PWD}/charts/pds"
    return 0
  fi
  return 1
}

download_hub_chart() {
  mkdir -p "${tmp}/chart"
  if ! curl_retry -fsSL "${HUB_URL}/api/internal/connect/pds.tgz" -o "${tmp}/pds.tgz"; then
    return 1
  fi
  tar -xzf "${tmp}/pds.tgz" -C "${tmp}/chart"
  if [[ -f "${tmp}/chart/pds/Chart.yaml" ]]; then
    printf '%s' "${tmp}/chart/pds"
    return 0
  fi
  if [[ -f "${tmp}/chart/Chart.yaml" ]]; then
    printf '%s' "${tmp}/chart"
    return 0
  fi
  return 1
}

resolve_chart() {
  CHART_REF=""
  CHART_VERSION="${PDS_CHART_VERSION:-}"
  if CHART_REF="$(find_local_chart)"; then
    echo "using local Helm chart ${CHART_REF}"
    update_chart_dependencies "$CHART_REF"
    return 0
  fi
  if CHART_REF="$(download_hub_chart)"; then
    echo "using Helm chart downloaded from the control plane"
    update_chart_dependencies "$CHART_REF"
    return 0
  fi
  # A released chart, only when the operator names one. A published release
  # does not set the agent role, so reaching for it implicitly would install
  # a spoke that cannot heartbeat.
  if [[ -n "${PDS_CHART_OCI:-}" ]]; then
    CHART_REF="$PDS_CHART_OCI"
    echo "using released Helm chart ${CHART_REF} ${CHART_VERSION:-(latest)}"
    return 0
  fi
  echo "no Helm chart is available for this environment." >&2
  echo "This control plane image carries no packaged chart, and this machine has no" >&2
  echo "charts/pds checkout. Either export PDS_CHART to" >&2
  echo "a checkout, or export PDS_CHART_OCI (with" >&2
  echo "PDS_CHART_VERSION) to a released chart that supports" >&2
  echo "ADAPTER_ROLE=agent, then rerun." >&2
  exit 1
}

# Unpackaged charts still need their subcharts. This runs during resolve_chart
# so a missing dependency repository fails before eksctl spends money, and
# install_platform can call it again without repeating the work.
update_chart_dependencies() {
  local chart="${1:-}"
  [[ -f "${chart}/Chart.yaml" ]] || return 0
  if [[ "${CHART_DEPS_UPDATED:-}" == "$chart" ]]; then
    return 0
  fi
  echo "updating Helm chart dependencies"
  local repo_root
  repo_root="$(cd "${chart}/../.." && pwd)"
  if [[ -x "${repo_root}/scripts/helm-deps.sh" ]]; then
    "${repo_root}/scripts/helm-deps.sh"
  else
    helm dependency update "$chart" >/dev/null
  fi
  CHART_DEPS_UPDATED="$chart"
}

helm_release_deployed() {
  local release="$1"
  if [[ -z "$release" || -z "${PLATFORM_NAMESPACE:-}" ]]; then
    return 1
  fi
  local status
  status="$(helm status "$release" --namespace "$PLATFORM_NAMESPACE" -o json 2>/dev/null |
    python3 -c 'import json,sys; print(json.load(sys.stdin).get("info",{}).get("status") or "")' 2>/dev/null || true)"
  [[ "$status" == "deployed" ]]
}

platform_installed() {
  helm_release_deployed "${PLATFORM_RELEASE:-}"
}

# resolve_platform_release finds the release this cluster already runs.
#
# A spoke installed before the product was renamed to PDS has it under the old
# name. Upgrading that release in place is the repair; installing beside it
# would leave two adapters beating for the same cluster and collide on the
# chart's cluster scoped objects. A cluster with no release installs under the
# current name, so nothing is ever created under the old one.
resolve_platform_release() {
  if [[ "$PLATFORM_RELEASE_NAMED" == 1 ]]; then
    return 0
  fi
  if helm_release_deployed "$PLATFORM_RELEASE"; then
    return 0
  fi
  local before_rename="percona-services" # allow-legacy-name
  if helm_release_deployed "$before_rename"; then
    PLATFORM_RELEASE="$before_rename"
    echo "the platform release here predates the rename. Reconciling ${PLATFORM_RELEASE} rather than installing beside it."
  fi
}

# helm_wait_flag picks how to ask Helm to wait.
#
# Helm 3 has one behaviour: wait for workloads to become ready. Helm 4 turned
# that into strategies and made a bare --wait mean "watcher", which judges
# every resource in the release, CustomResourceDefinitions included. A CRD
# reports an unknown status to that watcher and never resolves, so an install
# carrying operator CRDs waits out the whole timeout and then fails with every
# CRD listed as not ready. "legacy" is the Helm 3 behaviour under a name, and
# Helm 3 rejects the argument, so it only goes on when Helm 4 is answering.
helm_wait_flag() {
  local version
  version="$(helm version --short 2>/dev/null || true)"
  case "$version" in
    v4* | 4*) echo "--wait=legacy" ;;
    *) echo "--wait" ;;
  esac
}

# helm_force_conflicts_flag lets Helm 4 take over a field another manager wrote.
# Helm 4 applies server side, so a run that follows a failed install meets the
# fields the operators set on their own objects in the meantime, such as the
# selector on the PXC operator Service, and stops on the conflict. Helm 3 has
# no such flag and no such conflict.
helm_force_conflicts_flag() {
  if helm upgrade --help 2>/dev/null | grep -q -- '--force-conflicts'; then
    echo "--force-conflicts"
  fi
}

# spoke_pmm_enabled prints whether this cluster gets its own PMM. The PMM
# server image is amd64 only, so a cluster with no amd64 node skips it and its
# databases go without monitoring rather than failing the install. A cluster
# whose nodes cannot be read gets PMM, which is the chart default.
spoke_pmm_enabled() {
  local arches
  arches="$(kubectl get nodes -o jsonpath='{.items[*].status.nodeInfo.architecture}' 2>/dev/null || true)"
  if [[ -z "$arches" || " $arches " == *" amd64 "* ]]; then
    echo true
  else
    echo false
  fi
}

install_platform() {
  # This used to return early when a release was already deployed, which left
  # a re-run pointing the old adapter at a token the hub may have replaced,
  # and made install_agent add a second adapter beside it. Both then beat for
  # the same cluster and raced for the same work queue. helm upgrade --install
  # is idempotent, so reconciling is the safe thing to do on every run.
  local had_release=0
  if platform_installed; then
    echo "platform already installed in ${PLATFORM_NAMESPACE}. Reconciling it."
    had_release=1
  fi
  if [[ -z "$IMAGE_REPOSITORY" ]]; then
    echo "the control plane did not provide an image to install the platform with." >&2
    echo "Contact Percona support." >&2
    exit 1
  fi
  if [[ -z "${CHART_REF:-}" ]]; then
    resolve_chart
  fi
  if [[ "$LOCAL" != 1 ]]; then
    ensure_storage
  fi
  update_chart_dependencies "${CHART_REF}"
  # A local join has no S3 bucket and no credentials for one, so it keeps
  # the chart's MinIO. The spoke reports whichever it ends up with on its
  # heartbeat, and the hub writes that name into this cluster's manifests.
  local storage_values backup_storage
  if [[ "$LOCAL" == 1 ]]; then
    backup_storage="minio"
    storage_values="  minio:
    enabled: true
  s3:
    enabled: false"
  else
    backup_storage="s3"
    storage_values="  minio:
    enabled: false
  s3:
    enabled: true
    bucket: ${BUCKET}
    region: ${REGION}
    storageName: s3
    existingSecret: pds-s3"
  fi
  local pmm_enabled
  pmm_enabled="$(spoke_pmm_enabled)"
  if [[ "$pmm_enabled" != true ]]; then
    echo "this Kubernetes cluster has no amd64 node, so monitoring is not installed on it" >&2
  fi
  cat >"${tmp}/spoke.yaml" <<EOF
ui:
  enabled: false
  replicaCount: 0
# OpenEverest, the operators, MinIO and monitoring stay only while the
# release says the stack is on. A chart default is not what keeps them.
stack:
  enabled: true
  pmm:
    enabled: ${pmm_enabled}
    # Flux installs the PMM server from this cluster's hub Git, so it can be
    # changed later without reinstalling the platform. The release keeps the
    # admin password, the token Job and the MonitoringConfig.
    subchart: false
  dbNamespace: "${DB_NAMESPACE}"
${storage_values}
adapter:
  # The agent calls the OpenEverest Service in this cluster. The address
  # is stored on the release so an empty chart default cannot drop it.
  everestURL: http://everest.everest-system.svc.cluster.local:8080
  pmmURL: ""
  everestNamespace: "${DB_NAMESPACE}"
  everestBackupStorage: ${backup_storage}
  # The hub annotates each Instance with the preset a create resolves
  # against, so the spoke has to have them.
  installInstancePresets: true
  role: agent
  # The agent polls the hub and never serves the customer API, so it has no
  # sessions to keep and needs no database of its own. Leaving this on would
  # provision a PostgreSQL cluster the agent never reads, and leave its pod
  # waiting on a Secret it does not need.
  database:
    enabled: false
    # The stack's PostgreSQL provider installs this operator. Turning it
    # on as well installs a second one, and the chart refuses the pair.
    operator:
      enabled: false
  image:
    repository: ${IMAGE_REPOSITORY}
    tag: ${IMAGE_TAG}
  hub:
    url: "${HUB_URL}"
    environmentName: "${NAME}"
    agentToken: "${TOKEN}"
openeverest:
  upgrade:
    preflightChecks: false
EOF
  write_spoke_registry_pull_secret "${tmp}/spoke.yaml"
  if need_registry_pull_secret; then
    ensure_registry_pull_secret "$PLATFORM_NAMESPACE"
  fi
  echo "installing Percona Database Service on ${NAME}"
  if [[ "$had_release" != 1 ]]; then
    INSTALLED_PLATFORM=1
  fi
  helm_args=(upgrade --install "$PLATFORM_RELEASE" "$CHART_REF"
    --namespace "$PLATFORM_NAMESPACE" --create-namespace
    --timeout 15m "$(helm_wait_flag)"
    -f "${tmp}/spoke.yaml"
    --set ui.enabled=false
    --set ui.replicaCount=0
    --set openeverest.upgrade.preflightChecks=false)
  force_flag="$(helm_force_conflicts_flag)"
  if [[ -n "$force_flag" ]]; then
    helm_args+=("$force_flag")
  fi
  if [[ "$had_release" == 1 ]]; then
    # -f without --reuse-values replaces the release's stored values with
    # chart defaults plus spoke.yaml. A repair would drop persistence,
    # resources, and other settings this run does not mention.
    # --reuse-values keeps those, and spoke.yaml is merged on top, so the
    # stack values written above replace whatever the previous release stored.
    helm_args+=(--reuse-values)
  fi
  if [[ "$CHART_REF" == oci://* && -n "$CHART_VERSION" ]]; then
    helm_args+=(--version "$CHART_VERSION")
  fi
  if ! helm "${helm_args[@]}"; then
    echo "Helm could not install the platform from ${CHART_REF}." >&2
    echo "Export PDS_CHART to a charts/pds checkout, or PDS_CHART_VERSION to pin a released chart, and rerun." >&2
    exit 1
  fi
  # Helm has already written spoke.yaml into the release. A later
  # rollout timeout must not delete the bucket or IAM user that
  # existing install now uses.
  if [[ "$had_release" == 1 && "${CREATED_BUCKET:-0}" == 1 ]]; then
    APPLIED_BUCKET_TO_EXISTING=1
  fi
  if [[ "$had_release" == 1 && ( "${CREATED_IAM_USER:-0}" == 1 || -n "${CREATED_ACCESS_KEY_ID:-}" ) ]]; then
    APPLIED_IDENTITY_TO_EXISTING=1
  fi
  echo "waiting for the platform to reach the control plane"
  kubectl -n "$PLATFORM_NAMESPACE" rollout status "deploy/${PLATFORM_RELEASE}-adapter" \
    --timeout="$(agent_rollout_timeout)"
}

install_agent() {
  if [[ -z "$IMAGE_REPOSITORY" ]]; then
    echo "no platform image was found on this Kubernetes cluster. Install Percona Database Service first, or contact Percona support." >&2
    exit 1
  fi
  if kubectl get namespace "$AGENT_NAMESPACE" >/dev/null 2>&1; then
    had_agent_ns=1
  else
    had_agent_ns=0
  fi
  kubectl create namespace "$AGENT_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
  if [[ "$had_agent_ns" != 1 ]]; then
    CREATED_AGENT=1
  fi
  if kubectl -n "$PLATFORM_NAMESPACE" get secret pds-everest >/dev/null 2>&1; then
    token_b64="$(kubectl -n "$PLATFORM_NAMESPACE" get secret pds-everest -o jsonpath='{.data.EVEREST_TOKEN}' 2>/dev/null || true)"
    if [[ -n "$token_b64" ]]; then
      everest_token="$(printf '%s' "$token_b64" | python3 -c 'import base64,sys; sys.stdout.write(base64.b64decode(sys.stdin.read()).decode())')"
      local everest_file="${tmp}/everest-token"
      mkdir -p "$tmp"
      printf '%s' "$everest_token" >"$everest_file"
      chmod 600 "$everest_file"
      kubectl -n "$AGENT_NAMESPACE" create secret generic pds-everest \
        --from-file=EVEREST_TOKEN="$everest_file" \
        --dry-run=client -o yaml | kubectl apply -f -
      rm -f "$everest_file"
    fi
  fi
  pull_secrets_yaml=""
  if need_registry_pull_secret; then
    ensure_registry_pull_secret "$AGENT_NAMESPACE"
    pull_secrets_yaml=$'      imagePullSecrets:\n        - name: '"${REGISTRY_PULL_SECRET_NAME:-ghcr-pull}"$'\n'
  fi
  local agent_token_file="${tmp}/hub-agent-token"
  mkdir -p "$tmp"
  printf '%s' "$TOKEN" >"$agent_token_file"
  chmod 600 "$agent_token_file"
  kubectl -n "$AGENT_NAMESPACE" create secret generic pds-hub-agent-token \
    --from-file=HUB_AGENT_TOKEN="$agent_token_file" \
    --dry-run=client -o yaml | kubectl apply -f -
  rm -f "$agent_token_file"
  cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${RELEASE}
  namespace: ${AGENT_NAMESPACE}
  labels:
    app.kubernetes.io/name: pds-agent
    app.kubernetes.io/instance: ${NAME}
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: pds-agent
  template:
    metadata:
      labels:
        app.kubernetes.io/name: pds-agent
        app.kubernetes.io/instance: ${NAME}
    spec:
${pull_secrets_yaml}      containers:
        - name: adapter
          image: ${IMAGE_REPOSITORY}:${IMAGE_TAG}
          imagePullPolicy: IfNotPresent
          env:
            - name: ADAPTER_ADDR
              value: ":8080"
            - name: ADAPTER_ROLE
              value: agent
            - name: HUB_URL
              value: "${HUB_URL}"
            - name: ENVIRONMENT_NAME
              value: "${NAME}"
            - name: HUB_AGENT_TOKEN
              valueFrom:
                secretKeyRef:
                  name: pds-hub-agent-token
                  key: HUB_AGENT_TOKEN
            - name: EVEREST_NAMESPACE
              value: "${DB_NAMESPACE:-pds}"
            - name: EVEREST_URL
              value: http://everest.everest-system.svc.cluster.local:8080
          envFrom:
            - secretRef:
                name: pds-everest
                optional: true
          ports:
            - name: http
              containerPort: 8080
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
EOF
  if ! kubectl -n "$AGENT_NAMESPACE" rollout status "deploy/${RELEASE}" \
    --timeout="$(agent_rollout_timeout)"; then
    echo "agent did not become Ready. Current pods:" >&2
    kubectl -n "$AGENT_NAMESPACE" get po -o wide >&2 || true
    kubectl -n "$AGENT_NAMESPACE" describe po -l app.kubernetes.io/name=pds-agent >&2 || true
    exit 1
  fi
}

# agent_service_account prints the service account the agent Deployment runs
# as. The chart names it, so the grant below follows the Deployment instead of
# guessing a name.
agent_service_account() {
  kubectl -n "$PLATFORM_NAMESPACE" get deploy \
    -l "app.kubernetes.io/instance=${PLATFORM_RELEASE},app.kubernetes.io/component=adapter" \
    -o jsonpath='{.items[0].spec.template.spec.serviceAccountName}' || true
}

# support_read_rules prints the rules that let a support engineer look at the
# environment. Both access levels start from them. Secrets, exec, port-forward
# and token minting are absent on purpose: RBAC cannot narrow a Secret read to
# the ones labeled for support, so no level reads them.
support_read_rules() {
  cat <<'EOF'
  - apiGroups: [""]
    resources: [namespaces, nodes, pods, pods/log, services, endpoints, events, configmaps, serviceaccounts, persistentvolumeclaims, persistentvolumes, resourcequotas, limitranges]
    verbs: [get, list, watch]
  - apiGroups: [apps]
    resources: [deployments, statefulsets, daemonsets, replicasets]
    verbs: [get, list, watch]
  - apiGroups: [batch]
    resources: [jobs, cronjobs]
    verbs: [get, list, watch]
  - apiGroups: [networking.k8s.io]
    resources: [ingresses, networkpolicies]
    verbs: [get, list, watch]
  - apiGroups: [storage.k8s.io]
    resources: [storageclasses, volumeattachments]
    verbs: [get, list, watch]
  - apiGroups: [policy]
    resources: [poddisruptionbudgets]
    verbs: [get, list, watch]
  - apiGroups: [events.k8s.io]
    resources: [events]
    verbs: [get, list, watch]
  - apiGroups: [metrics.k8s.io]
    resources: [nodes, pods]
    verbs: [get, list, watch]
  - apiGroups: [apiextensions.k8s.io]
    resources: [customresourcedefinitions]
    verbs: [get, list, watch]
  - apiGroups: [rbac.authorization.k8s.io]
    resources: [roles, rolebindings, clusterroles, clusterrolebindings]
    verbs: [get, list, watch]
  - apiGroups: [core.openeverest.io, backup.openeverest.io, monitoring.openeverest.io, pgv2.percona.com, pxc.percona.com, ps.percona.com, psmdb.percona.com]
    resources: ["*"]
    verbs: [get, list, watch]
EOF
}

# install_support_access installs, once, what a support access window needs on
# this environment, so opening one later asks the customer for nothing: the two
# access levels as roles, the namespace the access workload runs in, a role on
# Secrets in that namespace for the workload, the workload's permission to
# impersonate engineers, and a narrow grant for the agent.
# The agent can bind the two fixed roles and no others, and it is not
# cluster-admin. Per window it creates the workload and a binding, and removes
# both when the window ends.
#
# Admin is a write allowlist: the database objects and a pod restart. It leaves
# out Deployments, ServiceAccounts, ConfigMaps, Secrets and RBAC, so a window
# cannot be used to change the workload, its binding or the agent.
#
# Everything is applied declaratively, so running it again changes nothing.
install_support_access() {
  local ns="pds-support" agent_sa
  agent_sa="$(agent_service_account)"
  if [[ -z "$agent_sa" ]]; then
    echo "could not find the service account the agent runs as, so support access cannot be set up." >&2
    echo "Contact Percona support." >&2
    exit 1
  fi
  {
    cat <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: ${ns}
  labels:
    app.kubernetes.io/name: pds-support
    app.kubernetes.io/part-of: pds
    pod-security.kubernetes.io/enforce: baseline
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: pds-support-read
  labels:
    app.kubernetes.io/part-of: pds
rules:
EOF
    support_read_rules
    cat <<EOF
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: pds-support-admin
  labels:
    app.kubernetes.io/part-of: pds
rules:
EOF
    support_read_rules
    cat <<EOF
  - apiGroups: [core.openeverest.io]
    resources: [instances]
    verbs: [create, update, patch]
  - apiGroups: [backup.openeverest.io]
    resources: [backups]
    verbs: [create, update, patch]
  - apiGroups: [backup.openeverest.io]
    resources: [restores]
    verbs: [create, update, patch, delete]
  - apiGroups: [""]
    resources: [pods]
    verbs: [delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: pds-support-proxy
  labels:
    app.kubernetes.io/part-of: pds
rules:
  - apiGroups: [""]
    resources: [users]
    verbs: [impersonate]
  - apiGroups: [""]
    resources: [groups]
    verbs: [impersonate]
    resourceNames: [pds.percona.com:support]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: pds-support-proxy
  labels:
    app.kubernetes.io/part-of: pds
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: pds-support-proxy
subjects:
  - kind: ServiceAccount
    name: pds-support-proxy
    namespace: ${ns}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: pds-support-agent
  labels:
    app.kubernetes.io/part-of: pds
rules:
  - apiGroups: [rbac.authorization.k8s.io]
    resources: [clusterroles]
    verbs: [bind]
    resourceNames: [pds-support-read, pds-support-admin]
  - apiGroups: [rbac.authorization.k8s.io]
    resources: [clusterrolebindings]
    verbs: [get, list, create, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: pds-support-agent
  labels:
    app.kubernetes.io/part-of: pds
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: pds-support-agent
subjects:
  - kind: ServiceAccount
    name: ${agent_sa}
    namespace: ${PLATFORM_NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: pds-support-agent
  namespace: ${ns}
  labels:
    app.kubernetes.io/part-of: pds
rules:
  - apiGroups: [""]
    resources: [serviceaccounts, configmaps, secrets]
    verbs: [get, list, watch, create, update, patch, delete]
  - apiGroups: [apps]
    resources: [deployments]
    verbs: [get, list, watch, create, update, patch, delete]
  - apiGroups: [""]
    resources: [pods, events]
    verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: pds-support-agent
  namespace: ${ns}
  labels:
    app.kubernetes.io/part-of: pds
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: pds-support-agent
subjects:
  - kind: ServiceAccount
    name: ${agent_sa}
    namespace: ${PLATFORM_NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: pds-support-secrets
  namespace: ${ns}
  labels:
    app.kubernetes.io/part-of: pds
rules:
  - apiGroups: [""]
    resources: [secrets]
    verbs: [get, update, patch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: pds-support-secrets
  namespace: ${ns}
  labels:
    app.kubernetes.io/part-of: pds
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: pds-support-secrets
subjects:
  - kind: ServiceAccount
    name: pds-support-proxy
    namespace: ${ns}
EOF
  } | kubectl apply -f -
}

# sha256_of prints the digest of a file. macOS ships shasum, most Linux images
# ship sha256sum, and this script runs on whichever machine the operator pasted
# the command into.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d" " -f1
  else
    shasum -a 256 "$1" | cut -d" " -f1
  fi
}

# flux_expected_digest prints the sha256 install_flux must see, or fails when
# the operator asked for a version nobody pinned. Kept apart from install_flux
# so the refusal is testable without standing up a cluster.
flux_expected_digest() {
  if [ -n "$FLUX_SHA256" ]; then
    echo "$FLUX_SHA256"

    return 0
  fi
  if [ "$FLUX_VERSION" = "$FLUX_PINNED_VERSION" ]; then
    echo "$FLUX_PINNED_SHA256"

    return 0
  fi

  echo "FLUX_VERSION is ${FLUX_VERSION}, which has no pinned digest." >&2
  echo "Set FLUX_SHA256 to its install.yaml sha256, or FLUX_SHA256=skip to install unverified." >&2

  return 1
}

# mark_created_flux records that this run created flux-system. A missing
# GitRepository CRD is not enough: a partial install can already own that
# namespace, and deleting it would take someone else's Flux with it.
mark_created_flux() {
  if kubectl get namespace flux-system >/dev/null 2>&1; then
    return 0
  fi
  CREATED_FLUX=1
}

# flux_install_needed is true when the pinned Flux install is missing or
# incomplete. A leftover GitRepository CRD is not a working install.
flux_install_needed() {
  kubectl get crd gitrepositories.source.toolkit.fluxcd.io >/dev/null 2>&1 || return 0
  kubectl get crd kustomizations.kustomize.toolkit.fluxcd.io >/dev/null 2>&1 || return 0
  kubectl get namespace flux-system >/dev/null 2>&1 || return 0
  kubectl -n flux-system get deploy/source-controller >/dev/null 2>&1 || return 0
  kubectl -n flux-system get deploy/kustomize-controller >/dev/null 2>&1 || return 0
  kubectl -n flux-system get deploy/helm-controller >/dev/null 2>&1 || return 0
  # An update also replaces an install of another version than the pinned one.
  if [[ "${UPDATE:-0}" == 1 ]]; then
    local have
    have="$(kubectl -n flux-system get deploy/source-controller \
      -o jsonpath='{.metadata.labels.app\.kubernetes\.io/version}' 2>/dev/null || true)"
    if [[ -n "$have" && "$have" != "v${FLUX_VERSION}" ]]; then
      return 0
    fi
  fi
  return 1
}

# delete_applied_flux removes objects install_flux applied from install.yaml.
# keep_namespace=1 leaves a flux-system namespace this run did not create.
delete_applied_flux() {
  local names="$1"
  local keep_namespace="${2:-0}"
  [[ -n "$names" && -f "$names" ]] || return 0
  while IFS= read -r res || [[ -n "$res" ]]; do
    res="${res%$'\r'}"
    [[ -n "$res" ]] || continue
    if [[ "$keep_namespace" == 1 && "$res" == "namespace/flux-system" ]]; then
      continue
    fi
    kubectl delete "$res" --ignore-not-found --wait=false || true
  done < "$names"
}

install_flux() {
  echo "installing the component that applies this environment's databases from the control plane"
  if flux_install_needed; then
    mark_created_flux

    local want manifest actual
    want="$(flux_expected_digest)" || exit 1

    manifest="$(mktemp)"
    curl_retry -fsSL -o "$manifest" "https://github.com/fluxcd/flux2/releases/download/v${FLUX_VERSION}/install.yaml"

    if [ "$want" = "skip" ]; then
      echo "installing Git reconciliation ${FLUX_VERSION} without verifying its digest"
    else
      actual="$(sha256_of "$manifest")"
      if [ "$actual" != "$want" ]; then
        rm -f "$manifest"
        echo "The install manifest does not match its pinned digest." >&2
        echo "  expected ${want}" >&2
        echo "  got      ${actual}" >&2
        echo "Refusing to apply it. A release asset can be replaced after publication." >&2
        exit 1
      fi
    fi

    FLUX_APPLIED_NAMES="${FLUX_APPLIED_NAMES:-$(mktemp)}"
    if ! kubectl apply -f "$manifest" -o name >"$FLUX_APPLIED_NAMES"; then
      APPLIED_FLUX=1
      rm -f "$manifest"
      echo "The install manifest could not be applied." >&2
      exit 1
    fi
    APPLIED_FLUX=1
    rm -f "$manifest"
    kubectl wait --for=condition=Established crd/gitrepositories.source.toolkit.fluxcd.io --timeout=120s
    kubectl wait --for=condition=Established crd/kustomizations.kustomize.toolkit.fluxcd.io --timeout=120s
    kubectl wait --for=condition=Established crd/helmreleases.helm.toolkit.fluxcd.io --timeout=120s
  fi
  kubectl -n flux-system rollout status deploy/source-controller --timeout=180s
  kubectl -n flux-system rollout status deploy/kustomize-controller --timeout=180s
  kubectl -n flux-system rollout status deploy/helm-controller --timeout=180s
  secret_yaml=""
  if [[ -n "${GIT_IDENTITY:-}" ]]; then
    local identity_dir identity_file identity_pub_file known_hosts_file
    identity_dir="$(mktemp -d)"
    identity_file="${identity_dir}/identity"
    identity_pub_file="${identity_dir}/identity.pub"
    known_hosts_file="${identity_dir}/known_hosts"
    printf '%s' "${GIT_IDENTITY}" > "${identity_file}"
    if [[ -s "${identity_file}" && -n "$(tail -c 1 "${identity_file}")" ]]; then
      printf '\n' >> "${identity_file}"
    fi
    printf '%s\n' "${GIT_IDENTITY_PUB}" > "${identity_pub_file}"
    printf '%s\n' "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl" > "${known_hosts_file}"
    kubectl -n flux-system create secret generic hub-github \
      --from-file=identity="${identity_file}" \
      --from-file=identity.pub="${identity_pub_file}" \
      --from-file=known_hosts="${known_hosts_file}" \
      --dry-run=client -o yaml | kubectl apply -f -
    rm -rf "${identity_dir}"
    secret_yaml=$'  secretRef:\n    name: hub-github'
  elif [[ -n "${GIT_PASSWORD:-}" ]]; then
    local password_file="${tmp}/git-password"
    mkdir -p "$tmp"
    printf '%s' "$GIT_PASSWORD" >"$password_file"
    chmod 600 "$password_file"
    kubectl -n flux-system create secret generic hub-github \
      --from-literal=username="${GIT_USERNAME:-x-access-token}" \
      --from-file=password="${password_file}" \
      --dry-run=client -o yaml | kubectl apply -f -
    rm -f "$password_file"
    secret_yaml=$'  secretRef:\n    name: hub-github'
  fi
  kubectl create namespace "${DB_NAMESPACE:-pds}" --dry-run=client -o yaml | kubectl apply -f -
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: hub-instances
  namespace: flux-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: hub-instances
  namespace: ${DB_NAMESPACE:-pds}
rules:
  - apiGroups: ["core.openeverest.io"]
    resources: ["instances"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: hub-instances
  namespace: ${DB_NAMESPACE:-pds}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: hub-instances
subjects:
  - kind: ServiceAccount
    name: hub-instances
    namespace: flux-system
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: hub
  namespace: flux-system
spec:
  interval: 15s
  url: "${GIT_URL}"
${secret_yaml}
  ref:
    branch: main
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: instances
  namespace: flux-system
spec:
  interval: 15s
  prune: true
  serviceAccountName: hub-instances
  targetNamespace: ${DB_NAMESPACE:-pds}
  sourceRef:
    kind: GitRepository
    name: hub
  path: clusters/${NAME}/instances
EOF
  apply_platform_kustomization
}

# apply_platform_kustomization points Flux at this cluster's platform tree in
# hub Git, which is where PMM is defined. It gets a service account of its own
# that can only manage HelmRelease and HelmRepository objects and the ConfigMap
# of PMM's alert rules in the platform namespace: the instances Kustomization may only touch Instances, and the
# helm-controller does the install itself. A cluster with no amd64 node gets
# no PMM, so nothing is applied for it.
apply_platform_kustomization() {
  if [[ "$(spoke_pmm_enabled)" != true ]]; then
    return 0
  fi
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: hub-platform
  namespace: flux-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: hub-platform
  namespace: ${PLATFORM_NAMESPACE}
rules:
  - apiGroups: ["helm.toolkit.fluxcd.io"]
    resources: ["helmreleases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["source.toolkit.fluxcd.io"]
    resources: ["helmrepositories"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: hub-platform
  namespace: ${PLATFORM_NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: hub-platform
subjects:
  - kind: ServiceAccount
    name: hub-platform
    namespace: flux-system
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
  namespace: flux-system
spec:
  interval: 15s
  prune: true
  serviceAccountName: hub-platform
  targetNamespace: ${PLATFORM_NAMESPACE}
  sourceRef:
    kind: GitRepository
    name: hub
  path: clusters/${NAME}/platform
EOF
}

# update_refuse prints what an update found and exits. The reasons are
# collected first, so one run names everything in the way instead of one
# problem per attempt.
update_refuse() {
  local reason
  for reason in "$@"; do
    echo "$reason" >&2
  done
  echo "Nothing was changed." >&2
  exit 1
}

# update_check reads two things: the pin the chart carries, and the manifest of
# the release the Kubernetes cluster runs. It prints one VERSIONS line naming
# both sets of versions and one REFUSE line for each version the update cannot
# move. python3 is the only parser the customer's machine is sure to have, so
# the pin is read here rather than with a YAML library.
#
# A floor is a ">= version" in support.update.from, keyed openeverest, pxc,
# postgresql or mongodb. A component the release does not run is not checked,
# which is how an environment that joined before a provider was pinned is
# updated: the chart's upgrade installs that provider.
update_check() {
  python3 - "$1" "$2" "$3" <<'PY'
import re
import sys

pin_path, manifest_path, name = sys.argv[1:4]


def read_pin(path):
    root = {}
    stack = [(-1, root)]
    with open(path, encoding="utf-8") as f:
        for raw in f:
            line = re.sub(r"\s+#.*$", "", raw.rstrip("\n"))
            if not line.strip() or line.lstrip().startswith(("#", "- ")):
                continue
            indent = len(line) - len(line.lstrip())
            key, _, value = line.strip().partition(":")
            value = value.strip().strip("\"'")
            while stack[-1][0] >= indent:
                stack.pop()
            if value:
                stack[-1][1][key] = value
            else:
                child = {}
                stack[-1][1][key] = child
                stack.append((indent, child))
    return root


def walk(tree, *keys):
    for key in keys:
        if not isinstance(tree, dict):
            return None
        tree = tree.get(key)
    return tree


def parse(version):
    m = re.match(r"^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+.*)?$", version or "")
    if not m:
        return None
    pre = m.group(4).split(".") if m.group(4) else None
    return (int(m.group(1)), int(m.group(2)), int(m.group(3))), pre


def compare(a, b):
    (acore, apre), (bcore, bpre) = parse(a), parse(b)
    if acore != bcore:
        return -1 if acore < bcore else 1
    if apre == bpre:
        return 0
    if apre is None:
        return 1
    if bpre is None:
        return -1
    for x, y in zip(apre, bpre):
        if x == y:
            continue
        if x.isdigit() and y.isdigit():
            return -1 if int(x) < int(y) else 1
        if x.isdigit():
            return -1
        if y.isdigit():
            return 1
        return -1 if x < y else 1
    return (len(apre) > len(bpre)) - (len(apre) < len(bpre))


pin = read_pin(pin_path)
with open(manifest_path, encoding="utf-8") as f:
    manifest = f.read()


def first(pattern):
    m = re.search(pattern, manifest, re.M)
    return m.group(1) if m else ""


# (label, floor key, installed, version in this control plane's chart)
components = [
    ("OpenEverest", "openeverest", first(r"^\s*openeverest\.version:\s*[\"']?([^\"'\s]+)"),
     walk(pin, "openeverest", "version")),
    ("provider-pxc", "pxc", first(r"helm\.sh/chart:\s*[\"']?provider-pxc-([^\"'\s]+)"),
     walk(pin, "providers", "pxc", "version")),
    ("provider-postgresql", "postgresql", first(r"helm\.sh/chart:\s*[\"']?provider-postgresql-([^\"'\s]+)"),
     walk(pin, "providers", "postgresql", "version")),
    ("provider-mongodb", "mongodb", first(r"helm\.sh/chart:\s*[\"']?provider-mongodb-([^\"'\s]+)"),
     walk(pin, "providers", "mongodb", "version")),
]
floors = walk(pin, "support", "update", "from") or {}

have, want = [], []
for label, key, installed, new in components:
    if not installed:
        if key == "openeverest":
            print(
                "REFUSE The release does not say which OpenEverest version it runs, so an update cannot check "
                "it is new enough to start from. Contact Percona support."
            )
        continue
    if parse(installed) is None:
        print("REFUSE %s %s is not a version this update can compare. Contact Percona support." % (label, installed))
        continue
    if not new or parse(new) is None:
        print("REFUSE This control plane's release does not say which %s it carries. Contact Percona support." % label)
        continue
    have.append("%s %s" % (label, installed))
    want.append("%s %s" % (label, new))
    floor = floors.get(key, "")
    floor_version = floor.replace(">=", "").strip()
    if floor_version and parse(floor_version) and compare(installed, floor_version) < 0:
        print(
            "REFUSE %s %s is older than the oldest version an update starts from (>= %s). "
            "Back up your databases and reinstall this environment instead." % (label, installed, floor_version)
        )
    if compare(installed, new) > 0:
        print(
            "REFUSE %s %s is newer than %s, the version in this control plane's release. "
            "An update never downgrades. Use a control plane that carries %s or newer."
            % (label, installed, new, installed)
        )
print("VERSIONS updating %s from %s to %s" % (name, ", ".join(have), ", ".join(want)))
PY
}

# update_unfinished lists what is still running in the database namespace: a
# database in any phase but Ready (Suspended and Failed included), and a Backup
# or Restore that has not finished. Each one is named, one line each.
update_unfinished() {
  local kind="$1" resource="$2" noun="$3" namespace="$4" out err="${tmp}/kubectl-read.err"
  if ! out="$(kubectl -n "$namespace" get "$resource" -o json 2>"$err")"; then
    echo "could not read the ${noun}s in ${namespace}: $(tail -n 1 "$err"). Check that kubectl can read them, then run this command again."
    return 0
  fi
  # A refusal that cannot be read must still be one: an answer that does not
  # parse reads as unfinished work, never as none.
  if ! printf '%s' "$out" | python3 -c '
import json, sys

kind, noun = sys.argv[1:3]
for item in json.load(sys.stdin).get("items") or []:
    name = (item.get("metadata") or {}).get("name") or "unnamed"
    status = item.get("status") or {}
    if kind == "instance":
        phase = status.get("phase") or ""
        if phase == "Ready":
            continue
        if phase == "Failed":
            print("database %s is Failed. Fix it or delete it, then run this command again." % name)
        elif phase == "Suspended":
            print("database %s is Suspended. Resume it, then run this command again." % name)
        else:
            print("database %s is %s, not Ready. Wait until it is Ready, then run this command again." % (name, phase or "not reporting a phase"))
    else:
        state = status.get("state") or ""
        if state in ("", "Pending", "Running", "Deleting"):
            print("%s %s has not finished (%s). Wait for it to finish, then run this command again." % (noun, name, state or "no state yet"))
' "$kind" "$noun" 2>/dev/null; then
    echo "could not read the ${noun}s in ${namespace}: kubectl did not answer with a list. Run this command again."
  fi
}

# update_find_release finds the release this Kubernetes cluster runs, whatever
# state an earlier update left it in, the name from before the rename included.
update_find_release() {
  local candidates=("$PLATFORM_RELEASE") candidate status
  local before_rename="percona-services" # allow-legacy-name
  if [[ "$PLATFORM_RELEASE_NAMED" != 1 ]]; then
    candidates+=("$before_rename")
  fi
  for candidate in "${candidates[@]}"; do
    status="$(helm status "$candidate" --namespace "$PLATFORM_NAMESPACE" -o json 2>/dev/null |
      python3 -c 'import json,sys; print(json.load(sys.stdin).get("info",{}).get("status") or "")' 2>/dev/null || true)"
    if [[ -n "$status" ]]; then
      PLATFORM_RELEASE="$candidate"
      UPDATE_RELEASE_STATUS="$status"
      return 0
    fi
  done
  update_refuse "there is no Percona Database Service release in ${PLATFORM_NAMESPACE} on this Kubernetes cluster, so there is nothing to update. Point kubectl at the Kubernetes cluster of ${NAME}, or run the join command to install it."
}

# update_read_manifest writes the manifest of what the release is running. A
# release left failed by an earlier update holds the manifest of the revision it
# tried, which may not be what is running, so the last revision that was in
# place is read instead and the failed one is kept beside it for comparison.
update_read_manifest() {
  local manifest="$1" good
  UPDATE_ATTEMPTED_MANIFEST=""
  if [[ "$UPDATE_RELEASE_STATUS" != failed ]]; then
    helm get manifest "$PLATFORM_RELEASE" --namespace "$PLATFORM_NAMESPACE" >"$manifest" 2>/dev/null
    return
  fi
  good="$(helm history "$PLATFORM_RELEASE" --namespace "$PLATFORM_NAMESPACE" -o json 2>/dev/null |
    python3 -c '
import json, sys
revisions = [r["revision"] for r in json.load(sys.stdin) if r.get("status") in ("deployed", "superseded")]
print(max(revisions) if revisions else "")' 2>/dev/null || true)"
  if [[ -z "$good" ]]; then
    update_refuse "the release ${PLATFORM_RELEASE} failed and has no earlier revision that was running, so what it runs cannot be read. Run the join command again to repair it."
  fi
  helm get manifest "$PLATFORM_RELEASE" --namespace "$PLATFORM_NAMESPACE" --revision "$good" >"$manifest" 2>/dev/null || return 1
  UPDATE_ATTEMPTED_MANIFEST="${manifest}.attempted"
  helm get manifest "$PLATFORM_RELEASE" --namespace "$PLATFORM_NAMESPACE" >"$UPDATE_ATTEMPTED_MANIFEST" 2>/dev/null
}

# update_preflight is the first phase of --update. It checks what the Kubernetes
# cluster runs against the chart this control plane carries, using the
# environment the preview named. The enrollment that spends the token comes
# after it returns.
update_preflight() {
  phase "checking prerequisites"
  local context
  context="$(kubectl config current-context 2>/dev/null || true)"
  if [[ -z "$context" ]]; then
    update_refuse "kubectl has no current context, so there is nothing to update. Point kubectl at the Kubernetes cluster of ${NAME}."
  fi
  echo "updating the Kubernetes cluster kubectl points at:"
  echo "  context: ${context}"
  echo "  name:    ${NAME}"
  if [[ -z "$IMAGE" ]]; then
    update_refuse "the control plane did not provide an agent image. Contact Percona support."
  fi
  update_find_release
  # A release that failed an earlier update can be upgraded again. One that is
  # still pending cannot, and Helm says so only after the token is spent.
  case "$UPDATE_RELEASE_STATUS" in
    deployed | failed) ;;
    *)
      update_refuse "the release ${PLATFORM_RELEASE} is ${UPDATE_RELEASE_STATUS}, which an update cannot start from. Look at it with helm status ${PLATFORM_RELEASE} --namespace ${PLATFORM_NAMESPACE} and put it back with helm rollback ${PLATFORM_RELEASE} --namespace ${PLATFORM_NAMESPACE}, then run this command again."
      ;;
  esac
  # The release names its environment and its database namespace. Neither is
  # guessed: a release that names no environment is not one this command is for.
  local values release_env release_ns release_hub
  values="$(helm get values "$PLATFORM_RELEASE" --namespace "$PLATFORM_NAMESPACE" -o json 2>/dev/null |
    python3 -c '
import json, sys
values = json.load(sys.stdin) or {}
hub = (values.get("adapter") or {}).get("hub") or {}
print(hub.get("environmentName") or hub.get("clusterName") or "")  # allow-legacy-name
print((values.get("stack") or {}).get("dbNamespace") or "")
print(hub.get("url") or "")' 2>/dev/null || true)"
  release_env="$(printf '%s\n' "$values" | sed -n 1p)"
  release_ns="$(printf '%s\n' "$values" | sed -n 2p)"
  release_hub="$(printf '%s\n' "$values" | sed -n 3p)"
  if [[ -z "$release_env" ]]; then
    update_refuse "the release ${PLATFORM_RELEASE} on this Kubernetes cluster does not name an environment, so it is not one this command can update. Point kubectl at the Kubernetes cluster of ${NAME}."
  fi
  if [[ "$release_env" != "$NAME" ]]; then
    update_refuse "the release on this Kubernetes cluster belongs to environment ${release_env}, and this command is for ${NAME}. Point kubectl at the Kubernetes cluster of ${NAME}."
  fi
  # Two control planes can each have an environment of this name. The release
  # remembers the one it reports to, and a token from another would repoint it.
  if [[ -z "$release_hub" ]]; then
    update_refuse "the release ${PLATFORM_RELEASE} on this Kubernetes cluster does not name a control plane, so it is not one this command can update. Point kubectl at the Kubernetes cluster of ${NAME}."
  fi
  if [[ "${release_hub%/}" != "${HUB_URL%/}" ]]; then
    update_refuse "the environment ${NAME} on this Kubernetes cluster reports to ${release_hub}, and this command is from ${HUB_URL}. Run the update from the control plane it reports to."
  fi
  # Two Teams on one control plane can each own an environment of this name, and
  # what tells them apart here is the Git repository the Kubernetes cluster
  # applies its databases from. The control plane names one per environment, and
  # a token from the other Team would repoint this one at it, with pruning on.
  local installed_git git_err="${tmp}/git-read.err"
  if ! installed_git="$(kubectl -n flux-system get gitrepository.source.toolkit.fluxcd.io hub \
    --ignore-not-found -o jsonpath='{.spec.url}' 2>"$git_err")"; then
    # Only a cluster that has no such resource type has nothing applied yet. Any
    # other failure leaves the identity unchecked, so it refuses.
    if grep -qE "doesn't have a resource type|no matches for kind" "$git_err"; then
      installed_git=""
    else
      update_refuse "could not read the Git repository this Kubernetes cluster applies its databases from: $(tail -n 1 "$git_err"). Check that kubectl can read it, then run this command again."
    fi
  fi
  if [[ -n "$installed_git" && "${installed_git%/}" != "${GIT_URL%/}" ]]; then
    update_refuse "this Kubernetes cluster applies its databases from ${installed_git}, but this control plane names ${GIT_URL} for ${NAME}. It may belong to another Team or Organization. Point kubectl at the Kubernetes cluster of ${NAME}."
  fi
  # Git reconciliation is applied in the namespace the control plane names,
  # while Helm keeps the one the release was installed with. If they differ the
  # databases' Kustomization moves with pruning on, so the update refuses.
  if [[ -n "$release_ns" && "$release_ns" != "$DB_NAMESPACE" ]]; then
    update_refuse "the release keeps its databases in the namespace ${release_ns}, but this control plane applies them in ${DB_NAMESPACE}, and an update would move them. Contact Percona support."
  fi
  # --reuse-values would keep the old chart's built-in defaults, and the new
  # chart refuses a pinned version that differs from them. The update needs
  # Helm to take the new defaults and keep only what the release was given.
  if ! helm upgrade --help 2>/dev/null | grep -q -- '--reset-then-reuse-values'; then
    update_refuse "this version of Helm cannot keep a release's own values while taking the new chart's defaults. Install Helm 3.14 or newer, then run this command again."
  fi
  local namespace="$DB_NAMESPACE"

  resolve_chart
  # A released chart is a reference, not files, and its pin has to be read.
  if [[ "$CHART_REF" == oci://* ]]; then
    local pulled="${tmp}/oci-chart" pull_args=(pull "$CHART_REF")
    if [[ -n "$CHART_VERSION" ]]; then
      pull_args+=(--version "$CHART_VERSION")
    fi
    if ! helm "${pull_args[@]}" --untar --untardir "$pulled" >/dev/null 2>&1; then
      update_refuse "could not pull the released chart ${CHART_REF}. Check PDS_CHART_OCI and PDS_CHART_VERSION, then run this command again."
    fi
    local unpacked
    for unpacked in "$pulled"/*/; do
      CHART_REF="${unpacked%/}"
      break
    done
    CHART_VERSION=""
  fi
  local manifest="${tmp}/release-manifest.yaml" check refusals=() line
  if ! update_read_manifest "$manifest"; then
    update_refuse "could not read the release ${PLATFORM_RELEASE} in ${PLATFORM_NAMESPACE}. Check that kubectl and helm can read it, then run this command again."
  fi
  if ! check="$(update_check "${CHART_REF}/compatibility.yaml" "$manifest" "$NAME")"; then
    update_refuse "could not read the versions this update would install from ${CHART_REF}. Contact Percona support."
  fi
  while IFS= read -r line; do
    case "$line" in
      REFUSE\ *) refusals+=("${line#REFUSE }") ;;
      VERSIONS\ *) UPDATE_VERSIONS_LINE="${line#VERSIONS }" ;;
    esac
  done <<<"$check"
  # An update that tried other versions than the ones running may have changed
  # some workloads and not others, so nothing here can say what is installed.
  if [[ -n "$UPDATE_ATTEMPTED_MANIFEST" ]]; then
    local attempted
    attempted="$(update_check "${CHART_REF}/compatibility.yaml" "$UPDATE_ATTEMPTED_MANIFEST" "$NAME" | grep '^VERSIONS ' || true)"
    if [[ "$attempted" != "VERSIONS ${UPDATE_VERSIONS_LINE}" ]]; then
      update_refuse "an earlier update of ${NAME} stopped part way, and it tried other versions than the ones running, so this Kubernetes cluster may hold some of each. Put the release back with helm rollback ${PLATFORM_RELEASE} --namespace ${PLATFORM_NAMESPACE}, then run this command again."
    fi
  fi
  while IFS= read -r line; do
    [[ -n "$line" ]] && refusals+=("$line")
  done < <(
    update_unfinished instance instances.core.openeverest.io database "$namespace"
    update_unfinished backup backups.backup.openeverest.io backup "$namespace"
    update_unfinished restore restores.backup.openeverest.io restore "$namespace"
  )
  if [[ "${#refusals[@]}" -gt 0 ]]; then
    update_refuse "${refusals[@]}"
  fi
  echo "${UPDATE_VERSIONS_LINE}"
}

# update_platform moves the whole platform in one Helm upgrade. It is the
# repair upgrade of a join without its values file: the release keeps every
# value it was given, the new chart supplies its own defaults, and only the
# agent image and the agent token are written over them.
update_platform() {
  local values="${tmp}/update-values.yaml"
  (
    umask 077
    cat >"$values" <<EOF
adapter:
  image:
    repository: ${IMAGE_REPOSITORY}
    tag: ${IMAGE_TAG}
  hub:
    agentToken: "${TOKEN}"
EOF
  )
  write_spoke_registry_pull_secret "$values"
  if need_registry_pull_secret; then
    ensure_registry_pull_secret "$PLATFORM_NAMESPACE"
  fi
  update_chart_dependencies "$CHART_REF"
  local helm_args force_flag
  helm_args=(upgrade "$PLATFORM_RELEASE" "$CHART_REF"
    --namespace "$PLATFORM_NAMESPACE"
    --timeout 15m "$(helm_wait_flag)"
    -f "$values" --reset-then-reuse-values
    --set openeverest.upgrade.preflightChecks=false)
  force_flag="$(helm_force_conflicts_flag)"
  if [[ -n "$force_flag" ]]; then
    helm_args+=("$force_flag")
  fi
  if [[ "$CHART_REF" == oci://* && -n "$CHART_VERSION" ]]; then
    helm_args+=(--version "$CHART_VERSION")
  fi
  helm "${helm_args[@]}"
}

# run_update is --update after the preview: the checks, then the enrollment
# that puts the new agent token and the Git credentials in this shell, then the
# three phases that change the environment.
run_update() {
  update_preflight
  enroll="$(load_enrollment "$TOKEN" "$HUB_URL" "$ENROLL_CACHE")" || exit 1
  read_enrollment
  UPDATE_ENROLLED=1
  resolve_image
  UPDATE_STARTED=1
  phase "updating Percona Database Service"
  update_platform
  install_support_access
  phase "updating Git reconciliation"
  install_flux
  phase "waiting for the agent"
  # The Deployment's name comes from the chart and any name override, so it is
  # found by the release and component labels rather than built from the release.
  kubectl -n "$PLATFORM_NAMESPACE" rollout status deploy \
    -l "app.kubernetes.io/instance=${PLATFORM_RELEASE},app.kubernetes.io/component=adapter" \
    --timeout="$(agent_rollout_timeout)"
  echo "environment ${NAME} is updated. Watch the console for Connected."
}

# update_failure_notice names the phase an update stopped in and the commands
# that look at the release and put it back. A refusal never gets here: it
# changed nothing, and offering a rollback after it would be wrong.
update_failure_notice() {
  [[ "${UPDATE_STARTED:-0}" == 1 ]] || return 0
  echo "" >&2
  if [[ "${INTERRUPTED:-0}" == 1 ]]; then
    echo "interrupted. Nothing was rolled back." >&2
  else
    echo "the update did not finish. Nothing was rolled back." >&2
  fi
  echo "  it stopped at: ${PHASE}" >&2
  echo "the last lines it printed:" >&2
  tail -n 20 "${PHASE_LOG:-/dev/null}" 2>/dev/null | scrub_tail | sed 's/^/  /' >&2 || true
  echo "To see where the release is:" >&2
  echo "  helm status ${PLATFORM_RELEASE} --namespace ${PLATFORM_NAMESPACE}" >&2
  echo "  helm history ${PLATFORM_RELEASE} --namespace ${PLATFORM_NAMESPACE}" >&2
  echo "  kubectl -n ${PLATFORM_NAMESPACE} get pods" >&2
  echo "To go back to the previous revision of the release:" >&2
  echo "  helm rollback ${PLATFORM_RELEASE} --namespace ${PLATFORM_NAMESPACE}" >&2
  echo "Or fix the cause and run a new command from the environment's Update page." >&2
}

if [[ "$UPDATE" == 1 ]]; then
  run_update
  exit 0
fi

# Ownership recorded by an earlier attempt, so a resource this machine created
# and could not finish is still known to be ours. The file only outlives a run
# that failed, because a successful join clears it.
load_state

if [[ "$STATUS_ONLY" == 1 ]]; then
  print_status
  exit 0
fi

if [[ "$DRY_RUN" == 1 ]]; then
  print_plan
  exit 0
fi

# Everything that can be checked without spending money is checked here. The
# chart and the agent image are the two things most likely to be missing on a
# first attempt, and discovering either after eksctl has built a control plane
# and a node group leaves the customer paying for a cluster this run cannot
# finish. resolve_image needs a cluster to fall back to scanning one, so only
# the hub-provided image counts at this point.
phase "checking prerequisites"
if [[ -z "$IMAGE" ]]; then
  echo "the control plane did not provide an agent image." >&2
  echo "Contact Percona support." >&2
  exit 1
fi
resolve_chart

if [[ "$LOCAL" == 1 ]]; then
  phase "joining the Kubernetes cluster kubectl points at"
  context="$(kubectl config current-context 2>/dev/null || true)"
  server="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null || true)"
  if [[ -z "$context" ]]; then
    echo "kubectl has no current context, so there is nothing to join." >&2
    exit 1
  fi
  echo "creating no AWS resources:"
  echo "  context: ${context}"
  echo "  server:  ${server}"
  echo "  name:    ${NAME}"
else
  phase "creating or attaching the EKS cluster"
  ensure_cluster
  retry 3 "writing the kubeconfig for ${NAME}" \
    aws eks update-kubeconfig --name "$NAME" --region "$REGION" >/dev/null
fi

phase "waiting for a Ready worker node"
wait_for_ready_nodes

resolve_image
resolve_platform_release
if [[ "$LOCAL" != 1 ]]; then
  phase "preparing backup storage"
  # Resolved in this shell, not in a command substitution, so the cleanup path
  # reads the same identity the run actually used.
  resolve_spoke_iam_user
  ensure_spoke_bucket "$BUCKET"
  ensure_spoke_identity "$BUCKET"
fi
phase "installing Percona Database Service"
install_platform
install_support_access

# The BackupStorage in the database namespace resolves its credential there,
# and the credential was written to the Helm namespace, so it has to reach both.
if [[ "$LOCAL" != 1 ]]; then
  mirror_s3_secret
fi

# install_platform already reconciles adapter.role=agent. A sidecar Deployment
# would heartbeat on the same token and run delivered work twice.

phase "installing Git reconciliation"
install_flux

phase "finishing the join"
if [[ "$LOCAL" != 1 ]]; then
  retire_previous_backup_key
fi

echo "environment ${NAME} is joining. Watch the console for Connected."
