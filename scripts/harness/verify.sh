#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${SCRIPT_DIR}"

MODE="${1:-test}"
LOG_FILE="/tmp/dc_harness_verify.log"

echo "=== [Harness Verification Oracle: ${MODE}] ==="

case "${MODE}" in
    test)
        if make test > "${LOG_FILE}" 2>&1; then
            echo "✓ Verification PASS: Unit test suites green."
            exit 0
        else
            echo "✗ Verification FAIL: Unit test suite failure detected." >&2
            echo "--- [Deep Digger Failure Signature (Tail 25 Lines)] ---" >&2
            tail -n 25 "${LOG_FILE}" >&2
            echo "---------------------------------------------------------" >&2
            exit 1
        fi
        ;;
    e2e)
        if make e2e > "${LOG_FILE}" 2>&1; then
            echo "✓ Verification PASS: End-to-end integration tests green."
            exit 0
        else
            echo "✗ Verification FAIL: E2E integration test failure detected." >&2
            echo "--- [Deep Digger Failure Signature (Tail 25 Lines)] ---" >&2
            tail -n 25 "${LOG_FILE}" >&2
            echo "---------------------------------------------------------" >&2
            exit 1
        fi
        ;;
    full)
        echo "--> [1/2] Running Unit Tests..."
        if ! make test > "${LOG_FILE}" 2>&1; then
            echo "✗ FAIL on unit tests." >&2
            tail -n 25 "${LOG_FILE}" >&2
            exit 1
        fi
        echo "--> [2/2] Running E2E Stdio Tests..."
        if ! make e2e > "${LOG_FILE}" 2>&1; then
            echo "✗ FAIL on E2E tests." >&2
            tail -n 25 "${LOG_FILE}" >&2
            exit 1
        fi
        echo "=========================================================="
        echo "✓ Observable Proof Complete: All unit and E2E suites green."
        echo "=========================================================="
        exit 0
        ;;
    *)
        echo "Usage: verify.sh [test | e2e | full]" >&2
        exit 1
        ;;
esac
