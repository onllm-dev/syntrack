#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════
# SynTrack Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/onllm-dev/syntrack/main/install.sh | bash
# ═══════════════════════════════════════════════════════════════════════
set -euo pipefail

INSTALL_DIR="${SYNTRACK_INSTALL_DIR:-$HOME/.syntrack}"
BIN_DIR="${INSTALL_DIR}/bin"
REPO="onllm-dev/syntrack"
SERVICE_NAME="syntrack"
SYSTEMD_MODE="user"  # "user" or "system" — auto-detected at runtime

# Collected during interactive setup, used by start_service
SETUP_USERNAME=""
SETUP_PASSWORD=""
SETUP_PORT=""

# ─── Colors ───────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; BOLD='\033[1m'
DIM='\033[2m'; NC='\033[0m'

info()    { printf "  ${BLUE}info${NC}  %s\n" "$*"; }
ok()      { printf "  ${GREEN} ok ${NC}  %s\n" "$*"; }
warn()    { printf "  ${YELLOW}warn${NC}  %s\n" "$*"; }
fail()    { printf "  ${RED}fail${NC}  %s\n" "$*" >&2; exit 1; }

# ─── systemd Helpers ────────────────────────────────────────────────
# Wrappers that use --user or system-wide mode based on SYSTEMD_MODE
_systemctl() {
    if [[ "$SYSTEMD_MODE" == "system" ]]; then
        systemctl "$@"
    else
        systemctl --user "$@"
    fi
}

_journalctl() {
    if [[ "$SYSTEMD_MODE" == "system" ]]; then
        journalctl -u syntrack "$@"
    else
        journalctl --user -u syntrack "$@"
    fi
}

_sctl_cmd() {
    if [[ "$SYSTEMD_MODE" == "system" ]]; then
        echo "systemctl"
    else
        echo "systemctl --user"
    fi
}

_jctl_cmd() {
    if [[ "$SYSTEMD_MODE" == "system" ]]; then
        echo "journalctl -u syntrack"
    else
        echo "journalctl --user -u syntrack"
    fi
}

# ─── Input Helpers ──────────────────────────────────────────────────

# Generate a random 12-char alphanumeric password
generate_password() {
    LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 12
}

# Numbered menu, returns selection number
# Usage: choice=$(prompt_choice "Which provider?" "Synthetic only" "Z.ai only" "Both")
prompt_choice() {
    local prompt="$1"; shift
    local options=("$@")
    printf "\n  ${BOLD}%s${NC}\n" "$prompt"
    for i in "${!options[@]}"; do
        printf "    ${CYAN}%d)${NC} %s\n" "$((i+1))" "${options[$i]}"
    done
    while true; do
        printf "  ${BOLD}>${NC} "
        read -u 3 -r choice
        if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#options[@]} )); then
            echo "$choice"
            return
        fi
        printf "  ${RED}Please enter 1-%d${NC}\n" "${#options[@]}"
    done
}

