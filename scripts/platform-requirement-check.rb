#!/usr/bin/env ruby
# frozen_string_literal: true

ROOT = File.expand_path("..", __dir__)
CHANGE_SPECS = File.join(ROOT, "openspec", "changes", "expand-controller-platform-capabilities", "specs", "*", "spec.md")
BASELINE_ROOT = File.join(ROOT, "openspec", "specs")
EVIDENCE_PATH = File.join(ROOT, "docs", "platform-requirement-evidence.md")

abort "missing platform evidence document" unless File.file?(EVIDENCE_PATH)

evidence = File.read(EVIDENCE_PATH)
failures = []
scenario_count = 0

Dir.glob(CHANGE_SPECS).sort.each do |change_path|
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
