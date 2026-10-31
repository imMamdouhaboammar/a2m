#!/usr/bin/env bash
set -euo pipefail

echo "=========================================================="
echo " Building A2M (Agent to Machine) Native Microservices"
echo "=========================================================="

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "${ROOT_DIR}/bin"

echo "--> [1/3] Building Rust Filesystem & Search Engine (a2m-fs)..."
(
    cd "${ROOT_DIR}/services/dc-fs-engine"
    cargo build --release
    rm -f "${ROOT_DIR}/bin/a2m-fs" "${ROOT_DIR}/bin/dc-fs-engine"
    cp target/release/dc-fs-engine "${ROOT_DIR}/bin/a2m-fs"
    ln -sf a2m-fs "${ROOT_DIR}/bin/dc-fs-engine"
)

echo "--> [2/3] Building Go MCP Gateway & Supervisor (a2m-gateway)..."
(
    cd "${ROOT_DIR}/services/dc-gateway"
    go build -o "${ROOT_DIR}/bin/a2m-gateway" .
    rm -f "${ROOT_DIR}/bin/dc-gateway"
    ln -sf a2m-gateway "${ROOT_DIR}/bin/dc-gateway"
)

echo "--> [3/3] Building Go Document & Spreadsheet Service (a2m-docs)..."
(
    cd "${ROOT_DIR}/services/dc-doc-service"
    go build -o "${ROOT_DIR}/bin/a2m-docs" .
    rm -f "${ROOT_DIR}/bin/dc-doc-service"
    ln -sf a2m-docs "${ROOT_DIR}/bin/dc-doc-service"
)

# CLI wrapper
cat << 'EOF' > "${ROOT_DIR}/bin/a2m"
#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "${SCRIPT_DIR}/../.env" ]; then
    set -a
    source "${SCRIPT_DIR}/../.env"
    set +a
elif [ -f "${PWD}/.env" ]; then
    set -a
    source "${PWD}/.env"
    set +a
fi
SOCKET_PATH="${A2M_IPC_SOCKET:-/tmp/a2m-ipc.sock}"
GATEWAY_BIN="${SCRIPT_DIR}/a2m-gateway"

if [ ! -x "${GATEWAY_BIN}" ]; then
    GATEWAY_BIN="${SCRIPT_DIR}/dc-gateway"
fi

if [ $# -gt 0 ]; then
    case "$1" in
        ps|sessions|watch|logs|attach|kill|status|connect|disconnect|server)
            exec "${GATEWAY_BIN}" "$@"
            ;;
    esac
fi

exec "${GATEWAY_BIN}" --socket "${SOCKET_PATH}" "$@"
EOF
chmod +x "${ROOT_DIR}/bin/a2m"
ln -sf a2m "${ROOT_DIR}/bin/desktop-commander"

echo "=========================================================="
echo " Build Completed Successfully!"
echo " A2M Binaries located in ./bin:"
ls -lh "${ROOT_DIR}/bin"
echo "=========================================================="