# Read a secret value (no echo), show masked version, validate with callback
# Usage: prompt_secret "Synthetic API key" synthetic_key "starts_with_syn"
prompt_secret() {
    local prompt="$1" validation="$2"
    local value=""
    while true; do
        printf "  %s: " "$prompt"
        read -u 3 -rs value
        echo ""
        if [[ -z "$value" ]]; then
            printf "  ${RED}Cannot be empty${NC}\n"
            continue
        fi
        # Run validation
        if eval "$validation \"$value\""; then
            local masked
            if [[ ${#value} -gt 10 ]]; then
                masked="${value:0:6}...${value: -4}"
            else
                masked="${value:0:3}..."
            fi
            printf "  ${GREEN}✓${NC} ${DIM}%s${NC}\n" "$masked"
            echo "$value"
            return
        fi
    done
}

# Prompt with a default value shown in brackets
# Usage: result=$(prompt_with_default "Dashboard port" "9211")
prompt_with_default() {
    local prompt="$1" default="$2"
    printf "  %s ${DIM}[%s]${NC}: " "$prompt" "$default"
    read -u 3 -r value
    if [[ -z "$value" ]]; then
        echo "$default"
    else
        echo "$value"
    fi
}

# ─── Validation Helpers ─────────────────────────────────────────────

validate_synthetic_key() {
    local val="$1"
    if [[ "$val" == syn_* ]]; then
        return 0
    fi
    printf "  ${RED}Key must start with 'syn_'${NC}\n"
    return 1
}

validate_nonempty() {
    local val="$1"
    if [[ -n "$val" ]]; then
        return 0
    fi
    printf "  ${RED}Cannot be empty${NC}\n"
    return 1
}

validate_https_url() {
    local val="$1"
    if [[ "$val" == https://* ]]; then
        return 0
    fi
    printf "  ${RED}URL must start with 'https://'${NC}\n"
    return 1
}

validate_port() {
    local val="$1"
    if [[ "$val" =~ ^[0-9]+$ ]] && (( val >= 1 && val <= 65535 )); then
        return 0
    fi
    printf "  ${RED}Must be a number between 1 and 65535${NC}\n"
    return 1
}

validate_interval() {
    local val="$1"
    if [[ "$val" =~ ^[0-9]+$ ]] && (( val >= 10 && val <= 3600 )); then
        return 0
    fi
    printf "  ${RED}Must be a number between 10 and 3600${NC}\n"
    return 1
}

# ─── Detect Platform ─────────────────────────────────────────────────
detect_platform() {
    local os arch
    os="$(uname -s)"
    arch="$(uname -m)"

    case "$os" in
        Linux)   OS="linux" ;;
        Darwin)  OS="darwin" ;;
        MINGW*|MSYS*|CYGWIN*)
            fail "Windows detected. Download manually: https://github.com/$REPO/releases" ;;
        *) fail "Unsupported OS: $os (supported: Linux, macOS)" ;;
    esac

    case "$arch" in
        x86_64|amd64)   ARCH="amd64" ;;
        aarch64|arm64)  ARCH="arm64" ;;
        *) fail "Unsupported architecture: $arch (supported: x86_64, arm64)" ;;
    esac

    PLATFORM="${OS}-${ARCH}"
    ASSET_NAME="syntrack-${PLATFORM}"
}

# ─── Stop Existing Instance ──────────────────────────────────────────
stop_existing() {
    if [[ -f "${BIN_DIR}/syntrack" ]]; then
        if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
            _systemctl stop syntrack 2>/dev/null || true
        else
            "${BIN_DIR}/syntrack" stop 2>/dev/null || true
        fi
        sleep 1
    fi
}

# ─── Download Binary ─────────────────────────────────────────────────
download() {
    local url="https://github.com/${REPO}/releases/latest/download/${ASSET_NAME}"
    local dest="${BIN_DIR}/syntrack"

    info "Downloading syntrack for ${BOLD}${PLATFORM}${NC}..."

    if command -v curl &>/dev/null; then
        if ! curl -fsSL -o "$dest" "$url"; then
            fail "Download failed. Check your internet connection.\n       URL: $url"
        fi
    elif command -v wget &>/dev/null; then
        if ! wget -q -O "$dest" "$url"; then
            fail "Download failed. Check your internet connection.\n       URL: $url"
        fi
    else
        fail "curl or wget is required"
    fi

    chmod +x "$dest"

    local ver
    ver="$("$dest" --version 2>/dev/null | head -1 || echo "unknown")"
    ok "Installed ${BOLD}syntrack ${ver}${NC}"
}

