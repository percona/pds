#!/usr/bin/env bash
#
# Tests for adapter/internal/api/cloud_connection_connect.sh. The script runs
# whole, in a subprocess, with fake aws, curl and sleep on PATH, so what is
# checked is what a customer's shell would do: the calls it makes, the order,
# and the words it prints.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${REPO_ROOT}/adapter/internal/api/cloud_connection_connect.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}
pass() { echo "ok $*"; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# Customer copy: what the script says to a person. The usage text and every
# say and fail argument may not name the platform's internals or call a
# Kubernetes cluster a bare cluster.
python3 - "${SCRIPT}" <<'PY' || fail "the script's words break the customer copy rules"
import re
import sys

text = open(sys.argv[1]).read()
banned = re.compile(r"\bPMM\b|\bFlux\b|\b[Tt]he (?:hub|adapter)\b|\b[Tt]his (?:hub|adapter)\b|\b[Ss]pokes?\b|\bclusters?\b", re.I)
spoken = []
for line in text.splitlines():
    stripped = line.strip()
    if stripped.startswith("#"):
        continue
    if re.match(r"(say|fail|usage)\b|.*\b(say|fail)\s+\"", stripped):
        spoken.append(stripped)
usage = re.search(r"<<'USAGE'\n(.*?)\nUSAGE", text, re.S).group(1)
spoken.append(usage)
bad = [line for line in spoken if banned.search(line) or "—" in line or "–" in line]
if bad:
    print("\n".join(bad), file=sys.stderr)
    sys.exit(1)
PY
pass "customer copy"

# make_env builds a PATH with the fakes and a script copy pointing at a hub.
# FAKE_STACK_SECONDS stretches the stack step so a heartbeat can land.
make_env() {
  local dir="$1"
  mkdir -p "${dir}/bin"
  sed 's#__HUB_URL__#https://hub.example#' "${SCRIPT}" >"${dir}/connect.sh"

  cat >"${dir}/bin/aws" <<'AWS'
#!/usr/bin/env bash
echo "aws $*" >>"${FAKE_DIR}/calls.log"
case "$1 $2" in
  "sts get-caller-identity") echo "123456789012" ;;
  "iam list-account-aliases") echo "${FAKE_ALIAS:-acme}" ;;
  "configure get") echo "eu-west-1" ;;
  "cloudformation deploy")
    command sleep "${FAKE_STACK_SECONDS:-0}"
    [ "${FAKE_STACK_FAILS:-}" = "1" ] && exit 255
    exit 0
    ;;
  "cloudformation update-termination-protection") exit 0 ;;
  *) echo "unexpected aws $*" >&2; exit 2 ;;
esac
AWS

  cat >"${dir}/bin/sleep" <<'SLEEP'
#!/usr/bin/env bash
# 30 seconds between heartbeats becomes a fraction of one, and the retry
# waits vanish.
case "$1" in
  30) command sleep 0.3 ;;
  *) : ;;
esac
SLEEP

  cat >"${dir}/bin/curl" <<'CURL'
#!/usr/bin/env bash
every_arg=" $* "
out=""
url=""
data=""
auth=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -d) data="$2"; shift 2 ;;
    -H) case "$2" in Authorization:*) auth="$2" ;; esac; shift 2 ;;
    -w|-X) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
route="${url##*/}"
echo "curl ${route} ${auth} ${data}" >>"${FAKE_DIR}/calls.log"
body='{}'
status=200
case "${route}" in
  enroll)
    body='{"connectionId":"abc123abc123","externalId":"extsecretextsecretextsecretextsecret","stackName":"pds-abc123abc123","stackRegion":"eu-west-1","allowedRegions":["eu-west-1","us-east-1"],"principalArn":"arn:aws:iam::111122223333:role/pds-control-plane","templateUrl":"https://hub.example/api/internal/cloud-connection/template.yaml","templateVersion":1}'
    [ -n "${FAKE_ENROLL_STATUS:-}" ] && status="${FAKE_ENROLL_STATUS}" && body='{"code":"validation","message":"The account is not allowed."}'
    ;;
  template.yaml) body='AWSTemplateFormatVersion: "2010-09-09"' ;;
  verify)
    ready='{"state":"ready"}'
    body="${FAKE_VERIFY_BODY:-${ready}}"
    ;;
  events) status=204; body='' ;;
esac
if [ -n "${out}" ]; then
  printf '%s' "${body}" >"${out}"
fi
if [[ "${every_arg}" == *" -w "* ]]; then
  printf '%s' "${status}"
fi
exit 0
CURL
  chmod +x "${dir}/bin/aws" "${dir}/bin/sleep" "${dir}/bin/curl"
}

# run_script runs the script and leaves its output in OUT and status in CODE.
run_script() {
  local dir="$1"
  shift
  : >"${dir}/calls.log"
  set +e
  OUT="$(FAKE_DIR="${dir}" PATH="${dir}/bin:${PATH}" bash "${dir}/connect.sh" "$@" 2>&1)"
  CODE=$?
  set -e
}

assert_contains() {
  case "$1" in *"$2"*) ;; *) fail "expected to find '$2' in: $1" ;; esac
}
assert_not_contains() {
  case "$1" in *"$2"*) fail "did not expect '$2' in: $1" ;; esac
}

