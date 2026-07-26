class Termp < Formula
  desc "Discord Rich Presence daemon for terminal tools"
  homepage "https://github.com/polter-dev/discord_terminal_presence"
  version "REPLACE_WITH_VERSION"
  license "MIT"

  # Release checklist:
  # 1. Replace REPLACE_WITH_VERSION with the release version without the leading "v"
  #    (for example, 1.2.3 for tag v1.2.3).
  # 2. In that release's checksums.txt, find each archive named below and replace
  #    its REPLACE_WITH_SHA256_<os>_<arch> value with the corresponding SHA-256.
  # 3. Confirm all four URLs resolve to assets attached to that GitHub release.
  #
  # This formula is intended for `brew tap polter-dev/tap`. The archives MUST
  # remain GitHub release assets, never files on a separate host, so GitHub
  # records the Homebrew downloads in each asset's download_count.

  on_macos do
    on_intel do
      url "https://github.com/polter-dev/discord_terminal_presence/releases/download/v#{version}/termp_#{version}_darwin_amd64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_darwin_amd64"
    end

    on_arm do
      url "https://github.com/polter-dev/discord_terminal_presence/releases/download/v#{version}/termp_#{version}_darwin_arm64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_darwin_arm64"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/polter-dev/discord_terminal_presence/releases/download/v#{version}/termp_#{version}_linux_amd64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_linux_amd64"
    end

    on_arm do
      url "https://github.com/polter-dev/discord_terminal_presence/releases/download/v#{version}/termp_#{version}_linux_arm64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_linux_arm64"
    end
  end

  def install
    bin.install "termp"
  end

  test do
    system "#{bin}/termp", "version"
  end
end