# ─── Create Wrapper Script ───────────────────────────────────────────
# The binary loads .env from the working directory. This wrapper ensures
# we always cd to ~/.syntrack before running, so .env is always found.
create_wrapper() {
    local wrapper="${INSTALL_DIR}/syntrack"

    cat > "$wrapper" <<WRAPPER
#!/usr/bin/env bash
cd "\$HOME/.syntrack" 2>/dev/null && exec "\$HOME/.syntrack/bin/syntrack" "\$@"
WRAPPER
    chmod +x "$wrapper"
}

# ─── .env Helpers ───────────────────────────────────────────────────

# Read a value from the existing .env file
# Usage: val=$(env_get "SYNTHETIC_API_KEY")
env_get() {
    local key="$1" env_file="${INSTALL_DIR}/.env"
    grep -E "^${key}=" "$env_file" 2>/dev/null | cut -d= -f2- | tr -d '[:space:]'
}

# Check if a provider key is configured (non-empty, not a placeholder)
has_synthetic_key() {
    local val
    val="$(env_get SYNTHETIC_API_KEY)"
    [[ -n "$val" && "$val" != "syn_your_api_key_here" ]]
}

has_zai_key() {
    local val
    val="$(env_get ZAI_API_KEY)"
    [[ -n "$val" && "$val" != "your_zai_api_key_here" ]]
}

# Append a provider section to the existing .env
append_synthetic_to_env() {
    local key="$1" env_file="${INSTALL_DIR}/.env"
    printf '\n# Synthetic API key (https://synthetic.new/settings/api)\nSYNTHETIC_API_KEY=%s\n' "$key" >> "$env_file"
}

append_zai_to_env() {
    local key="$1" base_url="$2" env_file="${INSTALL_DIR}/.env"
    printf '\n# Z.ai API key (https://www.z.ai/api-keys)\nZAI_API_KEY=%s\n\n# Z.ai base URL\nZAI_BASE_URL=%s\n' "$key" "$base_url" >> "$env_file"
}

# ─── Collect Z.ai Key + Base URL ────────────────────────────────────
# Shared between fresh setup and add-provider flow
collect_zai_config() {
    local _zai_key _zai_base_url

    printf "\n  ${DIM}Get your key: https://www.z.ai/api-keys${NC}\n"
    _zai_key=$(prompt_secret "Z.ai API key" validate_nonempty)

    printf "\n"
    local use_default_url
    use_default_url=$(prompt_with_default "Use default Z.ai base URL (https://api.z.ai/api)? (Y/n)" "Y")
    if [[ "$use_default_url" =~ ^[Nn] ]]; then
        while true; do
            _zai_base_url=$(prompt_with_default "Z.ai base URL" "https://open.bigmodel.cn/api")
            if validate_https_url "$_zai_base_url" 2>/dev/null; then
                break
            fi
            printf "  ${RED}URL must start with 'https://'${NC}\n"
        done
    else
        _zai_base_url="https://api.z.ai/api"
    fi

    # Return both values separated by newline
    printf '%s\n%s' "$_zai_key" "$_zai_base_url"
}

