#!/bin/bash

# k8s-healer management script
# This script manages the k8s-healer daemon for test environments

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KUBECONFIG="${SCRIPT_DIR}/.kubeconfigs/test-config"
HEALER_BINARY="${SCRIPT_DIR}/k8s-healer"
PID_FILE="${SCRIPT_DIR}/.k8s-healer.pid"
LOG_FILE="${SCRIPT_DIR}/.k8s-healer.log"

# Source kubeconfig location (can be overridden via --source-kubeconfig flag)
SOURCE_KUBECONFIG="${HOME}/Developer/Projects/kubevirt-ui/.kubeconfigs/test-config"

# CRD resources to monitor
CRD_RESOURCES="virtualmachines.kubevirt.io/v1,virtualmachineinstances.kubevirt.io/v1,datavolumes.cdi.kubevirt.io"

# Namespaces to watch (empty = all namespaces)
NAMESPACES="default"

# Foreground mode flag
FOREGROUND="false"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if binary exists
if [ ! -f "$HEALER_BINARY" ]; then
    echo -e "${RED}Error: k8s-healer binary not found at $HEALER_BINARY${NC}"
    echo "Build with: make build  (or: go build -o k8s-healer ./cmd/main.go)"
    exit 1
fi

# Check if kubeconfig exists, offer to copy from source if missing
check_and_copy_kubeconfig() {
    if [ ! -f "$KUBECONFIG" ]; then
        if [ -f "$SOURCE_KUBECONFIG" ]; then
            echo -e "${YELLOW}Kubeconfig not found at $KUBECONFIG${NC}"
            echo -e "${YELLOW}Copying from source: $SOURCE_KUBECONFIG${NC}"
            mkdir -p "$(dirname "$KUBECONFIG")"
            cp "$SOURCE_KUBECONFIG" "$KUBECONFIG"
            echo -e "${GREEN}✓ Kubeconfig copied successfully${NC}"
        else
            echo -e "${RED}Error: Kubeconfig not found at $KUBECONFIG${NC}"
            echo -e "${YELLOW}Expected source: $SOURCE_KUBECONFIG${NC}"
            echo "Please copy your kubeconfig file to: $KUBECONFIG"
            echo "Or specify a different source with: --source-kubeconfig /path/to/source"
            exit 1
        fi
    fi
}

# Function to start the daemon
start() {
    check_and_copy_kubeconfig

    # Check if foreground mode is requested
    if [ "$FOREGROUND" = "true" ]; then
        echo -e "${GREEN}Starting k8s-healer in foreground mode...${NC}"
        echo "  Kubeconfig: $KUBECONFIG"
        echo "  Namespaces: ${NAMESPACES:-all}"
        echo "  CRD Resources: $CRD_RESOURCES"
        echo "  Press Ctrl+C to stop"
        echo ""
        
    "$HEALER_BINARY" \
        -k "$KUBECONFIG" \
        --crd-resources "$CRD_RESOURCES" \
        --stale-age 6m \
        ${NAMESPACES:+-n "$NAMESPACES"}
        return $?
    fi

    # Daemon mode
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            echo -e "${YELLOW}k8s-healer is already running (PID: $PID)${NC}"
            return 0
        else
            echo -e "${YELLOW}Removing stale PID file${NC}"
            rm -f "$PID_FILE"
        fi
    fi

    echo -e "${GREEN}Starting k8s-healer daemon...${NC}"
    echo "  Kubeconfig: $KUBECONFIG"
    echo "  Namespaces: ${NAMESPACES:-all}"
    echo "  CRD Resources: $CRD_RESOURCES"
    echo "  Log file: $LOG_FILE"
    echo "  PID file: $PID_FILE"
    echo ""

    "$HEALER_BINARY" start \
        -k "$KUBECONFIG" \
        --crd-resources "$CRD_RESOURCES" \
        --stale-age 6m \
        --pid-file "$PID_FILE" \
        --log-file "$LOG_FILE" \
        ${NAMESPACES:+-n "$NAMESPACES"}

    sleep 1

    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if ps -p "$PID" > /dev/null 2>&1; then
            echo -e "${GREEN}✓ k8s-healer started successfully (PID: $PID)${NC}"
            echo -e "${GREEN}  View logs with: tail -f $LOG_FILE${NC}"
            echo -e "${GREEN}  Stop with: $0 stop${NC}"
        else
            echo -e "${RED}✗ Failed to start k8s-healer${NC}"
            echo "  Check logs: $LOG_FILE"
            exit 1
        fi
    else
        echo -e "${RED}✗ Failed to start k8s-healer (PID file not created)${NC}"
        exit 1
    fi
}

