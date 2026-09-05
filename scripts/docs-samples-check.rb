#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

ROOT = File.expand_path("..", __dir__)
READMES = %w[README.md README.ko.md charts/fortigate-external-dns/README.md manifests/README.md].freeze
REQUIRED_SAMPLES = %w[
  samples/policy.yaml
  samples/targets.yaml
  samples/ownership-adoption.yaml
  samples/plan-approval.yaml
  samples/externalname-service.yaml
  samples/headless-dual-stack.yaml
  samples/monitoring-values.yaml
  samples/one-shot-plan.sh
  samples/release-verification.sh
].freeze

def fail_check(message)
  warn "documentation/sample validation failed: #{message}"
  exit 1
end

(READMES + REQUIRED_SAMPLES).each do |relative|
  fail_check("missing #{relative}") unless File.file?(File.join(ROOT, relative))
end

READMES.each do |relative|
  path = File.join(ROOT, relative)
  File.read(path).scan(/\[[^\]]*\]\(([^)]+)\)/).flatten.each do |raw_target|
    target = raw_target.strip.delete_prefix("<").delete_suffix(">")
    next if target.empty? || target.start_with?("#") || target.match?(%r{\A(?:https?|mailto):})

    target = target.split("#", 2).first.split("?", 2).first
    resolved = File.expand_path(target, File.dirname(path))
    fail_check("broken local link in #{relative}: #{raw_target}") unless File.exist?(resolved)
  end
end

yaml_paths = Dir.glob(File.join(ROOT, "samples/**/*.yaml")).sort
yaml_paths.each do |path|
  documents = YAML.load_stream(File.read(path)).compact
  documents.each do |document|
    next unless document.is_a?(Hash)
    if document["kind"] == "Secret" && (document.key?("data") || document.key?("stringData"))
      fail_check("sample Secret embeds data: #{path}")
    end
  end
rescue StandardError => e
  fail_check("invalid YAML #{path}: #{e.message}")
end

config = File.read(File.join(ROOT, "internal/config/config.go"))
%w[
  plan-output plan-output-overwrite approved-plan-hash target-mode
  platform-namespace policy-enforcement event-driven debounce resync
  status-retention plan-retention publish-external-name-services publish-headless-services
].each do |flag|
  fail_check("runtime flag --#{flag} is not implemented") unless config.include?(%Q{"#{flag}"})
  READMES.first(2).each do |readme|
    fail_check("#{readme} does not document --#{flag}") unless File.read(File.join(ROOT, readme)).include?("--#{flag}")
  end
end

active_platform_evidence = {
  "README.md" => /multi-target mode.*supports/m,
  "README.ko.md" => /멀티 타깃 모드를 모두 지원/m,
  "charts/fortigate-external-dns/README.md" => /targetMode\.enabled=true.*switches/m,
  "charts/fortigate-external-dns/templates/NOTES.txt" => /Platform runtime is ENABLED/,
  "manifests/README.md" => /--target-mode --platform-namespace/,
  "samples/targets.yaml" => /Active target-mode examples/
}
active_platform_evidence.each do |relative, pattern|
  fail_check("#{relative} does not describe the active platform runtime") unless File.read(File.join(ROOT, relative)).match?(pattern)
end

%w[README.md README.ko.md charts/fortigate-external-dns/README.md manifests/README.md].each do |relative|
  contents = File.read(File.join(ROOT, relative))
  fail_check("#{relative} lacks shared replacement adoption warning") unless contents.match?(/replacement/i)
end

headless = YAML.load_stream(File.read(File.join(ROOT, "samples/headless-dual-stack.yaml"))).compact
headless_kinds = headless.map { |doc| doc["kind"] }
fail_check("headless sample must contain Service and two EndpointSlices") unless headless_kinds.count("Service") == 1 && headless_kinds.count("EndpointSlice") == 2 && headless_kinds.length == 3
fail_check("headless sample must cover IPv4 and IPv6") unless headless.map { |doc| doc["addressType"] }.compact.sort == %w[IPv4 IPv6]

approval = File.read(File.join(ROOT, "samples/plan-approval.yaml"))
fail_check("plan approval sample lacks the exact annotation") unless approval.include?("fortigate-external-dns.kgskr.io/approved-plan-hash")
adoption = File.read(File.join(ROOT, "samples/ownership-adoption.yaml"))
fail_check("adoption sample lacks adopt annotation") unless adoption.include?("fortigate-external-dns.kgskr.io/adopt")

forbidden = /(?:-----BEGIN (?:RSA |EC )?PRIVATE KEY-----|fortigate[_-]?api[_-]?token\s*[:=]\s*[A-Za-z0-9._\/+==-]{12,})/i
Dir.glob(File.join(ROOT, "samples/**/*")).select { |path| File.file?(path) }.each do |path|
  fail_check("possible credential in #{path}") if File.read(path).match?(forbidden)
end

puts "documentation links, runtime flags, active platform samples, safety warnings, and credential-free examples validated"
