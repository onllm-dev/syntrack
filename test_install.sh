#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════
# install.sh Test Suite
# Tests the interactive setup flow with mocked inputs.
#
# Usage: bash test_install.sh
# ═══════════════════════════════════════════════════════════════════════
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SCRIPT="${SCRIPT_DIR}/install.sh"
TEST_DIR=""
PASS=0
FAIL=0
TOTAL=0

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BOLD='\033[1m'; NC='\033[0m'

# ─── Test Helpers ───────────────────────────────────────────────────

setup() {
    TEST_DIR=$(mktemp -d)
    export ONWATCH_INSTALL_DIR="$TEST_DIR"
    export INSTALL_DIR="$TEST_DIR"
    export BIN_DIR="${TEST_DIR}/bin"
    mkdir -p "$TEST_DIR/bin" "$TEST_DIR/data"
}

teardown() {
    # Close fd 3 if open
    exec 3<&- 2>/dev/null || true
    [[ -n "$TEST_DIR" ]] && rm -rf "$TEST_DIR"
    TEST_DIR=""
}

# Source install.sh functions (without running main)
source_functions() {
    local func_script="${TEST_DIR}/functions.sh"
    # macOS head doesn't support -n -2, use wc+head
    local total keep
    total=$(wc -l < "$INSTALL_SCRIPT")
    keep=$((total - 2))
    head -n "$keep" "$INSTALL_SCRIPT" > "$func_script"
    # Override platform/download so they don't do real work
    cat >> "$func_script" <<'OVERRIDES'
detect_platform() { OS="darwin"; ARCH="arm64"; PLATFORM="darwin-arm64"; ASSET_NAME="onwatch-darwin-arm64"; }
download() { touch "${BIN_DIR}/onwatch"; chmod +x "${BIN_DIR}/onwatch"; }
OVERRIDES
    source "$func_script"
}

# Run interactive_setup with piped input via fd 3
run_setup() {
    local input="$1"
    exec 3< <(printf '%b' "$input")
    interactive_setup >/dev/null 2>&1
    local rc=$?
    exec 3<&- 2>/dev/null || true
    return $rc
}

env_val() {
    local key="$1"
    grep -E "^${key}=" "${TEST_DIR}/.env" 2>/dev/null | cut -d= -f2- | tr -d '[:space:]'
}

assert_eq() {
    local key="$1" expected="$2"
    local actual
    actual=$(env_val "$key")
    if [[ "$actual" != "$expected" ]]; then
        echo "ASSERT: $key expected='$expected' got='$actual'" >&2
        return 1
    fi
}

assert_set() {
    local key="$1"
    local val
    val=$(env_val "$key")
    if [[ -z "$val" ]]; then
        echo "ASSERT: $key should be set" >&2
        return 1
    fi
}

assert_unset() {
    local key="$1"
    if grep -qE "^${key}=" "${TEST_DIR}/.env" 2>/dev/null; then
        echo "ASSERT: $key should NOT be set" >&2
        return 1
    fi
}

run_test() {
    local name="$1"
    TOTAL=$((TOTAL + 1))
    printf "  ${BOLD}%2d.${NC} %-52s" "$TOTAL" "$name"
    local output
    if output=$("$name" 2>&1); then
        printf "${GREEN}PASS${NC}\n"
        PASS=$((PASS + 1))
    else
        printf "${RED}FAIL${NC}\n"
        if [[ -n "$output" ]]; then
            echo "$output" | head -3 | sed 's/^/      /'
        fi
        FAIL=$((FAIL + 1))
    fi
    teardown
}

# ─── Test Cases ─────────────────────────────────────────────────────

test_synthetic_only() {
    setup && source_functions
    run_setup "1\nsyn_test123456\nadmin\n\n9211\n60\n"
    assert_eq "SYNTHETIC_API_KEY" "syn_test123456" && \
    assert_unset "ZAI_API_KEY" && \
    assert_unset "ANTHROPIC_TOKEN" && \
    assert_eq "ONWATCH_ADMIN_USER" "admin" && \
    assert_eq "ONWATCH_PORT" "9211"
}

test_zai_only_default_url() {
    setup && source_functions
    run_setup "2\nzai_key_123\nY\nadmin\nmypass\n9211\n60\n"
    assert_unset "SYNTHETIC_API_KEY" && \
    assert_eq "ZAI_API_KEY" "zai_key_123" && \
    assert_eq "ZAI_BASE_URL" "https://api.z.ai/api" && \
    assert_unset "ANTHROPIC_TOKEN"
}

