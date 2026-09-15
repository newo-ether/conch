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
user_mode_tmp=""
trap 'rm -rf -- "$checksum_contract_tmp" "${user_mode_tmp:-}"' EXIT
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

assert_contains 'DEFAULT_MAX_TIMEOUT=1800'
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
assert_contains 'SELECTED_MODE="user"'
assert_contains 'MODE="$SELECTED_MODE"'
assert_contains 'Both system and user installations exist. Specify --mode system or --mode user.'
assert_contains 'The custom --prefix already exists but its installation mode cannot be identified.'

mode_selector="$(sed -n '/^select_install_mode() {$/,/^}$/p' "$installer")"
select_mode_case() {
    MODE_SELECTOR="$mode_selector" bash -c '
        set -euo pipefail
        die() { printf "%s\n" "$*" >&2; exit 1; }
        eval "$MODE_SELECTOR"
        SELECTED_MODE=""
        select_install_mode "$1" "$2" "$3" "$4" "$5" "$6"
        printf "%s\n" "$SELECTED_MODE"
    ' _ "$@"
}
[ "$(select_mode_case "" false false false false false)" = "user" ] ||
    { echo "new install did not default to user mode" >&2; exit 1; }
[ "$(select_mode_case "" false false false true false)" = "system" ] ||
    { echo "existing system install was not retained" >&2; exit 1; }
[ "$(select_mode_case "" false false false false true)" = "user" ] ||
    { echo "existing user install was not retained" >&2; exit 1; }
[ "$(select_mode_case "system" false false false false false)" = "system" ] ||
    { echo "explicit system mode was not retained" >&2; exit 1; }
if select_mode_case "" false false false true true >/dev/null 2>&1; then
    echo "installer guessed a mode when both modes exist" >&2
    exit 1
fi
if select_mode_case "" false true true false false >/dev/null 2>&1; then
    echo "installer guessed the mode of an existing custom prefix" >&2
    exit 1
fi

if bash "$installer" --port invalid --no-start >/dev/null 2>&1; then
    echo "installer accepted an invalid port" >&2
    exit 1
fi
if bash "$installer" --version v1x0x9 --no-start >/dev/null 2>&1; then
    echo "installer accepted an invalid release version" >&2
    exit 1
fi
if bash "$installer" --mode invalid --no-start >/dev/null 2>&1; then
    echo "installer accepted an invalid mode" >&2
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

for marker in \
    'Existing $MODE-mode installation detected' \
    'Refusing an implicit migration' \
    'systemctl --user show-environment' \
    'ExecStart="${UNIT_BIN_PATH}"' \
    'EnvironmentFile="${UNIT_ENV_PATH}"' \
    'Failed to enable $MODE-mode auto-start' \
    'Auto-start at user login: enabled' \
    'push_rollback "rm -f -- '\''$BIN_PATH'\''"'
do
    assert_contains "$marker"
done
if grep -Eq '^[[:space:]]*(sudo[[:space:]]+)?loginctl[[:space:]]+enable-linger' "$installer"; then
    echo "installer must not silently persist system linger policy" >&2
    exit 1
fi

# Exercise a complete, isolated user-mode install/upgrade/uninstall without
# touching the host systemd manager. A custom prefix containing a space proves
# that the generated unit quotes paths correctly.
user_mode_tmp="$(mktemp -d)"
test_home="$user_mode_tmp/home"
test_prefix="$user_mode_tmp/install root"
stub_bin="$user_mode_tmp/stubs"
systemctl_log="$user_mode_tmp/systemctl.log"
mkdir -p "$test_home" "$stub_bin"

