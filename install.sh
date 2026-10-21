#!/bin/sh
# Installs the nimbus CLI from GitHub Releases:
#
#   curl -fsSL https://raw.githubusercontent.com/wceriale/nimbus-cli/main/install.sh | sh
#
# Environment:
#   NIMBUS_VERSION      version to install, e.g. v0.0.1 (default: the latest release)
#   NIMBUS_INSTALL_DIR  where to put the binary (default: ~/.local/bin; no sudo needed)
set -eu

repo="wceriale/nimbus-cli"
version="${NIMBUS_VERSION:-latest}"
install_dir="${NIMBUS_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "nimbus install: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "this installer needs $1"
}

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported OS $(uname -s): nimbus runs on macOS and Linux" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported architecture $(uname -m): nimbus runs on amd64 and arm64" ;;
esac

if [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
else
	case "$version" in v*) ;; *) version="v$version" ;; esac
	base="https://github.com/$repo/releases/download/$version"
fi
asset="nimbus_${os}_${arch}.tar.gz"

need curl
need tar
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "couldn't download $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "couldn't download $base/checksums.txt"

expected="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$expected" ] || fail "checksums.txt has no entry for $asset"
if command -v sha256sum >/dev/null 2>&1; then
	actual="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
	actual="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
else
	fail "this installer needs sha256sum or shasum to verify the download"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $asset: the download is corrupt or was tampered with"

tar -xzf "$tmp/$asset" -C "$tmp" nimbus
mkdir -p "$install_dir"
# Copy then rename, so a running nimbus is never half-overwritten.
cp "$tmp/nimbus" "$install_dir/.nimbus.new"
chmod 0755 "$install_dir/.nimbus.new"
mv -f "$install_dir/.nimbus.new" "$install_dir/nimbus"

echo "Installed $("$install_dir/nimbus" --version) at $install_dir/nimbus"
case ":$PATH:" in
*":$install_dir:"*) ;;
*) echo "Note: $install_dir isn't on PATH. Run it by full path, or: export PATH=\"$install_dir:\$PATH\"" ;;
esac
echo "Next: run \`nimbus docs\` for the agent guide, then \`nimbus login\`."
