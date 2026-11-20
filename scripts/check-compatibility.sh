#!/usr/bin/env bash
#
# Check Chart.yaml, values.yaml, and docs against the compatibility pin.
#
#   scripts/check-compatibility.sh
#   scripts/check-compatibility.sh --root DIR
#
# The pin is charts/pds/compatibility.yaml. A mismatch prints
# both versions and names the pin. Mixed component versions are not supported.
set -euo pipefail

ROOT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --root)
      ROOT="${2:-}"
      shift 2
      ;;
    -h | --help)
      echo "usage: $(basename "$0") [--root DIR]"
      exit 0
      ;;
    *)
      echo "usage: $(basename "$0") [--root DIR]" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${ROOT}" ]]; then
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi

command -v ruby >/dev/null || {
  echo "ruby is required to read the compatibility pin" >&2
  exit 1
}

export COMPAT_ROOT="${ROOT}"
ruby - "${ROOT}" <<'RUBY'
require "yaml"

root = ARGV.fetch(0)
pin_rel = "charts/pds/compatibility.yaml"
chart_rel = "charts/pds/Chart.yaml"
values_rel = "charts/pds/values.yaml"
docs_rel = "docs/compatibility.md"
types_rel = "adapter/internal/client/everest/types.go"
bootstrap_rel = "adapter/internal/api/connect_bootstrap.sh"

def fail(msg)
  warn msg
  exit 1
end

def read_yaml(path, rel)
  YAML.safe_load(File.read(path), permitted_classes: [], aliases: false, filename: rel)
rescue Errno::ENOENT
  fail "#{rel} is missing. The compatibility pin lives at charts/pds/compatibility.yaml."
rescue Psych::Exception, ArgumentError => e
  fail "#{rel} did not parse: #{e.message}. The compatibility pin lives at charts/pds/compatibility.yaml."
end

def walk(obj, *keys)
  keys.reduce(obj) do |cur, key|
    return nil unless cur.is_a?(Hash)

    cur[key.to_s]
  end
end

def must_eq(got, want, where)
  if want.nil?
    fail "the compatibility pin is missing a required value for #{where}. Use the versions in charts/pds/compatibility.yaml."
  end
  if got.nil?
    fail "#{where} is missing. The compatibility pin requires #{want}. Use the versions in charts/pds/compatibility.yaml."
  end
  got_s = got.to_s
  want_s = want.to_s
  return if got_s == want_s

  fail "#{where} is #{got_s}. The compatibility pin requires #{want_s}. Use the versions in charts/pds/compatibility.yaml."
end

def must_include(text, needle, where)
  if needle.nil?
    fail "the compatibility pin is missing a required value for #{where}. Use the versions in charts/pds/compatibility.yaml."
  end
  return if text.include?(needle)

  fail "#{where} does not mention #{needle}. The compatibility pin requires that string."
end

pin_path = File.join(root, pin_rel)
fail "#{pin_rel} is missing. It is the supported version pin." unless File.file?(pin_path)

pin = read_yaml(pin_path, pin_rel)
fail "#{pin_rel} is empty. It is the supported version pin." if pin.nil? || pin == false
fail "#{pin_rel} is missing openeverest. It is the supported version pin." if walk(pin, "openeverest").nil?

flux_version = walk(pin, "flux", "version")
fail "flux.version in #{pin_rel} is missing. The pin names the Flux version the join script installs." if flux_version.nil?

# A managed environment's stack is stamped with the template revision. A chart
# or Flux version change must not slip past it: either the revision is bumped
# or cutFor is moved to the new versions with the revision left on purpose.
revision = walk(pin, "managedTemplate", "revision")
unless revision.is_a?(Integer) && revision.positive?
  fail "managedTemplate.revision in #{pin_rel} is #{revision.inspect}. It is a whole number from 1 that counts the managed environment template revisions."
