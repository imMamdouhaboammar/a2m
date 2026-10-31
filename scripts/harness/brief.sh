#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
AGENT_DIR="${SCRIPT_DIR}/.agent"
BRIEF_FILE="${AGENT_DIR}/brief.json"

mkdir -p "${AGENT_DIR}"

CMD="${1:-status}"
shift || true
export HARNESS_CMD="${CMD}"

python3 - "$@" << 'PYEOF'
import json
import os
import sys
import time

script_dir = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
agent_dir = os.path.join(script_dir, ".agent")
brief_file = os.path.join(agent_dir, "brief.json")

os.makedirs(agent_dir, exist_ok=True)
cmd = os.environ.get("HARNESS_CMD", "status")
args = sys.argv[1:]

def load_brief():
    if os.path.exists(brief_file):
        try:
            with open(brief_file, "r") as f:
                return json.load(f)
        except Exception:
            pass
    return {
        "title": "Untitled Task",
        "created_at": int(time.time()),
        "status": "in_progress",
        "outcome": "",
        "facts": [],
        "hypotheses": [],
        "next_action": "",
        "completion_evidence": ""
    }

def save_brief(data):
    with open(brief_file, "w") as f:
        json.dump(data, f, indent=2)

b = load_brief()

if cmd == "init":
    title = " ".join(args) if args else "New Task"
    b = {
        "title": title,
        "created_at": int(time.time()),
        "status": "in_progress",
        "outcome": "",
        "facts": [],
        "hypotheses": [],
        "next_action": "",
        "completion_evidence": ""
    }
    save_brief(b)
    print(f"[GTD Brief] Initialized task: '{title}'")

elif cmd == "set-outcome":
    outcome = " ".join(args)
    b["outcome"] = outcome
    save_brief(b)
    print(f"[GTD Brief] Outcome defined: {outcome}")

elif cmd == "add-fact":
    fact = " ".join(args)
    if fact:
        b["facts"].append(fact)
        save_brief(b)
        print(f"[GTD Brief] Fact recorded: {fact}")

elif cmd == "add-hypothesis":
    hypo = " ".join(args)
    if hypo:
        b["hypotheses"].append(hypo)
        save_brief(b)
        print(f"[Deep Digger] Hypothesis added: {hypo}")

elif cmd == "next":
    action = " ".join(args)
    b["next_action"] = action
    save_brief(b)
    print(f"[GTD Brief] Next single action: {action}")

elif cmd == "close":
    evidence = " ".join(args)
    b["status"] = "completed"
    b["completed_at"] = int(time.time())
    b["completion_evidence"] = evidence
    save_brief(b)
    print(f"[GTD Brief] Task closed with evidence: {evidence}")

elif cmd == "status":
    print("=" * 60)
    print(f" TASK BRIEF: {b.get('title')} [{b.get('status').upper()}]")
    print("=" * 60)
    print(f" Target Outcome: {b.get('outcome') or 'Not defined'}")
    print(f" Next Action:    {b.get('next_action') or 'None'}")
    if b.get("facts"):
        print(" Verified Facts:")
        for f in b["facts"]:
            print(f"   • {f}")
    if b.get("hypotheses"):
        print(" Active Hypotheses (Deep Digger):")
        for h in b["hypotheses"]:
            print(f"   ? {h}")
    if b.get("completion_evidence"):
        print(f" Evidence:       {b.get('completion_evidence')}")
    print("=" * 60)

else:
    print(f"Unknown brief command: {cmd}", file=sys.stderr)
    print("Usage: brief.sh [init|set-outcome|add-fact|add-hypothesis|next|close|status] [arguments...]", file=sys.stderr)
    sys.exit(1)
PYEOF
