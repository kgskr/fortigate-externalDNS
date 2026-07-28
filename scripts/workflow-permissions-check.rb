#!/usr/bin/env ruby
# frozen_string_literal: true

require "yaml"

path = ARGV.fetch(0)
workflow = YAML.safe_load(File.read(path), aliases: true)
abort "CI workflow must decode to a mapping" unless workflow.is_a?(Hash)

top_level = workflow["permissions"]
unless top_level.is_a?(Hash) && top_level["contents"].to_s.downcase == "read"
  abort "CI must declare top-level contents: read"
end

scopes = [["workflow", top_level]]
jobs = workflow["jobs"]
abort "CI workflow must declare jobs" unless jobs.is_a?(Hash)
jobs.each do |name, job|
  next unless job.is_a?(Hash) && job.key?("permissions")

  scopes << ["job #{name}", job["permissions"]]
end

scopes.each do |name, permissions|
  if permissions.to_s.strip.downcase == "write-all"
    abort "#{name} permissions must not use write-all"
  end
  abort "#{name} permissions must be a mapping" unless permissions.is_a?(Hash)

  writable = permissions.select { |_scope, access| access.to_s.strip.downcase == "write" }.keys
  abort "#{name} permissions grant write access: #{writable.sort.join(', ')}" unless writable.empty?

  invalid = permissions.reject { |_scope, access| %w[read none].include?(access.to_s.strip.downcase) }.keys
  abort "#{name} permissions use unsupported access: #{invalid.sort.join(', ')}" unless invalid.empty?
end