cat > "$stub_bin/systemctl" <<'SYSTEMCTL'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SYSTEMCTL_LOG"
case "$*" in
    "--user show-environment")
        [ "${SYSTEMCTL_NO_MANAGER:-0}" = "1" ] && exit 1
        exit 0
        ;;
    "--user is-active --quiet conch") exit 1 ;;
    "--user is-enabled --quiet conch")
        [ "${SYSTEMCTL_IS_ENABLED:-0}" = "1" ] && exit 0
        exit 1
        ;;
    "--user stop conch") exit 0 ;;
    "--user daemon-reload") exit 0 ;;
    "--user enable conch") exit 0 ;;
    "--user disable conch") exit 0 ;;
    "--user start conch")
        [ "${SYSTEMCTL_FAIL_START:-0}" = "1" ] && exit 1
        exit 0
        ;;
    *) echo "unexpected systemctl call: $*" >&2; exit 64 ;;
esac
SYSTEMCTL
chmod +x "$stub_bin/systemctl"

make_fake_binary() {
    local path="$1" label="$2"
    printf '#!/usr/bin/env bash\nif [ "${1:-}" = "--version" ]; then echo "%s v9.9.9"; exit 0; fi\nexit 0\n#' "$label" > "$path"
    head -c 1049000 /dev/zero | tr '\0' '#' >> "$path"
    printf '\n' >> "$path"
    chmod +x "$path"
}
fake_server="$user_mode_tmp/conch-source"
fake_mcp="$user_mode_tmp/conch-mcp-source"
make_fake_binary "$fake_server" "conch"
make_fake_binary "$fake_mcp" "conch-mcp"

run_user_installer() {
    HOME="$test_home" \
    SYSTEMCTL_LOG="$systemctl_log" \
    PATH="$stub_bin:$PATH" \
        bash "$installer" \
        --prefix "$test_prefix" \
        --bin "$fake_server" \
        --mcp-bin "$fake_mcp" \
        --version v9.9.9 \
        --no-start \
        --yes \
        "$@"
}

ambiguous_prefix="$user_mode_tmp/ambiguous root"
mkdir -p "$ambiguous_prefix"
: > "$systemctl_log"
if HOME="$test_home" \
   SYSTEMCTL_LOG="$systemctl_log" \
   PATH="$stub_bin:$PATH" \
       bash "$installer" \
       --prefix "$ambiguous_prefix" \
       --bin "$fake_server" \
       --mcp-bin "$fake_mcp" \
       --version v9.9.9 \
       --no-start --yes \
       > "$user_mode_tmp/ambiguous.log" 2>&1
then
    echo "installer guessed the mode of an existing custom prefix" >&2
    exit 1
fi
grep -Fq 'installation mode cannot be identified' "$user_mode_tmp/ambiguous.log" ||
    { echo "ambiguous custom prefix did not explain how to select a mode" >&2; exit 1; }
[ ! -s "$systemctl_log" ] ||
    { echo "ambiguous mode detection contacted the user manager" >&2; exit 1; }

run_user_installer --api-key regression-test-key > "$user_mode_tmp/install.log"
unit_file="$test_home/.config/systemd/user/conch.service"
[ -f "$unit_file" ] || { echo "user unit was not created" >&2; exit 1; }
grep -Fq "ExecStart=\"$test_prefix/conch\"" "$unit_file" ||
    { echo "user unit did not quote ExecStart" >&2; exit 1; }
grep -Fq "EnvironmentFile=\"$test_prefix/env\"" "$unit_file" ||
    { echo "user unit did not quote EnvironmentFile" >&2; exit 1; }
grep -Fq 'CONCH_API_KEY=regression-test-key' "$test_prefix/env" ||
    { echo "user config was not written" >&2; exit 1; }

rm -f "$unit_file"
: > "$systemctl_log"
config_before="$(sha256sum "$test_prefix/env" | awk '{print $1}')"
run_user_installer --mode user > "$user_mode_tmp/stale-upgrade.log"
config_after="$(sha256sum "$test_prefix/env" | awk '{print $1}')"
[ "$config_before" = "$config_after" ] ||
    { echo "user upgrade changed existing configuration" >&2; exit 1; }
if grep -Fq -- '--user stop conch' "$systemctl_log"; then
    echo "stale-file upgrade tried to stop a nonexistent user unit" >&2
    exit 1
fi