# ─── Interactive Setup ──────────────────────────────────────────────
# Fully interactive .env configuration for fresh installs.
# On upgrade: checks for missing providers and offers to add them.
# Reads from /dev/tty (fd 3) for piped install compatibility.
interactive_setup() {
    local env_file="${INSTALL_DIR}/.env"

    if [[ -f "$env_file" ]]; then
        # Load existing values for start_service display
        SETUP_PORT="$(env_get SYNTRACK_PORT)"
        SETUP_PORT="${SETUP_PORT:-9211}"
        SETUP_USERNAME="$(env_get SYNTRACK_ADMIN_USER)"
        SETUP_USERNAME="${SETUP_USERNAME:-admin}"
        SETUP_PASSWORD=""  # Don't show existing password

        local has_syn=false has_zai=false
        has_synthetic_key && has_syn=true
        has_zai_key && has_zai=true

        if $has_syn && $has_zai; then
            # Both providers configured — nothing to do
            info "Existing .env found — both providers configured"
            return
        fi

        if ! $has_syn && ! $has_zai; then
            # .env exists but no keys at all — run full setup
            warn "Existing .env found but no API keys configured"
            info "Running interactive setup..."
            # Remove the empty .env so the fresh setup flow creates a new one
            rm -f "$env_file"
            # Fall through to fresh setup below
        else
            # One provider configured — offer to add the missing one
            exec 3</dev/tty || fail "Cannot read from terminal. Run the script directly instead of piping."

            if $has_syn && ! $has_zai; then
                info "Existing .env found — Synthetic configured"
                printf "\n"
                local add_zai
                add_zai=$(prompt_with_default "Add Z.ai provider? (y/N)" "N")
                if [[ "$add_zai" =~ ^[Yy] ]]; then
                    local zai_result zai_key zai_base_url
                    zai_result=$(collect_zai_config)
                    zai_key=$(echo "$zai_result" | head -1)
                    zai_base_url=$(echo "$zai_result" | tail -1)
                    append_zai_to_env "$zai_key" "$zai_base_url"
                    ok "Added Z.ai provider to .env"
                fi
            elif $has_zai && ! $has_syn; then
                info "Existing .env found — Z.ai configured"
                printf "\n"
                local add_syn
                add_syn=$(prompt_with_default "Add Synthetic provider? (y/N)" "N")
                if [[ "$add_syn" =~ ^[Yy] ]]; then
                    printf "\n  ${DIM}Get your key: https://synthetic.new/settings/api${NC}\n"
                    local syn_key
                    syn_key=$(prompt_secret "Synthetic API key (syn_...)" validate_synthetic_key)
                    append_synthetic_to_env "$syn_key"
                    ok "Added Synthetic provider to .env"
                fi
            fi

            exec 3<&-
            return
        fi
    fi

    # ── Fresh setup (no .env or empty keys) ──

    # Open /dev/tty for reading — works even when script is piped via curl | bash
    exec 3</dev/tty || fail "Cannot read from terminal. Run the script directly instead of piping."

    printf "\n"
    printf "  ${BOLD}━━━ Configuration ━━━${NC}\n"

    # ── Provider Selection ──
    local provider_choice
    provider_choice=$(prompt_choice "Which providers do you want to track?" \
        "Synthetic only" \
        "Z.ai only" \
        "Both")

    local synthetic_key="" zai_key="" zai_base_url=""

    # ── Synthetic API Key ──
    if [[ "$provider_choice" == "1" || "$provider_choice" == "3" ]]; then
        printf "\n  ${DIM}Get your key: https://synthetic.new/settings/api${NC}\n"
        synthetic_key=$(prompt_secret "Synthetic API key (syn_...)" validate_synthetic_key)
    fi

    # ── Z.ai API Key ──
    if [[ "$provider_choice" == "2" || "$provider_choice" == "3" ]]; then
        local zai_result
        zai_result=$(collect_zai_config)
        zai_key=$(echo "$zai_result" | head -1)
        zai_base_url=$(echo "$zai_result" | tail -1)
    fi

    # ── Dashboard Credentials ──
    printf "\n  ${BOLD}━━━ Dashboard Credentials ━━━${NC}\n\n"

    SETUP_USERNAME=$(prompt_with_default "Dashboard username" "admin")

    local generated_pass
    generated_pass=$(generate_password)
    printf "  Dashboard password ${DIM}[Enter = auto-generate]${NC}: "
    read -u 3 -rs pass_input
    echo ""
    if [[ -z "$pass_input" ]]; then
        SETUP_PASSWORD="$generated_pass"
        printf "  ${GREEN}✓${NC} Generated password: ${BOLD}${SETUP_PASSWORD}${NC}\n"
        printf "  ${YELLOW}Save this password — it won't be shown again${NC}\n"
    else
        SETUP_PASSWORD="$pass_input"
        printf "  ${GREEN}✓${NC} Password set\n"
    fi

    # ── Optional Settings ──
    printf "\n  ${BOLD}━━━ Optional Settings ━━━${NC}\n\n"

    while true; do
        SETUP_PORT=$(prompt_with_default "Dashboard port" "9211")
        if validate_port "$SETUP_PORT" 2>/dev/null; then
            break
        fi
        printf "  ${RED}Must be a number between 1 and 65535${NC}\n"
    done

    local poll_interval
    while true; do
        poll_interval=$(prompt_with_default "Polling interval in seconds" "60")
        if validate_interval "$poll_interval" 2>/dev/null; then
            break
        fi
        printf "  ${RED}Must be a number between 10 and 3600${NC}\n"
    done

    # Close the tty fd
    exec 3<&-

    # ── Write .env ──
    {
        echo "# ═══════════════════════════════════════════════════════════════"
        echo "# SynTrack Configuration"
        echo "# Generated by installer on $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
        echo "# ═══════════════════════════════════════════════════════════════"
        echo ""

        if [[ -n "$synthetic_key" ]]; then
            echo "# Synthetic API key (https://synthetic.new/settings/api)"
            echo "SYNTHETIC_API_KEY=${synthetic_key}"
            echo ""
        fi

        if [[ -n "$zai_key" ]]; then
            echo "# Z.ai API key (https://www.z.ai/api-keys)"
            echo "ZAI_API_KEY=${zai_key}"
            echo ""
            echo "# Z.ai base URL"
            echo "ZAI_BASE_URL=${zai_base_url}"
            echo ""
        fi

        echo "# Dashboard credentials"
        echo "SYNTRACK_ADMIN_USER=${SETUP_USERNAME}"
        echo "SYNTRACK_ADMIN_PASS=${SETUP_PASSWORD}"
        echo ""
        echo "# Polling interval in seconds (10-3600)"
        echo "SYNTRACK_POLL_INTERVAL=${poll_interval}"
        echo ""
        echo "# Dashboard port"
        echo "SYNTRACK_PORT=${SETUP_PORT}"
    } > "$env_file"

    ok "Created ${env_file}"

    # ── Summary ──
    local provider_label
    case "$provider_choice" in
        1) provider_label="Synthetic" ;;
        2) provider_label="Z.ai" ;;
        3) provider_label="Synthetic + Z.ai" ;;
    esac

    local masked_pass
    masked_pass=$(printf '%*s' ${#SETUP_PASSWORD} '' | tr ' ' '•')

    printf "\n"
    printf "  ${BOLD}┌─ Configuration Summary ──────────────────┐${NC}\n"
    printf "  ${BOLD}│${NC}  Provider:  %-29s${BOLD}│${NC}\n" "$provider_label"
    printf "  ${BOLD}│${NC}  Dashboard: %-29s${BOLD}│${NC}\n" "http://localhost:${SETUP_PORT}"
    printf "  ${BOLD}│${NC}  Username:  %-29s${BOLD}│${NC}\n" "$SETUP_USERNAME"
    printf "  ${BOLD}│${NC}  Password:  %-29s${BOLD}│${NC}\n" "$masked_pass"
    printf "  ${BOLD}│${NC}  Interval:  %-29s${BOLD}│${NC}\n" "${poll_interval}s"
    printf "  ${BOLD}└───────────────────────────────────────────┘${NC}\n"
}

# ─── systemd Service (Linux) ─────────────────────────────────────────
setup_systemd() {
    if [[ "$OS" != "linux" ]]; then return 1; fi
    if ! command -v systemctl &>/dev/null; then
        warn "systemd not found — skipping service setup"
        return 1
    fi

    local svc_dir svc_file

    if [[ "$SYSTEMD_MODE" == "system" ]]; then
        # ── System-wide service (running as root/sudo) ──
        svc_dir="/etc/systemd/system"
        svc_file="${svc_dir}/${SERVICE_NAME}.service"

        cat > "$svc_file" <<EOF
[Unit]
Description=SynTrack - AI API Quota Tracker
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN_DIR}/syntrack --debug
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=syntrack

[Install]
WantedBy=multi-user.target
EOF

        systemctl daemon-reload 2>/dev/null || true
        systemctl enable syntrack 2>/dev/null || true

        ok "Created system-wide systemd service"
    else
        # ── User service (running without root) ──
        svc_dir="$HOME/.config/systemd/user"
        svc_file="${svc_dir}/${SERVICE_NAME}.service"

        mkdir -p "$svc_dir"

        # Enable lingering so user services persist after logout
        if command -v loginctl &>/dev/null; then
            loginctl enable-linger "$(whoami)" 2>/dev/null || true
        fi

        cat > "$svc_file" <<EOF
[Unit]
Description=SynTrack - AI API Quota Tracker
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN_DIR}/syntrack --debug
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=syntrack

