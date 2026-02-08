#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════
# onWatch Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/onllm-dev/onwatch/main/install.sh | bash
# ═══════════════════════════════════════════════════════════════════════
set -euo pipefail

INSTALL_DIR="${ONWATCH_INSTALL_DIR:-$HOME/.onwatch}"
BIN_DIR="${INSTALL_DIR}/bin"
REPO="onllm-dev/onwatch"
SERVICE_NAME="onwatch"
SYSTEMD_MODE="user"  # "user" or "system" — auto-detected at runtime

# Collected during interactive setup, used by start_service
SETUP_USERNAME=""
SETUP_PASSWORD=""
SETUP_PORT=""

# ─── Colors ───────────────────────────────────────────────────────────
RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'
BLUE=$'\033[0;34m'; CYAN=$'\033[0;36m'; BOLD=$'\033[1m'
DIM=$'\033[2m'; NC=$'\033[0m'

info()    { printf "  ${BLUE}info${NC}  %s\n" "$*"; }
ok()      { printf "  ${GREEN} ok ${NC}  %s\n" "$*"; }
warn()    { printf "  ${YELLOW}warn${NC}  %s\n" "$*"; }
fail()    { printf "  ${RED}fail${NC}  %b\n" "$*" >&2; exit 1; }

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
        journalctl -u onwatch "$@"
    else
        journalctl --user -u onwatch "$@"
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
        echo "journalctl -u onwatch"
    else
        echo "journalctl --user -u onwatch"
    fi
}

# ─── Input Helpers ──────────────────────────────────────────────────

# Generate a random 12-char alphanumeric password
generate_password() {
    local bytes
    bytes=$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c 12) || true
    printf '%s' "$bytes"
}

