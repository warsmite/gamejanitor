#!/bin/bash
set -e

REPO="0xkowalskidev/gamejanitor"
INSTALL_DIR="/usr/local/bin"
BINARY="gamejanitor"

echo "Installing gamejanitor..."

# Check Docker
if ! command -v docker &>/dev/null; then
    echo "WARNING: Docker is not installed. Gamejanitor requires Docker to run."
    echo "Install Docker: https://docs.docker.com/engine/install/"
fi

# Download latest release
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BINARY}"
echo "Downloading from ${DOWNLOAD_URL}..."

TMP=$(mktemp)
if ! curl -fsSL "$DOWNLOAD_URL" -o "$TMP"; then
    echo "ERROR: Failed to download. Check https://github.com/${REPO}/releases for available releases."
    rm -f "$TMP"
    exit 1
fi

chmod +x "$TMP"

# Install
if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP" "${INSTALL_DIR}/${BINARY}"
else
    echo "Need sudo to install to ${INSTALL_DIR}"
    sudo mv "$TMP" "${INSTALL_DIR}/${BINARY}"
fi

echo ""
echo "gamejanitor installed to ${INSTALL_DIR}/${BINARY}"
echo ""
echo "Get started:"
echo "  gamejanitor serve"
echo ""
echo "Web UI: http://localhost:8080"