end
{
  "chart" => walk(pin, "pds", "chart"),
  "flux" => flux_version
}.each do |component, current|
  # A missing chart version is reported below with the other required values.
  next if current.nil?

  cut_for = walk(pin, "managedTemplate", "cutFor", component)
  next if cut_for.to_s == current.to_s

  fail "#{pin_rel} has #{component} #{current}, but managedTemplate.cutFor.#{component} is #{cut_for.inspect}. " \
       "Bump managedTemplate.revision, or set managedTemplate.cutFor.#{component} to #{current} to leave the revision as it is."
end

chart = read_yaml(File.join(root, chart_rel), chart_rel)
values = read_yaml(File.join(root, values_rel), values_rel)

must_eq(chart["version"], walk(pin, "pds", "chart"), "#{chart_rel} version")
must_eq(chart["appVersion"], walk(pin, "pds", "app"), "#{chart_rel} appVersion")
must_eq(chart["kubeVersion"], walk(pin, "kubernetes", "min"), "#{chart_rel} kubeVersion")

deps = Array(chart["dependencies"])
{
  "openeverest" => ["openeverest", "version"],
  "provider-percona-xtradb-cluster" => ["providers", "pxc", "version"],
  "provider-percona-postgresql" => ["providers", "postgresql", "version"],
  "pmm" => ["pmm", "version"]
}.each do |name, keys|
  dep = deps.find { |d| d.is_a?(Hash) && d["name"] == name }
  fail "#{chart_rel} is missing dependency #{name}. The compatibility pin requires it." if dep.nil?

  must_eq(dep["version"], walk(pin, *keys), "#{chart_rel} dependency #{name} version")
  repo_keys =
    case name
    when "openeverest" then ["openeverest", "repository"]
    when "provider-percona-xtradb-cluster" then ["providers", "pxc", "repository"]
    when "provider-percona-postgresql" then ["providers", "postgresql", "repository"]
    when "pmm" then ["pmm", "repository"]
    end
  must_eq(dep["repository"], walk(pin, *repo_keys), "#{chart_rel} dependency #{name} repository")
end

ann = ((chart["annotations"] || {})["pds.percona.com/platform-charts"] || "").to_s
[
  walk(pin, "openeverest", "version"),
  walk(pin, "providers", "pxc", "version"),
  walk(pin, "providers", "postgresql", "version"),
  walk(pin, "pmm", "version"),
  walk(pin, "pmm", "app")
].each do |ver|
  must_include(ann, ver, "#{chart_rel} platform-charts annotation")
end

must_eq(
  walk(values, "platform", "openeverest", "version"),
  walk(pin, "openeverest", "version"),
  "#{values_rel} platform.openeverest.version"
)
must_eq(
  walk(values, "platform", "openeverest", "repository"),
  walk(pin, "openeverest", "repository"),
  "#{values_rel} platform.openeverest.repository"
)
must_eq(
  walk(values, "platform", "pxcOperator", "version"),
  walk(pin, "providers", "pxc", "operator"),
  "#{values_rel} platform.pxcOperator.version"
)
must_eq(
  walk(values, "platform", "postgresqlOperator", "version"),
  walk(pin, "providers", "postgresql", "operator"),
  "#{values_rel} platform.postgresqlOperator.version"
)
must_eq(
  walk(values, "platform", "pmm", "version"),
  walk(pin, "pmm", "version"),
  "#{values_rel} platform.pmm.version"
)
must_eq(
  walk(values, "platform", "pmm", "appVersion"),
  walk(pin, "pmm", "app"),
  "#{values_rel} platform.pmm.appVersion"
)
must_eq(
  walk(values, "platform", "pmm", "repository"),
  walk(pin, "pmm", "repository"),
  "#{values_rel} platform.pmm.repository"
)

# support.update.from holds the oldest installed version an in-place update
# starts from, per component. The join script reads it from the chart it
# downloads, so a floor it cannot compare must fail here, not on a customer's
# machine.
update_from = walk(pin, "support", "update", "from")
unless update_from.nil?
  fail "support.update.from in #{pin_rel} must map a component to a minimum version." unless update_from.is_a?(Hash)

  checked = ["openeverest"] + (walk(pin, "providers") || {}).keys
  update_from.each do |component, floor|
    where = "support.update.from.#{component}"
    unless checked.include?(component.to_s)
      fail "#{where} in #{pin_rel} is not a component an update checks. Use #{checked.join(', ')}."
    end
    unless floor.to_s.match?(/\A>=\s*\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?\z/)
      fail "#{where} in #{pin_rel} is #{floor.inspect}. A floor is a >= constraint on one version, such as \">= 2.0.0-dev.4\"."
    end
  end
