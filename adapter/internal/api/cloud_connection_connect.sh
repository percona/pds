#!/usr/bin/env bash
#
# Connects an AWS account to Percona Database Service. Run it in AWS CloudShell,
# or in any shell where `aws` is signed in as someone who can create IAM roles:
#
#   curl -fsSL <control plane>/api/internal/cloud-connection/connect.sh | bash -s -- --token ccn_...
#
# It reads the account, asks the control plane for this connection's id and
# secret, creates or updates one CloudFormation stack, and asks the control
# plane to check the result. Rerunning it is always safe: the same command
# creates, finishes, repairs or updates the stack.
set -euo pipefail

HUB_URL="__HUB_URL__"

TOKEN=""
REGIONS=""
STACK_REGION=""
HEARTBEAT_PID=""
WORK_DIR=""

usage() {
  cat <<'USAGE'
Usage: connect.sh --token <token> [--regions <list>] [--region <region>]

  --token    The token the console printed in the connect command.
  --regions  Comma separated regions environments may be built in. Narrows the
             regions chosen in the console, and cannot add to them.
  --region   The region to create the stack in. Defaults to the region aws uses.
USAGE
}

say() { printf '%s\n' "$*"; }
fail() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  stop_heartbeat
  if [ -n "${WORK_DIR}" ]; then
    rm -rf "${WORK_DIR}"
  fi
}
trap cleanup EXIT

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --token)
        [ "$#" -ge 2 ] || fail "--token needs a value."
        TOKEN="$2"
        shift 2
        ;;
      --regions)
        [ "$#" -ge 2 ] || fail "--regions needs a value."
        REGIONS="$2"
        shift 2
        ;;
      --region)
        [ "$#" -ge 2 ] || fail "--region needs a value."
        STACK_REGION="$2"
        shift 2
        ;;
      -h | --help)
        usage
        exit 0
        ;;
      *)
        usage >&2
        fail "Unknown option $1."
        ;;
    esac
  done
  [ -n "${TOKEN}" ] || {
    usage >&2
    fail "A token is required. Create one in the console."
  }
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required and was not found."
}

# hub_call sends one JSON request to the control plane with the token and
# prints the response body. A refusal prints the control plane's sentence.
hub_call() {
  local method="$1" path="$2" body="${3:-}" out status
  out="${WORK_DIR}/response.json"
  local args=(-sS -o "${out}" -w '%{http_code}' -X "${method}" -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json')
  if [ -n "${body}" ]; then
    args+=(-d "${body}")
  fi
  status="$(curl "${args[@]}" "${HUB_URL}/api/internal/cloud-connection/${path}")" || fail "Could not reach the control plane at ${HUB_URL}."
  case "${status}" in
    2*) cat "${out}" ;;
    401) fail "The token is not valid. Create a new one in the console." ;;
    *) fail "$(jq -r '.message // "The control plane refused the request."' "${out}" 2>/dev/null || echo "The control plane refused the request.")" ;;
  esac
}

# report tells the control plane which step is running. It is a hint for the
# progress view: a failed report never stops the connection.
report() {
  local step="$1" status="$2" detail="${3:-}" body
  body="$(jq -nc --arg step "${step}" --arg status "${status}" --arg detail "${detail}" '{step: $step, status: $status, detail: $detail}')"
  curl -sS -o /dev/null -X POST -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
    -d "${body}" "${HUB_URL}/api/internal/cloud-connection/events" >/dev/null 2>&1 || true
}

start_heartbeat() {
  local step="$1"
  (
    while sleep 30; do
      report "${step}" heartbeat
    done
  ) &
  HEARTBEAT_PID=$!
}

stop_heartbeat() {
  if [ -n "${HEARTBEAT_PID}" ]; then
    kill "${HEARTBEAT_PID}" 2>/dev/null || true
    wait "${HEARTBEAT_PID}" 2>/dev/null || true
    HEARTBEAT_PID=""
  fi
}

# run_step runs a command as one reported step.
run_step() {
  local step="$1" label="$2"
  shift 2
  say "${label}"
  report "${step}" started
  start_heartbeat "${step}"
  if "$@"; then
    stop_heartbeat
    report "${step}" succeeded
  else
    stop_heartbeat
    report "${step}" failed "${label} did not finish."
    fail "${label} did not finish."
  fi
}