[Install]
WantedBy=default.target
EOF

        systemctl --user daemon-reload 2>/dev/null || true
        systemctl --user enable syntrack 2>/dev/null || true

        ok "Created systemd user service"
    fi

    local sctl jctl
    sctl="$(_sctl_cmd)"
    jctl="$(_jctl_cmd)"

    echo ""
    printf "  ${DIM}Manage with:${NC}\n"
    printf "    ${CYAN}${sctl} start syntrack${NC}    # Start\n"
    printf "    ${CYAN}${sctl} stop syntrack${NC}     # Stop\n"
    printf "    ${CYAN}${sctl} status syntrack${NC}   # Status\n"
    printf "    ${CYAN}${sctl} restart syntrack${NC}  # Restart\n"
    printf "    ${CYAN}${jctl} -f${NC}   # Logs\n"
    return 0
}

# ─── launchd (macOS) ─────────────────────────────────────────────────
setup_launchd() {
    if [[ "$OS" != "darwin" ]]; then return 1; fi

    echo ""
    ok "macOS detected — SynTrack self-daemonizes"
    printf "  ${DIM}Manage with:${NC}\n"
    printf "    ${CYAN}syntrack${NC}           # Start (runs in background)\n"
    printf "    ${CYAN}syntrack stop${NC}      # Stop\n"
    printf "    ${CYAN}syntrack status${NC}    # Status\n"
    printf "    ${CYAN}syntrack --debug${NC}   # Run in foreground (logs to stdout)\n"
    return 0
}

