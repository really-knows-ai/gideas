#!/bin/bash
# Watch a Workitem CRD with rich formatting and live haiku display.
# Usage: bash watch.sh <workitem-name> [namespace]
# Requires: kubectl, jq, grpcurl
#
# Starts a port-forward to the Archivist to fetch the haiku artefact
# after each forge/refine iteration. Displays phase transitions,
# routing decisions, per-node visit counters, and elapsed time.

set -uo pipefail

NAME="${1:-}"
NS="${2:-default}"
ARCHIVIST_PORT="${3:-50054}"

if [ -z "$NAME" ]; then
    echo "Usage: $0 <workitem-name> [namespace] [archivist-port]"
    exit 1
fi

# --- ANSI escape codes ---
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"
RED="\033[31m"
GREEN="\033[32m"
YELLOW="\033[33m"
MAGENTA="\033[35m"
CYAN="\033[36m"
GRAY="\033[90m"

cleanup() {
    [ -n "${PF_PID:-}" ] && kill "$PF_PID" 2>/dev/null
    exit
}
trap cleanup EXIT INT TERM

style_phase() {
    case "$1" in
        Pending)    echo -ne "${DIM}● Pending${RESET}" ;;
        Running)    echo -ne "${YELLOW}▶ Running${RESET}" ;;
        Routing)    echo -ne "${CYAN}→ Routing${RESET}" ;;
        Suspended)  echo -ne "${MAGENTA}⏸ Suspended${RESET}" ;;
        Completed)  echo -ne "${GREEN}★ Completed${RESET}" ;;
        Failed)     echo -ne "${RED}✗ Failed${RESET}" ;;
        *)          echo -ne "${1}" ;;
    esac
}

plural() {
    [ "$1" -eq 1 ] && echo "" || echo "s"
}

# Fetch the current haiku text from the Archivist.
# Returns empty string on failure (Archivist not ready, etc.)
fetch_haiku() {
    local raw
    raw=$(grpcurl -plaintext \
        -d "{\"workitem_id\":\"${NAME}\",\"artefact_id\":\"haiku\"}" \
        "localhost:${ARCHIVIST_PORT}" \
        flow.v1.ArchivistService/GetArtefact 2>/dev/null) || return 1

    echo "$raw" | jq -r '.content | @base64d' 2>/dev/null
}

