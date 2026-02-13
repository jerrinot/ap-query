#!/bin/sh
# Install ap-query from GitHub releases.
# Usage: curl -fsSL https://raw.githubusercontent.com/jerrinot/ap-query/master/install.sh | sh
set -e

REPO="jerrinot/ap-query"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
if [ -z "$VERSION" ]; then
  echo "error: could not determine latest version" >&2
  exit 1
fi

URL="https://github.com/${REPO}/releases/download/${VERSION}/ap-query_${OS}_${ARCH}.tar.gz"
echo "downloading ap-query ${VERSION} for ${OS}/${ARCH}..."

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -fsSL "$URL" -o "$TMP/ap-query.tar.gz"
tar -xzf "$TMP/ap-query.tar.gz" -C "$TMP"
mkdir -p "$INSTALL_DIR"
install -m 755 "$TMP/ap-query" "$INSTALL_DIR/ap-query"

echo "installed ap-query to ${INSTALL_DIR}/ap-query"