: > "$systemctl_log"
run_user_installer > "$user_mode_tmp/unit-upgrade.log"
grep -Fq -- '--user stop conch' "$systemctl_log" ||
    { echo "user unit upgrade did not stop its service" >&2; exit 1; }
if grep -Evq '^--user ' "$systemctl_log"; then
    echo "user-mode upgrade invoked system-level systemctl" >&2
    exit 1
fi

: > "$systemctl_log"
HOME="$test_home" \
SYSTEMCTL_LOG="$systemctl_log" \
PATH="$stub_bin:$PATH" \
    bash "$installer" \
    --uninstall --mode user --prefix "$test_prefix" --yes \
    > "$user_mode_tmp/uninstall.log"
[ ! -e "$unit_file" ] || { echo "user unit survived uninstall" >&2; exit 1; }
[ ! -e "$test_prefix" ] || { echo "user files survived uninstall" >&2; exit 1; }
if grep -Evq '^--user ' "$systemctl_log"; then
    echo "user-mode uninstall invoked system-level systemctl" >&2
    exit 1
fi

# A failed first start must roll back the new unit, enable state, binaries,
# configuration and newly created custom prefix.
failure_home="$user_mode_tmp/failure-home"
failure_prefix="$user_mode_tmp/failure root"
mkdir -p "$failure_home"
: > "$systemctl_log"
if HOME="$failure_home" \
   SYSTEMCTL_LOG="$systemctl_log" \
   SYSTEMCTL_FAIL_START=1 \
   PATH="$stub_bin:$PATH" \
       bash "$installer" \
       --mode user \
       --prefix "$failure_prefix" \
       --bin "$fake_server" \
       --mcp-bin "$fake_mcp" \
       --version v9.9.9 \
       --api-key rollback-test-key \
       --yes \
       > "$user_mode_tmp/failure.log" 2>&1
then
    echo "user-mode install unexpectedly survived a start failure" >&2
    exit 1
fi
[ ! -e "$failure_prefix" ] ||
    { echo "failed user install left its custom prefix behind" >&2; exit 1; }
[ ! -e "$failure_home/.config/systemd/user/conch.service" ] ||
    { echo "failed user install left its systemd unit behind" >&2; exit 1; }
grep -Fq -- '--user disable conch' "$systemctl_log" ||
    { echo "failed user install did not roll back auto-start" >&2; exit 1; }

# User-mode uninstall remains possible after logout when the user manager is
# unavailable; in that case only the owned unit and files are removed.
offline_home="$user_mode_tmp/offline-home"
offline_prefix="$user_mode_tmp/offline root"
mkdir -p "$offline_home"
HOME="$offline_home" \
SYSTEMCTL_LOG="$systemctl_log" \
PATH="$stub_bin:$PATH" \
    bash "$installer" \
    --mode user \
    --prefix "$offline_prefix" \
    --bin "$fake_server" \
    --mcp-bin "$fake_mcp" \
    --version v9.9.9 \
    --api-key offline-test-key \
    --no-start --yes \
    > "$user_mode_tmp/offline-install.log"
: > "$systemctl_log"
HOME="$offline_home" \
SYSTEMCTL_LOG="$systemctl_log" \
SYSTEMCTL_NO_MANAGER=1 \
PATH="$stub_bin:$PATH" \
    bash "$installer" \
    --uninstall --mode user --prefix "$offline_prefix" --yes \
    > "$user_mode_tmp/offline-uninstall.log" 2>&1
[ ! -e "$offline_prefix" ] ||
    { echo "offline user uninstall left its custom prefix behind" >&2; exit 1; }
[ ! -e "$offline_home/.config/systemd/user/conch.service" ] ||
    { echo "offline user uninstall left its systemd unit behind" >&2; exit 1; }
if grep -Fq -- '--user stop conch' "$systemctl_log"; then
    echo "offline user uninstall contacted an unavailable user manager" >&2
    exit 1
fi

echo "install.sh hardening and isolated user-mode lifecycle checks passed."