# --- connecting an account ---------------------------------------------------
D="${WORK}/happy"
make_env "${D}"
run_script "${D}" --token ccn_secret --regions eu-west-1
[ "${CODE}" -eq 0 ] || fail "a healthy account connects, got ${CODE}: ${OUT}"
assert_contains "${OUT}" "AWS account 123456789012 is connected."
CALLS="$(cat "${D}/calls.log")"
assert_contains "${CALLS}" "curl enroll Authorization: Bearer ccn_secret"
assert_contains "${CALLS}" '"accountId":"123456789012"'
assert_contains "${CALLS}" '"alias":"acme"'
assert_contains "${CALLS}" '"allowedRegions":["eu-west-1"]'
assert_contains "${CALLS}" "--stack-name pds-abc123abc123"
assert_contains "${CALLS}" "--region eu-west-1"
assert_contains "${CALLS}" "--capabilities CAPABILITY_NAMED_IAM"
assert_contains "${CALLS}" "ConnectionId=abc123abc123"
assert_contains "${CALLS}" "ExternalId=extsecretextsecretextsecretextsecret"
assert_contains "${CALLS}" "ControlPlanePrincipalArn=arn:aws:iam::111122223333:role/pds-control-plane"
assert_contains "${CALLS}" "AllowedRegions=eu-west-1,us-east-1"
assert_contains "${CALLS}" "update-termination-protection --region eu-west-1 --stack-name pds-abc123abc123 --enable-termination-protection"
pass "a healthy account connects"

ORDER="$(grep -E 'aws cloudformation|curl (enroll|template.yaml|verify)' "${D}/calls.log" | awk '{print $1" "$2" "$3}' | tr '\n' '|')"
assert_contains "${ORDER}" "curl enroll"
case "${ORDER}" in
  *"enroll"*"template.yaml"*"cloudformation deploy"*"update-termination-protection"*"verify"*) ;;
  *) fail "steps ran out of order: ${ORDER}" ;;
esac
pass "steps run in order"

EVENTS="$(grep -E '^curl events' "${D}/calls.log" | grep -v heartbeat | sed -E 's/.*"step":"([a-z]+)","status":"([a-z]+)".*/\1:\2/' | tr '\n' ' ')"
assert_contains "${EVENTS}" "template:started template:succeeded stack:started stack:succeeded protect:started protect:succeeded verify:started verify:succeeded"
pass "every step reports its start and its end"

# --- a stack that cannot be created -------------------------------------------
D="${WORK}/stackfail"
make_env "${D}"
FAKE_STACK_FAILS=1 run_script "${D}" --token ccn_secret
[ "${CODE}" -ne 0 ] || fail "a failed stack must fail the command"
assert_contains "${OUT}" "Creating the stack pds-abc123abc123 in eu-west-1 did not finish."
assert_contains "$(cat "${D}/calls.log")" '"step":"stack","status":"failed"'
assert_not_contains "$(cat "${D}/calls.log")" "update-termination-protection"
pass "a failed stack is reported and stops the run"

# --- a long step keeps saying it is alive -------------------------------------
D="${WORK}/heartbeat"
make_env "${D}"
FAKE_STACK_SECONDS=1.2 run_script "${D}" --token ccn_secret
[ "${CODE}" -eq 0 ] || fail "a slow stack still connects: ${OUT}"
BEATS="$(grep -c '"status":"heartbeat"' "${D}/calls.log" || true)"
[ "${BEATS}" -ge 1 ] || fail "a running step must send heartbeats"
pass "a running step sends heartbeats"

# --- the control plane's answers ----------------------------------------------
D="${WORK}/pending"
make_env "${D}"
FAKE_VERIFY_BODY='{"state":"pending","reason":"The stack in your AWS account is still being created."}' run_script "${D}" --token ccn_secret
[ "${CODE}" -ne 0 ] || fail "a connection that is not Ready must fail the command"
assert_contains "${OUT}" "The stack in your AWS account is still being created."
[ "$(grep -c '^curl verify' "${D}/calls.log")" -eq 6 ] || fail "a pending answer is asked again, six times in all"
pass "a pending connection is asked again and its reason is shown"

D="${WORK}/refused"
make_env "${D}"
FAKE_ENROLL_STATUS=400 run_script "${D}" --token ccn_secret
[ "${CODE}" -ne 0 ] || fail "a refused enrollment must fail the command"
assert_contains "${OUT}" "The account is not allowed."
assert_not_contains "$(cat "${D}/calls.log")" "cloudformation deploy"
pass "a refused enrollment shows the control plane's sentence and creates nothing"

# --- arguments ----------------------------------------------------------------
D="${WORK}/notoken"
make_env "${D}"
run_script "${D}"
[ "${CODE}" -ne 0 ] || fail "a missing token must fail"
assert_contains "${OUT}" "A token is required."
[ ! -s "${D}/calls.log" ] || fail "nothing is called without a token"
pass "a token is required"

run_script "${D}" --token ccn_secret --nonsense
[ "${CODE}" -ne 0 ] || fail "an unknown option must fail"
assert_contains "${OUT}" "Unknown option --nonsense."
pass "an unknown option is refused"

echo "all cloud connection script tests passed"
