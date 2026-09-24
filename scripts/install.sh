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
DEFAULT_MAX_TIMEOUT=1800
DEFAULT_JOB_RETENTION_HOURS=24
DEFAULT_RELEASE_VERSION="latest"

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
    local title="${PLATFORM^} (${ARCH}) Installer"
    # Box internal width = 46 (48 total with | borders)
    local pad1=$((46 - 10 - 18))   # "Conch Shell Server" = 18 chars
    local pad2=$((46 - 10 - ${#title}))
    echo ""
    echo "  ${CYAN}+----------------------------------------------+${RESET}"
    printf "  ${CYAN}|          %s%*s|${RESET}\n" "Conch Shell Server" "$pad1" ""
    printf "  ${CYAN}|          %s%*s|${RESET}\n" "$title" "$pad2" ""
    echo "  ${CYAN}+----------------------------------------------+${RESET}"
    echo ""
}

step()  { echo "  ${CYAN}[$1/$2]${RESET} $3"; }
ok()    { echo "    ${GREEN}[OK]${RESET} $*"; }
warn()  { echo "    ${YELLOW}[WARN]${RESET} $*" >&2; }
err()   { echo "    ${RED}[ERROR]${RESET} $*" >&2; }
info()  { echo "    ${CYAN}[INFO]${RESET} $*"; }
die()   { echo ""; err "$@"; rollback; echo ""; echo "  For help: https://github.com/newo-ether/conch"; echo ""; exit 1; }

# Prompt helper - respects YES_ALL
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
        echo "    ${YELLOW}[ROLLBACK]${RESET} $cmd"
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
download_url() {
    local url="$1" dest="$2" desc="$3"
    if command -v curl &>/dev/null; then
        retry 3 3 "$desc" \
            curl -fsSL --connect-timeout 10 --max-time 120 -o "$dest" "$url"
    elif command -v wget &>/dev/null; then
        retry 3 3 "$desc" \
            wget -q --timeout=120 -O "$dest" "$url"
    else
        return 1
    fi
}

expected_release_hash() {
    local name="$1"
    if [ ! -f "$CHECKSUM_MANIFEST" ]; then
        info "Downloading release checksum manifest..." >&2
        download_url "$GITHUB_RELEASES/checksums.txt" "$CHECKSUM_MANIFEST" "download checksums.txt" ||
            return 1
    fi
    tr -d '\r' < "$CHECKSUM_MANIFEST" |
        awk -v name="$name" '$2 == name || $2 == "*" name { print tolower($1); exit }'
}

file_sha256() {
    if command -v sha256sum &>/dev/null; then
        sha256sum "$1" | awk '{print tolower($1)}'
    elif command -v shasum &>/dev/null; then
        shasum -a 256 "$1" | awk '{print tolower($1)}'
    else
        return 1
    fi
}

download_binary() {
    local name="$1" dest="$2"
    local expected actual attempt
    info "Downloading $name from release $RELEASE_VERSION..."
    expected="$(expected_release_hash "$name")"
    [ -n "$expected" ] || { warn "No checksum found for $name"; return 1; }

    attempt=1
    while [ "$attempt" -le 3 ]; do
        if download_url "$GITHUB_RELEASES/$name" "$dest" "download $name"; then
            actual="$(file_sha256 "$dest")" ||
                { warn "No SHA-256 tool available"; rm -f "$dest"; return 1; }
            if [ "$actual" = "$expected" ]; then
                chmod 755 "$dest"
                ok "Verified SHA-256: $name"
                return 0
            fi
            warn "SHA-256 mismatch for $name (attempt $attempt/3)"
            rm -f "$dest"
        fi
        if [ "$attempt" -lt 3 ]; then
            warn "Retrying verified download for $name..."
            sleep "$attempt"
        fi
        attempt=$((attempt + 1))
    done
    return 1
}

# ============================================================================
# # Check binary looks valid (>= 1MB)
# ============================================================================
valid_binary() {
    [ -f "$1" ] && [ "$(stat -c%s "$1" 2>/dev/null || echo 0)" -gt 1048576 ]
}

# ============================================================================
# Parse arguments
# ============================================================================
API_KEY=""
API_KEY_SET=false
PORT_SET=false
HOST_SET=false
TIMEOUT_SET=false
MAX_TIMEOUT_SET=false
JOB_RETENTION_HOURS_SET=false
NO_AUTH_SET=false
PORT="${DEFAULT_PORT}"
HOST="${DEFAULT_HOST}"
TIMEOUT="${DEFAULT_TIMEOUT}"
MAX_TIMEOUT="${DEFAULT_MAX_TIMEOUT}"
JOB_RETENTION_HOURS="${DEFAULT_JOB_RETENTION_HOURS}"
ALLOW_NO_AUTH="false"
SRC_BIN=""
SRC_MCP=""
NO_START=false
DO_UNINSTALL=false
YES_ALL=false
INSTALL_PREFIX=""
RELEASE_VERSION="${DEFAULT_RELEASE_VERSION}"
MODE=""

while [ $# -gt 0 ]; do
    case "$1" in
        --api-key)
            [ $# -ge 2 ] || die "--api-key requires a value"
            API_KEY="$2"; API_KEY_SET=true; shift 2 ;;
        --port)
            [ $# -ge 2 ] || die "--port requires a value"
            PORT="$2"; PORT_SET=true; shift 2 ;;
        --host)
            [ $# -ge 2 ] || die "--host requires a value"
            HOST="$2"; HOST_SET=true; shift 2 ;;
        --timeout)
            [ $# -ge 2 ] || die "--timeout requires a value"
            TIMEOUT="$2"; TIMEOUT_SET=true; shift 2 ;;
        --max-timeout)
            [ $# -ge 2 ] || die "--max-timeout requires a value"
            MAX_TIMEOUT="$2"; MAX_TIMEOUT_SET=true; shift 2 ;;
        --job-retention-hours)
            [ $# -ge 2 ] || die "--job-retention-hours requires a value"
            JOB_RETENTION_HOURS="$2"; JOB_RETENTION_HOURS_SET=true; shift 2 ;;
        --no-auth)       ALLOW_NO_AUTH="true"; NO_AUTH_SET=true; shift ;;
        --prefix)
            [ $# -ge 2 ] || die "--prefix requires a value"
            INSTALL_PREFIX="$2"; shift 2 ;;
        --bin)
            [ $# -ge 2 ] || die "--bin requires a value"
            SRC_BIN="$2"; shift 2 ;;
        --mcp-bin)
            [ $# -ge 2 ] || die "--mcp-bin requires a value"
            SRC_MCP="$2"; shift 2 ;;
        --version)
            [ $# -ge 2 ] || die "--version requires a value"
            RELEASE_VERSION="$2"; shift 2 ;;
        --no-start)      NO_START=true; shift ;;
        --uninstall)     DO_UNINSTALL=true; shift ;;
        --mode)
            [ $# -ge 2 ] || die "--mode requires a value"
            MODE="$2"; shift 2 ;;
        -y|--yes)        YES_ALL=true; shift ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo "  --api-key KEY      Pre-shared API key"
            echo "  --port PORT        Listen port (default: ${DEFAULT_PORT})"
            echo "  --host ADDR        Listen address (default: ${DEFAULT_HOST})"
            echo "  --timeout SEC      Command timeout seconds (default: ${DEFAULT_TIMEOUT})"
            echo "  --max-timeout SEC  Max timeout seconds (default: ${DEFAULT_MAX_TIMEOUT})"
            echo "  --job-retention-hours H  Hours to retain completed background jobs (default: ${DEFAULT_JOB_RETENTION_HOURS})"
            echo "  --no-auth          Disable authentication (dev only)"
            echo "  --prefix DIR       Install root directory"
            echo "  --bin PATH         Use pre-built binary"
            echo "  --mcp-bin PATH     Use pre-built MCP binary"
            echo "  --version VERSION  Pin GitHub release (for example v1.0.9)"
            echo "  --no-start         Install but don't start"
            echo "  --uninstall        Remove one selected installation mode"
            echo "  --mode MODE        user (default for new installs) or system (machine-wide systemd)"
            echo "  -y, --yes          Skip prompts, use defaults"
            exit 0
            ;;
        *) die "Unknown flag: $1 (use --help)" ;;
    esac
