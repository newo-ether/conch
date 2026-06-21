#!/usr/bin/env bash
# Install Conch Shell Server on Linux / Termux.
# Safe to run directly or via: curl -fsSL <url> | bash
set -euo pipefail

# ============================================================================
# Wrapped in subshell so 'exit' does not kill the user's shell when piped.
# ============================================================================
(

# ============================================================================
# Defaults
# ============================================================================
DEFAULT_PORT=14216
DEFAULT_HOST="0.0.0.0"
DEFAULT_TIMEOUT=30
DEFAULT_MAX_TIMEOUT=120

# ============================================================================
# Platform detection
# ============================================================================
if [ -d /data/data/com.termux/files/usr ] && [ -n "${PREFIX:-}" ]; then
    PLATFORM="termux"
else
    PLATFORM="linux"
fi

# ============================================================================
# Output helpers
# ============================================================================
BOLD=""; CYAN=""; GREEN=""; YELLOW=""; RED=""; RESET=""
if [ -t 1 ] || [ -n "${TERM:-}" ]; then
    BOLD=$(tput bold 2>/dev/null || printf '\033[1m')
    CYAN=$(tput setaf 6 2>/dev/null || printf '\033[36m')
    GREEN=$(tput setaf 2 2>/dev/null || printf '\033[32m')
    YELLOW=$(tput setaf 3 2>/dev/null || printf '\033[33m')
    RED=$(tput setaf 1 2>/dev/null || printf '\033[31m')
    RESET=$(tput sgr0 2>/dev/null || printf '\033[0m')
fi

banner() {
    local title="${PLATFORM^} Installer"
    # Box internal width = 46 (48 total with ║ borders)
    local pad1=$((46 - 10 - 18))   # "Conch Shell Server" = 18 chars
    local pad2=$((46 - 10 - ${#title}))
    echo ""
    echo "  ${CYAN}╔══════════════════════════════════════════════╗${RESET}"
    printf "  ${CYAN}║          %s%*s║${RESET}\n" "Conch Shell Server" "$pad1" ""
    printf "  ${CYAN}║          %s%*s║${RESET}\n" "$title" "$pad2" ""
    echo "  ${CYAN}╚══════════════════════════════════════════════╝${RESET}"
    echo ""
}

step()  { echo "  ${CYAN}[$1/$2]${RESET} $3"; }
ok()    { echo "    ${GREEN}✓${RESET} $*"; }
warn()  { echo "    ${YELLOW}⚠${RESET} $*" >&2; }
err()   { echo "    ${RED}✗${RESET} $*" >&2; }
info()  { echo "    ${CYAN}→${RESET} $*"; }
die()   { echo ""; err "$@"; rollback; echo ""; echo "  For help: https://github.com/newo-ether/conch"; echo ""; exit 1; }

# Prompt helper — respects YES_ALL
prompt() {
    local msg="$1" default="${2:-y}"
    local yn
    if $YES_ALL; then
        [ "$default" = "y" ] && return 0 || return 1
    fi
    if [ "$default" = "y" ]; then yn="[Y/n]"; else yn="[y/N]"; fi
    printf "    ? %s %s " "$msg" "$yn" >&2
    read -r reply </dev/tty 2>/dev/null || { reply="$default"; echo "$reply" >&2; }
    reply="${reply:-$default}"
    case "$reply" in [yY]*) return 0 ;; *) return 1 ;; esac
}

# ============================================================================
# Rollback state
# ============================================================================
ROLLBACK_CMDS=()
push_rollback() { ROLLBACK_CMDS=("$1" "${ROLLBACK_CMDS[@]}"); }
rollback() {
    [ ${#ROLLBACK_CMDS[@]} -eq 0 ] && return
    echo ""
    echo "  ${YELLOW}Cleaning up partial installation...${RESET}"
    for cmd in "${ROLLBACK_CMDS[@]}"; do
        echo "    ${YELLOW}→${RESET} $cmd"
        eval "$cmd" 2>/dev/null || true
    done
}

# ============================================================================
# Retry helper
# ============================================================================
retry() {
    local max="${1:-3}" delay="${2:-2}" desc="${3:-operation}"
    shift 3 || true
    local attempt=0
    while [ $attempt -lt "$max" ]; do
        attempt=$((attempt + 1))
        if "$@" 2>/dev/null; then return 0; fi
        if [ $attempt -lt "$max" ]; then
            warn "Retry $attempt/$max for $desc... (waiting ${delay}s)"
            sleep "$delay"
            delay=$(( (delay * 2 < 15) ? delay * 2 : 15 ))
        fi
    done
    return 1
}

# ============================================================================
# Atomic file write
# ============================================================================
atomic_write() {
    local content="$1" target="$2"
    local tmp="${target}.tmp.$$"
    echo "$content" > "$tmp"
    chmod 600 "$tmp"
    mv -f "$tmp" "$target"
}

# ============================================================================
# Detect architecture for prebuilt binary download
# ============================================================================
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)   echo "amd64" ;;
        aarch64|arm64)  echo "arm64" ;;
        *)              echo "unknown" ;;
    esac
}