# ─── PATH Setup ──────────────────────────────────────────────────────
setup_path() {
    local path_line="export PATH=\"\$HOME/.syntrack:\$PATH\""
    local shell_rc=""

    # Already in PATH?
    if command -v syntrack &>/dev/null 2>&1; then
        return
    fi

    case "${SHELL:-}" in
        */zsh)  shell_rc="$HOME/.zshrc" ;;
        */bash)
            if [[ -f "$HOME/.bash_profile" ]]; then
                shell_rc="$HOME/.bash_profile"
            else
                shell_rc="$HOME/.bashrc"
            fi
            ;;
    esac

    if [[ -n "$shell_rc" && -f "$shell_rc" ]]; then
        if ! grep -q '\.syntrack' "$shell_rc" 2>/dev/null; then
            printf '\n# SynTrack\n%s\n' "$path_line" >> "$shell_rc"
            ok "Added to PATH in ${shell_rc}"
        fi
    else
        warn "Add to your shell profile:"
        printf "    ${CYAN}%s${NC}\n" "$path_line"
    fi

    export PATH="${INSTALL_DIR}:$PATH"
}

# ─── Start Service ───────────────────────────────────────────────────
start_service() {
    local port="${SETUP_PORT:-9211}"
    local username="${SETUP_USERNAME:-admin}"
    local password="${SETUP_PASSWORD}"

    info "Starting SynTrack..."

    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        # ── systemd start ──
        if ! _systemctl start syntrack 2>/dev/null; then
            print_errors "$port"
            return 1
        fi

        sleep 2

        if _systemctl is-active --quiet syntrack 2>/dev/null; then
            ok "SynTrack is running"
        else
            print_errors "$port"
            return 1
        fi
    else
        # ── Direct start (macOS / Linux without systemd) ──
        cd "$INSTALL_DIR"
        if "${BIN_DIR}/syntrack" 2>&1; then
            sleep 1
            ok "SynTrack is running in background"
        else
            print_errors "$port"
            return 1
        fi
    fi

    echo ""
    printf "  ${GREEN}${BOLD}Dashboard: http://localhost:${port}${NC}\n"
    if [[ -n "$password" ]]; then
        printf "  ${DIM}Login with: ${username} / ${password}${NC}\n"
    else
        printf "  ${DIM}Login with: ${username} / <your configured password>${NC}\n"
    fi
    return 0
}

