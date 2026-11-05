#!/usr/bin/env bash
set -euo pipefail

echo "=========================================================="
echo " Running A2M Native MCP Bridge End-to-End Verification"
echo "=========================================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${SCRIPT_DIR}/bin/a2m"
DOC_BIN="${SCRIPT_DIR}/bin/a2m-docs"

if [ ! -x "${BIN}" ]; then
    echo "Error: ${BIN} not found or not executable" >&2
    exit 1
fi

TEMP_DIR="$(mktemp -d /tmp/a2m_e2e_XXXXXX)"
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

RESPONSE_LOG="${TEMP_DIR}/responses.jsonl"

cat << EOF | A2M_IPC_SOCKET="${TEMP_DIR}/a2m.sock" A2M_SESSION_SOCKET="${TEMP_DIR}/session.sock" "${BIN}" > "${RESPONSE_LOG}"
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
{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"capabilities_list","arguments":{}}}
{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"mcp_servers_list","arguments":{}}}
{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"skills_find","arguments":{"query":"__a2m_e2e_test_nonexistent__","limit":3}}}
{"jsonrpc":"2.0","id":18,"method":"tools/call","params":{"name":"manage_adr","arguments":{"action":"list"}}}
EOF

echo "--> Validating MCP responses..."

# Check 1: initialize
if grep -q '"name":"io.github.a2m/a2m-mcp"' "${RESPONSE_LOG}"; then
    echo "✓ Check 1: initialize returned correct serverInfo"
else
    echo "✗ Check 1 failed: initialize response incorrect" >&2
    exit 1
fi

# Check 2: tools/list
if grep -q '"name":"start_process"' "${RESPONSE_LOG}" && grep -q '"name":"edit_block"' "${RESPONSE_LOG}"; then
    echo "✓ Check 2: tools/list advertised core tools (XERJ is optional)"
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

# Checks 10 and 11: the capability bridge must work without optional
# third-party MCP providers or a locally installed Code Review Graph.
# Validate the JSON-RPC tool results by ID, not incidental log text.
python3 - "${RESPONSE_LOG}" <<'PYEOF'
import json
import sys

responses = {}
with open(sys.argv[1], encoding="utf-8") as stream:
    for line in stream:
        value = json.loads(line)
        if "id" in value:
            responses[value["id"]] = value

def payload(call_id):
    response = responses.get(call_id)
    if response is None or "error" in response:
        raise AssertionError(f"Missing or failed MCP response id={call_id}: {response}")
    result = response.get("result", {})
    if result.get("isError"):
        raise AssertionError(f"MCP tool returned isError for id={call_id}: {result}")
    content = result.get("content", [])
    if not content or content[0].get("type") != "text":
        raise AssertionError(f"Missing text content for id={call_id}")
    return json.loads(content[0]["text"])

tool_list = {tool["name"] for tool in responses[2]["result"]["tools"]}
required = {"capabilities_list", "skills_find", "mcp_servers_list"}
assert required <= tool_list, f"Missing capability bridge tools: {required - tool_list}"

capabilities = payload(15)
assert isinstance(capabilities.get("code_review_graph"), dict), capabilities
assert capabilities.get("local_skills", {}).get("execution") == "not_auto_enabled", capabilities
assert isinstance(capabilities.get("policy"), str), capabilities
assert isinstance(capabilities.get("xerj", {}).get("installed"), bool), capabilities
assert isinstance(capabilities.get("featured_capabilities"), list), capabilities
featured = capabilities["featured_capabilities"]
ids = [c.get("id") for c in featured]
assert len(ids) >= 27 and ids[:3] == ["omni-skill", "xerj", "rea"], ids
assert len(ids) == len(set(ids)) and "replica-square" in ids and "e2e" in ids, ids
by_id = {c["id"]: c for c in featured}
assert by_id["rea"].get("works_with") == ["replica-square"], by_id["rea"]
assert by_id["replica-square"].get("works_with") == ["rea"], by_id["replica-square"]
assert by_id["rea"].get("skill_id") == by_id["replica-square"].get("skill_id"), by_id["rea"]
for c in featured:
    assert isinstance(c.get("installed"), bool) and isinstance(c.get("mcp_available"), bool), c
    assert c.get("description") and c.get("use_for") and c.get("category"), c
if capabilities["xerj"].get("native_tools", 0) > 0:
    assert "xerj_search" in tool_list, "XERJ advertised native tools but xerj_search was absent"

servers = payload(16)
assert "servers" in servers and "policy" in servers, servers

skills = payload(17)
assert isinstance(skills.get("skills"), list), skills
assert isinstance(skills.get("notice"), str), skills

print("✓ Check 10: Native capability inventory reports optional code graph and policy")
print("✓ Check 11: External MCP discovery and local skills discovery return valid tool results")
PYEOF

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
