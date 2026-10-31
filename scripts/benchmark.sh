#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${SCRIPT_DIR}"

echo "=========================================================="
echo " Running Performance Benchmarks (Chat On steriod Native)"
echo "=========================================================="

# 1. Cold Startup Measurement (average of 10 runs)
echo "--> [1/4] Measuring Cold Process Startup Latency..."
TOTAL_MS=0
ROUNDS=10
for i in $(seq 1 $ROUNDS); do
    START_NS=$(python3 -c 'import time; print(int(time.time_ns()))')
    "${SCRIPT_DIR}/bin/desktop-commander" --help > /dev/null 2>&1 || true
    END_NS=$(python3 -c 'import time; print(int(time.time_ns()))')
    DIFF_MS=$(( (END_NS - START_NS) / 1000000 ))
    TOTAL_MS=$(( TOTAL_MS + DIFF_MS ))
done
AVG_STARTUP=$(( TOTAL_MS / ROUNDS ))
echo "    Average cold startup: ${AVG_STARTUP} ms"

# 2. Tool Execution Latency (100 rounds of echo & read_file via stdio)
echo "--> [2/4] Measuring End-to-End Command Latency (100 rounds)..."
START_NS=$(python3 -c 'import time; print(int(time.time_ns()))')
python3 -c '
import json, subprocess, time

req = json.dumps({"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"start_process","arguments":{"command":"echo bench_test","timeout_ms":500}}}) + "\n"
proc = subprocess.Popen(["'"${SCRIPT_DIR}/bin/desktop-commander"'"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

# Initialize
proc.stdin.write(json.dumps({"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}) + "\n")
proc.stdin.flush()
proc.stdout.readline()

for _ in range(50):
    proc.stdin.write(req)
    proc.stdin.flush()
    proc.stdout.readline()

proc.stdin.close()
proc.terminate()
'
END_NS=$(python3 -c 'import time; print(int(time.time_ns()))')
TOTAL_TIME_MS=$(( (END_NS - START_NS) / 1000000 ))
AVG_CMD_LATENCY=$(( TOTAL_TIME_MS / 50 ))
echo "    Average command latency (process spawn + capture): ${AVG_CMD_LATENCY} ms"

# 3. Memory Footprint (RSS)
echo "--> [3/4] Measuring Idle RSS Memory..."
"${SCRIPT_DIR}/bin/desktop-commander" server --host 127.0.0.1 --port 9876 > /dev/null 2>&1 &
SERVER_PID=$!
sleep 0.5
RSS_KB=$(ps -o rss= -p $SERVER_PID | tr -d ' ')
RSS_MB=$(python3 -c "print(round(${RSS_KB}/1024, 2))")
kill -9 $SERVER_PID 2>/dev/null || true
echo "    Idle Gateway RSS: ${RSS_MB} MB (${RSS_KB} KB)"

# 4. Binary Sizes
echo "--> [4/4] Measuring Executable Sizes..."
GATEWAY_SIZE=$(ls -lhL "${SCRIPT_DIR}/bin/dc-gateway" | awk '{print $5}')
FS_SIZE=$(ls -lhL "${SCRIPT_DIR}/bin/dc-fs-engine" | awk '{print $5}')
DOC_SIZE=$(ls -lhL "${SCRIPT_DIR}/bin/dc-doc-service" | awk '{print $5}')

if [ -f "${SCRIPT_DIR}/apps/desktop/macos/A2M.app/Contents/MacOS/A2M" ]; then
    APP_SIZE=$(ls -lhL "${SCRIPT_DIR}/apps/desktop/macos/A2M.app/Contents/MacOS/A2M" | awk '{print $5}')
elif [ -f "${SCRIPT_DIR}/apps/desktop/macos/ChatOnSteriod.app/Contents/MacOS/ChatOnSteriod" ]; then
    APP_SIZE=$(ls -lhL "${SCRIPT_DIR}/apps/desktop/macos/ChatOnSteriod.app/Contents/MacOS/ChatOnSteriod" | awk '{print $5}')
else
    APP_SIZE="N/A"
fi

if [ -f "${SCRIPT_DIR}/dist/A2M-macos-arm64.dmg" ]; then
    DMG_SIZE=$(ls -lhL "${SCRIPT_DIR}/dist/A2M-macos-arm64.dmg" | awk '{print $5}')
elif [ -f "${SCRIPT_DIR}/dist/ChatOnSteriod-macos-arm64.dmg" ]; then
    DMG_SIZE=$(ls -lhL "${SCRIPT_DIR}/dist/ChatOnSteriod-macos-arm64.dmg" | awk '{print $5}')
else
    DMG_SIZE="N/A"
fi

echo "    dc-gateway:    ${GATEWAY_SIZE}"
echo "    dc-fs-engine:  ${FS_SIZE}"
echo "    dc-doc-service:${DOC_SIZE}"
echo "    Desktop App:   ${APP_SIZE} (Mach-O)"
echo "    DMG Installer: ${DMG_SIZE}"

echo "=========================================================="
echo " Benchmark Completed Successfully!"
echo "=========================================================="
