#!/usr/bin/env bash
# Writes the Homebrew formula for a release to stdout: the release's archives
# for macOS and Linux, with their SHA-256 from its checksums.txt. The release
# workflow puts it into secretli/homebrew-tap.
#
#   homebrew-formula.sh v0.5.0 checksums.txt > Formula/secretli.rb
set -euo pipefail

tag="${1:?usage: homebrew-formula.sh <tag> <checksums.txt>}"
checksums="${2:?usage: homebrew-formula.sh <tag> <checksums.txt>}"
base="https://github.com/secretli/cli/releases/download/${tag}"

sha() {
  local file="secretli_${tag}_$1.tar.gz" sum
  sum="$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$checksums")"
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || { echo "no checksum for $file in $checksums" >&2; exit 1; }
  echo "$sum"
}

# Looked up first: a failure inside the here-document would not stop the script.
darwin_arm64="$(sha darwin_arm64)"
darwin_amd64="$(sha darwin_amd64)"
linux_arm64="$(sha linux_arm64)"
linux_amd64="$(sha linux_amd64)"

cat <<EOF
# Written by the release workflow of secretli/cli for ${tag}; changes here are
# overwritten by the next release. The formula lives in secretli/cli's
# scripts/homebrew-formula.sh.
class Secretli < Formula
  desc "Share secrets encrypted on your machine, with links the web app opens"
  homepage "https://secretli.app"
  license "MIT"

  on_macos do
    on_arm do
      url "${base}/secretli_${tag}_darwin_arm64.tar.gz"
      sha256 "${darwin_arm64}"
    end
    on_intel do
      url "${base}/secretli_${tag}_darwin_amd64.tar.gz"
      sha256 "${darwin_amd64}"
    end
  end

  on_linux do
    on_arm do
      url "${base}/secretli_${tag}_linux_arm64.tar.gz"
      sha256 "${linux_arm64}"
    end
    on_intel do
      url "${base}/secretli_${tag}_linux_amd64.tar.gz"
      sha256 "${linux_amd64}"
    end
  end

  def install
    bin.install "secretli"
    generate_completions_from_executable(bin/"secretli", "completion")
  end

  test do
    assert_match "secretli v#{version}", shell_output("#{bin}/secretli --version")
  end
end
EOF