# Numbered menu, returns selection number
# Usage: choice=$(prompt_choice "Which provider?" "Synthetic only" "Z.ai only" "Both")
prompt_choice() {
    local prompt="$1"; shift
    local options=("$@")
    printf "\n  ${BOLD}%s${NC}\n" "$prompt" >&2
    for i in "${!options[@]}"; do
        printf "    ${CYAN}%d)${NC} %s\n" "$((i+1))" "${options[$i]}" >&2
    done
    while true; do
        printf "  ${BOLD}>${NC} " >&2
        read -u 3 -r choice
        if [[ "$choice" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= ${#options[@]} )); then
            echo "$choice"
            return
        fi
        printf "  ${RED}Please enter 1-%d${NC}\n" "${#options[@]}" >&2
    done
}

# Read a secret value (no echo), show masked version, validate with callback
# Usage: prompt_secret "Synthetic API key" synthetic_key "starts_with_syn"
prompt_secret() {
    local prompt="$1" validation="$2"
    local value=""
    while true; do
        printf "  %s: " "$prompt" >&2
        read -u 3 -rs value
        echo "" >&2
        if [[ -z "$value" ]]; then
            printf "  ${RED}Cannot be empty${NC}\n" >&2
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
            printf "  ${GREEN}✓${NC} ${DIM}%s${NC}\n" "$masked" >&2
            echo "$value"
            return
        fi
    done
}

# Prompt with a default value shown in brackets
# Usage: result=$(prompt_with_default "Dashboard port" "9211")
prompt_with_default() {
    local prompt="$1" default="$2"
    printf "  %s ${DIM}[%s]${NC}: " "$prompt" "$default" >&2
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
    printf "  ${RED}Key must start with 'syn_'${NC}\n" >&2
    return 1
}

validate_nonempty() {
    local val="$1"
    if [[ -n "$val" ]]; then
        return 0
    fi
    printf "  ${RED}Cannot be empty${NC}\n" >&2
    return 1
}

validate_https_url() {
    local val="$1"
    if [[ "$val" == https://* ]]; then
        return 0
    fi
    printf "  ${RED}URL must start with 'https://'${NC}\n" >&2
    return 1
}

validate_port() {
    local val="$1"
    if [[ "$val" =~ ^[0-9]+$ ]] && (( val >= 1 && val <= 65535 )); then
        return 0
    fi
    printf "  ${RED}Must be a number between 1 and 65535${NC}\n" >&2
    return 1
}

validate_interval() {
    local val="$1"
    if [[ "$val" =~ ^[0-9]+$ ]] && (( val >= 10 && val <= 3600 )); then
        return 0
    fi
    printf "  ${RED}Must be a number between 10 and 3600${NC}\n" >&2
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
    ASSET_NAME="onwatch-${PLATFORM}"
}

# ─── Migrate from SynTrack ──────────────────────────────────────────
migrate_from_syntrack() {
    local old_dir="$HOME/.syntrack"
    if [[ ! -d "$old_dir" ]]; then
        return
    fi

    info "Found existing SynTrack installation at ${old_dir}"
    info "Migrating to onWatch..."

    # Stop old service
    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        if [[ "$SYSTEMD_MODE" == "system" ]]; then
            systemctl stop syntrack 2>/dev/null || true
            systemctl disable syntrack 2>/dev/null || true
        else
            systemctl --user stop syntrack 2>/dev/null || true
            systemctl --user disable syntrack 2>/dev/null || true
        fi
    elif [[ -f "${old_dir}/bin/syntrack" ]]; then
        "${old_dir}/bin/syntrack" stop 2>/dev/null || true
        sleep 1
    fi

    # Create new directories
    mkdir -p "${INSTALL_DIR}" "${BIN_DIR}" "${INSTALL_DIR}/data"

    # Copy and transform .env (SYNTRACK_ -> ONWATCH_)
    # Also remove DB_PATH so the default (~/.onwatch/data/onwatch.db) takes effect
    if [[ -f "${old_dir}/.env" ]]; then
        sed -e 's/SYNTRACK_/ONWATCH_/g' -e '/^ONWATCH_DB_PATH=/d' -e '/^SYNTRACK_DB_PATH=/d' "${old_dir}/.env" > "${INSTALL_DIR}/.env"
        ok "Migrated .env (SYNTRACK_* -> ONWATCH_*, removed DB_PATH override)"
    fi

    # Move DB files (rename syntrack.db -> onwatch.db)
    for ext in "" "-journal" "-wal" "-shm"; do
        if [[ -f "${old_dir}/data/syntrack.db${ext}" ]]; then
            cp "${old_dir}/data/syntrack.db${ext}" "${INSTALL_DIR}/data/onwatch.db${ext}"
        elif [[ -f "${old_dir}/syntrack.db${ext}" ]]; then
            cp "${old_dir}/syntrack.db${ext}" "${INSTALL_DIR}/data/onwatch.db${ext}"
        fi
    done
    ok "Migrated database files (syntrack.db -> onwatch.db)"

    # Remove old systemd service
    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        if [[ "$SYSTEMD_MODE" == "system" ]]; then
            rm -f /etc/systemd/system/syntrack.service
            systemctl daemon-reload 2>/dev/null || true
        else
            rm -f "$HOME/.config/systemd/user/syntrack.service"
            systemctl --user daemon-reload 2>/dev/null || true
        fi
        ok "Removed old syntrack systemd service"
    fi

    # Clean PATH entries for .syntrack
    for rc_file in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.bash_profile"; do
        if [[ -f "$rc_file" ]]; then
            # Remove lines containing .syntrack PATH export and the comment above it
            sed -i.bak '/# SynTrack/d; /\.syntrack/d' "$rc_file" 2>/dev/null || true
            rm -f "${rc_file}.bak"
        fi
    done

    # Remove old directory
    rm -rf "$old_dir"
    ok "Removed old SynTrack directory"

    echo ""
    printf "  ${GREEN}${BOLD}Migration complete: SynTrack -> onWatch${NC}\n"
    printf "  ${DIM}Old directory ${old_dir} has been removed${NC}\n"
    echo ""
}

# ─── Stop Existing Instance ──────────────────────────────────────────
stop_existing() {
    if [[ -f "${BIN_DIR}/onwatch" ]]; then
        if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
            _systemctl stop onwatch 2>/dev/null || true
        else
            "${BIN_DIR}/onwatch" stop 2>/dev/null || true
        fi
        # Wait up to 5 seconds for the process to die.
        # On Linux, writing to a running binary returns ETXTBSY,
        # so we must ensure it's stopped before replacing.
        local waited=0
        while [[ $waited -lt 5 ]]; do
            # Check if any onwatch process is still running from BIN_DIR
            if ! pgrep -f "${BIN_DIR}/onwatch" >/dev/null 2>&1; then
                break
            fi
            sleep 1
            waited=$((waited + 1))
        done
    fi
}

# ─── Download Binary ─────────────────────────────────────────────────
# Downloads to /tmp first (avoids ETXTBSY and filesystem issues),
# then moves into place.
download() {
    local url="https://github.com/${REPO}/releases/latest/download/${ASSET_NAME}"
    local dest="${BIN_DIR}/onwatch"
    local tmp_dest="/tmp/onwatch-download-$$"

    info "Downloading onwatch for ${BOLD}${PLATFORM}${NC}..."
    info "  URL:  $url"
    info "  Dest: $dest"

    local dl_exit=0
    if command -v curl &>/dev/null; then
        curl -fSL --progress-bar -o "$tmp_dest" "$url" 2>&1 || dl_exit=$?
        if [[ $dl_exit -ne 0 ]]; then
            local msg="curl failed with exit code $dl_exit."
            [[ $dl_exit -eq 23 ]] && msg="$msg (Write error — check disk space and permissions)"
            [[ $dl_exit -eq 22 ]] && msg="$msg (HTTP error from server)"
            [[ $dl_exit -eq 6 ]]  && msg="$msg (Could not resolve host)"
            [[ $dl_exit -eq 7 ]]  && msg="$msg (Connection refused)"
            rm -f "$tmp_dest"
            fail "Download failed: $msg\n       URL: $url\n       Tmp: $tmp_dest"
        fi
    elif command -v wget &>/dev/null; then
        wget -q -O "$tmp_dest" "$url" || dl_exit=$?
        if [[ $dl_exit -ne 0 ]]; then
            rm -f "$tmp_dest"
            fail "Download failed: wget exit code $dl_exit\n       URL: $url"
        fi
    else
        fail "curl or wget is required"
    fi

    # Validate download
    if [[ ! -s "$tmp_dest" ]]; then
        rm -f "$tmp_dest"
        fail "Downloaded file is empty"
    fi

    local dl_size
    dl_size=$(wc -c < "$tmp_dest" 2>/dev/null || echo 0)
    info "Downloaded ${dl_size} bytes"

    # Sanity check: binary should be at least 1MB
    if [[ "$dl_size" -lt 1000000 ]]; then
        rm -f "$tmp_dest"
        fail "Downloaded file too small (${dl_size} bytes) — expected ~13MB binary"
    fi

    chmod +x "$tmp_dest"

    # Move into place (rm old first to avoid ETXTBSY on Linux)
    rm -f "$dest"
    mv "$tmp_dest" "$dest"

    local ver
    ver="$("$dest" --version 2>/dev/null | head -1 || echo "unknown")"
    ok "Installed ${BOLD}onwatch ${ver}${NC}"
}

# ─── Create Wrapper Script ───────────────────────────────────────────
# The binary loads .env from the working directory. This wrapper ensures
# we always cd to ~/.onwatch before running, so .env is always found.
create_wrapper() {
    local wrapper="${INSTALL_DIR}/onwatch"

    cat > "$wrapper" <<WRAPPER
#!/usr/bin/env bash
cd "\$HOME/.onwatch" 2>/dev/null && exec "\$HOME/.onwatch/bin/onwatch" "\$@"
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

    printf "\n  ${DIM}Get your key: https://www.z.ai/api-keys${NC}\n" >&2
    _zai_key=$(prompt_secret "Z.ai API key" validate_nonempty)

    printf "\n" >&2
    local use_default_url
    use_default_url=$(prompt_with_default "Use default Z.ai base URL (https://api.z.ai/api)? (Y/n)" "Y")
    if [[ "$use_default_url" =~ ^[Nn] ]]; then
        while true; do
            _zai_base_url=$(prompt_with_default "Z.ai base URL" "https://open.bigmodel.cn/api")
            if validate_https_url "$_zai_base_url" 2>/dev/null; then
                break
            fi
            printf "  ${RED}URL must start with 'https://'${NC}\n" >&2
        done
    else
        _zai_base_url="https://api.z.ai/api"
    fi

    # Return both values separated by newline
    printf '%s\n%s' "$_zai_key" "$_zai_base_url"
}

# Shared between fresh setup and add-provider flow
collect_anthropic_config() {
    local _anthropic_token=""

    # Try auto-detection first
    printf "\n  ${BOLD}Anthropic (Claude Code) Token Setup${NC}\n" >&2
    printf "  ${DIM}onWatch can auto-detect your Claude Code credentials.${NC}\n\n" >&2

    local auto_token=""
    auto_token=$(detect_anthropic_token 2>/dev/null) || true

    if [[ -n "$auto_token" ]]; then
        printf "  ${GREEN}✓${NC} Auto-detected Claude Code token\n" >&2
        local masked="${auto_token:0:8}...${auto_token: -4}"
        printf "  ${DIM}Token: %s${NC}\n" "$masked" >&2
        local use_auto
        use_auto=$(prompt_with_default "Use auto-detected token? (Y/n)" "Y")
        if [[ "$use_auto" =~ ^[Yy] ]] || [[ -z "$use_auto" ]]; then
            printf '%s' "$auto_token"
            return
        fi
    else
        printf "  ${YELLOW}!${NC} Could not auto-detect Claude Code credentials\n" >&2
    fi

    # Manual entry
    local entry_choice
    entry_choice=$(prompt_choice "How would you like to provide the token?" \
        "Enter token directly" \
        "Show help for retrieving token")

    if [[ "$entry_choice" == "2" ]]; then
        printf "\n  ${BOLD}How to retrieve your Anthropic token:${NC}\n\n" >&2
        printf "  ${CYAN}macOS Keychain:${NC}\n" >&2
        printf "    security find-generic-password -s \"Claude Code-credentials\" -a \"\$(whoami)\" -w \\ \n" >&2
        printf "      | python3 -c \"import sys,json; print(json.loads(sys.stdin.read())['claudeAiOauth']['accessToken'])\"\n\n" >&2
        printf "  ${CYAN}Linux Keyring (GNOME/KDE):${NC}\n" >&2
        printf "    secret-tool lookup service \"Claude Code-credentials\" account \"\$(whoami)\" \\ \n" >&2
        printf "      | python3 -c \"import sys,json; print(json.loads(sys.stdin.read())['claudeAiOauth']['accessToken'])\"\n\n" >&2
        printf "  ${CYAN}Linux / macOS / Windows File Fallback:${NC}\n" >&2
        printf "    python3 -c \"import json; print(json.load(open('\$HOME/.claude/.credentials.json'))['claudeAiOauth']['accessToken'])\"\n\n" >&2
        printf "  ${CYAN}Windows (PowerShell):${NC}\n" >&2
        printf "    (Get-Content \"\$env:USERPROFILE\\.claude\\.credentials.json\" | ConvertFrom-Json).claudeAiOauth.accessToken\n\n" >&2
        printf "  ${DIM}Paste the token below after running the appropriate command above.${NC}\n" >&2
    fi

    _anthropic_token=$(prompt_secret "Anthropic token" validate_nonempty)
    printf '%s' "$_anthropic_token"
}

# Auto-detect Anthropic token from Claude Code credentials
detect_anthropic_token() {
    local creds_json=""

    case "$(uname -s)" in
        Darwin*)
            creds_json=$(security find-generic-password -s "Claude Code-credentials" -a "$(whoami)" -w 2>/dev/null) || true
            ;;
        Linux*)
            if command -v secret-tool &>/dev/null; then
                creds_json=$(secret-tool lookup service "Claude Code-credentials" account "$(whoami)" 2>/dev/null) || true
            fi
            if [[ -z "$creds_json" ]] && [[ -f "$HOME/.claude/.credentials.json" ]]; then
                creds_json=$(cat "$HOME/.claude/.credentials.json" 2>/dev/null) || true
            fi
            ;;
        *)
            if [[ -f "$HOME/.claude/.credentials.json" ]]; then
                creds_json=$(cat "$HOME/.claude/.credentials.json" 2>/dev/null) || true
            fi
            ;;
    esac

    if [[ -z "$creds_json" ]]; then
        return 1
    fi

    # Extract accessToken using python3
    local token=""
    token=$(printf '%s' "$creds_json" | python3 -c "import sys,json; print(json.loads(sys.stdin.read())['claudeAiOauth']['accessToken'])" 2>/dev/null) || true

    if [[ -z "$token" ]]; then
        return 1
    fi

    printf '%s' "$token"
}

# Check if Anthropic key is configured
has_anthropic_key() {
    local val
    val=$(env_get ANTHROPIC_TOKEN)
    [[ -n "$val" && "$val" != "your_token_here" ]]
}

# Append Anthropic config to existing .env
append_anthropic_to_env() {
    local key="$1"
    local env_file="${INSTALL_DIR}/.env"
    {
        echo ""
        echo "# Anthropic token (Claude Code — auto-detected or manual)"
        echo "ANTHROPIC_TOKEN=${key}"
    } >> "$env_file"
}

# ─── Interactive Setup ──────────────────────────────────────────────
# Fully interactive .env configuration for fresh installs.
# On upgrade: checks for missing providers and offers to add them.
# Reads from /dev/tty (fd 3) for piped install compatibility.
interactive_setup() {
    local env_file="${INSTALL_DIR}/.env"
    local _opened_fd3=false

    if [[ -f "$env_file" ]]; then
        # Load existing values for start_service display
        SETUP_PORT="$(env_get ONWATCH_PORT)"
        SETUP_PORT="${SETUP_PORT:-9211}"
        SETUP_USERNAME="$(env_get ONWATCH_ADMIN_USER)"
        SETUP_USERNAME="${SETUP_USERNAME:-admin}"
        SETUP_PASSWORD=""  # Don't show existing password

        local has_syn=false has_zai=false has_anth=false
        has_synthetic_key && has_syn=true
        has_zai_key && has_zai=true
        has_anthropic_key && has_anth=true

        if $has_syn && $has_zai && $has_anth; then
            # All providers configured — nothing to do
            info "Existing .env found — all providers configured"
            return
        fi

        if ! $has_syn && ! $has_zai && ! $has_anth; then
            # .env exists but no keys at all — run full setup
            warn "Existing .env found but no API keys configured"
            info "Running interactive setup..."
            # Remove the empty .env so the fresh setup flow creates a new one
            rm -f "$env_file"
            # Fall through to fresh setup below
        else
            # Some providers configured — offer to add missing ones
            if ! { true <&3; } 2>/dev/null; then
                exec 3</dev/tty || fail "Cannot read from terminal. Run the script directly instead of piping."
                _opened_fd3=true
            fi

            local configured=""
            $has_syn && configured="${configured}Synthetic "
            $has_zai && configured="${configured}Z.ai "
            $has_anth && configured="${configured}Anthropic "
            info "Existing .env found — configured: ${configured}"
            printf "\n"

            if ! $has_syn; then
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

            if ! $has_zai; then
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
            fi

            if ! $has_anth; then
                # Try auto-detection silently first
                local auto_token=""
                auto_token=$(detect_anthropic_token 2>/dev/null) || true
                if [[ -n "$auto_token" ]]; then
                    printf "  ${GREEN}✓${NC} Claude Code credentials detected on this system\n"
                    local add_anth
                    add_anth=$(prompt_with_default "Enable Anthropic tracking? (Y/n)" "Y")
                    if [[ "$add_anth" =~ ^[Yy] ]] || [[ -z "$add_anth" ]]; then
                        append_anthropic_to_env "$auto_token"
                        ok "Added Anthropic provider to .env (auto-detected)"
                    fi
                else
                    local add_anth
                    add_anth=$(prompt_with_default "Add Anthropic (Claude Code) provider? (y/N)" "N")
                    if [[ "$add_anth" =~ ^[Yy] ]]; then
                        local anth_token
                        anth_token=$(collect_anthropic_config)
                        append_anthropic_to_env "$anth_token"
                        ok "Added Anthropic provider to .env"
                    fi
                fi
            fi

            $_opened_fd3 && exec 3<&- || true
            return
        fi
    fi

    # ── Fresh setup (no .env or empty keys) ──

    # Open /dev/tty for reading — works even when script is piped via curl | bash
    # Skip if fd 3 is already open (e.g., during testing)
    if ! { true <&3; } 2>/dev/null; then
        exec 3</dev/tty || fail "Cannot read from terminal. Run the script directly instead of piping."
        _opened_fd3=true
    fi

    printf "\n"
    printf "  ${BOLD}━━━ Configuration ━━━${NC}\n"

    # ── Provider Selection ──
    local provider_choice
    provider_choice=$(prompt_choice "Which providers do you want to track?" \
        "Synthetic only" \
        "Z.ai only" \
        "Anthropic (Claude Code) only" \
        "Multiple (choose one at a time)" \
        "All available")

    local synthetic_key="" zai_key="" zai_base_url="" anthropic_token=""

    if [[ "$provider_choice" == "4" ]]; then
        # ── Multiple: ask for each provider individually ──
        local add_it
        add_it=$(prompt_with_default "Add Synthetic provider? (y/N)" "N")
        if [[ "$add_it" =~ ^[Yy] ]]; then
            printf "\n  ${DIM}Get your key: https://synthetic.new/settings/api${NC}\n"
            synthetic_key=$(prompt_secret "Synthetic API key (syn_...)" validate_synthetic_key)
        fi

        add_it=$(prompt_with_default "Add Z.ai provider? (y/N)" "N")
        if [[ "$add_it" =~ ^[Yy] ]]; then
            local zai_result
            zai_result=$(collect_zai_config)
            zai_key=$(echo "$zai_result" | head -1)
            zai_base_url=$(echo "$zai_result" | tail -1)
        fi

        add_it=$(prompt_with_default "Add Anthropic (Claude Code) provider? (y/N)" "N")
        if [[ "$add_it" =~ ^[Yy] ]]; then
            anthropic_token=$(collect_anthropic_config)
        fi

        # Validate at least one provider selected
        if [[ -z "$synthetic_key" && -z "$zai_key" && -z "$anthropic_token" ]]; then
            printf "  ${RED}No providers selected. Please select at least one.${NC}\n"
            # Re-run provider selection by recursion-safe retry
            printf "\n"
            add_it=$(prompt_with_default "Add Synthetic provider? (y/N)" "N")
            if [[ "$add_it" =~ ^[Yy] ]]; then
                printf "\n  ${DIM}Get your key: https://synthetic.new/settings/api${NC}\n"
                synthetic_key=$(prompt_secret "Synthetic API key (syn_...)" validate_synthetic_key)
            fi
            if [[ -z "$synthetic_key" ]]; then
                add_it=$(prompt_with_default "Add Z.ai provider? (y/N)" "N")
                if [[ "$add_it" =~ ^[Yy] ]]; then
                    local zai_result
                    zai_result=$(collect_zai_config)
                    zai_key=$(echo "$zai_result" | head -1)
                    zai_base_url=$(echo "$zai_result" | tail -1)
                fi
            fi
            if [[ -z "$synthetic_key" && -z "$zai_key" ]]; then
                add_it=$(prompt_with_default "Add Anthropic (Claude Code) provider? (y/N)" "N")
                if [[ "$add_it" =~ ^[Yy] ]]; then
                    anthropic_token=$(collect_anthropic_config)
                fi
            fi
            if [[ -z "$synthetic_key" && -z "$zai_key" && -z "$anthropic_token" ]]; then
                fail "At least one provider is required"
            fi
        fi
    else
        # ── Single provider or All ──

        # ── Synthetic API Key ──
        if [[ "$provider_choice" == "1" || "$provider_choice" == "5" ]]; then
            printf "\n  ${DIM}Get your key: https://synthetic.new/settings/api${NC}\n"
            synthetic_key=$(prompt_secret "Synthetic API key (syn_...)" validate_synthetic_key)
        fi

        # ── Z.ai API Key ──
        if [[ "$provider_choice" == "2" || "$provider_choice" == "5" ]]; then
            local zai_result
            zai_result=$(collect_zai_config)
            zai_key=$(echo "$zai_result" | head -1)
            zai_base_url=$(echo "$zai_result" | tail -1)
        fi

        # ── Anthropic Token ──
        if [[ "$provider_choice" == "3" || "$provider_choice" == "5" ]]; then
            anthropic_token=$(collect_anthropic_config)
        fi
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

    # Close the tty fd (only if we opened it)
    $_opened_fd3 && exec 3<&- || true

    # ── Write .env ──
    {
        echo "# ═══════════════════════════════════════════════════════════════"
        echo "# onWatch Configuration"
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

        if [[ -n "$anthropic_token" ]]; then
            echo "# Anthropic token (Claude Code)"
            echo "ANTHROPIC_TOKEN=${anthropic_token}"
            echo ""
        fi

        echo "# Dashboard credentials"
        echo "ONWATCH_ADMIN_USER=${SETUP_USERNAME}"
        echo "ONWATCH_ADMIN_PASS=${SETUP_PASSWORD}"
        echo ""
        echo "# Polling interval in seconds (10-3600)"
        echo "ONWATCH_POLL_INTERVAL=${poll_interval}"
        echo ""
        echo "# Dashboard port"
        echo "ONWATCH_PORT=${SETUP_PORT}"
    } > "$env_file"

    ok "Created ${env_file}"

    # ── Summary ──
    local provider_label
    case "$provider_choice" in
        1) provider_label="Synthetic" ;;
        2) provider_label="Z.ai" ;;
        3) provider_label="Anthropic" ;;
        4)
            # Multiple — build label from selected providers
            local parts=()
            [[ -n "$synthetic_key" ]] && parts+=("Synthetic")
            [[ -n "$zai_key" ]] && parts+=("Z.ai")
            [[ -n "$anthropic_token" ]] && parts+=("Anthropic")
            provider_label=$(IFS=", "; echo "${parts[*]}")
            ;;
        5) provider_label="All providers" ;;
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
Description=onWatch - AI API Quota Tracker
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target
StartLimitBurst=3
StartLimitIntervalSec=120

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN_DIR}/onwatch --debug
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=onwatch