# ============================================================================
# Download from GitHub Releases
# ============================================================================
GITHUB_RELEASES="https://github.com/newo-ether/conch/releases/latest/download"
download_binary() {
    local name="$1" dest="$2"
    local url="${GITHUB_RELEASES}/${name}"
    info "Downloading ${name}..."
    if command -v curl &>/dev/null; then
        retry 3 3 "download $name" \
            curl -fsSL --connect-timeout 10 --max-time 120 -o "$dest" "$url" || return 1
    elif command -v wget &>/dev/null; then
        retry 3 3 "download $name" \
            wget -q --timeout=120 -O "$dest" "$url" || return 1
    else
        return 1
    fi
    chmod 755 "$dest"
    ok "Downloaded: $name"
}

# ============================================================================
# Check binary looks valid (>= 1MB)
# ============================================================================
valid_binary() {
    [ -f "$1" ] && [ "$(stat -c%s "$1" 2>/dev/null || echo 0)" -gt 1048576 ]
}

# ============================================================================
# Parse arguments
# ============================================================================
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
INSTALL_PREFIX=""

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
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo "  --api-key KEY      Pre-shared API key"
            echo "  --port PORT        Listen port (default: ${DEFAULT_PORT})"
            echo "  --host ADDR        Listen address (default: ${DEFAULT_HOST})"
            echo "  --timeout SEC      Command timeout seconds (default: ${DEFAULT_TIMEOUT})"
            echo "  --max-timeout SEC  Max timeout seconds (default: ${DEFAULT_MAX_TIMEOUT})"
            echo "  --no-auth          Disable authentication (dev only)"
            echo "  --prefix DIR       Install root directory"
            echo "  --bin PATH         Use pre-built binary"
            echo "  --mcp-bin PATH     Use pre-built MCP binary"
            echo "  --no-start         Install but don't start"
            echo "  --uninstall        Remove service and binary"
            echo "  -y, --yes          Skip prompts, use defaults"
            exit 0
            ;;
        *) die "Unknown flag: $1 (use --help)" ;;
    esac
done

# ============================================================================
# Constants
# ============================================================================
SERVICE_NAME="conch"
ARCH=$(detect_arch)

# Platform-specific paths
if [ -z "${INSTALL_PREFIX}" ]; then
    if [ "$PLATFORM" = "termux" ]; then
        BIN_DIR="${PREFIX}/bin"
        CFG_DIR="${PREFIX}/etc/conch"
        SVC_DIR="${PREFIX}/var/service/conch"
        BOOT_DIR="${HOME}/.termux/boot"
    else
        BIN_DIR="/usr/local/bin"
        CFG_DIR="/etc/conch"
    fi
else
    BIN_DIR="${INSTALL_PREFIX}"
    CFG_DIR="${INSTALL_PREFIX}"
    if [ "$PLATFORM" = "termux" ]; then
        SVC_DIR="${INSTALL_PREFIX}/service"
    fi
fi

BIN_PATH="${BIN_DIR}/conch"
MCP_BIN_PATH="${BIN_DIR}/conch-mcp"
ENV_FILE="${CFG_DIR}/env"

# Download names
if [ "$PLATFORM" = "termux" ] || [ "$ARCH" = "arm64" ]; then
    SERVER_BIN_NAME="conch-linux-arm64"
    MCP_BIN_NAME="conch-mcp-linux-arm64"
else
    SERVER_BIN_NAME="conch-linux-amd64"
    MCP_BIN_NAME="conch-mcp-linux-amd64"
fi

