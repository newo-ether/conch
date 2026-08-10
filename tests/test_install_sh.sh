#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
installer="$repo_root/scripts/install.sh"

bash -n "$installer"

assert_contains() {
    local needle="$1"
    grep -Fq -- "$needle" "$installer" ||
        { echo "missing installer hardening marker: $needle" >&2; exit 1; }
}

assert_contains 'checksums.txt'
assert_contains 'Verified SHA-256'
assert_contains 'Preserving existing configuration and durable job settings'
assert_contains 'ENV_BACKUP="${ENV_FILE}.previous"'
assert_contains 'SERVICE_WAS_ACTIVE=false'
assert_contains 'systemctl stop conch 2>/dev/null || true; cp -f'
assert_contains 'sv stop '"'"'$SVC_DIR'"'"' 2>/dev/null || true; cp -f'
assert_contains 'stored in protected config (not printed)'
assert_contains '^v[0-9]+\.[0-9]+\.[0-9]+$'
assert_contains 'if $SERVICE_WAS_ACTIVE; then systemctl start conch'
assert_contains 'if $SERVICE_WAS_ACTIVE; then sv up'

if bash "$installer" --port invalid --no-start >/dev/null 2>&1; then
    echo "installer accepted an invalid port" >&2
    exit 1
fi
if bash "$installer" --version v1x0x9 --no-start >/dev/null 2>&1; then
    echo "installer accepted an invalid release version" >&2
    exit 1
fi

if grep -Eq 'API key:.*\$API_KEY|echo[[:space:]]+"?\$API_KEY' "$installer"; then
    echo "installer prints the API key" >&2
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