[Install]
WantedBy=multi-user.target
EOF

        systemctl daemon-reload 2>/dev/null || true
        systemctl enable onwatch 2>/dev/null || true

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
Description=onWatch - AI API Quota Tracker
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target
StartLimitBurst=3
StartLimitIntervalSec=120

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN_DIR}/onwatch --debug
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=onwatch

[Install]
WantedBy=default.target
EOF

        systemctl --user daemon-reload 2>/dev/null || true
        systemctl --user enable onwatch 2>/dev/null || true

        ok "Created systemd user service"
    fi

    local sctl jctl
    sctl="$(_sctl_cmd)"
    jctl="$(_jctl_cmd)"

    echo ""
    printf "  ${DIM}Manage with:${NC}\n"
    printf "    ${CYAN}${sctl} start onwatch${NC}    # Start\n"
    printf "    ${CYAN}${sctl} stop onwatch${NC}     # Stop\n"
    printf "    ${CYAN}${sctl} status onwatch${NC}   # Status\n"
    printf "    ${CYAN}${sctl} restart onwatch${NC}  # Restart\n"
    printf "    ${CYAN}${jctl} -f${NC}   # Logs\n"
    return 0
}

# ─── launchd (macOS) ─────────────────────────────────────────────────
setup_launchd() {
    if [[ "$OS" != "darwin" ]]; then return 1; fi

    echo ""
    ok "macOS detected — onWatch self-daemonizes"
    printf "  ${DIM}Manage with:${NC}\n"
    printf "    ${CYAN}onwatch${NC}           # Start (runs in background)\n"
    printf "    ${CYAN}onwatch stop${NC}      # Stop\n"
    printf "    ${CYAN}onwatch status${NC}    # Status\n"
    printf "    ${CYAN}onwatch --debug${NC}   # Run in foreground (logs to stdout)\n"
    return 0
}