# Repo/working directory (handles piped execution where $0 is not a script path)
if [ -f "$0" ] && [ "$(basename "$0")" != "bash" ]; then
    REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
else
    REPO_DIR="$(mktemp -d /tmp/conch-install.XXXXXX)"
    push_rollback "rm -rf $REPO_DIR"
fi

# ============================================================================
# Banner & step init
# ============================================================================
banner
STEP=1
TOTAL_STEPS=6
$DO_UNINSTALL && TOTAL_STEPS=2

# ============================================================================
# Step 1 — Environment checks
# ============================================================================
step $STEP $TOTAL_STEPS "Checking environment..."

# --- OS / arch ---
ok "OS: $(uname -s) $(uname -r)"
ok "Arch: ${ARCH} (platform: ${PLATFORM})"

# --- Root check (Linux only) ---
if [ "$PLATFORM" = "linux" ] && [ "$(id -u)" -ne 0 ]; then
    if $DO_UNINSTALL; then
        die "Uninstall requires root. Run: sudo $0 --uninstall"
    fi
    die "Root privileges required. Run: sudo $0"
fi
ok "$( [ "$PLATFORM" = "linux" ] && echo 'root' || echo 'termux user' )"

# --- Connectivity check ---
if ! $DO_UNINSTALL && [ -z "${SRC_BIN}" ]; then
    if command -v curl &>/dev/null || command -v wget &>/dev/null; then
        ok "Download tool: $(command -v curl || command -v wget)"
    else
        die "Neither curl nor wget found. Install one to continue."
    fi
fi

# --- Port conflict check ---
if ! $DO_UNINSTALL; then
    if command -v ss &>/dev/null; then
        if ss -tlnp 2>/dev/null | grep -q ":${PORT}\b"; then
            proc_name=$(ss -tlnp 2>/dev/null | grep ":${PORT}\b" | sed -n 's/.*users:(("\([^"]*\).*/\1/p')
            warn "Port $PORT is already in use by: ${proc_name:-unknown}"
            prompt "Continue anyway?" "y" || die "Aborted. Choose a different port with --port <number>."
        else
            ok "Port $PORT available"
        fi
    elif command -v netstat &>/dev/null; then
        if netstat -tlnp 2>/dev/null | grep -q ":${PORT}\b"; then
            warn "Port $PORT is already in use"
            prompt "Continue anyway?" "y" || die "Aborted. Choose a different port with --port <number>."
        else
            ok "Port $PORT available"
        fi
    fi
fi

# --- Dependency checks ---
if [ "$PLATFORM" = "linux" ] && ! command -v systemctl &>/dev/null; then
    die "systemd required but not found. This script supports systemd-based Linux distributions."
fi
if [ "$PLATFORM" = "termux" ] && ! command -v sv &>/dev/null; then
    die "termux-services required. Run: pkg install termux-services"
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 2 — Uninstall (if requested)
# ============================================================================
if $DO_UNINSTALL; then
    step $STEP $TOTAL_STEPS "Uninstalling Conch..."

    FOUND=false

    if [ "$PLATFORM" = "linux" ] && [ -f /etc/systemd/system/conch.service ]; then
        FOUND=true
        info "Stopping and removing systemd service..."
        systemctl stop conch 2>/dev/null || true
        systemctl disable conch 2>/dev/null || true
        rm -f /etc/systemd/system/conch.service
        systemctl daemon-reload
        ok "systemd service removed"
    fi

    if [ "$PLATFORM" = "termux" ]; then
        if [ -d "${SVC_DIR}" ]; then
            FOUND=true
            sv stop conch >/dev/null 2>&1 || true
            rm -rf "${SVC_DIR}"
            ok "runit service removed"
        fi
        if [ -f "${BOOT_DIR}/01-conch" ]; then
            rm -f "${BOOT_DIR}/01-conch"
            ok "boot script removed"
        fi
    fi

    if [ -f "${BIN_PATH}" ] || [ -f "${MCP_BIN_PATH}" ] || [ -d "${CFG_DIR}" ]; then
        FOUND=true
        rm -f "${BIN_PATH}" "${MCP_BIN_PATH}"
        rm -rf "${CFG_DIR}"
        ok "Files removed"
    fi

    if ! $FOUND; then
        warn "No existing Conch installation found."
        exit 0
    fi

    STEP=$((STEP + 1))
    step $STEP $TOTAL_STEPS "Done."
    echo ""
    ok "Conch has been uninstalled."
    echo ""
    exit 0
