#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${SCRIPT_DIR}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac

DIST_DIR="${SCRIPT_DIR}/dist"
PKG_NAME="a2m-${OS}-${ARCH}"
TARGET_DIR="${DIST_DIR}/${PKG_NAME}"

echo "=========================================================="
echo " Packaging A2M (Agent to Machine) Release: ${PKG_NAME}"
echo "=========================================================="

# 1. Ensure fresh build of microservices & Desktop App
./scripts/build_desktop.sh

rm -rf "${TARGET_DIR}"
mkdir -p "${TARGET_DIR}"

cp bin/a2m "${TARGET_DIR}/"
cp bin/a2m-gateway "${TARGET_DIR}/"
cp bin/a2m-fs "${TARGET_DIR}/"
cp bin/a2m-docs "${TARGET_DIR}/"
cp bin/dc-gateway "${TARGET_DIR}/"
cp bin/dc-fs-engine "${TARGET_DIR}/"
cp bin/dc-doc-service "${TARGET_DIR}/"
cp README.md "${TARGET_DIR}/"
cp LICENSE "${TARGET_DIR}/"

# Add Windows runner helper if on windows
cat << 'EOF' > "${TARGET_DIR}/a2m.cmd"
@echo off
set SCRIPT_DIR=%~dp0
"%SCRIPT_DIR%a2m-gateway.exe" %*
EOF

TARBALL="${DIST_DIR}/${PKG_NAME}.tar.gz"
echo "--> Compressing archive to ${TARBALL}..."
(cd "${DIST_DIR}" && tar -czf "${PKG_NAME}.tar.gz" "${PKG_NAME}")

# Generate SHA256
if command -v shasum >/dev/null 2>&1; then
    (cd "${DIST_DIR}" && shasum -a 256 "${PKG_NAME}.tar.gz" > "${PKG_NAME}.tar.gz.sha256")
elif command -v sha256sum >/dev/null 2>&1; then
    (cd "${DIST_DIR}" && sha256sum "${PKG_NAME}.tar.gz" > "${PKG_NAME}.tar.gz.sha256")
fi

echo "=========================================================="
echo " Packaging Completed Successfully!"
echo " CLI Tarball:  ${TARBALL}"
echo " Desktop DMG:  ${DIST_DIR}/A2M-macos-arm64.dmg"
echo " Checksum:     $(cat "${DIST_DIR}/${PKG_NAME}.tar.gz.sha256")"
echo "=========================================================="
