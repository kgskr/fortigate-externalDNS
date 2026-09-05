#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "yaml"

ROOT = File.expand_path("..", __dir__)
CHART_CRDS = File.join(ROOT, "charts/fortigate-external-dns/crds/fortigate-external-dns.yaml")
RAW_CRDS = File.join(ROOT, "manifests/crds/fortigate-external-dns.yaml")
TYPES = File.join(ROOT, "internal/apis/v1alpha1/types.go")
VALUES = File.join(ROOT, "charts/fortigate-external-dns/values.yaml")
VALUES_SCHEMA = File.join(ROOT, "charts/fortigate-external-dns/values.schema.json")
CHART = File.join(ROOT, "charts/fortigate-external-dns/Chart.yaml")
RAW_DEPLOYMENT = File.join(ROOT, "manifests/deployment.yaml")

def fail_check(message)
  warn "platform artifact validation failed: #{message}"
  exit 1
end

def yaml_documents(path)
  YAML.load_stream(File.read(path)).compact
rescue StandardError => e
  fail_check("#{path} is not valid YAML: #{e.message}")
end

fail_check("raw and chart CRDs differ") unless File.binread(CHART_CRDS) == File.binread(RAW_CRDS)

crds = yaml_documents(CHART_CRDS)
expected = {
  "FortiGateDNSTarget" => ["FortiGateDNSTargetSpec", true],
  "FortiGateDNSRecordOwnership" => ["FortiGateDNSRecordOwnershipSpec", true],
  "FortiGateDNSPolicy" => ["FortiGateDNSPolicySpec", false],
  "FortiGateDNSChangePlan" => ["FortiGateDNSChangePlanSpec", true],
  "FortiGateDNSStatus" => ["FortiGateDNSStatusSpec", true]
}
fail_check("expected 5 CRDs, got #{crds.length}") unless crds.length == expected.length

go_types = File.read(TYPES)

