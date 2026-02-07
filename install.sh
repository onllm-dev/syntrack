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

# ─── Create .env ─────────────────────────────────────────────────────
setup_env() {
    local env_file="${INSTALL_DIR}/.env"

    if [[ -f "$env_file" ]]; then
        info "Existing .env found — keeping current configuration"
        return
    fi

    cat > "$env_file" <<'EOF'
# ═══════════════════════════════════════════════════════════════
# SynTrack Configuration
# At least one provider API key is required.
# ═══════════════════════════════════════════════════════════════

# Synthetic API key (https://synthetic.new/settings/api)
SYNTHETIC_API_KEY=

# Z.ai API key (https://www.z.ai/api-keys)
ZAI_API_KEY=

# Dashboard credentials
SYNTRACK_ADMIN_USER=admin
SYNTRACK_ADMIN_PASS=changeme

# Polling interval in seconds (10-3600, default: 60)
SYNTRACK_POLL_INTERVAL=60

# Dashboard port (default: 9211)
SYNTRACK_PORT=9211
EOF

    ok "Created ${env_file}"
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

# ─── Check API Keys ─────────────────────────────────────────────────
has_api_keys() {
    local env_file="${INSTALL_DIR}/.env"
    [[ -f "$env_file" ]] || return 1

    local key val
    while IFS='=' read -r key val || [[ -n "$key" ]]; do
        # Skip comments and empty lines
        [[ "$key" =~ ^[[:space:]]*# ]] && continue
        [[ -z "$key" ]] && continue
        key="$(echo "$key" | tr -d '[:space:]')"
        val="$(echo "$val" | tr -d '[:space:]')"
        case "$key" in
            SYNTHETIC_API_KEY|ZAI_API_KEY)
                if [[ -n "$val" && "$val" != "syn_your_api_key_here" && "$val" != "your_zai_api_key_here" ]]; then
                    return 0
                fi
                ;;
        esac
    done < "$env_file"
    return 1
}

# ─── Start Service ───────────────────────────────────────────────────
start_service() {
    local port
    port="$(grep -E '^SYNTRACK_PORT=' "${INSTALL_DIR}/.env" 2>/dev/null | cut -d= -f2 | tr -d '[:space:]')"
    port="${port:-9211}"

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
    printf "  ${DIM}Login with: admin / changeme (change from dashboard footer)${NC}\n"
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

    printf "  ${YELLOW}1.${NC} No API keys configured\n"
    printf "     Edit ${CYAN}${INSTALL_DIR}/.env${NC}\n"
    printf "     Add SYNTHETIC_API_KEY or ZAI_API_KEY\n\n"

    printf "  ${YELLOW}2.${NC} Port ${port} already in use\n"
    printf "     Change SYNTRACK_PORT in ${CYAN}${INSTALL_DIR}/.env${NC}\n"
    printf "     Check what's using it: ${CYAN}lsof -i :${port}${NC}\n\n"

    printf "  ${YELLOW}3.${NC} Invalid API key\n"
    printf "     Synthetic: ${CYAN}https://synthetic.new/settings/api${NC}\n"
    printf "     Z.ai:      ${CYAN}https://www.z.ai/api-keys${NC}\n\n"

    printf "  ${YELLOW}4.${NC} Network error\n"
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

    # Create .env configuration
    setup_env

    # Set up service management
    echo ""
    if [[ "$OS" == "linux" ]]; then
        setup_systemd || true
    elif [[ "$OS" == "darwin" ]]; then
        setup_launchd || true
    fi

    # Add to PATH
    setup_path

    # Check config and optionally start
    echo ""
    if has_api_keys; then
        start_service || true
    else
        warn "No API keys configured yet"
        echo ""
        printf "  ${BOLD}Next steps:${NC}\n\n"
        printf "  1. Edit ${CYAN}${INSTALL_DIR}/.env${NC}\n"
        printf "     Add your Synthetic or Z.ai API key\n\n"
        printf "  2. Start SynTrack:\n"
        if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
            printf "     ${CYAN}$(_sctl_cmd) start syntrack${NC}\n"
        else
            printf "     ${CYAN}syntrack${NC}\n"
        fi
        echo ""
        printf "  3. Open ${CYAN}http://localhost:9211${NC}\n"
    fi

    printf "\n  ${GREEN}${BOLD}Installation complete${NC}\n\n"
}

main "$@"
