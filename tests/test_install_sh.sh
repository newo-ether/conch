#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
installer="$repo_root/scripts/install.sh"

bash -n "$installer"

if LC_ALL=C grep -n '[^ -~	]' "$installer"; then
    echo "installer must remain ASCII-only for cross-terminal portability" >&2
    exit 1
fi
if grep -Eq '(^|[^$])\{RESET\}' "$installer"; then
    echo "installer contains a literal, non-expanded {RESET} placeholder" >&2
    exit 1
fi

checksum_contract_tmp="$(mktemp -d)"
trap 'rm -rf -- "$checksum_contract_tmp"' EXIT
(
    CHECKSUM_MANIFEST="$checksum_contract_tmp/checksums.txt"
    GITHUB_RELEASES="https://example.invalid/releases"
    expected_hash="e26b899c9c77b3d29edd0f69d62e003992779dee4531908dc1aeb11d45482948"
    info() { printf 'INFO %s\n' "$*"; }
    download_url() { printf '%s  conch-linux-arm64\n' "$expected_hash" > "$2"; }
    eval "$(sed -n '/^expected_release_hash() {$/,/^}$/p' "$installer")"
    captured="$(expected_release_hash conch-linux-arm64 2> "$checksum_contract_tmp/stderr")"
    if [ "$captured" != "$expected_hash" ]; then
        echo "expected_release_hash polluted captured stdout: $captured" >&2
        exit 1
    fi
)

assert_contains() {
    local needle="$1"
    grep -Fq -- "$needle" "$installer" ||
        { echo "missing installer hardening marker: $needle" >&2; exit 1; }
}

assert_contains 'checksums.txt'
assert_contains 'Verified SHA-256'
assert_contains 'SERVICE_WAS_ACTIVE=false'
assert_contains 'systemctl stop conch 2>/dev/null || true; cp -f'
assert_contains 'sv stop '"'"'$SVC_DIR'"'"' 2>/dev/null || true; cp -f'
assert_contains 'Existing configuration preserved without changes'
assert_contains 'EXISTING_CONFIG_SHA256="$(file_sha256 "$ENV_FILE")"'
assert_contains 'Existing configuration changed during upgrade; refusing to continue'
assert_contains 'Configuration override flags cannot be used during an in-place upgrade'
assert_contains 'Existing API key contains unsupported characters and cannot be printed safely'
assert_contains 'API key:${RESET}       ${API_KEY}'
assert_contains '^v[0-9]+\.[0-9]+\.[0-9]+$'
assert_contains 'if $SERVICE_WAS_ACTIVE; then systemctl start conch'
assert_contains 'if $SERVICE_WAS_ACTIVE; then sv up'
assert_contains 'REPO_IS_TEMP=true'
assert_contains 'Retrying verified download for $name'
assert_contains 'Could not download and verify $SERVER_BIN_NAME'
assert_contains 'The existing installation was left unchanged.'

if bash "$installer" --port invalid --no-start >/dev/null 2>&1; then
    echo "installer accepted an invalid port" >&2
    exit 1
fi
if bash "$installer" --version v1x0x9 --no-start >/dev/null 2>&1; then
    echo "installer accepted an invalid release version" >&2
    exit 1
fi

if grep -Fq 'stored in protected config (not printed)' "$installer"; then
    echo "installer still hides the effective API key" >&2
    exit 1
fi
if grep -Fq '$API_KEY_SET && set_env_value' "$installer" ||
   grep -Fq '$PORT_SET && set_env_value' "$installer"; then
    echo "installer can overwrite an existing configuration" >&2
    exit 1
fi

unit_rollback="$(grep -F "cp -f '\$UNIT_BACKUP' '\$UNIT_FILE'" "$installer")"
if grep -Fq 'systemctl start' <<<"$unit_rollback"; then
    echo "systemd unit rollback starts before binary restoration" >&2
    exit 1
fi

run_rollback="$(grep -F "cp -f '\$RUN_BACKUP' '\$SVC_DIR/run'" "$installer")"
if grep -Fq 'sv up' <<<"$run_rollback"; then
    echo "runit script rollback starts before binary restoration" >&2
    exit 1
fi

echo "install.sh hardening checks passed."
