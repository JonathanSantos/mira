# Homebrew formula for a tap (brew tap JonathanSantos/mira https://github.com/JonathanSantos/mira).
# Builds from source because the tree-sitter runtime is cgo.
class Mira < Formula
  desc "Code context for AI agents: navigate by symbol, read by line range, edit with verification"
  homepage "https://github.com/JonathanSantos/mira"
  url "https://github.com/JonathanSantos/mira.git", tag: "v0.2.0"
  head "https://github.com/JonathanSantos/mira.git", branch: "main"
  license "MIT"

  depends_on "go" => :build

  def install
    ldflags = "-s -w -X github.com/JonathanSantos/mira/internal/cli.Version=#{version}"
    system "go", "build", "-trimpath", "-ldflags", ldflags, "-o", bin/"mira", "./cmd/mira"
  end

  test do
    assert_match "mira", shell_output("#{bin}/mira version")
  end
end