fi

# ============================================================================
# Step 2 — Detect & handle existing installation
# ============================================================================
EXISTING=false
[ -f "${BIN_PATH}" ] && EXISTING=true
[ -d "${CFG_DIR}" ] && EXISTING=true
[ "$PLATFORM" = "linux" ] && [ -f /etc/systemd/system/conch.service ] && EXISTING=true
[ "$PLATFORM" = "termux" ] && [ -d "${SVC_DIR}" ] && EXISTING=true

if $EXISTING; then
    step $STEP $TOTAL_STEPS "Existing installation detected"

    [ -f "${BIN_PATH}" ] && warn "Binary: ${BIN_PATH}"
    [ -d "${CFG_DIR}" ]  && warn "Config: ${CFG_DIR}"
    [ "$PLATFORM" = "linux" ]  && [ -f /etc/systemd/system/conch.service ] && warn "Service: systemd unit"
    [ "$PLATFORM" = "termux" ] && [ -d "${SVC_DIR}" ] && warn "Service: runit"

    echo ""
    if prompt "Remove existing installation before proceeding?" "y"; then
        info "Removing existing installation..."
        if [ "$PLATFORM" = "linux" ] && [ -f /etc/systemd/system/conch.service ]; then
            systemctl stop conch 2>/dev/null || true
            systemctl disable conch 2>/dev/null || true
            rm -f /etc/systemd/system/conch.service
            systemctl daemon-reload
            ok "systemd service removed"
        fi
        if [ "$PLATFORM" = "termux" ] && [ -d "${SVC_DIR}" ]; then
            sv stop conch >/dev/null 2>&1 || true
            rm -rf "${SVC_DIR}"
            ok "runit service removed"
        fi
        rm -f "${BIN_PATH}" "${MCP_BIN_PATH}"
        rm -rf "${CFG_DIR}"
        ok "Files removed"
    else
        info "Keeping existing files. Proceeding with in-place update."
    fi
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 3 — Acquire binary
# ============================================================================
step $STEP $TOTAL_STEPS "Acquiring binaries..."

SRC=""
if [ -n "${SRC_BIN}" ]; then
    if [ ! -f "${SRC_BIN}" ]; then
        die "Binary not found: ${SRC_BIN}"
    fi
    ok "Using provided binary: ${SRC_BIN}"
    SRC="${SRC_BIN}"
elif download_binary "${SERVER_BIN_NAME}" "${REPO_DIR}/conch"; then
    SRC="${REPO_DIR}/conch"
    if ! valid_binary "$SRC"; then
        warn "Downloaded file appears invalid. Trying alternatives..."
        rm -f "$SRC"
        SRC=""
    fi
fi

if [ -z "$SRC" ]; then
    warn "GitHub download failed or produced invalid file, trying alternatives..."

    if command -v go &>/dev/null && [ -f "${REPO_DIR}/go.mod" ]; then
        info "Building from source..."
        (cd "${REPO_DIR}" && go build -o conch .) || warn "Build failed"
        [ -f "${REPO_DIR}/conch" ] && SRC="${REPO_DIR}/conch" && ok "Built from source"
    fi

    [ -z "$SRC" ] && [ -f "${REPO_DIR}/conch" ] && SRC="${REPO_DIR}/conch" && ok "Using local conch"

    if [ -z "$SRC" ] && command -v conch &>/dev/null; then
        SRC="$(command -v conch)"
        ok "Using conch from PATH: ${SRC}"
    fi

    if [ -z "$SRC" ]; then
        die "Could not acquire binary. Download from: https://github.com/newo-ether/conch/releases/latest"
    fi
fi

if ! valid_binary "$SRC"; then
    warn "Binary at $SRC is smaller than expected ($(stat -c%s "$SRC") bytes)"
    warn "  Installation may succeed but the server might not work."
fi

# --- MCP binary acquisition (same step) ---
SRC_MCP_FINAL=""
if [ -n "${SRC_MCP}" ]; then
    if [ ! -f "${SRC_MCP}" ]; then
        warn "MCP binary not found: ${SRC_MCP} — skipping conch-mcp"
    else
        SRC_MCP_FINAL="${SRC_MCP}"
        ok "Using provided MCP binary: ${SRC_MCP}"
    fi
