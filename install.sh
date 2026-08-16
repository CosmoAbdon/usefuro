#!/bin/sh
# Furo installer — downloads the latest release binary for this machine.
#
#   client:  curl -fsSL https://raw.githubusercontent.com/CosmoAbdon/usefuro/main/install.sh | sh
#   server:  curl -fsSL https://raw.githubusercontent.com/CosmoAbdon/usefuro/main/install.sh | sh -s -- --server
#
# Options (after --): --server (install furo-server), --dir DIR (install dir).
# Later, update with:  furo update   /   furo-server update
set -eu

REPO="CosmoAbdon/usefuro"
BIN="furo"
DIR=""

while [ $# -gt 0 ]; do
  case "$1" in
    --server) BIN="furo-server" ;;
    --dir) DIR="$2"; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux|darwin) ;;
  *) echo "unsupported OS: $OS (download manually: https://github.com/$REPO/releases)" >&2; exit 1 ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
  grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4)
[ -n "$TAG" ] || { echo "could not resolve latest release" >&2; exit 1; }
VER=${TAG#v}

ASSET="${BIN}_${VER}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "downloading $ASSET ..."
curl -fsSL -o "$TMP/$ASSET" "$URL"
curl -fsSL -o "$TMP/checksums.txt" "https://github.com/$REPO/releases/download/$TAG/checksums.txt"

WANT=$(grep " $ASSET\$" "$TMP/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  GOT=$(sha256sum "$TMP/$ASSET" | cut -d' ' -f1)
else
  GOT=$(shasum -a 256 "$TMP/$ASSET" | cut -d' ' -f1)
fi
[ "$WANT" = "$GOT" ] || { echo "checksum mismatch for $ASSET" >&2; exit 1; }

tar xzf "$TMP/$ASSET" -C "$TMP"

if [ -z "$DIR" ]; then
  if [ -w /usr/local/bin ]; then DIR=/usr/local/bin; else DIR="$HOME/.local/bin"; fi
fi
mkdir -p "$DIR"
install -m 0755 "$TMP/$BIN" "$DIR/$BIN"

echo "installed $BIN $TAG to $DIR/$BIN"
case ":$PATH:" in
  *":$DIR:"*) ;;
  *) echo "note: $DIR is not in your PATH — add it, e.g.: export PATH=\"$DIR:\$PATH\"" ;;
esac

if [ "$BIN" = "furo" ]; then
  echo "next: furo login <token> --server <host>:7835   then   furo http 3000"
else
  echo "next: furo-server init   then   furo-server user add <name> && furo-server serve"
fi
