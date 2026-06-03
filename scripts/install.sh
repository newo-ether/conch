#!/usr/bin/env bash
set -euo pipefail

# --- Defaults ---
DEFAULT_PORT=14216
DEFAULT_HOST="0.0.0.0"
DEFAULT_TIMEOUT=30
DEFAULT_MAX_TIMEOUT=120

# --- Platform detection ---
if [ -d /data/data/com.termux/files/usr ] && [ -n "${PREFIX:-}" ]; then
    PLATFORM="termux"
else
    PLATFORM="linux"
fi

INSTALL_PREFIX=""

if [ "$PLATFORM" = "termux" ]; then
    BIN_DIR="${PREFIX}/bin"
    CFG_DIR="${PREFIX}/etc/conch"
    SVC_DIR="${PREFIX}/var/service/conch"
    BOOT_DIR="${HOME}/.termux/boot"
else
    BIN_DIR="/usr/local/bin"
    CFG_DIR="/etc/conch"
fi

# --- Colors ---
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[+]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
die()  { echo -e "${RED}[x]${NC} $*" >&2; exit 1; }

# --- Usage ---
usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Install Conch as a system service.

Options:
  --api-key KEY        Pre-shared API key (default: auto-generate)
  --port PORT          Listen port (default: ${DEFAULT_PORT})
  --host ADDR          Listen address (default: ${DEFAULT_HOST})
  --timeout SEC        Default command timeout seconds (default: ${DEFAULT_TIMEOUT})
  --max-timeout SEC    Max command timeout seconds (default: ${DEFAULT_MAX_TIMEOUT})
  --no-auth            Disable authentication (insecure, dev only)
  --prefix DIR         Install root directory (default: platform-specific)
  --bin PATH           Use pre-built binary, skip Go build
  --mcp-bin PATH       Use pre-built MCP binary, skip Go build for MCP
  --no-start           Install but don't start the service
  --uninstall          Remove service and binary
  -y, --yes            Skip all prompts, use defaults
  -h, --help           Show this help

Examples:
  # Full auto (random key, default port, build from source):
  sudo $0

  # Custom install location:
  sudo $0 --prefix /opt/conch

  # Custom port and key:
  sudo $0 --port 9000 --api-key "\$(head -c 32 /dev/urandom | base64)"

  # Termux (no root needed):
  $0 --port 8080

  # Uninstall:
  sudo $0 --uninstall
EOF
    exit 0
}

# --- Parse args ---
API_KEY=""
PORT="${DEFAULT_PORT}"
HOST="${DEFAULT_HOST}"
TIMEOUT="${DEFAULT_TIMEOUT}"
MAX_TIMEOUT="${DEFAULT_MAX_TIMEOUT}"
ALLOW_NO_AUTH="false"
SRC_BIN=""
SRC_MCP=""
NO_START=false
DO_UNINSTALL=false
YES_ALL=false

while [ $# -gt 0 ]; do
    case "$1" in
        --api-key)       API_KEY="$2"; shift 2 ;;
        --port)          PORT="$2"; shift 2 ;;
        --host)          HOST="$2"; shift 2 ;;
        --timeout)       TIMEOUT="$2"; shift 2 ;;
        --max-timeout)   MAX_TIMEOUT="$2"; shift 2 ;;
        --no-auth)       ALLOW_NO_AUTH="true"; shift ;;
        --prefix)        INSTALL_PREFIX="$2"; shift 2 ;;
        --bin)           SRC_BIN="$2"; shift 2 ;;
        --mcp-bin)       SRC_MCP="$2"; shift 2 ;;
        --no-start)      NO_START=true; shift ;;
        --uninstall)     DO_UNINSTALL=true; shift ;;
        -y|--yes)        YES_ALL=true; shift ;;
        -h|--help)       usage ;;
        *) die "Unknown flag: $1 (use --help)" ;;
    esac
done