test_zai_only_custom_url() {
    setup && source_functions
    run_setup "2\nzai_key_456\nn\nhttps://open.bigmodel.cn/api\nadmin\ntestpass\n9211\n60\n"
    assert_eq "ZAI_API_KEY" "zai_key_456" && \
    assert_eq "ZAI_BASE_URL" "https://open.bigmodel.cn/api"
}

test_anthropic_only_manual_direct() {
    setup && source_functions
    detect_anthropic_token() { return 1; }
    run_setup "3\n1\nsk-ant-test123\nadmin\ntestpass\n9211\n60\n"
    assert_unset "SYNTHETIC_API_KEY" && \
    assert_unset "ZAI_API_KEY" && \
    assert_eq "ANTHROPIC_TOKEN" "sk-ant-test123"
}

test_anthropic_only_manual_help() {
    setup && source_functions
    detect_anthropic_token() { return 1; }
    run_setup "3\n2\nsk-ant-help-test\nadmin\ntestpass\n9211\n60\n"
    assert_eq "ANTHROPIC_TOKEN" "sk-ant-help-test"
}

test_anthropic_auto_detected_accepted() {
    setup && source_functions
    detect_anthropic_token() { printf 'auto-detected-token-12345'; }
    run_setup "3\nY\nadmin\ntestpass\n9211\n60\n"
    assert_eq "ANTHROPIC_TOKEN" "auto-detected-token-12345"
}

test_anthropic_auto_rejected_manual() {
    setup && source_functions
    detect_anthropic_token() { printf 'auto-detected-token-12345'; }
    run_setup "3\nn\n1\nmanual-token\nadmin\ntestpass\n9211\n60\n"
    assert_eq "ANTHROPIC_TOKEN" "manual-token"
}

test_multiple_syn_and_anth() {
    setup && source_functions
    detect_anthropic_token() { return 1; }
    run_setup "4\ny\nsyn_multi_123\nN\ny\n1\nanth-multi\nadmin\ntestpass\n9211\n60\n"
    assert_eq "SYNTHETIC_API_KEY" "syn_multi_123" && \
    assert_unset "ZAI_API_KEY" && \
    assert_eq "ANTHROPIC_TOKEN" "anth-multi"
}

test_multiple_zai_only() {
    setup && source_functions
    run_setup "4\nN\ny\nzai_multi_456\nY\nN\nadmin\ntestpass\n9211\n60\n"
    assert_unset "SYNTHETIC_API_KEY" && \
    assert_eq "ZAI_API_KEY" "zai_multi_456" && \
    assert_unset "ANTHROPIC_TOKEN"
}

test_multiple_all_three() {
    setup && source_functions
    detect_anthropic_token() { return 1; }
    run_setup "4\ny\nsyn_all_789\ny\nzai_all_789\nY\ny\n1\nanth_all_789\nadmin\ntestpass\n9211\n60\n"
    assert_eq "SYNTHETIC_API_KEY" "syn_all_789" && \
    assert_eq "ZAI_API_KEY" "zai_all_789" && \
    assert_eq "ANTHROPIC_TOKEN" "anth_all_789"
}

test_all_available_choice5() {
    setup && source_functions
    detect_anthropic_token() { return 1; }
    run_setup "5\nsyn_all_avail\nzai_all_avail\nY\n1\nanth_all_avail\nadmin\ntestpass\n9211\n60\n"
    assert_eq "SYNTHETIC_API_KEY" "syn_all_avail" && \
    assert_eq "ZAI_API_KEY" "zai_all_avail" && \
    assert_eq "ANTHROPIC_TOKEN" "anth_all_avail"
}

test_custom_port_and_interval() {
    setup && source_functions
    run_setup "1\nsyn_custom_123\nmyuser\nmypass\n8080\n30\n"
    assert_eq "ONWATCH_PORT" "8080" && \
    assert_eq "ONWATCH_POLL_INTERVAL" "30" && \
    assert_eq "ONWATCH_ADMIN_USER" "myuser"
}

test_upgrade_add_zai() {
    setup && source_functions
    cat > "${TEST_DIR}/.env" <<EOF
SYNTHETIC_API_KEY=syn_existing_key
ONWATCH_ADMIN_USER=admin
ONWATCH_ADMIN_PASS=existingpass
ONWATCH_PORT=9211
EOF
    run_setup "y\nzai_upgrade_123\nY\nN\n"
    assert_eq "SYNTHETIC_API_KEY" "syn_existing_key" && \
    assert_set "ZAI_API_KEY"
}