detect_region() {
  if [ -n "${STACK_REGION}" ]; then
    return
  fi
  STACK_REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
  if [ -z "${STACK_REGION}" ]; then
    STACK_REGION="$(aws configure get region 2>/dev/null || true)"
  fi
  STACK_REGION="${STACK_REGION:-us-east-1}"
}

enroll_body() {
  local alias_name="$1" account="$2"
  jq -nc --arg account "${account}" --arg alias "${alias_name}" --arg region "${STACK_REGION}" --arg regions "${REGIONS}" '
    {accountId: $account, alias: $alias, region: $region}
    + (if $regions == "" then {} else {allowedRegions: ($regions | split(","))} end)'
}

create_stack() {
  aws cloudformation deploy \
    --region "${STACK_REGION}" \
    --stack-name "${STACK_NAME}" \
    --template-file "${WORK_DIR}/template.yaml" \
    --capabilities CAPABILITY_NAMED_IAM \
    --no-fail-on-empty-changeset \
    --tags "percona.com/pds-connection=${CONNECTION_ID}" \
    --parameter-overrides \
    "ConnectionId=${CONNECTION_ID}" \
    "ExternalId=${EXTERNAL_ID}" \
    "ControlPlanePrincipalArn=${PRINCIPAL_ARN}" \
    "AllowedRegions=${ALLOWED_REGIONS}" >/dev/null
}

protect_stack() {
  aws cloudformation update-termination-protection \
    --region "${STACK_REGION}" \
    --stack-name "${STACK_NAME}" \
    --enable-termination-protection >/dev/null
}

download_template() {
  curl -fsS -o "${WORK_DIR}/template.yaml" "${TEMPLATE_URL}"
}

# verify asks the control plane to check the account. A new role can take a
# few seconds to be assumable, so a pending answer is asked again.
verify() {
  local attempt state reason answer
  for attempt in 1 2 3 4 5 6; do
    answer="$(hub_call POST verify)" || return 1
    state="$(jq -r '.state' <<<"${answer}")"
    reason="$(jq -r '.reason // ""' <<<"${answer}")"
    if [ "${state}" = "ready" ]; then
      return 0
    fi
    if [ "${attempt}" -lt 6 ] && [ "${state}" = "pending" ]; then
      sleep 10
    fi
  done
  say "${reason}"

  return 1
}

main() {
  parse_args "$@"
  need aws
  need curl
  need jq
  WORK_DIR="$(mktemp -d)"
  detect_region

  say "Reading your AWS account"
  local account alias_name
  account="$(aws sts get-caller-identity --query Account --output text)" || fail "aws is not signed in. Sign in, then run this command again."
  alias_name="$(aws iam list-account-aliases --query 'AccountAliases[0]' --output text 2>/dev/null || true)"
  if [ "${alias_name}" = "None" ]; then
    alias_name=""
  fi

  local enrolled
  enrolled="$(hub_call POST enroll "$(enroll_body "${alias_name}" "${account}")")"
  CONNECTION_ID="$(jq -r '.connectionId' <<<"${enrolled}")"
  EXTERNAL_ID="$(jq -r '.externalId' <<<"${enrolled}")"
  STACK_NAME="$(jq -r '.stackName' <<<"${enrolled}")"
  PRINCIPAL_ARN="$(jq -r '.principalArn' <<<"${enrolled}")"
  TEMPLATE_URL="$(jq -r '.templateUrl' <<<"${enrolled}")"
  ALLOWED_REGIONS="$(jq -r '.allowedRegions | join(",")' <<<"${enrolled}")"
  say "Connecting AWS account ${account} as ${CONNECTION_ID}"

  run_step template "Downloading the template" download_template
  run_step stack "Creating the stack ${STACK_NAME} in ${STACK_REGION}" create_stack
  run_step protect "Turning on termination protection" protect_stack
  run_step verify "Checking the connection" verify

  say "AWS account ${account} is connected."
}

main "$@"
