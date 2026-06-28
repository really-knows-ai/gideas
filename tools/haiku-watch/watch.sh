#!/bin/bash
# Watch a Workitem CRD through the Kubernetes API.
# Usage: bash watch.sh <workitem-name> [namespace]
# Requires: kubectl, jq

set -euo pipefail

NAME="${1:-}"
NS="${2:-default}"

if [ -z "$NAME" ]; then
    echo "Usage: $0 <workitem-name> [namespace]"
    exit 1
fi

echo "=== Watching workitem: $NS/$NAME ==="
echo ""

kubectl get workitem "$NAME" -n "$NS" -w -o json | jq --unbuffered '
  select(.status != null)
  | .status
  | {
      phase: .phase,
      node: .currentAssignee,
      visits: (.visitCount // 0),
      message: (.message // null)
    }
  | "[\(.phase)] \(.node // "idle") (visits: \(.visits))" +
    (if .message then " - \(.message)" else "" end)
'
