#!/usr/bin/env ruby
# frozen_string_literal: true

ROOT = File.expand_path("..", __dir__)
CHANGE_NAME = "expand-controller-platform-capabilities"
CHANGES_ROOT = File.join(ROOT, "openspec", "changes")
ACTIVE_CHANGE_ROOT = File.join(CHANGES_ROOT, CHANGE_NAME)
ARCHIVED_CHANGE_ROOTS = Dir.glob(File.join(CHANGES_ROOT, "archive", "*-#{CHANGE_NAME}")).select { |path| File.directory?(path) }.sort.freeze
BASELINE_ROOT = File.join(ROOT, "openspec", "specs")
EVIDENCE_PATH = File.join(ROOT, "docs", "platform-requirement-evidence.md")

change_root = if File.directory?(ACTIVE_CHANGE_ROOT)
                ACTIVE_CHANGE_ROOT
              elsif ARCHIVED_CHANGE_ROOTS.length == 1
                ARCHIVED_CHANGE_ROOTS.first
              elsif ARCHIVED_CHANGE_ROOTS.empty?
                abort "missing active or archived OpenSpec change #{CHANGE_NAME}"
              else
                abort "ambiguous archived OpenSpec changes for #{CHANGE_NAME}: #{ARCHIVED_CHANGE_ROOTS.join(', ')}"
              end
change_specs = File.join(change_root, "specs", "*", "spec.md")

abort "missing platform evidence document" unless File.file?(EVIDENCE_PATH)

evidence = File.read(EVIDENCE_PATH)
failures = []
scenario_count = 0

Dir.glob(change_specs).sort.each do |change_path|
  capability = File.basename(File.dirname(change_path))
  baseline_path = File.join(BASELINE_ROOT, capability, "spec.md")
  unless File.file?(baseline_path)
    failures << "missing baseline capability #{capability}"
    next
  end

  change = File.read(change_path)
  baseline = File.read(baseline_path)
  change.scan(/^### Requirement: (.+)$/).flatten.each do |requirement|
    failures << "baseline #{capability} is missing requirement: #{requirement}" unless baseline.include?("### Requirement: #{requirement}")
  end
  change.scan(/^#### Scenario: (.+)$/).flatten.each do |scenario|
    scenario_count += 1
    failures << "baseline #{capability} is missing scenario: #{scenario}" unless baseline.include?("#### Scenario: #{scenario}")
    failures << "evidence table is missing scenario: #{scenario}" unless evidence.include?("| #{scenario} |")
  end
end

failures << "expected at least one scenario" if scenario_count.zero?
abort("platform requirement evidence failed:\n- #{failures.join("\n- ")}") unless failures.empty?

puts "platform requirement evidence covers #{scenario_count} OpenSpec scenarios"