done

if [[ "$RELEASE_VERSION" != "latest" &&
      ! "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "Invalid --version '$RELEASE_VERSION' (expected latest or vX.Y.Z)"
fi
[[ "$PORT" =~ ^[0-9]+$ ]] && (( 10#$PORT >= 1 && 10#$PORT <= 65535 )) ||
    die "--port must be an integer between 1 and 65535"
[[ "$TIMEOUT" =~ ^[0-9]+$ ]] && (( 10#$TIMEOUT >= 1 && 10#$TIMEOUT <= 604800 )) ||
    die "--timeout must be an integer between 1 and 604800"
[[ "$MAX_TIMEOUT" =~ ^[0-9]+$ ]] && (( 10#$MAX_TIMEOUT >= 1 && 10#$MAX_TIMEOUT <= 604800 )) ||
    die "--max-timeout must be an integer between 1 and 604800"
[[ "$JOB_RETENTION_HOURS" =~ ^[0-9]+$ ]] && (( 10#$JOB_RETENTION_HOURS >= 0 && 10#$JOB_RETENTION_HOURS <= 8760 )) ||
    die "--job-retention-hours must be an integer between 0 and 8760"
(( 10#$TIMEOUT <= 10#$MAX_TIMEOUT )) || die "--timeout must not exceed --max-timeout"
[ -n "$HOST" ] || die "--host must not be empty"
for config_value in "$API_KEY" "$HOST"; do
    [[ "$config_value" != *$'\n'* && "$config_value" != *$'\r'* ]] ||
        die "Configuration values must not contain newlines"
done
if [ -n "$API_KEY" ] && [[ ! "$API_KEY" =~ ^[A-Za-z0-9._~:/+=-]+$ ]]; then
    die "--api-key contains characters unsafe for an environment file"
fi
[[ "$HOST" =~ ^[A-Za-z0-9._:%-]+$ ]] ||
    die "--host contains unsupported characters"
for path_value in "$INSTALL_PREFIX" "$SRC_BIN" "$SRC_MCP"; do
    [[ "$path_value" != *$'\n'* && "$path_value" != *$'\r'* && "$path_value" != *"'"* ]] ||
        die "Installer paths must not contain newlines or apostrophes"
done
case "$MODE" in
    ""|system|user) ;;
    *) die "Invalid --mode '$MODE' (expected system or user)" ;;
esac

if [ "$RELEASE_VERSION" = "latest" ]; then
    GITHUB_RELEASES="https://github.com/newo-ether/conch/releases/latest/download"
else
    GITHUB_RELEASES="https://github.com/newo-ether/conch/releases/download/${RELEASE_VERSION}"
fi

# ============================================================================
# Install mode resolution (system service vs per-user service)
# ============================================================================
select_install_mode() {
    local requested="$1"
    local for_uninstall="$2"
    local has_custom_prefix="$3"
    local custom_prefix_exists="$4"
    local system_present="$5"
    local user_present="$6"

    if [ -n "$requested" ]; then
        SELECTED_MODE="$requested"
        return
    fi
    if $for_uninstall && $has_custom_prefix; then
        die "Specify --mode system or --mode user when uninstalling a custom --prefix."
    fi
    if $system_present && $user_present; then
        die "Both system and user installations exist. Specify --mode system or --mode user."
    fi
    if $system_present; then
        SELECTED_MODE="system"
        return
    fi
    if $user_present; then
        SELECTED_MODE="user"
        return
    fi
    if ! $for_uninstall && $has_custom_prefix && $custom_prefix_exists; then
        die "The custom --prefix already exists but its installation mode cannot be identified. Specify --mode system or --mode user."
    fi

    # New installations default to least-privilege user mode.
    SELECTED_MODE="user"
}

if [ "$PLATFORM" = "termux" ]; then
    MODE="user"
elif [ -z "$MODE" ]; then
    system_present=false
    user_present=false
    { [ -f /etc/systemd/system/conch.service ] ||
      [ -f /usr/local/bin/conch ] || [ -d /etc/conch ]; } && system_present=true
    { [ -f "${HOME}/.config/systemd/user/conch.service" ] ||
      [ -f "${HOME}/.local/bin/conch" ] || [ -d "${HOME}/.config/conch" ]; } && user_present=true
    has_custom_prefix=false
    custom_prefix_exists=false
    [ -n "$INSTALL_PREFIX" ] && has_custom_prefix=true
    [ -n "$INSTALL_PREFIX" ] && [ -e "$INSTALL_PREFIX" ] && custom_prefix_exists=true

    SELECTED_MODE=""
    select_install_mode \
        "$MODE" "$DO_UNINSTALL" "$has_custom_prefix" "$custom_prefix_exists" \
        "$system_present" "$user_present"
    MODE="$SELECTED_MODE"
fi

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
        # $HOME may be wrong under sudo; derive from PREFIX
        termux_home="${HOME}"
        if [ -n "${PREFIX:-}" ]; then termux_home="$(dirname "$PREFIX")/home"; fi
        BOOT_DIR="${termux_home}/.termux/boot"
    else
        if [ "$MODE" = "user" ]; then
            BIN_DIR="${HOME}/.local/bin"
            CFG_DIR="${HOME}/.config/conch"
        else
            BIN_DIR="/usr/local/bin"
            CFG_DIR="/etc/conch"
        fi
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

# Rollback commands are evaluated later, so reject path forms that cannot be
# represented safely by the single-quoted rollback entries below.
for managed_path in "$BIN_DIR" "$CFG_DIR" "${SVC_DIR:-}"; do
    [[ "$managed_path" != *$'\n'* && "$managed_path" != *$'\r'* &&
       "$managed_path" != *"'"* ]] ||
        die "Managed install paths must not contain newlines or apostrophes"
done

SYSTEM_MODE_PRESENT=false
USER_MODE_PRESENT=false
if [ "$PLATFORM" = "linux" ]; then
    { [ -f /etc/systemd/system/conch.service ] ||
      [ -f /usr/local/bin/conch ] || [ -d /etc/conch ]; } && SYSTEM_MODE_PRESENT=true
    { [ -f "${HOME}/.config/systemd/user/conch.service" ] ||
      [ -f "${HOME}/.local/bin/conch" ] || [ -d "${HOME}/.config/conch" ]; } && USER_MODE_PRESENT=true
    if ! $DO_UNINSTALL && [ "$MODE" = "user" ] && $SYSTEM_MODE_PRESENT; then
        die "A system-mode Conch installation exists. Refusing an implicit migration; uninstall it with --uninstall --mode system first."
    fi
    if ! $DO_UNINSTALL && [ "$MODE" = "system" ] && $USER_MODE_PRESENT; then
        die "A user-mode Conch installation exists. Refusing an implicit migration; uninstall it with --uninstall --mode user first."
    fi
fi

# Download names
if [ "$PLATFORM" = "termux" ] || [ "$ARCH" = "arm64" ]; then
    SERVER_BIN_NAME="conch-linux-arm64"
    MCP_BIN_NAME="conch-mcp-linux-arm64"
else
    SERVER_BIN_NAME="conch-linux-amd64"
    MCP_BIN_NAME="conch-mcp-linux-amd64"
fi

# Repo/working directory (handles piped execution where $0 is not a script path)
REPO_IS_TEMP=false
if [ -f "$0" ] && [ "$(basename "$0")" != "bash" ]; then
    REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
else
    REPO_IS_TEMP=true
    REPO_DIR="$(mktemp -d "${TMPDIR:-/tmp}/conch-install.XXXXXX")"
    printf -v repo_cleanup 'rm -rf -- %q' "$REPO_DIR"
    push_rollback "$repo_cleanup"
fi

# ============================================================================
CHECKSUM_MANIFEST="${REPO_DIR}/checksums-${RELEASE_VERSION}.txt"

# Banner & step init
# ============================================================================
banner
STEP=1
TOTAL_STEPS=6
$DO_UNINSTALL && TOTAL_STEPS=2

# ============================================================================
# Step 1 - Environment checks
# ============================================================================
step $STEP $TOTAL_STEPS "Checking environment..."

# --- OS / arch ---
ok "OS: $(uname -s) $(uname -r)"
ok "Arch: ${ARCH} (platform: ${PLATFORM})"

# --- Identity check (Linux only) ---
if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "user" ] && [ "$(id -u)" -eq 0 ]; then
    die "User mode must run as the target user, without sudo or a root shell."
fi
if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "system" ] && [ "$(id -u)" -ne 0 ]; then
    if $DO_UNINSTALL; then
        die "System-mode uninstall requires root. Run: sudo $0 --uninstall --mode system"
    fi
    die "System mode requires root. Run: sudo $0 --mode system"
fi
if [ "$PLATFORM" = "linux" ]; then
    if [ "$MODE" = "user" ]; then
        ok "user mode (no root required)"
    else
        ok "root"
    fi
else
    ok "termux user"
fi

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
USER_SYSTEMD_AVAILABLE=true
if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "user" ] &&
   ! systemctl --user show-environment >/dev/null 2>&1; then
    if $DO_UNINSTALL; then
        USER_SYSTEMD_AVAILABLE=false
        warn "The systemd user manager is unavailable; removing the unit and files without contacting it."
    else
        die "The systemd user manager is unavailable. Run from a normal login session with user systemd support."
    fi
fi
if [ "$PLATFORM" = "termux" ] && ! command -v sv &>/dev/null; then
    die "termux-services required. Run: pkg install termux-services"
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 2 - Uninstall (if requested)
# ============================================================================
if $DO_UNINSTALL; then
    step $STEP $TOTAL_STEPS "Uninstalling Conch ($MODE mode)..."

    FOUND=false
    if ! $YES_ALL; then
        prompt "Remove the $MODE-mode Conch installation and its files?" "y" || {
            info "Aborted by user."
            exit 0
        }
    fi

    if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "system" ] &&
       [ -f /etc/systemd/system/conch.service ]; then
        FOUND=true
        info "Stopping and removing systemd service..."
        systemctl stop conch 2>/dev/null || true
        systemctl disable conch 2>/dev/null || true
        rm -f /etc/systemd/system/conch.service
        systemctl daemon-reload
        ok "systemd service removed"
    fi

    if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "user" ] &&
       [ -f "${HOME}/.config/systemd/user/conch.service" ]; then
        FOUND=true
        info "Stopping and removing systemd user service..."
        if $USER_SYSTEMD_AVAILABLE; then
            systemctl --user stop conch 2>/dev/null || true
            systemctl --user disable conch 2>/dev/null || true
        fi
        rm -f "${HOME}/.config/systemd/user/conch.service"
        if $USER_SYSTEMD_AVAILABLE; then
            systemctl --user daemon-reload
        fi
        ok "systemd user service removed"
    fi

    if [ "$PLATFORM" = "termux" ]; then
        if [ -d "${SVC_DIR}" ]; then
            FOUND=true
            sv stop "${SVC_DIR}" >/dev/null 2>&1 || true
            chmod -R u+w "${SVC_DIR}" 2>/dev/null || true
            rm -rf "${SVC_DIR}"
            ok "runit service removed"
        fi
        if [ -f "${BOOT_DIR}/01-conch" ]; then
            FOUND=true
            rm -f "${BOOT_DIR}/01-conch"
            ok "boot script removed"
        fi
    fi

    if [ -f "${BIN_PATH}" ] || [ -f "${MCP_BIN_PATH}" ] || [ -d "${CFG_DIR}" ]; then
        FOUND=true
        rm -f "${BIN_PATH}" "${MCP_BIN_PATH}"
        rm -rf "${CFG_DIR}"
        ok "$MODE-mode files removed"
    fi

    if ! $FOUND; then
        warn "No $MODE-mode Conch installation found."
        exit 0
    fi

    STEP=$((STEP + 1))
    step $STEP $TOTAL_STEPS "Done."
    echo ""
    ok "The $MODE-mode Conch installation has been uninstalled."
    echo ""
    exit 0
fi

# ============================================================================
# Step 2 - Detect & handle existing installation
# ============================================================================
EXISTING=false
SERVICE_WAS_ACTIVE=false
EXISTING_CONFIG_SHA256=""
[ -f "$BIN_PATH" ] && EXISTING=true
[ -d "$CFG_DIR" ] && EXISTING=true
if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "system" ] &&
   [ -f /etc/systemd/system/conch.service ]; then
    EXISTING=true
fi
if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "user" ] &&
   [ -f "${HOME}/.config/systemd/user/conch.service" ]; then
    EXISTING=true
fi
[ "$PLATFORM" = "termux" ] && [ -d "$SVC_DIR" ] && EXISTING=true

if $EXISTING; then
    step $STEP $TOTAL_STEPS "Existing $MODE-mode installation detected"
    [ -f "$BIN_PATH" ] && warn "Binary: $BIN_PATH"
    [ -d "$CFG_DIR" ] && warn "Config: $CFG_DIR"
    info "Performing an in-place upgrade; configuration and durable job state will be preserved."
    if [ -f "$ENV_FILE" ]; then
        EXISTING_CONFIG_SHA256="$(file_sha256 "$ENV_FILE")" ||
            die "Cannot verify existing configuration before upgrade"
        if $API_KEY_SET || $PORT_SET || $HOST_SET || $TIMEOUT_SET ||
           $MAX_TIMEOUT_SET || $NO_AUTH_SET || $JOB_RETENTION_HOURS_SET; then
            die "Configuration override flags cannot be used during an in-place upgrade. Existing configuration will not be overwritten."
        fi
        existing_api_key="$(grep -E '^CONCH_API_KEY=' "$ENV_FILE" 2>/dev/null | cut -d= -f2- || true)"
        existing_no_auth="$(grep -E '^CONCH_ALLOW_NO_AUTH=' "$ENV_FILE" 2>/dev/null | cut -d= -f2- || true)"
        if [ -z "$existing_api_key" ] && [ "$existing_no_auth" != "true" ]; then
            die "Existing configuration has no API key and authentication is enabled. Repair $ENV_FILE before upgrading."
        fi
        if [ -n "$existing_api_key" ] &&
           [[ ! "$existing_api_key" =~ ^[A-Za-z0-9._~:/+=-]+$ ]]; then
            die "Existing API key contains unsupported characters and cannot be printed safely. Repair $ENV_FILE before upgrading."
        fi
    fi
    if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "user" ] &&
       [ -f "${HOME}/.config/systemd/user/conch.service" ]; then
        systemctl --user is-active --quiet conch 2>/dev/null && SERVICE_WAS_ACTIVE=true
    elif [ "$PLATFORM" = "linux" ] && [ "$MODE" = "system" ] &&
         [ -f /etc/systemd/system/conch.service ]; then
        systemctl is-active --quiet conch 2>/dev/null && SERVICE_WAS_ACTIVE=true
    elif [ "$PLATFORM" = "termux" ] && [ -d "$SVC_DIR" ]; then
        sv status "$SVC_DIR" 2>/dev/null | grep -q '^run:' && SERVICE_WAS_ACTIVE=true
    fi
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 3 - Acquire binary
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
    if $REPO_IS_TEMP; then
        die "Could not download and verify $SERVER_BIN_NAME from release $RELEASE_VERSION. The existing installation was left unchanged."
    fi
    warn "Release download failed; trying source-checkout alternatives..."

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
        warn "MCP binary not found: ${SRC_MCP} - skipping conch-mcp"
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
    warn "conch-mcp not available - MCP bridge will not be installed"
fi

assert_binary_version() {
    local path="$1" label="$2" reported
    reported="$("$path" --version 2>&1)" || die "$label did not return version metadata"
    [ -n "$reported" ] || die "$label returned empty version metadata"
    if [ "$RELEASE_VERSION" != "latest" ]; then
        echo "$reported" | grep -Fq "$RELEASE_VERSION" ||
            die "$label reports '$reported', requested $RELEASE_VERSION"
    fi
    ok "$label version: $reported"
}
assert_binary_version "$SRC" "conch"
[ -n "$SRC_MCP_FINAL" ] && assert_binary_version "$SRC_MCP_FINAL" "conch-mcp"

STEP=$((STEP + 1))

# ============================================================================
# Step 4 - Install files
# ============================================================================
step $STEP $TOTAL_STEPS "Installing files..."

# Delay the first upgrade side effect until both release binaries have been acquired and verified.
if $EXISTING; then
    if [ "$PLATFORM" = "linux" ] && [ "$MODE" = "user" ] &&
       [ -f "${HOME}/.config/systemd/user/conch.service" ]; then
        push_rollback "if $SERVICE_WAS_ACTIVE; then systemctl --user start conch 2>/dev/null || true; fi"
        systemctl --user stop conch 2>/dev/null || die "Failed to stop existing user conch service"
        systemctl --user is-active --quiet conch 2>/dev/null &&
            die "Existing user conch service is still active"
    elif [ "$PLATFORM" = "linux" ] && [ "$MODE" = "system" ] &&
         [ -f /etc/systemd/system/conch.service ]; then
        push_rollback "if $SERVICE_WAS_ACTIVE; then systemctl start conch 2>/dev/null || true; fi"
        systemctl stop conch 2>/dev/null || die "Failed to stop existing conch service"
        systemctl is-active --quiet conch 2>/dev/null &&
            die "Existing conch service is still active"
    elif [ "$PLATFORM" = "termux" ] && [ -d "$SVC_DIR" ]; then
        push_rollback "if $SERVICE_WAS_ACTIVE; then sv up '$SVC_DIR' 2>/dev/null || true; fi"
        sv stop "$SVC_DIR" >/dev/null 2>&1 ||
            die "Failed to stop existing conch service"
    fi
fi

BIN_DIR_EXISTED=false
[ -d "$BIN_DIR" ] && BIN_DIR_EXISTED=true
mkdir -p "${BIN_DIR}"
if ! $BIN_DIR_EXISTED; then
    push_rollback "rmdir -- '$BIN_DIR' 2>/dev/null || true"
fi

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

SERVER_BACKUP="$BIN_PATH.previous"
MCP_BACKUP="$MCP_BIN_PATH.previous"
SERVER_EXISTED=false
MCP_EXISTED=false
[ -f "$BIN_PATH" ] && SERVER_EXISTED=true
[ -f "$MCP_BIN_PATH" ] && MCP_EXISTED=true
if $SERVER_EXISTED; then
    cp -f "$BIN_PATH" "$SERVER_BACKUP"
    if [ "$PLATFORM" = "linux" ]; then
        if [ "$MODE" = "user" ]; then
            push_rollback "systemctl --user stop conch 2>/dev/null || true; cp -f '$SERVER_BACKUP' '$BIN_PATH'; chmod 755 '$BIN_PATH'; systemctl --user daemon-reload"
        else
            push_rollback "systemctl stop conch 2>/dev/null || true; cp -f '$SERVER_BACKUP' '$BIN_PATH'; chmod 755 '$BIN_PATH'; systemctl daemon-reload"
        fi
    else
        push_rollback "sv stop '$SVC_DIR' 2>/dev/null || true; cp -f '$SERVER_BACKUP' '$BIN_PATH'; chmod 755 '$BIN_PATH'"
    fi
fi
if $MCP_EXISTED; then
    cp -f "$MCP_BIN_PATH" "$MCP_BACKUP"
    push_rollback "cp -f '$MCP_BACKUP' '$MCP_BIN_PATH'; chmod 755 '$MCP_BIN_PATH'"
fi

copy_if_different "$SRC" "$BIN_PATH" "conch"
if ! $SERVER_EXISTED; then
    push_rollback "rm -f -- '$BIN_PATH'"
fi
if [ -n "$SRC_MCP_FINAL" ]; then
    copy_if_different "$SRC_MCP_FINAL" "${MCP_BIN_PATH}" "conch-mcp"
    if ! $MCP_EXISTED; then
        push_rollback "rm -f -- '$MCP_BIN_PATH'"
    fi
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 5 - Configuration
# ============================================================================
step $STEP $TOTAL_STEPS "Configuring..."

CFG_DIR_EXISTED=false
[ -d "$CFG_DIR" ] && CFG_DIR_EXISTED=true
mkdir -p "${CFG_DIR}"
if ! $CFG_DIR_EXISTED; then
    push_rollback "rmdir -- '$CFG_DIR' 2>/dev/null || true"
fi

if [ -f "$ENV_FILE" ]; then
    CURRENT_CONFIG_SHA256="$(file_sha256 "$ENV_FILE")" ||
        die "Cannot verify existing configuration after binary installation"
    [ "$CURRENT_CONFIG_SHA256" = "$EXISTING_CONFIG_SHA256" ] ||
        die "Existing configuration changed during upgrade; refusing to continue"
    API_KEY="$(grep -E '^CONCH_API_KEY=' "$ENV_FILE" 2>/dev/null | cut -d= -f2- || true)"
    PORT="$(grep -E '^CONCH_PORT=' "$ENV_FILE" 2>/dev/null | cut -d= -f2- || echo "$PORT")"
    ALLOW_NO_AUTH="$(grep -E '^CONCH_ALLOW_NO_AUTH=' "$ENV_FILE" 2>/dev/null | cut -d= -f2- || echo "$ALLOW_NO_AUTH")"
    ok "Existing configuration preserved without changes: $ENV_FILE"
else
    push_rollback "rm -f '$ENV_FILE'"
    if [ -z "$API_KEY" ]; then
        API_KEY="$(head -c 32 /dev/urandom 2>/dev/null | base64 | tr -d '=+' | head -c 43)"
        ok "Generated new API key"
    fi
    cat > "$ENV_FILE.tmp.$$" <<EOF
CONCH_API_KEY=$API_KEY
CONCH_PORT=$PORT
CONCH_HOST=$HOST
CONCH_TIMEOUT=$TIMEOUT
CONCH_MAX_TIMEOUT=$MAX_TIMEOUT
CONCH_ALLOW_NO_AUTH=$ALLOW_NO_AUTH
CONCH_JOB_RETENTION_HOURS=$JOB_RETENTION_HOURS
EOF
    chmod 600 "$ENV_FILE.tmp.$$"
    mv -f "$ENV_FILE.tmp.$$" "$ENV_FILE"
    ok "Config written atomically: $ENV_FILE"
fi

if [ "$ALLOW_NO_AUTH" = "true" ]; then
    warn "Authentication is DISABLED - do not expose to untrusted networks!"
fi

STEP=$((STEP + 1))

# ============================================================================
# Step 6 - Register & start service
# ============================================================================
step $STEP $TOTAL_STEPS "Registering service..."

if [ "$PLATFORM" = "linux" ]; then
    if [ "$MODE" = "user" ]; then
        UNIT_FILE="${HOME}/.config/systemd/user/conch.service"
        UNIT_WANTEDBY="default.target"
        UNIT_DESC="Conch Shell Server (user)"
        SVCCTL=(systemctl --user)
        SVCCTL_DISPLAY="systemctl --user"
    else
        UNIT_FILE="/etc/systemd/system/conch.service"
        UNIT_WANTEDBY="multi-user.target"
        UNIT_DESC="Conch Shell Server"
        SVCCTL=(systemctl)
        SVCCTL_DISPLAY="systemctl"
    fi
    mkdir -p "$(dirname "${UNIT_FILE}")"

    # ExecStart is tokenized, so quote its path. EnvironmentFile consumes the
    # remainder as one path and must stay unquoted. Escape percent specifiers in both.
    systemd_escape_path() {
        local escaped="$1"
        escaped="${escaped//\\/\\\\}"
        escaped="${escaped//\"/\\\"}"
        escaped="${escaped//%/%%}"
        printf '%s' "$escaped"
    }
    UNIT_BIN_PATH="$(systemd_escape_path "$BIN_PATH")"
    UNIT_ENV_PATH="$(systemd_escape_path "$ENV_FILE")"

    # Update the existing unit in place and retain a restorable registration.
    UNIT_BACKUP="${UNIT_FILE}.previous"
    UNIT_EXISTED=false
    UNIT_WAS_ENABLED=false
    if [ -f "${UNIT_FILE}" ]; then
        UNIT_EXISTED=true
        cp -f "${UNIT_FILE}" "$UNIT_BACKUP"
        "${SVCCTL[@]}" is-enabled --quiet conch 2>/dev/null && UNIT_WAS_ENABLED=true
        "${SVCCTL[@]}" stop conch 2>/dev/null || true
    fi

    cat > "${UNIT_FILE}" <<UNIT
[Unit]
Description=${UNIT_DESC}
After=network.target

[Service]
Type=simple
ExecStart="${UNIT_BIN_PATH}"
EnvironmentFile=${UNIT_ENV_PATH}
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=${UNIT_WANTEDBY}
UNIT

    if $UNIT_EXISTED; then
        push_rollback "$SVCCTL_DISPLAY stop conch 2>/dev/null || true; cp -f '$UNIT_BACKUP' '$UNIT_FILE'; $SVCCTL_DISPLAY daemon-reload; if $UNIT_WAS_ENABLED; then $SVCCTL_DISPLAY enable conch 2>/dev/null || true; else $SVCCTL_DISPLAY disable conch 2>/dev/null || true; fi"
    else
        push_rollback "$SVCCTL_DISPLAY stop conch 2>/dev/null || true; $SVCCTL_DISPLAY disable conch 2>/dev/null || true; rm -f '$UNIT_FILE'; $SVCCTL_DISPLAY daemon-reload"
    fi
    "${SVCCTL[@]}" daemon-reload
    ok "systemd unit created"

    ENABLE_BOOT=true
    DO_START=true
    if $NO_START; then
        ENABLE_BOOT=false
        DO_START=false
    elif ! $YES_ALL; then
        echo ""
        if [ "$MODE" = "user" ]; then
            prompt "Enable auto-start when this user logs in?" "y" || ENABLE_BOOT=false
        else
            prompt "Enable auto-start on boot?" "y" || ENABLE_BOOT=false
        fi
        prompt "Start service now?" "y" || DO_START=false
    fi

    if $ENABLE_BOOT; then
        "${SVCCTL[@]}" enable conch 2>/dev/null ||
            die "Failed to enable $MODE-mode auto-start"
        if [ "$MODE" = "user" ]; then
            ok "Auto-start at user login: enabled"
            info "For start before login, explicitly enable linger if appropriate: loginctl enable-linger $(id -un)"
        else
            ok "Auto-start on boot: enabled"
        fi
    else
        ok "Auto-start: skipped"
    fi

    if $DO_START; then
        "${SVCCTL[@]}" start conch 2>/dev/null || die "Failed to start service"
        sleep 2
        if "${SVCCTL[@]}" is-active --quiet conch 2>/dev/null; then
            ok "Service started"
            if command -v curl &>/dev/null; then
                health="$(curl -fsS --max-time 5 "http://localhost:${PORT}/health")" || die "Health check failed"
                echo "$health" | grep -q '"status":"ok"' || die "Health response is not ok"
                echo "$health" | grep -q '"version":"' || die "Health response has no version"
                if [ "$RELEASE_VERSION" != "latest" ]; then
                    echo "$health" | grep -q "\"version\":\"$RELEASE_VERSION\"" ||
                        die "Installed version does not match $RELEASE_VERSION"
                fi
                ok "Health/version check passed"
            fi
        else
            die "Service did not reach active state. Check: $SVCCTL_DISPLAY status conch"
        fi
    else
        info "Service installed but not started. Start with: $SVCCTL_DISPLAY start conch"
    fi

elif [ "$PLATFORM" = "termux" ]; then
    mkdir -p "${SVC_DIR}"

    # Preserve and stop an existing runit service before replacing its run script.
    RUN_EXISTED=false
    RUN_BACKUP="${SVC_DIR}/run.previous"
    if [ -f "${SVC_DIR}/run" ]; then
        RUN_EXISTED=true
        cp -f "${SVC_DIR}/run" "$RUN_BACKUP"
        sv stop "${SVC_DIR}" >/dev/null 2>&1 || true
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

    if $RUN_EXISTED; then
        push_rollback "sv stop '$SVC_DIR' 2>/dev/null || true; cp -f '$RUN_BACKUP' '$SVC_DIR/run'; chmod 755 '$SVC_DIR/run'"
    else
        push_rollback "sv stop '$SVC_DIR' 2>/dev/null || true; rm -rf '$SVC_DIR'"
    fi

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
        prompt "Enable auto-start on boot? (requires Termux:Boot app)" "y" && SETUP_BOOT=true
    elif ! $NO_START; then
        SETUP_BOOT=true
    fi

    if $SETUP_BOOT; then
        if [ -d "${BOOT_DIR}" ]; then
            cat > "${BOOT_DIR}/01-conch" <<BOOT
#!/data/data/com.termux/files/usr/bin/sh
sv up "${SVC_DIR}" 2>/dev/null || true
BOOT
            chmod 755 "${BOOT_DIR}/01-conch"
            ok "Boot script installed"
        else
            warn "Termux:Boot directory not found. Auto-start on boot NOT set up."
            warn "  Install Termux:Boot APK from F-Droid: https://f-droid.org/packages/com.termux.boot/"
            warn "  Open the app once, then: mkdir -p ~/.termux/boot"
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
        sv up "$SVC_DIR" >/dev/null 2>&1 || { sleep 2; sv up "$SVC_DIR" >/dev/null 2>&1; } ||
            die "Failed to start runit service"
        sleep 1
        if command -v curl &>/dev/null; then
            health="$(curl -fsS --max-time 5 "http://localhost:$PORT/health")" ||
                die "Health check failed"
            echo "$health" | grep -q '"status":"ok"' || die "Health response is not ok"
            if [ "$RELEASE_VERSION" != "latest" ]; then
                echo "$health" | grep -q "\"version\":\"$RELEASE_VERSION\"" ||
                    die "Installed version does not match $RELEASE_VERSION"
            fi
        fi
        ok "Service started and verified"
    else
        info "Service installed but not started. Start with: sv up ${SVC_DIR}"
    fi
fi

# ============================================================================
# Done
# ============================================================================
rpad=$((46 - 2 - 21))  # "Installation Complete" = 21 chars
echo ""
echo "  ${GREEN}+----------------------------------------------+${RESET}"
printf "  ${GREEN}|  ${BOLD}%s${RESET}%*s${GREEN}|${RESET}\n" "Installation Complete" "$rpad" ""
echo "  ${GREEN}+----------------------------------------------+${RESET}"
echo ""
echo "  ${CYAN}Health check:${RESET}   curl -s http://localhost:${PORT}/health"
if [ -n "$API_KEY" ]; then
    echo "  ${CYAN}API key:${RESET}       ${API_KEY}"
else
    echo "  ${CYAN}API key:${RESET}       not required (authentication disabled)"
fi
echo "  ${CYAN}Config file:${RESET}   ${ENV_FILE}"
echo ""
if [ "$PLATFORM" = "linux" ]; then
    echo "  ${CYAN}Manage:${RESET}"
    if [ "$MODE" = "user" ]; then
        echo "    systemctl --user {start,stop,restart,status} conch"
        echo "    journalctl --user -u conch -f"
    else
        echo "    systemctl {start,stop,restart,status} conch"
        echo "    journalctl -u conch -f"
    fi
    echo "    Uninstall: $0 --uninstall --mode $MODE"
elif [ "$PLATFORM" = "termux" ]; then
    echo "  ${CYAN}Manage:${RESET}"
    echo "    sv {up,down,status} conch"
    echo "    Uninstall: $0 --uninstall"
fi
echo ""

) # end subshell - protects the user's shell from exit