test_upgrade_all_configured_skips() {
    setup && source_functions
    cat > "${TEST_DIR}/.env" <<EOF
SYNTHETIC_API_KEY=syn_full_key
ZAI_API_KEY=zai_full_key
ANTHROPIC_TOKEN=anth_full_token
ONWATCH_ADMIN_USER=admin
ONWATCH_ADMIN_PASS=pass
ONWATCH_PORT=9211
EOF
    run_setup ""
    assert_eq "SYNTHETIC_API_KEY" "syn_full_key" && \
    assert_eq "ZAI_API_KEY" "zai_full_key" && \
    assert_eq "ANTHROPIC_TOKEN" "anth_full_token"
}

test_upgrade_auto_detect_anthropic() {
    setup && source_functions
    detect_anthropic_token() { printf 'auto-upgrade-token'; }
    cat > "${TEST_DIR}/.env" <<EOF
SYNTHETIC_API_KEY=syn_existing
ONWATCH_ADMIN_USER=admin
ONWATCH_ADMIN_PASS=pass
ONWATCH_PORT=9211
EOF
    run_setup "N\nY\n"
    assert_set "ANTHROPIC_TOKEN"
}

test_has_anthropic_key_rejects_placeholder() {
    setup && source_functions
    cat > "${TEST_DIR}/.env" <<EOF
ANTHROPIC_TOKEN=your_token_here
EOF
    if has_anthropic_key; then
        echo "Placeholder should not count as configured" >&2
        return 1
    fi
}

test_has_anthropic_key_accepts_real() {
    setup && source_functions
    cat > "${TEST_DIR}/.env" <<EOF
ANTHROPIC_TOKEN=sk-ant-real-token
EOF
    has_anthropic_key
}

test_env_get_reads_values() {
    setup && source_functions
    cat > "${TEST_DIR}/.env" <<EOF
SYNTHETIC_API_KEY=syn_test
ZAI_API_KEY=zai_test
EOF
    local val
    val=$(env_get "SYNTHETIC_API_KEY")
    [[ "$val" == "syn_test" ]]
}

test_multiple_none_triggers_retry() {
    setup && source_functions
    # choice=4, all N (triggers retry), then add Synthetic on retry
    run_setup "4\nN\nN\nN\ny\nsyn_retry_123\nadmin\ntestpass\n9211\n60\n"
    assert_eq "SYNTHETIC_API_KEY" "syn_retry_123"
}

test_detect_anthropic_file_fallback() {
    setup && source_functions
    local claude_dir="${TEST_DIR}/.claude"
    mkdir -p "$claude_dir"
    cat > "${claude_dir}/.credentials.json" <<'EOF'
{"claudeAiOauth":{"accessToken":"file-fallback-token-xyz"}}
EOF
    # Override detect to simulate Linux file fallback (macOS uses keychain, not files)
    detect_anthropic_token() {
        local creds_json=""
        if [[ -f "$HOME/.claude/.credentials.json" ]]; then
            creds_json=$(cat "$HOME/.claude/.credentials.json" 2>/dev/null) || true
        fi
        if [[ -z "$creds_json" ]]; then return 1; fi
        local token=""
        token=$(printf '%s' "$creds_json" | python3 -c "import sys,json; print(json.loads(sys.stdin.read())['claudeAiOauth']['accessToken'])" 2>/dev/null) || true
        if [[ -z "$token" ]]; then return 1; fi
        printf '%s' "$token"
    }
    local orig_home="$HOME"
    export HOME="$TEST_DIR"
    local token=""
    token=$(detect_anthropic_token 2>/dev/null) || true
    export HOME="$orig_home"
    [[ "$token" == "file-fallback-token-xyz" ]]
}

# ─── Run All Tests ──────────────────────────────────────────────────

printf "\n${BOLD}  install.sh Test Suite${NC}\n"
printf "  ${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n\n"

run_test test_synthetic_only
run_test test_zai_only_default_url
run_test test_zai_only_custom_url
run_test test_anthropic_only_manual_direct
run_test test_anthropic_only_manual_help
run_test test_anthropic_auto_detected_accepted
run_test test_anthropic_auto_rejected_manual
run_test test_multiple_syn_and_anth
run_test test_multiple_zai_only
run_test test_multiple_all_three
run_test test_all_available_choice5
run_test test_custom_port_and_interval
run_test test_upgrade_add_zai
run_test test_upgrade_all_configured_skips
run_test test_upgrade_auto_detect_anthropic
run_test test_has_anthropic_key_rejects_placeholder
run_test test_has_anthropic_key_accepts_real
run_test test_env_get_reads_values
run_test test_multiple_none_triggers_retry
run_test test_detect_anthropic_file_fallback

printf "\n  ${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
printf "  ${BOLD}Results:${NC} %d/%d passed" "$PASS" "$TOTAL"
if [[ $FAIL -gt 0 ]]; then
    printf ", ${RED}%d failed${NC}" "$FAIL"
fi
printf "\n\n"

exit $FAIL