# --- Apply custom prefix ---
if [ -n "${INSTALL_PREFIX}" ]; then
    BIN_DIR="${INSTALL_PREFIX}"
    CFG_DIR="${INSTALL_PREFIX}"
    if [ "$PLATFORM" = "termux" ]; then
        SVC_DIR="${INSTALL_PREFIX}/service"
    fi
fi

BIN_PATH="${BIN_DIR}/conch"
MCP_BIN_PATH="${BIN_DIR}/conch-mcp"
ENV_FILE="${CFG_DIR}/env"

# --- Uninstall ---
if $DO_UNINSTALL; then
    log "Uninstalling Conch..."

    if [ "$PLATFORM" = "linux" ] && [ "$(id -u)" -ne 0 ]; then
        die "uninstall requires root: sudo $0 --uninstall"
    fi

    if [ "$PLATFORM" = "linux" ] && [ -f /etc/systemd/system/conch.service ]; then
        systemctl stop conch 2>/dev/null || true
        systemctl disable conch 2>/dev/null || true
        rm -f /etc/systemd/system/conch.service
        systemctl daemon-reload
        log "Removed systemd service"
    fi

    if [ "$PLATFORM" = "termux" ]; then
        sv stop conch 2>/dev/null || true
        rm -rf "${SVC_DIR}" 2>/dev/null || true
        rm -f "${BOOT_DIR}/01-conch" 2>/dev/null || true
        log "Removed runit service"
    fi

    rm -f "${BIN_PATH}"
    log "Removed binary: ${BIN_PATH}"
    rm -f "${MCP_BIN_PATH}"
    rm -rf "${CFG_DIR}"
    log "Removed config: ${CFG_DIR}"
    log "Uninstall complete."
    exit 0
fi

# --- Root check (Linux only) ---
if [ "$PLATFORM" = "linux" ] && [ "$(id -u)" -ne 0 ]; then
    die "Root privileges required. Run with: sudo $0"
fi

# --- Locate or build binary ---
REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GITHUB_RELEASES="https://github.com/newo-ether/conch/releases/latest/download"

# Detect platform arch for prebuilt binary download
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)   echo "amd64" ;;
        aarch64|arm64)  echo "arm64" ;;
        *)              echo "unknown" ;;
    esac
}

# Download from GitHub Releases
download_binary() {
    local name="$1" dest="$2"
    local url="${GITHUB_RELEASES}/${name}"
    log "Downloading ${name}..."
    if command -v curl &>/dev/null; then
        curl -fsSL --connect-timeout 10 --max-time 120 -o "${dest}" "${url}" || return 1
    elif command -v wget &>/dev/null; then
        wget -q --timeout=120 -O "${dest}" "${url}" || return 1
    else
        return 1
    fi
    chmod 755 "${dest}"
}

ARCH=$(detect_arch)
if [ "$PLATFORM" = "windows" ]; then
    SERVER_BIN_NAME="conch-windows-amd64.exe"
    MCP_BIN_NAME="conch-mcp-windows-amd64.exe"
elif [ "$PLATFORM" = "termux" ] || [ "$ARCH" = "arm64" ]; then
    SERVER_BIN_NAME="conch-linux-arm64"
    MCP_BIN_NAME="conch-mcp-linux-arm64"
else
    SERVER_BIN_NAME="conch-linux-amd64"
    MCP_BIN_NAME="conch-mcp-linux-amd64"
fi

if [ -n "${SRC_BIN}" ]; then
    if [ ! -f "${SRC_BIN}" ]; then
        die "binary not found: ${SRC_BIN}"
    fi
    log "Using provided binary: ${SRC_BIN}"
elif download_binary "${SERVER_BIN_NAME}" "${REPO_DIR}/conch"; then
    SRC_BIN="${REPO_DIR}/conch"
elif command -v go &>/dev/null && [ -f "${REPO_DIR}/go.mod" ]; then
    log "Download failed. Building from source..."
    cd "${REPO_DIR}"
    go build -o conch .
    SRC_BIN="${REPO_DIR}/conch"
elif [ -f "${REPO_DIR}/conch" ]; then
    SRC_BIN="${REPO_DIR}/conch"