elif download_binary "${MCP_BIN_NAME}" "${REPO_DIR}/conch-mcp"; then
    SRC_MCP_FINAL="${REPO_DIR}/conch-mcp"
else
    if command -v go &>/dev/null && [ -f "${REPO_DIR}/go.mod" ]; then
        info "Building conch-mcp from source..."
        if (cd "${REPO_DIR}" && go build -o conch-mcp ./cmd/mcp); then
            SRC_MCP_FINAL="${REPO_DIR}/conch-mcp"
            ok "MCP built from source"
        else
            warn "Failed to build conch-mcp"
        fi
    elif [ -f "${REPO_DIR}/conch-mcp" ]; then
        SRC_MCP_FINAL="${REPO_DIR}/conch-mcp"
    elif command -v conch-mcp &>/dev/null; then
        SRC_MCP_FINAL="$(command -v conch-mcp)"
        ok "Using conch-mcp from PATH"
    fi
fi
if [ -z "$SRC_MCP_FINAL" ]; then
    warn "conch-mcp not available — MCP bridge will not be installed"
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 4 — Install files
# ============================================================================
step $STEP $TOTAL_STEPS "Installing files..."

mkdir -p "${BIN_DIR}"

copy_if_different() {
    local src="$1" dst="$2" label="$3"
    if [ -f "$dst" ] && [ "$(realpath "$src" 2>/dev/null || readlink -f "$src")" = "$(realpath "$dst" 2>/dev/null || readlink -f "$dst")" ]; then
        ok "$label already in place (same file)"
        return
    fi
    cp -f "$src" "$dst"
    chmod 755 "$dst"
    ok "$label installed"
}

copy_if_different "$SRC" "${BIN_PATH}" "conch"
if [ -n "$SRC_MCP_FINAL" ]; then
    copy_if_different "$SRC_MCP_FINAL" "${MCP_BIN_PATH}" "conch-mcp"
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 5 — Configuration
# ============================================================================
step $STEP $TOTAL_STEPS "Configuring..."

mkdir -p "${CFG_DIR}"

if [ -z "${API_KEY}" ]; then
    if [ -f "${ENV_FILE}" ]; then
        API_KEY=$(grep -E '^CONCH_API_KEY=' "${ENV_FILE}" 2>/dev/null | cut -d= -f2- || true)
        if [ -n "${API_KEY}" ]; then
            ok "Reusing API key from existing env"
        fi
    fi
    if [ -z "${API_KEY}" ]; then
        API_KEY=$(head -c 32 /dev/urandom 2>/dev/null | base64 | tr -d '=+' | head -c 43)
        ok "Generated new API key"
    fi
fi

cat > "${ENV_FILE}.tmp.$$" <<EOF
CONCH_API_KEY=${API_KEY}
CONCH_PORT=${PORT}
CONCH_HOST=${HOST}
CONCH_TIMEOUT=${TIMEOUT}
CONCH_MAX_TIMEOUT=${MAX_TIMEOUT}
CONCH_ALLOW_NO_AUTH=${ALLOW_NO_AUTH}
EOF
chmod 600 "${ENV_FILE}.tmp.$$"
mv -f "${ENV_FILE}.tmp.$$" "${ENV_FILE}"

ok "Config written: ${ENV_FILE}"
info "API key: ${API_KEY}"

if $ALLOW_NO_AUTH; then
    warn "Authentication is DISABLED — do not expose to untrusted networks!"
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 6 — Register & start service
# ============================================================================
step $STEP $TOTAL_STEPS "Registering service..."

if [ "$PLATFORM" = "linux" ]; then
    UNIT_FILE="/etc/systemd/system/conch.service"

    # Clean up stale unit
    if [ -f "${UNIT_FILE}" ]; then
        info "Removing stale systemd unit..."
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

    push_rollback "systemctl stop conch 2>/dev/null; rm -f ${UNIT_FILE}; systemctl daemon-reload"
    systemctl daemon-reload
    ok "systemd unit created"

    ENABLE_BOOT=true
    DO_START=true
    if $NO_START; then
        ENABLE_BOOT=false
        DO_START=false
    elif ! $YES_ALL; then
        echo ""
        prompt "Enable auto-start on boot?" "y" || ENABLE_BOOT=false
        prompt "Start service now?" "y" || DO_START=false
    fi

    if $ENABLE_BOOT; then
        systemctl enable conch 2>/dev/null || warn "Failed to enable auto-start"
        ok "Auto-start on boot: enabled"
    else
        ok "Auto-start on boot: skipped"
    fi

    if $DO_START; then
        systemctl start conch 2>/dev/null || warn "Failed to start service"
        sleep 2
        if systemctl is-active --quiet conch 2>/dev/null; then
            ok "Service started"
            # Quick health check
            if command -v curl &>/dev/null; then
                if curl -s --max-time 5 "http://localhost:${PORT}/health" >/dev/null 2>&1; then
                    ok "Health check passed: localhost:${PORT}/health"
                else
                    warn "Health check failed — service may still be initializing"
                fi
            fi
        else
            warn "Service may not have started. Check: systemctl status conch"
        fi
    else
        info "Service installed but not started. Start with: systemctl start conch"
    fi