struct_fields = lambda do |struct_name|
  match = go_types.match(/^type #{Regexp.escape(struct_name)} struct \{\n(.*?)^\}/m)
  fail_check("Go struct #{struct_name} is missing") unless match
  match[1].scan(/`json:"([^",]+)(?:,[^"]*)?"`/).flatten.sort
end

walk_schema = lambda do |node, path|
  return unless node.is_a?(Hash)
  fail_check("#{path} uses preserve-unknown fields") if node["x-kubernetes-preserve-unknown-fields"]
  if node.key?("properties") && node["type"] != "object"
    fail_check("#{path} has properties without type: object")
  end
  if node["type"] == "array" && !node["items"].is_a?(Hash)
    fail_check("#{path} array has no structural items schema")
  end
  node.each do |key, value|
    if value.is_a?(Hash)
      walk_schema.call(value, "#{path}.#{key}")
    elsif value.is_a?(Array)
      value.each_with_index { |item, index| walk_schema.call(item, "#{path}.#{key}[#{index}]") }
    end
  end
end

seen = []
crds.each do |crd|
  fail_check("non-CRD document in CRD bundle") unless crd["apiVersion"] == "apiextensions.k8s.io/v1" && crd["kind"] == "CustomResourceDefinition"
  spec = crd.fetch("spec")
  fail_check("CRD group drift") unless spec["group"] == "fortigate-external-dns.kgskr.io"
  fail_check("CRD must be namespaced") unless spec["scope"] == "Namespaced"
  names = spec.fetch("names")
  kind = names.fetch("kind")
  seen << kind
  struct_name, needs_status = expected.fetch(kind) { fail_check("unexpected CRD kind #{kind}") }
  fail_check("#{kind} has no short name") if Array(names["shortNames"]).empty?
  fail_check("#{kind} has no category") unless Array(names["categories"]).include?("fortigate-external-dns")
  versions = spec.fetch("versions")
  fail_check("#{kind} must have exactly one storage version") unless versions.length == 1
  version = versions.first
  fail_check("#{kind} version must be served/storage v1alpha1") unless version["name"] == "v1alpha1" && version["served"] == true && version["storage"] == true
  has_status = version.dig("subresources", "status") == {}
  fail_check("#{kind} status subresource mismatch") unless has_status == needs_status
  openapi = version.dig("schema", "openAPIV3Schema")
  fail_check("#{kind} lacks structural root schema") unless openapi.is_a?(Hash) && openapi["type"] == "object"
  fail_check("#{kind} does not require spec") unless Array(openapi["required"]).include?("spec")
  schema_fields = openapi.dig("properties", "spec", "properties")&.keys&.sort
  fail_check("#{kind} spec schema is missing") unless schema_fields
  go_fields = struct_fields.call(struct_name)
  unless schema_fields == go_fields
    fail_check("#{kind} Go/CRD spec drift: Go=#{go_fields.inspect} CRD=#{schema_fields.inspect}")
  end
  printer_paths = Array(version["additionalPrinterColumns"]).map { |column| column["jsonPath"].to_s }
  if printer_paths.any? { |path| path.match?(/zone|record|provider|fingerprint|canonicalDocument|source/i) }
    fail_check("#{kind} printer columns expose record/provider detail: #{printer_paths.inspect}")
  end
  walk_schema.call(openapi, kind)
end
fail_check("CRD kinds are incomplete") unless seen.sort == expected.keys.sort

schema = JSON.parse(File.read(VALUES_SCHEMA))
values = YAML.safe_load(File.read(VALUES), aliases: true)

resolve_schema = lambda do |node|
  return node unless node.is_a?(Hash) && node["$ref"]
  path = node["$ref"].delete_prefix("#/").split("/")
  path.reduce(schema) { |current, key| current.fetch(key) }
end

check_values = lambda do |value, schema_node, path|
  schema_node = resolve_schema.call(schema_node)
  return unless value.is_a?(Hash)
  properties = schema_node["properties"]
  return unless properties
  unknown = value.keys - properties.keys
  fail_check("values key absent from schema at #{path}: #{unknown.join(', ')}") unless unknown.empty?
  value.each do |key, child|
    child_schema = resolve_schema.call(properties.fetch(key))
    if child.is_a?(Hash)
      check_values.call(child, child_schema, "#{path}.#{key}")
    elsif child.is_a?(Array) && child_schema["items"]
      child.each_with_index do |item, index|
        check_values.call(item, child_schema["items"], "#{path}.#{key}[#{index}]") if item.is_a?(Hash)
      end
    end
  end
end
check_values.call(values, schema, "values")

Dir.glob(File.join(ROOT, "{manifests,samples}/**/*.yaml")).sort.each do |path|
  yaml_documents(path)
end

chart_version = YAML.safe_load(File.read(CHART)).fetch("appVersion").to_s
raw_deployment = yaml_documents(RAW_DEPLOYMENT).find { |document| document["kind"] == "Deployment" }
raw_image = raw_deployment&.dig("spec", "template", "spec", "containers")&.find { |container| container["name"] == "controller" }&.fetch("image", nil)
expected_image = "ghcr.io/kgskr/fortigate-external-dns:#{chart_version}"
fail_check("raw Deployment image #{raw_image.inspect} does not match chart appVersion #{chart_version}") unless raw_image == expected_image

monitoring_values = YAML.safe_load(File.read(File.join(ROOT, "samples/monitoring-values.yaml")), aliases: true)
Array(monitoring_values.dig("metrics", "networkPolicy", "allowedNamespaces")).each do |selector|
  fail_check("monitoring namespace selector must be a map of string labels") unless selector.is_a?(Hash) && selector.values.all? { |value| value.is_a?(String) }
end

legacy_rules = yaml_documents(File.join(ROOT, "manifests/rbac.yaml"))
  .select { |doc| %w[Role ClusterRole].include?(doc["kind"]) }
  .flat_map { |doc| doc.fetch("rules", []) }
legacy_resources = legacy_rules.flat_map { |rule| Array(rule["resources"]) }
fail_check("legacy raw RBAC unexpectedly enables EndpointSlices") if legacy_resources.include?("endpointslices")

platform_rules = yaml_documents(File.join(ROOT, "manifests/platform-rbac.yaml"))
  .select { |doc| doc["kind"] == "Role" }
  .flat_map { |doc| doc.fetch("rules", []) }
platform_resources = platform_rules.flat_map { |rule| Array(rule["resources"]) }
%w[endpointslices fortigatednstargets fortigatednstargets/finalizers fortigatednsrecordownerships/status fortigatednschangeplans/finalizers fortigatednsstatuses/finalizers secrets configmaps].each do |resource|
  fail_check("platform RBAC is missing #{resource}") unless platform_resources.include?(resource)
end
platform_rules.select { |rule| Array(rule["resources"]).any? { |resource| %w[secrets configmaps].include?(resource) } }.each do |rule|
  fail_check("credential RBAC is not resourceName-bound") if Array(rule["resourceNames"]).empty? || Array(rule["verbs"]) != ["get"]
end

puts "CRD structure, Go/schema drift, values/schema drift, raw YAML, and RBAC validated"