end

# The Flux version is what the join script installs and what a managed
# environment bootstraps, so it is pinned once, here.
bootstrap_path = File.join(root, bootstrap_rel)
fail "#{bootstrap_rel} is missing. It installs the pinned Flux version." unless File.file?(bootstrap_path)
bootstrap = File.read(bootstrap_path)
{
  "FLUX_VERSION" => /^FLUX_VERSION="\$\{FLUX_VERSION:-([^}"]*)\}"$/,
  "FLUX_PINNED_VERSION" => /^FLUX_PINNED_VERSION="([^"]*)"$/
}.each do |name, pattern|
  found = bootstrap[pattern, 1]
  fail "#{bootstrap_rel} does not set #{name}. The compatibility pin requires Flux #{flux_version}." if found.nil?

  must_eq(found, flux_version, "#{bootstrap_rel} #{name}")
end

# support.managedUpgrade.from maps a version an environment runs to the
# versions the control plane may move it to. A version missing here has no
# managed upgrade.
managed_upgrade = walk(pin, "support", "managedUpgrade", "from")
unless managed_upgrade.is_a?(Hash)
  fail "support.managedUpgrade.from in #{pin_rel} is missing. It maps a version an environment runs to the versions it may move to."
end
semver = /\A\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?\z/
managed_upgrade.each do |from, targets|
  where = "support.managedUpgrade.from.#{from}"
  unless from.to_s.match?(semver)
    fail "#{where} in #{pin_rel} is not a version. Use a version such as \"0.1.0\"."
  end
  unless targets.is_a?(Array) && !targets.empty?
    fail "#{where} in #{pin_rel} must list the versions to move to."
  end
  targets.each do |target|
    next if target.to_s.match?(semver)

    fail "#{where} in #{pin_rel} lists #{target.inspect}, which is not a version. Use a version such as \"0.2.0\"."
  end
end

docs_path = File.join(root, docs_rel)
fail "#{docs_rel} is missing. It is the published compatibility matrix." unless File.file?(docs_path)
docs = File.read(docs_path)

[
  walk(pin, "pds", "chart"),
  walk(pin, "openeverest", "version"),
  walk(pin, "providers", "pxc", "version"),
  walk(pin, "providers", "pxc", "operator"),
  walk(pin, "providers", "postgresql", "version"),
  walk(pin, "providers", "postgresql", "operator"),
  walk(pin, "pmm", "version"),
  walk(pin, "pmm", "app"),
  walk(pin, "crds", "instance"),
  walk(pin, "crds", "backup"),
  walk(pin, "support", "upgrade"),
  walk(pin, "flux", "version")
].each do |needle|
  must_include(docs, needle, docs_rel)
end

# "supported" is a substring of "unsupported", so require the fresh-install
# phrase instead of the pin's freshInstall token.
must_include(docs.downcase, "fresh install", docs_rel)

n_to_n1 = "control-plane, provider, operator, CRD, and database"
must_include(docs, n_to_n1, docs_rel)
must_include(docs, "hub-smoke", docs_rel)
must_include(docs, "smoke.yml", docs_rel)

types_path = File.join(root, types_rel)
fail "#{types_rel} is missing. The compatibility pin records the Instance and Backup CRDs there." unless File.file?(types_path)
types = File.read(types_path)
must_include(types, walk(pin, "crds", "instance"), types_rel)
must_include(types, walk(pin, "crds", "backup"), types_rel)

%w[README.md docs/deployment.md docs/architecture.md].each do |rel|
  path = File.join(root, rel)
  next unless File.file?(path)

  text = File.read(path)
  next unless text.include?("OpenEverest")

  must_include(text, walk(pin, "openeverest", "version"), rel)
end

puts "compatibility pin matches Chart.yaml, values.yaml, and docs"
RUBY