elif [ "$PLATFORM" = "termux" ]; then
    mkdir -p "${SVC_DIR}"

    # Only try to stop if the supervise directory already existed (from a prior install)
    if [ -f "${SVC_DIR}/run" ]; then
        sv stop conch >/dev/null 2>&1 || true
    fi

    cat > "${SVC_DIR}/run" <<'RUN'
#!/data/data/com.termux/files/usr/bin/sh
exec 2>&1
set -a
. CFG_FILE_PLACEHOLDER
set +a
exec BIN_PATH_PLACEHOLDER
RUN
    sed -i "s|CFG_FILE_PLACEHOLDER|${ENV_FILE}|" "${SVC_DIR}/run"
    sed -i "s|BIN_PATH_PLACEHOLDER|${BIN_PATH}|" "${SVC_DIR}/run"
    chmod 755 "${SVC_DIR}/run"

    push_rollback "sv stop conch 2>/dev/null; rm -rf ${SVC_DIR}"

    # runit env directory
    mkdir -p "${SVC_DIR}/env"
    while IFS='=' read -r key value; do
        case "$key" in '#'*|'') continue ;; esac
        echo "$value" > "${SVC_DIR}/env/${key}"
    done < "${ENV_FILE}"

    ok "runit service created"

    # Boot auto-start
    SETUP_BOOT=false
    if ! $NO_START && ! $YES_ALL; then
        echo ""
        prompt "Enable auto-start on boot? (requires termux-boot)" "y" && SETUP_BOOT=true
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
            ok "Boot script installed"
        else
            warn "termux-boot directory not found. Auto-start on boot NOT set up."
            warn "  pkg install termux-boot && mkdir -p ~/.termux/boot"
            warn "  Then re-run this installer."
        fi
    fi

    # Start now
    DO_START=true
    $NO_START && DO_START=false
    if ! $NO_START && ! $YES_ALL; then
        prompt "Start service now?" "y" || DO_START=false
    fi

    if $DO_START; then
        # runsvdir may need a moment to notice the new directory
        sleep 1
        sv up conch >/dev/null 2>&1 || { sleep 2; sv up conch >/dev/null 2>&1; } || true
        sleep 1
        ok "Service started"
    else
        info "Service installed but not started. Start with: sv up conch"
    fi
fi

# ============================================================================
# Done
# ============================================================================
rpad=$((46 - 2 - 21))  # "Installation Complete" = 21 chars
echo ""
echo "  ${GREEN}╔══════════════════════════════════════════════╗${RESET}"
printf "  ${GREEN}║  ${BOLD}%s${RESET}%*s${GREEN}║${RESET}\n" "Installation Complete" "$rpad" ""
echo "  ${GREEN}╚══════════════════════════════════════════════╝${RESET}"
echo ""
echo "  ${CYAN}Health check:${RESET}   curl -s http://localhost:${PORT}/health"
echo "  ${CYAN}API key:${RESET}       ${API_KEY}"
echo "  ${CYAN}Config file:${RESET}   ${ENV_FILE}"
echo ""
if [ "$PLATFORM" = "linux" ]; then
    echo "  ${CYAN}Manage:${RESET}"
    echo "    systemctl {start,stop,restart,status} conch"
    echo "    journalctl -u conch -f"
elif [ "$PLATFORM" = "termux" ]; then
    echo "  ${CYAN}Manage:${RESET}"
    echo "    sv {up,down,status} conch"
    echo "    Uninstall: $0 --uninstall"
fi
echo ""

) # end subshell — protects the user's shell from exit