# ─── PATH Setup ──────────────────────────────────────────────────────
setup_path() {
    local path_line="export PATH=\"\$HOME/.onwatch:\$PATH\""
    local shell_rc=""

    # Already in PATH?
    if command -v onwatch &>/dev/null 2>&1; then
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
        if ! grep -q '\.onwatch' "$shell_rc" 2>/dev/null; then
            printf '\n# onWatch\n%s\n' "$path_line" >> "$shell_rc"
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

    info "Starting onWatch..."

    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        # ── systemd start ──
        if ! _systemctl start onwatch 2>/dev/null; then
            print_errors "$port"
            return 1
        fi

        sleep 2

        if _systemctl is-active --quiet onwatch 2>/dev/null; then
            ok "onWatch is running"
        else
            print_errors "$port"
            return 1
        fi
    else
        # ── Direct start (macOS / Linux without systemd) ──
        cd "$INSTALL_DIR"
        if "${BIN_DIR}/onwatch" 2>&1; then
            sleep 1
            ok "onWatch is running in background"
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
    printf "  ${RED}${BOLD}onWatch failed to start${NC}\n"

    # Show systemd status/logs on Linux
    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        echo ""
        printf "  ${DIM}Service status:${NC}\n"
        _systemctl status onwatch --no-pager 2>&1 | head -12 | sed 's/^/    /' || true
        echo ""
        printf "  ${DIM}Recent logs:${NC}\n"
        _journalctl -n 10 --no-pager 2>&1 | sed 's/^/    /' || true
    fi

    echo ""
    printf "  ${BOLD}Common issues:${NC}\n\n"

    printf "  ${YELLOW}1.${NC} Port ${port} already in use\n"
    printf "     Change ONWATCH_PORT in ${CYAN}${INSTALL_DIR}/.env${NC}\n"
    printf "     Check what's using it: ${CYAN}lsof -i :${port}${NC}\n\n"

    printf "  ${YELLOW}2.${NC} Invalid API key\n"
    printf "     Synthetic: ${CYAN}https://synthetic.new/settings/api${NC}\n"
    printf "     Z.ai:      ${CYAN}https://www.z.ai/api-keys${NC}\n\n"

    printf "  ${YELLOW}3.${NC} Network error\n"
    printf "     Verify you can reach the API endpoints\n\n"

    if [[ "$OS" == "linux" ]] && command -v systemctl &>/dev/null; then
        printf "  ${DIM}Full logs: $(_jctl_cmd) -f${NC}\n"
    else
        printf "  ${DIM}Debug mode: onwatch --debug${NC}\n"
    fi
}

# ─── Main ─────────────────────────────────────────────────────────────
main() {
    printf "\n"
    printf "  ${BOLD}onWatch Installer${NC}\n"
    printf "  ${DIM}https://github.com/${REPO}${NC}\n"
    printf "\n"

    # Detect platform
    detect_platform
    info "Platform: ${BOLD}${PLATFORM}${NC}"

    # Migrate from SynTrack if old installation exists
    migrate_from_syntrack

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