elif command -v conch &>/dev/null; then
    SRC_BIN="$(command -v conch)"
else
    die "Failed to download or build binary. Use --bin to specify a pre-built binary path."
fi

# --- Install binary ---
mkdir -p "${BIN_DIR}"
cp "${SRC_BIN}" "${BIN_PATH}"
chmod 755 "${BIN_PATH}"
log "Installed: ${BIN_PATH}"

# --- Locate or build MCP binary ---
if [ -n "${SRC_MCP}" ]; then
    if [ ! -f "${SRC_MCP}" ]; then
        warn "MCP binary not found: ${SRC_MCP}. Skipping conch-mcp."
        SRC_MCP=""
    else
        log "Using provided MCP binary: ${SRC_MCP}"
    fi
elif download_binary "${MCP_BIN_NAME}" "${REPO_DIR}/conch-mcp"; then
    SRC_MCP="${REPO_DIR}/conch-mcp"
elif command -v go &>/dev/null && [ -f "${REPO_DIR}/go.mod" ]; then
    log "Building conch-mcp from source..."
    cd "${REPO_DIR}"
    if go build -o conch-mcp ./cmd/mcp; then
        SRC_MCP="${REPO_DIR}/conch-mcp"
    else
        warn "Failed to build conch-mcp. Skipping MCP bridge install."
    fi
elif [ -f "${REPO_DIR}/conch-mcp" ]; then
    SRC_MCP="${REPO_DIR}/conch-mcp"
    log "Using prebuilt conch-mcp"
elif command -v conch-mcp &>/dev/null; then
    SRC_MCP="$(command -v conch-mcp)"
    log "Using conch-mcp from PATH: ${SRC_MCP}"
else
    warn "No conch-mcp binary found. Skipping MCP bridge install."
fi

if [ -n "${SRC_MCP}" ]; then
    cp "${SRC_MCP}" "${MCP_BIN_PATH}"
    chmod 755 "${MCP_BIN_PATH}"
    log "Installed: ${MCP_BIN_PATH}"
fi

# --- API Key ---
mkdir -p "${CFG_DIR}"

if [ -z "${API_KEY}" ]; then
    if [ -f "${ENV_FILE}" ]; then
        # Read existing key from env file
        # shellcheck disable=SC1090
        API_KEY=$(grep -E '^CONCH_API_KEY=' "${ENV_FILE}" 2>/dev/null | cut -d= -f2- || true)
        if [ -z "${API_KEY}" ]; then
            API_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '=+' | head -c 43)
            warn "Existing config has no API key, generated new one."
        else
            log "Using existing API key from ${ENV_FILE}"
        fi
    else
        API_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '=+' | head -c 43)
    fi
fi

# --- Write config ---
cat > "${ENV_FILE}" <<EOF
CONCH_API_KEY=${API_KEY}
CONCH_PORT=${PORT}
CONCH_HOST=${HOST}
CONCH_TIMEOUT=${TIMEOUT}
CONCH_MAX_TIMEOUT=${MAX_TIMEOUT}
CONCH_ALLOW_NO_AUTH=${ALLOW_NO_AUTH}
EOF
chmod 600 "${ENV_FILE}"
log "Config: ${ENV_FILE}"
log "API key: ${API_KEY}"

if $ALLOW_NO_AUTH; then
    warn "Authentication disabled (CONCH_ALLOW_NO_AUTH=true). Do not expose to untrusted networks."
fi

# --- Service setup ---

# Helper: prompt yn with default
# Usage: ask "question" [default]  -> sets $REPLY to "y" or "n"
ask() {
    local prompt="$1"
    local default="${2:-y}"
    local yn
    if [ "$default" = "y" ]; then
        yn="Y/n"
    else
        yn="y/N"
    fi
    printf "%s [%s] " "$prompt" "$yn" >&2
    read -r REPLY </dev/tty 2>/dev/null || REPLY="$default"
    REPLY="${REPLY:-$default}"
}

