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

ARCHIVE="ap-query_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"
echo "downloading ap-query ${VERSION} for ${OS}/${ARCH}..."

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -fsSL "$URL" -o "$TMP/ap-query.tar.gz"
curl -fsSL "$CHECKSUMS_URL" -o "$TMP/checksums.txt"

EXPECTED_HASH=$(awk -v file="$ARCHIVE" '$2 == file || $2 == "*" file { print $1; exit }' "$TMP/checksums.txt")
if [ -z "$EXPECTED_HASH" ]; then
  echo "error: checksum for ${ARCHIVE} not found in checksums.txt" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL_HASH=$(sha256sum "$TMP/ap-query.tar.gz" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL_HASH=$(shasum -a 256 "$TMP/ap-query.tar.gz" | awk '{print $1}')
elif command -v openssl >/dev/null 2>&1; then
  ACTUAL_HASH=$(openssl dgst -sha256 "$TMP/ap-query.tar.gz" | awk '{print $NF}')
else
  echo "error: no SHA-256 tool found (sha256sum, shasum, or openssl required)" >&2
  exit 1
fi

if [ "$ACTUAL_HASH" != "$EXPECTED_HASH" ]; then
  echo "error: checksum mismatch for ${ARCHIVE}" >&2
  echo "expected: $EXPECTED_HASH" >&2
  echo "actual:   $ACTUAL_HASH" >&2
  exit 1
fi

echo "checksum verified"
tar -xzf "$TMP/ap-query.tar.gz" -C "$TMP"
mkdir -p "$INSTALL_DIR"
install -m 755 "$TMP/ap-query" "$INSTALL_DIR/ap-query"

echo "installed ap-query to ${INSTALL_DIR}/ap-query"

# Check if INSTALL_DIR is already in PATH
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) exit 0 ;;
esac

# Detect shell rc file
RC_FILE=""
SHELL_NAME=$(basename "${SHELL:-/bin/sh}")
case "$SHELL_NAME" in
  zsh)  RC_FILE="$HOME/.zshrc" ;;
  bash)
    # macOS terminals open login shells (.bash_profile), Linux opens non-login shells (.bashrc)
    case "$OS" in
      darwin) RC_FILE="$HOME/.bash_profile" ;;
      *)      RC_FILE="$HOME/.bashrc" ;;
    esac
    ;;
  fish) RC_FILE="$HOME/.config/fish/config.fish" ;;
esac

if [ -z "$RC_FILE" ]; then
  echo "NOTE: ${INSTALL_DIR} is not in your PATH. Add it with:"
  echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
  exit 0
fi

# Ask user
printf "%s is not in your PATH. Add it to %s? [Y/n] " "$INSTALL_DIR" "$RC_FILE"

# When piped through sh, stdin is the script itself — reopen from tty
if [ ! -t 0 ]; then
  if ! (exec </dev/tty) 2>/dev/null; then
    echo ""
    echo "NOTE: ${INSTALL_DIR} is not in your PATH. Add it with:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    exit 0
  fi
  exec </dev/tty
fi

read -r REPLY
case "$REPLY" in
  [nN]*)
    echo "skipped. To add it manually:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
  *)
    case "$SHELL_NAME" in
      fish)
        EXPORT_LINE="fish_add_path ${INSTALL_DIR}"
        ;;
      *)
        EXPORT_LINE="export PATH=\"${INSTALL_DIR}:\$PATH\""
        ;;
    esac
    mkdir -p "$(dirname "$RC_FILE")"
    echo "" >> "$RC_FILE"
    echo "# Added by ap-query installer" >> "$RC_FILE"
    echo "$EXPORT_LINE" >> "$RC_FILE"
    echo "Added to ${RC_FILE}. Reload with:"
    echo "  source ${RC_FILE}"
    ;;
esac

echo ""
echo "Next, run 'ap-query init' to complete setup."
