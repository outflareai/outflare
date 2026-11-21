# frozen_string_literal: true

require_relative "lib/outflare/version"

Gem::Specification.new do |s|
  s.name = "outflare"
  s.version = Outflare::VERSION
  s.summary = "Send product events to Outflare from Ruby"
  s.description = "Queues identify, track, page, screen, group and alias calls and sends them to Outflare in batches, in the background, with retries."
  s.authors = ["Outflare"]
  s.email = ["hello@outflare.ai"]
  s.homepage = "https://outflare.ai/docs/sdk/ruby/"
  s.license = "Apache-2.0"
  s.required_ruby_version = ">= 3.0"
  s.files = Dir["lib/**/*.rb", "README.md"]
  s.metadata = {
    "source_code_uri" => "https://github.com/outflareai/outflare/tree/main/sdk/ruby",
    "bug_tracker_uri" => "https://github.com/outflareai/outflare/issues"
  }
end
