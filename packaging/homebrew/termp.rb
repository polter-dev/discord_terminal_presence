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
  # 3. At #9, confirm all four Worker URLs and GitHub mirrors resolve to the
  #    matching assets attached to that GitHub release.
  #
  # This formula is intended for `brew tap polter-dev/tap`. The Worker only
  # redirects to the GitHub asset; version and sha256 placeholders are filled
  # when release assets exist at #9.

  on_macos do
    on_intel do
      url "https://termp.polter.sh/dl/brew/darwin/amd64"
      mirror "https://github.com/polter-dev/discord_terminal_presence/releases/download/v#{version}/termp_#{version}_darwin_amd64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_darwin_amd64"
    end

    on_arm do
      url "https://termp.polter.sh/dl/brew/darwin/arm64"
      mirror "https://github.com/polter-dev/discord_terminal_presence/releases/download/v#{version}/termp_#{version}_darwin_arm64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_darwin_arm64"
    end
  end

  on_linux do
    on_intel do
      url "https://termp.polter.sh/dl/brew/linux/amd64"
      mirror "https://github.com/polter-dev/discord_terminal_presence/releases/download/v#{version}/termp_#{version}_linux_amd64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_linux_amd64"
    end

    on_arm do
      url "https://termp.polter.sh/dl/brew/linux/arm64"
      mirror "https://github.com/polter-dev/discord_terminal_presence/releases/download/v#{version}/termp_#{version}_linux_arm64.tar.gz"
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
