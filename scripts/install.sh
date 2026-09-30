#!/bin/sh
# Install the latest Sleipnir release for this machine.
#   curl -fsSL https://raw.githubusercontent.com/reee344/sleipnir/main/scripts/install.sh | sh
# Environment: SLEIPNIR_VERSION (default: latest), SLEIPNIR_BIN_DIR (default: ~/.local/bin).
set -eu

repo="reee344/sleipnir"
bin_dir="${SLEIPNIR_BIN_DIR:-$HOME/.local/bin}"
version="${SLEIPNIR_VERSION:-latest}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "unsupported OS: $os (use the release page for Windows)" >&2; exit 1;; esac
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; *) echo "unsupported architecture: $arch" >&2; exit 1;; esac

if [ "$version" = latest ]; then
  base="https://github.com/$repo/releases/latest/download"
  version=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest" | sed 's|.*/tag/v||')
else
  version=${version#v}
  base="https://github.com/$repo/releases/download/v$version"
fi

archive="sleipnir_${version}_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading $archive"
curl -fsSL "$base/$archive" -o "$tmp/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

want=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$archive" | cut -d' ' -f1); else got=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1); fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "checksum mismatch for $archive" >&2; exit 1; }

tar -xzf "$tmp/$archive" -C "$tmp" sleipnir
mkdir -p "$bin_dir"
install -m 0755 "$tmp/sleipnir" "$bin_dir/sleipnir"
echo "installed $bin_dir/sleipnir ($version)"
case ":$PATH:" in *":$bin_dir:"*) ;; *) echo "add $bin_dir to your PATH";; esac
