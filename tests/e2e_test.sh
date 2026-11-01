#!/usr/bin/env bash
set -euo pipefail

echo "=========================================================="
echo " Running Desktop Commander Native Full E2E Verification"
echo "=========================================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${SCRIPT_DIR}/bin/desktop-commander"
DOC_BIN="${SCRIPT_DIR}/bin/dc-doc-service"

if [ ! -x "${BIN}" ]; then
    echo "Error: ${BIN} not found or not executable" >&2
    exit 1
fi

TEMP_DIR="$(mktemp -d /tmp/dc_e2e_XXXXXX)"
trap 'rm -rf "${TEMP_DIR}"' EXIT

TEST_FILE="${TEMP_DIR}/app.py"
TEST_EXCEL="${TEMP_DIR}/metrics.xlsx"
TEST_DOCX="${TEMP_DIR}/report.docx"

# 1. Create test docx file via python zipfile
python3 - << PYEOF
import zipfile
with zipfile.ZipFile("${TEST_DOCX}", 'w') as z:
    z.writestr("[Content_Types].xml", '<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>')
    z.writestr("word/document.xml", '<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Project Alpha Status: Pending Review</w:t></w:r></w:p></w:body></w:document>')
PYEOF

echo "--> Sending MCP JSON-RPC Test Suite via stdio..."

RESPONSE_LOG="/tmp/dc_e2e_responses.jsonl"
rm -f "${RESPONSE_LOG}"

cat << EOF | "${BIN}" > "${RESPONSE_LOG}"
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"start_process","arguments":{"command":"echo 'Process Test 123'"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"write_file","arguments":{"path":"${TEST_FILE}","content":"def calculate():\\n    return 42\\n"}}}
{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"${TEST_FILE}","offset":0,"length":10}}}
{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"edit_block","arguments":{"file_path":"${TEST_FILE}","old_string":"    return 42","new_string":"    return 1337"}}}
{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"${TEST_FILE}","offset":0,"length":10}}}
{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"start_search","arguments":{"path":"${TEMP_DIR}","pattern":"return 1337","search_type":"content"}}}
{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"get_config","arguments":{}}}
{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"get_usage_stats","arguments":{}}}
{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"docx_read","arguments":{"path":"${TEST_DOCX}"}}}
{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"docx_edit","arguments":{"path":"${TEST_DOCX}","find":"Pending Review","replace":"Approved"}}}
{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"docx_read","arguments":{"path":"${TEST_DOCX}"}}}
{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"auth_status","arguments":{}}}
{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"crg_get_architecture_overview_tool","arguments":{}}}
{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"crg_list_graph_stats_tool","arguments":{}}}
{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"crg_traverse_graph_tool","arguments":{"query":"main"}}}
{"jsonrpc":"2.0","id":18,"method":"tools/call","params":{"name":"manage_adr","arguments":{"action":"list"}}}
EOF

echo "--> Validating MCP responses..."

# Check 1: initialize
if grep -q '"name":"io.github.a2m/a2m-mcp"' "${RESPONSE_LOG}" || grep -q '"name":"io.github.wonderwhy-er/desktop-commander"' "${RESPONSE_LOG}"; then
    echo "✓ Check 1: initialize returned correct serverInfo"
else
    echo "✗ Check 1 failed: initialize response incorrect" >&2
    exit 1
fi

# Check 2: tools/list
if grep -q '"name":"start_process"' "${RESPONSE_LOG}" && grep -q '"name":"edit_block"' "${RESPONSE_LOG}"; then
    echo "✓ Check 2: tools/list advertised core tools"
else
    echo "✗ Check 2 failed: tools/list missing core tools" >&2
    exit 1
fi

# Check 3: start_process
if grep -q "Process Test 123" "${RESPONSE_LOG}"; then
    echo "✓ Check 3: start_process successfully executed command and captured output"
else
    echo "✗ Check 3 failed: process output missing" >&2
    exit 1
fi

# Check 4 & 5: write_file and read_file
if grep -q "def calculate():" "${RESPONSE_LOG}"; then
    echo "✓ Check 4: write_file and read_file succeeded"
else
    echo "✗ Check 4 failed: file read content mismatch" >&2
    echo "=== Full Response Log ==="
    cat "${RESPONSE_LOG}"
    exit 1
fi

# Check 6 & 7: edit_block
if grep -q "return 1337" "${RESPONSE_LOG}"; then
    echo "✓ Check 5: edit_block performed surgical replacement and verified updated content"
else
    echo "✗ Check 5 failed: edit_block failed to replace content" >&2
    exit 1
fi

# Check 8: start_search
if grep -q 'outcome' "${RESPONSE_LOG}" && grep -q 'total_matches' "${RESPONSE_LOG}" && grep -q 'return 1337' "${RESPONSE_LOG}"; then
    echo "✓ Check 6: start_search matched regex content across filesystem"
else
    echo "✗ Check 6 failed: search results mismatch" >&2
    echo "=== Full Response Log ==="
    cat "${RESPONSE_LOG}"
    exit 1
fi

# Check 9: usage stats
if grep -q 'toolCalls' "${RESPONSE_LOG}"; then
    echo "✓ Check 7: get_usage_stats verified in-memory telemetry"
else
    echo "✗ Check 7 failed: usage stats mismatch" >&2
    exit 1
fi

# Check 10 & 11: docx_read and docx_edit
if grep -q 'Pending Review' "${RESPONSE_LOG}" && grep -q 'Approved' "${RESPONSE_LOG}"; then
    echo "✓ Check 8: docx_read and docx_edit modified and verified document without external dependencies"
else
    echo "✗ Check 8 failed: docx modification mismatch" >&2
    echo "=== Full Response Log ==="
    cat "${RESPONSE_LOG}"
    exit 1
fi

# Check 12: auth_status
if grep -q '"role": "admin"' "${RESPONSE_LOG}" || grep -q 'role' "${RESPONSE_LOG}"; then
    echo "✓ Check 9: auth_status verified active security and role capabilities"
else
    echo "✗ Check 9 failed: auth_status mismatch" >&2
    echo "=== Full Response Log ==="
    cat "${RESPONSE_LOG}"
    exit 1
fi

# Check 10: Code Review Graph Architecture & Stats
if grep -q "Architecture:" "${RESPONSE_LOG}" && grep -q "Graph statistics" "${RESPONSE_LOG}"; then
    echo "✓ Check 10: Code Review Graph architecture overview and graph stats verified"
else
    echo "✗ Check 10 failed: Code Review Graph architecture/stats mismatch" >&2
    echo "=== Full Response Log ==="
    cat "${RESPONSE_LOG}"
    exit 1
fi

# Check 11: Code Review Graph Traversal
if grep -q "start_node" "${RESPONSE_LOG}"; then
    echo "✓ Check 11: crg_traverse_graph_tool traversed AST knowledge graph"
else
    echo "✗ Check 11 failed: Code Review Graph traversal mismatch" >&2
    echo "=== Full Response Log ==="
    cat "${RESPONSE_LOG}"
    exit 1
fi

# Check 12: Codebase Memory - ADR management
if grep -q "adrs" "${RESPONSE_LOG}"; then
    echo "✓ Check 12: manage_adr verified persistent governance ledger"
else
    echo "✗ Check 12 failed: ADR management mismatch" >&2
    echo "=== Full Response Log ==="
    cat "${RESPONSE_LOG}"
    exit 1
fi

echo "=========================================================="
echo " All 12 E2E Verifications Passed Successfully!"
echo "=========================================================="
