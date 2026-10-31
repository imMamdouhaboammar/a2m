#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${SCRIPT_DIR}"

CMD="${1:-list}"
LABEL="${2:-$(date +%s)}"

case "${CMD}" in
    save)
        STASH_MSG="agent-checkpoint:${LABEL}"
        git stash push -u -m "${STASH_MSG}" >/dev/null 2>&1 || true
        # Re-apply so working directory remains active but checkpoint is preserved
        git stash apply stash@{0} >/dev/null 2>&1 || true
        echo "[Checkpoint] Saved checkpoint: '${LABEL}'"
        ;;
    restore)
        # Find matching stash index
        STASH_IDX=$(git stash list | grep "agent-checkpoint:${LABEL}" | head -n 1 | cut -d: -f1 || true)
        if [ -z "${STASH_IDX}" ]; then
            echo "Error: Checkpoint '${LABEL}' not found in stash." >&2
            exit 1
        fi
        git reset --hard HEAD >/dev/null 2>&1
        git stash apply "${STASH_IDX}" >/dev/null 2>&1
        echo "[Checkpoint] Restored working directory to checkpoint: '${LABEL}'"
        ;;
    list)
        echo "=== Active Checkpoints ==="
        git stash list | grep "agent-checkpoint:" || echo "No active checkpoints."
        echo "=========================="
        ;;
    clean)
        # Drop agent checkpoints from stash
        while git stash list | grep -q "agent-checkpoint:"; do
            IDX=$(git stash list | grep "agent-checkpoint:" | head -n 1 | cut -d: -f1)
            git stash drop "${IDX}" >/dev/null 2>&1
        done
        echo "[Checkpoint] Cleaned all agent checkpoints."
        ;;
    *)
        echo "Usage: checkpoint.sh [save <label> | restore <label> | list | clean]" >&2
        exit 1
        ;;
esac