# Function to stop the daemon
stop() {
    if [ ! -f "$PID_FILE" ]; then
        echo -e "${YELLOW}k8s-healer is not running${NC}"
        return 0
    fi

    PID=$(cat "$PID_FILE")
    if ! ps -p "$PID" > /dev/null 2>&1; then
        echo -e "${YELLOW}k8s-healer is not running (stale PID file)${NC}"
        rm -f "$PID_FILE"
        return 0
    fi

    echo -e "${GREEN}Stopping k8s-healer daemon (PID: $PID)...${NC}"
    "$HEALER_BINARY" stop --pid-file "$PID_FILE"

    # Wait a bit and check if it's still running
    sleep 2
    if ps -p "$PID" > /dev/null 2>&1; then
        echo -e "${YELLOW}Process still running, force killing...${NC}"
        kill -9 "$PID" 2>/dev/null || true
    fi

    rm -f "$PID_FILE"
    echo -e "${GREEN}✓ k8s-healer stopped${NC}"
}

# Function to check status
status() {
    if [ ! -f "$PID_FILE" ]; then
        echo -e "${RED}k8s-healer is not running${NC}"
        return 1
    fi

    PID=$(cat "$PID_FILE")
    if ps -p "$PID" > /dev/null 2>&1; then
        echo -e "${GREEN}k8s-healer is running (PID: $PID)${NC}"
        echo "  Log file: $LOG_FILE"
        echo "  PID file: $PID_FILE"
        return 0
    else
        echo -e "${RED}k8s-healer is not running (stale PID file)${NC}"
        rm -f "$PID_FILE"
        return 1
    fi
}

# Function to show logs
logs() {
    if [ ! -f "$LOG_FILE" ]; then
        echo -e "${YELLOW}Log file not found: $LOG_FILE${NC}"
        return 1
    fi

    if [ "$1" == "-f" ] || [ "$1" == "--follow" ]; then
        tail -f "$LOG_FILE"
    else
        tail -n 50 "$LOG_FILE"
    fi
}

# Function to restart the daemon
restart() {
    stop
    sleep 1
    start
}

# Show help message
show_help() {
    echo "Usage: $0 [OPTIONS] {start|stop|restart|status|logs}"
    echo ""
    echo "Commands:"
    echo "  start    - Start k8s-healer (daemon mode by default, use --foreground for terminal output)"
    echo "  stop     - Stop the running daemon"
    echo "  restart  - Restart the daemon"
    echo "  status   - Check if the daemon is running"
    echo "  logs     - Show last 50 lines of logs (use 'logs -f' to follow)"
    echo ""
    echo "Options:"
    echo "  --source-kubeconfig PATH  Source kubeconfig to copy from if target doesn't exist"
    echo "                          (default: ${HOME}/Developer/Projects/kubevirt-ui/.kubeconfigs/test-config)"
    echo "  --foreground, -f        Run in foreground mode (output to terminal, Ctrl+C to stop)"
    echo "  --help, -h               Show this help message"
    echo ""
    echo "Configuration:"
    echo "  Kubeconfig: $KUBECONFIG"
    echo "  Source Kubeconfig: $SOURCE_KUBECONFIG"
    echo "  Namespaces: ${NAMESPACES:-all}"
    echo "  CRD Resources: $CRD_RESOURCES"
    echo "  Stale Age: 6 minutes"
    echo ""
    echo "Examples:"
    echo "  $0 start                    # Start as daemon (background)"
    echo "  $0 start --foreground      # Start in foreground (terminal output)"
    echo "  $0 stop                    # Stop daemon"
    echo "  $0 status                  # Check daemon status"
    echo "  $0 logs -f                 # Follow logs in real-time"
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --source-kubeconfig)
            if [ -z "$2" ]; then
                echo -e "${RED}Error: --source-kubeconfig requires a path${NC}" >&2
                show_help
                exit 1
            fi
            SOURCE_KUBECONFIG="$2"
            shift 2
            ;;
        --foreground|-f)
            FOREGROUND="true"
            shift
            ;;
        --help|-h)
            show_help
            exit 0
            ;;
        --)
            shift
            break
            ;;
        -*)
            echo -e "${RED}Unknown option: $1${NC}" >&2
            show_help
            exit 1
            ;;
        *)
            # Not an option, likely the command - break and let case handle it
            break
            ;;
    esac
done

# Main command handling
case "${1:-}" in
    start)
        start
        ;;
    stop)
        stop
        ;;
    restart)
        restart
        ;;
    status)
        status
        ;;
    logs)
        shift
        logs "$@"
        ;;
    "")
        show_help
        exit 1
        ;;
    *)
        echo -e "${RED}Unknown command: $1${NC}" >&2
        show_help
        exit 1
        ;;
esac