if [ "$PLATFORM" = "linux" ]; then
    UNIT_FILE="/etc/systemd/system/conch.service"

    # Stop and remove any existing service before overwriting
    if [ -f "${UNIT_FILE}" ]; then
        log "Existing service found, stopping..."
        systemctl stop conch 2>/dev/null || true
        systemctl disable conch 2>/dev/null || true
        rm -f "${UNIT_FILE}"
        systemctl daemon-reload
    fi

    cat > "${UNIT_FILE}" <<UNIT
[Unit]
Description=Conch Shell Server
After=network.target

[Service]
Type=simple
ExecStart=${BIN_PATH}
EnvironmentFile=${ENV_FILE}
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
UNIT

    systemctl daemon-reload
    log "Service unit created"

    ENABLE_BOOT=true
    DO_START=true
    if $NO_START; then
        ENABLE_BOOT=false
        DO_START=false
    elif ! $YES_ALL; then
        ask "Enable auto-start on boot?" "y"
        [ "$REPLY" = "n" ] || [ "$REPLY" = "N" ] && ENABLE_BOOT=false
        ask "Start service now?" "y"
        [ "$REPLY" = "n" ] || [ "$REPLY" = "N" ] && DO_START=false
    fi

    if $ENABLE_BOOT; then
        systemctl enable conch
        log "Auto-start on boot: enabled"
    else
        log "Auto-start on boot: skipped"
    fi

    if $DO_START; then
        systemctl start conch
        log "Service started"
    else
        log "Service installed (not started). Start with: systemctl start conch"
    fi

    echo ""
    log "Manage: systemctl {start,stop,restart,status} conch"
    log "Logs:   journalctl -u conch -f"

elif [ "$PLATFORM" = "termux" ]; then
    if ! command -v sv &>/dev/null; then
        die "termux-services not installed. Run: pkg install termux-services"
    fi

    # Stop existing service if running
    sv stop conch 2>/dev/null || true

    mkdir -p "${SVC_DIR}"

    cat > "${SVC_DIR}/run" <<RUN
#!/data/data/com.termux/files/usr/bin/sh
exec 2>&1
set -a
. ${ENV_FILE}
set +a
exec ${BIN_PATH}
RUN
    chmod 755 "${SVC_DIR}/run"

    # runit env directory (one file per var)
    mkdir -p "${SVC_DIR}/env"
    while IFS='=' read -r key value; do
        case "$key" in '#'*|'') continue ;; esac
        echo "$value" > "${SVC_DIR}/env/${key}"
    done < "${ENV_FILE}"

    log "Runit service created"

    # --- Boot auto-start ---
    SETUP_BOOT=false
    if ! $NO_START && ! $YES_ALL; then
        ask "Enable auto-start on boot? (requires termux-boot)" "y"
        [ "$REPLY" != "n" ] && [ "$REPLY" != "N" ] && SETUP_BOOT=true
    elif ! $NO_START; then
        SETUP_BOOT=true
    fi

    if $SETUP_BOOT; then
        if [ -d "${BOOT_DIR}" ]; then
            cat > "${BOOT_DIR}/01-conch" <<BOOT
#!/data/data/com.termux/files/usr/bin/sh
sv up conch 2>/dev/null || true
BOOT
            chmod 755 "${BOOT_DIR}/01-conch"
            log "Boot script installed"
        else
            warn "termux-boot directory not found. Auto-start on boot NOT set up."
            warn "  pkg install termux-boot"
            warn "  mkdir -p ~/.termux/boot"
            warn "  Re-run this installer afterward."
        fi
    fi

    # --- Start now ---
    DO_START=true
    if $NO_START; then
        DO_START=false
    elif ! $YES_ALL; then
        ask "Start service now?" "y"
        [ "$REPLY" = "n" ] || [ "$REPLY" = "N" ] && DO_START=false
    fi

    if $DO_START; then
        sv up conch
        log "Service started"
    else
        log "Service installed (not started). Start with: sv up conch"
    fi
fi

echo ""
log "Test: curl -s http://localhost:${PORT}/health"