# ─── Print Errors ────────────────────────────────────────────────────
print_errors() {
    local port="${1:-9211}"

    echo ""
    printf "  ${RED}${BOLD}SynTrack failed to start${NC}\n"

    # Show systemd status/logs on Linux
    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        echo ""
        printf "  ${DIM}Service status:${NC}\n"
        _systemctl status syntrack --no-pager 2>&1 | head -12 | sed 's/^/    /' || true
        echo ""
        printf "  ${DIM}Recent logs:${NC}\n"
        _journalctl -n 10 --no-pager 2>&1 | sed 's/^/    /' || true
    fi

    echo ""
    printf "  ${BOLD}Common issues:${NC}\n\n"

    printf "  ${YELLOW}1.${NC} Port ${port} already in use\n"
    printf "     Change SYNTRACK_PORT in ${CYAN}${INSTALL_DIR}/.env${NC}\n"
    printf "     Check what's using it: ${CYAN}lsof -i :${port}${NC}\n\n"

    printf "  ${YELLOW}2.${NC} Invalid API key\n"
    printf "     Synthetic: ${CYAN}https://synthetic.new/settings/api${NC}\n"
    printf "     Z.ai:      ${CYAN}https://www.z.ai/api-keys${NC}\n\n"

    printf "  ${YELLOW}3.${NC} Network error\n"
    printf "     Verify you can reach the API endpoints\n\n"

    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        printf "  ${DIM}Full logs: $(_jctl_cmd) -f${NC}\n"
    else
        printf "  ${DIM}Debug mode: syntrack --debug${NC}\n"
    fi
}

# ─── Main ─────────────────────────────────────────────────────────────
main() {
    printf "\n"
    printf "  ${BOLD}SynTrack Installer${NC}\n"
    printf "  ${DIM}https://github.com/${REPO}${NC}\n"
    printf "\n"

    # Detect platform
    detect_platform
    info "Platform: ${BOLD}${PLATFORM}${NC}"

    # Detect root/sudo — determines system-wide vs user systemd service
    if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
        SYSTEMD_MODE="system"
        info "Running as root — will create system-wide service"
    fi

    # Create directories
    mkdir -p "${INSTALL_DIR}" "${BIN_DIR}" "${INSTALL_DIR}/data"

    # Stop existing instance if upgrading
    stop_existing

    # Download binary
    download

    # Create wrapper (so .env is always found)
    create_wrapper

    # Interactive .env configuration (skipped if .env already exists)
    interactive_setup

    # Set up service management
    echo ""
    if [[ "$OS" == "linux" ]]; then
        setup_systemd || true
    elif [[ "$OS" == "darwin" ]]; then
        setup_launchd || true
    fi

    # Add to PATH
    setup_path

    # Start the service
    echo ""
    start_service || true

    printf "\n  ${GREEN}${BOLD}Installation complete${NC}\n\n"
}

main "$@"
