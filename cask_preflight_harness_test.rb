# encoding: utf-8
# Test harness for the Homebrew cask's `preflight` verification block.
#
# The block itself is NOT copied here — it is read out of .goreleaser.yaml at test time (see
# install_cask_test.go), so this harness can never drift from what actually ships. What is faked
# is only the Homebrew context the block runs in: `version`, `cask`, `ohai`, `Formula[...]` and
# `system_command`. cosign and curl are really executed; curl's release URL is rewritten to a
# local fixture server, since the point is to exercise the block's control flow, not GitHub's.
#
# Usage: ruby cask_preflight_harness_test.rb <block-file> <version> <url> <sha256>
#   env: FIXTURE_BASE=<base url of the fixture server>  STUB_COSIGN_DIR=<dir holding `cosign`>
# Exits 0 if preflight passed, 1 (with the message on stderr) if it aborted.

require "open3"
require "pathname"
require "tmpdir"

RELEASE_BASE = "https://github.com/partyline-sh/cli/releases/download".freeze

class FakeStanza
  def initialize(str)
    @str = str
  end

  def to_s
    @str
  end
end

class FakeCask
  attr_reader :url, :sha256
  def initialize(url, sha256)
    @url = FakeStanza.new(url)
    @sha256 = FakeStanza.new(sha256)
  end
end

class FakeResult
  attr_reader :stdout, :stderr
  def initialize(stdout, stderr)
    @stdout = stdout
    @stderr = stderr
  end
end

class FakeFormulaEntry
  def initialize(dir)
    @dir = Pathname.new(dir)
  end

  def opt_bin
    @dir
  end
end

module Formula
  def self.[](_name)
    dir = ENV["STUB_COSIGN_DIR"]
    raise "no such formula" if dir.nil? || dir.empty?

    FakeFormulaEntry.new(dir)
  end
end

# Stands in for the cask DSL object the preflight block is instance_eval'd against.
class PreflightContext
  attr_reader :version, :cask

  def initialize(version, cask)
    @version = version
    @cask = cask
  end

  def ohai(msg)
    warn("ohai: #{msg}")
  end

  # Mirrors Homebrew's system_command, which uses run! and therefore RAISES on a non-zero
  # exit. The block under test relies on that (it rescues to produce its own message).
  def system_command(executable, args: [], **_opts)
    argv = args.map(&:to_s)
    if executable.to_s.include?("curl")
      argv = argv.map { |a| a.sub(RELEASE_BASE, ENV.fetch("FIXTURE_BASE")) }
    end
    stdout, stderr, status = Open3.capture3(executable.to_s, *argv)
    raise "#{executable} exited #{status.exitstatus}: #{stderr}" unless status.success?

    FakeResult.new(stdout, stderr)
  end
end

# Collects the `preflight do … end` block out of the cask's custom_block, ignoring the other
# stanzas (binary/manpage/depends_on) which need no context to evaluate.
class CaskBlockCollector
  attr_reader :preflight_block, :dependencies

  def initialize
    @dependencies = []
  end

  def binary(*_args, **_kwargs); end

  def manpage(*_args); end

  def depends_on(**kwargs)
    @dependencies << kwargs
  end

  def preflight(&block)
    @preflight_block = block
  end
end

block_file, version, url, sha256 = ARGV
collector = CaskBlockCollector.new
# force_encoding: Homebrew's ruby defaults to UTF-8, but a bare `ruby` with no LANG set may
# default to US-ASCII and choke on the em-dashes in the block's comments.
collector.instance_eval(File.read(block_file).force_encoding("UTF-8"), block_file)

unless collector.dependencies.any? { |d| d[:formula] == "cosign" }
  warn "cask does not depend on cosign — verification would be skippable by simply not having it"
  exit 2
end
abort "cask declares no preflight block" if collector.preflight_block.nil?

begin
  PreflightContext.new(version, FakeCask.new(url, sha256)).instance_eval(&collector.preflight_block)
rescue StandardError => e
  warn e.message
  exit 1
end
puts "preflight passed"