# Display the haiku in a framed box
display_haiku() {
    local text="$1"
    [ -z "$text" ] && return

    local max=0
    while IFS= read -r line; do
        [ ${#line} -gt $max ] && max=${#line}
    done <<< "$text"

    local width=$((max + 4))
    [ "$width" -gt 60 ] && width=60

    local top="${BOLD}${CYAN}┌$(printf '─%.0s' $(seq 1 $width))┐${RESET}"
    local bot="${BOLD}${CYAN}└$(printf '─%.0s' $(seq 1 $width))┘${RESET}"

    echo ""
    echo -e "$top"
    while IFS= read -r line; do
        local pad=$((width - ${#line} - 2))
        [ "$pad" -lt 0 ] && pad=0
        printf "${BOLD}${CYAN}│${RESET} ${BOLD}${line}${RESET}%*s${BOLD}${CYAN}│${RESET}\n" "$pad" ""
    done <<< "$text"
    echo -e "$bot"
    echo ""
}

# Required stamp vocabulary
stamp_vocab() {
    kubectl get governedartefact haiku -n "$NS" -o json 2>/dev/null \
        | jq -r '.spec.stamps // [] | join(", ")' 2>/dev/null || echo "n/a"
}

# Start port-forward to Archivist in background (only if not already reachable)
if grpcurl -plaintext "localhost:${ARCHIVIST_PORT}" list flow.v1.ArchivistService &>/dev/null; then
    echo -e "${DIM}Archivist already reachable on :${ARCHIVIST_PORT}${RESET}"
else
    echo -ne "${DIM}Starting port-forward to flow-archivist...${RESET} "
    kubectl port-forward "svc/flow-archivist" "${ARCHIVIST_PORT}:50054" -n "$NS" &>/dev/null &
    PF_PID=$!
    sleep 2
    if kill -0 "$PF_PID" 2>/dev/null; then
        echo -e "${GREEN}ready${RESET}"
    else
        echo -e "${RED}failed${RESET}"
    fi
fi

# Header
NOW=$(date '+%H:%M:%S')
STAMPS=$(stamp_vocab)
echo -e "${BOLD}${GREEN}╭──────────────────────────────────────────────────────────────────────────────╮${RESET}"
echo -e "${BOLD}${GREEN}│${RESET} ${BOLD}Haiku Watch${RESET}  ${DIM}${NAME}${RESET}  ${DIM}namespace: ${NS}${RESET}  ${DIM}started: ${NOW}${RESET}"
echo -e "${BOLD}${GREEN}│${RESET} ${DIM}Required stamps: [${DIM}${STAMPS}${DIM}]${RESET}"
echo -e "${BOLD}${GREEN}╰──────────────────────────────────────────────────────────────────────────────╯${RESET}"
echo ""

ELAPSED_START=$(date +%s)
LAST_PHASE=""
LAST_NODE=""
HAIKU_SHOWN_FOR=""

thrash_summary() {
    echo "$1" | jq -r '
        (.status.thrashCounters // {}) | to_entries
        | sort_by(.value) | reverse
        | map("\(.key):\(.value)")
        | join("  ")
    ' 2>/dev/null
}

# Show current state before entering watch loop
CURRENT=$(kubectl get workitem "$NAME" -n "$NS" -o json --ignore-not-found 2>/dev/null)
if [ -n "$CURRENT" ]; then
    CUR_PHASE=$(echo "$CURRENT" | jq -r '.status.phase // ""' 2>/dev/null)
    CUR_NODE=$(echo "$CURRENT" | jq -r '.status.currentAssignee // ""' 2>/dev/null)
    CUR_VISITS=$(echo "$CURRENT" | jq -r '[.status.thrashCounters // {} | to_entries | .[].value] | add // 0' 2>/dev/null)
    CUR_THRASH=$(thrash_summary "$CURRENT")
    echo -e "${DIM}Current state:${RESET}"

    case "$CUR_PHASE" in
        Running)
            echo -e "  $(style_phase "$CUR_PHASE")  on ${BOLD}${CUR_NODE}${RESET}"
            [ -n "$CUR_THRASH" ] && echo -e "  ${DIM}visits: ${RESET}${CUR_THRASH}"
            ;;
        Completed)
            echo -e "  $(style_phase "$CUR_PHASE")  ${CUR_VISITS} visit$(plural "$CUR_VISITS")"
            [ -n "$CUR_THRASH" ] && echo -e "  ${DIM}visits: ${RESET}${CUR_THRASH}"
            sleep 1
            HAIKU=$(fetch_haiku)
            if [ -n "$HAIKU" ]; then
                display_haiku "$HAIKU"
            fi
            echo -e "${GREEN}${BOLD}╭─── Flow already complete ───╮${RESET}"
            echo -e "${GREEN}${BOLD}╰─────────────────────────────╯${RESET}"
            exit 0
            ;;
        Failed)
            CUR_FAILURE=$(echo "$CURRENT" | jq -r '.status.failureReason // ""' 2>/dev/null)
            echo -e "  $(style_phase "$CUR_PHASE")  ${CUR_FAILURE}"
            [ -n "$CUR_THRASH" ] && echo -e "  ${DIM}visits: ${RESET}${CUR_THRASH}"
            exit 1
            ;;
        *)
            echo -e "  $(style_phase "$CUR_PHASE")  ${CUR_NODE}"
            ;;
    esac
    echo ""
    LAST_PHASE="$CUR_PHASE"
    LAST_NODE="$CUR_NODE"
else
    echo -e "${DIM}Workitem not yet created, waiting...${RESET}"
    echo ""
fi

watch_workitem() {
    kubectl get workitem "$NAME" -n "$NS" -w -o json --ignore-not-found 2>/dev/null
}

# Main loop — reconnect on disconnect
while true; do
    watch_workitem | while read -r line; do
        PHASE=$(echo "$line" | jq -r '.status.phase // ""' 2>/dev/null)
        [ -z "$PHASE" ] && continue

        NODE=$(echo "$line" | jq -r '.status.currentAssignee // ""' 2>/dev/null)
        ROUTE_TARGET=$(echo "$line" | jq -r '.status.routingInstruction.target // ""' 2>/dev/null)
        ROUTE_TYPE=$(echo "$line" | jq -r '.status.routingInstruction.type // ""' 2>/dev/null)
        FAILURE=$(echo "$line" | jq -r '.status.failureReason // ""' 2>/dev/null)
        SUSPENDED=$(echo "$line" | jq -r '.status.suspendedAt // ""' 2>/dev/null)
        RESUME_COND=$(echo "$line" | jq -r '.status.resumeCondition // ""' 2>/dev/null)
        THRASH=$(thrash_summary "$line")

        TOTAL_VISITS=$(echo "$line" | jq -r '
            [.status.thrashCounters // {} | to_entries | .[].value] | add // 0
        ' 2>/dev/null)

        ELAPSED=$(( $(date +%s) - ELAPSED_START ))
        ELAPSED_STR=$(printf "%dm%02ds" $((ELAPSED/60)) $((ELAPSED%60)))
        TS="[${GRAY}${ELAPSED_STR}${RESET}]"

        # Skip duplicate events
        if [ "$PHASE" = "$LAST_PHASE" ] && [ "$NODE" = "$LAST_NODE" ]; then
            continue
        fi

        # Show haiku after forge or refine finishes
        if [[ "$LAST_NODE" =~ ^(forge|refine)$ ]] && [ "$PHASE" != "Running" ]; then
            if [ "$LAST_NODE" != "$HAIKU_SHOWN_FOR" ]; then
                sleep 1  # let Archivist settle
                HAIKU=$(fetch_haiku)
                if [ -n "$HAIKU" ]; then
                    display_haiku "$HAIKU"
                    HAIKU_SHOWN_FOR="$LAST_NODE"
                fi
            fi
        fi
        # Reset haiku tracker when forge/refine runs again
        if [ "$NODE" = "forge" ] || [ "$NODE" = "refine" ]; then
            HAIKU_SHOWN_FOR=""
        fi

        case "$PHASE" in
            Running)
                echo -e "${TS} $(style_phase "$PHASE")  on ${BOLD}${NODE}${RESET}"
                [ -n "$THRASH" ] && echo -e "${TS}  ${DIM}visits: ${RESET}${THRASH}"
                ;;
            Routing)
                ROUTE_DETAIL=""
                [ -n "$ROUTE_TARGET" ] && ROUTE_DETAIL="  ${CYAN}→ ${ROUTE_TARGET}${RESET}"
                [ -n "$ROUTE_TYPE" ] && ROUTE_DETAIL="${ROUTE_DETAIL}  ${DIM}(${ROUTE_TYPE})${RESET}"
                echo -e "${TS} $(style_phase "$PHASE")${ROUTE_DETAIL}"
                ;;
            Completed)
                echo -e "${TS} $(style_phase "$PHASE")  ${TOTAL_VISITS} visit$(plural "$TOTAL_VISITS")"
                [ -n "$THRASH" ] && echo -e "${TS}  ${DIM}final visits: ${RESET}${THRASH}"
                echo -e "\n${GREEN}${BOLD}╭─── Flow complete ───╮${RESET}"
                echo -e "${GREEN}${BOLD}╰─────────────────────╯${RESET}"
                exit 0
                ;;
            Suspended)
                echo -e "${TS} $(style_phase "$PHASE")"
                [ -n "$RESUME_COND" ] && echo -e "${TS}  ${DIM}resume when: ${RESET}${RESUME_COND}"
                [ -n "$SUSPENDED" ] && echo -e "${TS}  ${DIM}suspended at: ${RESET}${SUSPENDED}"
                ;;
            Failed)
                echo -e "${TS} $(style_phase "$PHASE")"
                [ -n "$FAILURE" ] && echo -e "${TS}  ${RED}reason: ${FAILURE}${RESET}"
                [ -n "$THRASH" ] && echo -e "${TS}  ${DIM}visits: ${RESET}${THRASH}"
                exit 1
                ;;
            *)
                echo -e "${TS} $(style_phase "$PHASE")  ${NODE}"
                ;;
        esac

        LAST_PHASE="$PHASE"
        LAST_NODE="$NODE"
    done

    echo -e "\n${YELLOW}⚠ watch disconnected, reconnecting...${RESET}"
    sleep 2
done
