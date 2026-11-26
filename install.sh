#!/bin/sh
# Install pawabase-connector on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/Pawabase/pawabase-connector/main/install.sh | sh
#
# Settings (environment variables):
#   PAWABASE_CONNECTOR_VERSION   a release tag such as v0.1.0 (default: the latest release)
#   PAWABASE_CONNECTOR_DIR       where to put the binary (default: /usr/local/bin, or ~/.local/bin without root)
#   PAWABASE_CONNECTOR_BASE_URL  where releases are downloaded from (default: the GitHub releases of Pawabase/pawabase-connector)
set -eu

REPO="Pawabase/pawabase-connector"
BINARY="pawabase-connector"
VERSION="${PAWABASE_CONNECTOR_VERSION:-latest}"

say() { printf '%s\n' "$*"; }
fail() { printf 'pawabase-connector install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "this needs '$1'"; }

need uname
need tar
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
else
  fail "this needs curl or wget"
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported system $(uname -s): on Windows use install.ps1, anywhere else the Docker image" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported processor $(uname -m)" ;;
esac

if [ -n "${PAWABASE_CONNECTOR_BASE_URL:-}" ]; then
  base="${PAWABASE_CONNECTOR_BASE_URL%/}"
elif [ "$VERSION" = "latest" ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
asset="${BINARY}_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset ($VERSION)…"
fetch "$base/$asset" "$tmp/$asset" || fail "could not download $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "could not download the checksums"

expected="$(grep " $asset\$" "$tmp/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || fail "$asset is not in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $asset: the download is not the published release"

tar -xzf "$tmp/$asset" -C "$tmp" "$BINARY" || fail "could not unpack $asset"
chmod +x "$tmp/$BINARY"

if [ -n "${PAWABASE_CONNECTOR_DIR:-}" ]; then
  dir="$PAWABASE_CONNECTOR_DIR"
elif [ "$(id -u)" = "0" ] || [ -w /usr/local/bin ]; then
  dir=/usr/local/bin
elif command -v sudo >/dev/null 2>&1; then
  dir=/usr/local/bin
  use_sudo=1
else
  dir="$HOME/.local/bin"
fi
mkdir -p "$dir" 2>/dev/null || true
if [ "${use_sudo:-0}" = "1" ]; then
  say "Installing to $dir (asks for your password)…"
  sudo install -m 0755 "$tmp/$BINARY" "$dir/$BINARY"
else
  install -m 0755 "$tmp/$BINARY" "$dir/$BINARY"
fi

say "Installed $("$dir/$BINARY" version)"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "Add $dir to your PATH to run it by name." ;;
esac
say ""
say "Next, with the token Studio showed you:"
say "  sudo $BINARY enroll --server <your Pawabase address> --token <pbce_…>"
say "  sudo $BINARY service install"
