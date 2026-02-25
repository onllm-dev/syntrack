# ═══════════════════════════════════════════════════════════════════════
# onWatch Installer for Windows
# Usage: irm https://raw.githubusercontent.com/onllm-dev/onwatch/main/install.ps1 | iex
# Or run directly: .\install.ps1
# ═══════════════════════════════════════════════════════════════════════

#Requires -Version 5.1
$ErrorActionPreference = "Stop"

# ─── Configuration ─────────────────────────────────────────────────────
$INSTALL_DIR = if ($env:ONWATCH_INSTALL_DIR) { $env:ONWATCH_INSTALL_DIR } else { Join-Path $env:USERPROFILE ".onwatch" }
$BIN_DIR = Join-Path $INSTALL_DIR "bin"
$DATA_DIR = Join-Path $INSTALL_DIR "data"
$REPO = "onllm-dev/onwatch"
$ASSET_NAME = "onwatch-windows-amd64.exe"

# ─── Colors (ANSI escape sequences for modern terminals) ───────────────
$ESC = [char]27
$RED = "$ESC[31m"
$GREEN = "$ESC[32m"
$YELLOW = "$ESC[33m"
$BLUE = "$ESC[34m"
$CYAN = "$ESC[36m"
$BOLD = "$ESC[1m"
$DIM = "$ESC[2m"
$NC = "$ESC[0m"

# Check if terminal supports ANSI (Windows 10 1511+ and PowerShell 5.1+)
$SupportsAnsi = $true
try {
    $host.UI.SupportsVirtualTerminal
} catch {
    $SupportsAnsi = $false
}
if (-not $SupportsAnsi -or $env:NO_COLOR) {
    $RED = ""; $GREEN = ""; $YELLOW = ""; $BLUE = ""; $CYAN = ""; $BOLD = ""; $DIM = ""; $NC = ""
}

# ─── Output Functions ──────────────────────────────────────────────────
function Write-Info { param($msg) Write-Host "  ${BLUE}info${NC}  $msg" }
function Write-Ok { param($msg) Write-Host "  ${GREEN} ok ${NC}  $msg" }
function Write-Warn { param($msg) Write-Host "  ${YELLOW}warn${NC}  $msg" }
function Write-Fail {
    param($msg)
    Write-Host "  ${RED}fail${NC}  $msg" -ForegroundColor Red
    throw $msg
}

# ─── Input Helpers ─────────────────────────────────────────────────────

function Get-RandomPassword {
    param([int]$Length = 12)
    $chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
    -join ((1..$Length) | ForEach-Object { $chars[(Get-Random -Maximum $chars.Length)] })
}

function Read-PromptWithDefault {
    param(
        [string]$Prompt,
        [string]$Default
    )
    Write-Host "  $Prompt ${DIM}[$Default]${NC}: " -NoNewline
    $value = Read-Host
    if ([string]::IsNullOrWhiteSpace($value)) { return $Default }
    return $value
}

function Read-SecretPrompt {
    param(
        [string]$Prompt,
        [scriptblock]$Validation = { param($val) return $true }
    )
    while ($true) {
        Write-Host "  ${Prompt}: " -NoNewline
        $secureValue = Read-Host -AsSecureString
        $value = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
            [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureValue)
        )

        if ([string]::IsNullOrWhiteSpace($value)) {
            Write-Host "  ${RED}Cannot be empty${NC}"
            continue
        }

        if (-not (& $Validation $value)) {
            continue
        }

        # Show masked version
        if ($value.Length -gt 10) {
            $masked = $value.Substring(0, 6) + "..." + $value.Substring($value.Length - 4)
        } else {
            $masked = $value.Substring(0, 3) + "..."
        }
        Write-Host "  ${GREEN}OK${NC} ${DIM}$masked${NC}"
        return $value
    }
}

function Read-Choice {
    param(
        [string]$Prompt,
        [string[]]$Options
    )
    Write-Host ""
    Write-Host "  ${BOLD}$Prompt${NC}"
    for ($i = 0; $i -lt $Options.Count; $i++) {
        Write-Host "    ${CYAN}$($i + 1))${NC} $($Options[$i])"
    }
    while ($true) {
        Write-Host "  ${BOLD}>${NC} " -NoNewline
        $choice = Read-Host
        if ($choice -match '^\d+$') {
            $num = [int]$choice
            if ($num -ge 1 -and $num -le $Options.Count) {
                return $num
            }
        }
        Write-Host "  ${RED}Please enter 1-$($Options.Count)${NC}"
    }
}

# ─── Validation Functions ──────────────────────────────────────────────

function Test-SyntheticKey {
    param([string]$val)
    if ($val.StartsWith("syn_")) { return $true }
    Write-Host "  ${RED}Key must start with 'syn_'${NC}"
    return $false
}

function Test-HttpsUrl {
    param([string]$val)
    if ($val.StartsWith("https://")) { return $true }
    Write-Host "  ${RED}URL must start with 'https://'${NC}"
    return $false
}

function Test-Port {
    param([string]$val)
    if ($val -match '^\d+$') {
        $num = [int]$val
        if ($num -ge 1 -and $num -le 65535) { return $true }
    }
    Write-Host "  ${RED}Must be a number between 1 and 65535${NC}"
    return $false
}

function Test-Interval {
    param([string]$val)
    if ($val -match '^\d+$') {
        $num = [int]$val
        if ($num -ge 10 -and $num -le 3600) { return $true }
    }
    Write-Host "  ${RED}Must be a number between 10 and 3600${NC}"
    return $false
}

# ─── Token Detection ───────────────────────────────────────────────────

function Get-AnthropicToken {
    # Try file-based credentials first (most reliable on Windows)
    $credFile = Join-Path $env:USERPROFILE ".claude\.credentials.json"
    if (Test-Path $credFile) {
        try {
            $json = Get-Content $credFile -Raw | ConvertFrom-Json
            if ($json.claudeAiOauth.accessToken) {
                return $json.claudeAiOauth.accessToken
            }
        } catch {}
    }
    return $null
}

function Get-CodexToken {
    $authFile = if ($env:CODEX_HOME) {
        Join-Path $env:CODEX_HOME "auth.json"
    } else {
        Join-Path $env:USERPROFILE ".codex\auth.json"
    }

    if (Test-Path $authFile) {
        try {
            $json = Get-Content $authFile -Raw | ConvertFrom-Json
            if ($json.tokens.access_token) {
                return $json.tokens.access_token.Trim()
            }
        } catch {}
    }
    return $null
}

function Test-AntigravityRunning {
    try {
        $processes = Get-Process -Name "*windsurf*", "*antigravity*" -ErrorAction SilentlyContinue
        return $processes.Count -gt 0
    } catch {
        return $false
    }
}

# ─── Provider Configuration Collection ─────────────────────────────────

function Get-ZaiConfig {
    Write-Host ""
    Write-Host "  ${DIM}Get your key: https://www.z.ai/api-keys${NC}"
    $key = Read-SecretPrompt -Prompt "Z.ai API key" -Validation { param($val) return $true }

    Write-Host ""
    $useDefault = Read-PromptWithDefault -Prompt "Use default Z.ai base URL (https://api.z.ai/api)? (Y/n)" -Default "Y"
    if ($useDefault -match "^[Nn]") {
        while ($true) {
            $baseUrl = Read-PromptWithDefault -Prompt "Z.ai base URL" -Default "https://open.bigmodel.cn/api"
            if (Test-HttpsUrl $baseUrl) { break }
        }
    } else {
        $baseUrl = "https://api.z.ai/api"
    }

    return @{ Key = $key; BaseUrl = $baseUrl }
}

function Get-AnthropicConfig {
    Write-Host ""
    Write-Host "  ${BOLD}Anthropic (Claude Code) Token Setup${NC}"
    Write-Host "  ${DIM}onWatch can auto-detect your Claude Code credentials.${NC}"
    Write-Host ""

    $autoToken = Get-AnthropicToken

    if ($autoToken) {
        Write-Host "  ${GREEN}OK${NC} Auto-detected Claude Code token"
        $masked = $autoToken.Substring(0, 8) + "..." + $autoToken.Substring($autoToken.Length - 4)
        Write-Host "  ${DIM}Token: $masked${NC}"
        $useAuto = Read-PromptWithDefault -Prompt "Use auto-detected token? (Y/n)" -Default "Y"
        if ($useAuto -match "^[Yy]" -or [string]::IsNullOrWhiteSpace($useAuto)) {
            return $autoToken
        }
    } else {
        Write-Host "  ${YELLOW}!${NC} Could not auto-detect Claude Code credentials"
    }

    $entryChoice = Read-Choice -Prompt "How would you like to provide the token?" -Options @(
        "Enter token directly",
        "Show help for retrieving token"
    )

    if ($entryChoice -eq 2) {
        Write-Host ""
        Write-Host "  ${BOLD}How to retrieve your Anthropic token:${NC}"
        Write-Host ""
        Write-Host "  ${CYAN}Windows (PowerShell):${NC}"
        Write-Host '    (Get-Content "$env:USERPROFILE\.claude\.credentials.json" | ConvertFrom-Json).claudeAiOauth.accessToken'
        Write-Host ""
        Write-Host "  ${DIM}Paste the token below after running the command above.${NC}"
    }

    return Read-SecretPrompt -Prompt "Anthropic token" -Validation { param($val) return $true }
}

function Get-CodexConfig {
    Write-Host ""
    Write-Host "  ${BOLD}Codex Token Setup${NC}"
    Write-Host "  ${DIM}onWatch can auto-detect your Codex OAuth token from auth.json.${NC}"
    Write-Host ""

    $autoToken = Get-CodexToken

    if ($autoToken) {
        Write-Host "  ${GREEN}OK${NC} Auto-detected Codex token"
        $masked = $autoToken.Substring(0, 8) + "..." + $autoToken.Substring($autoToken.Length - 4)
        Write-Host "  ${DIM}Token: $masked${NC}"
        $useAuto = Read-PromptWithDefault -Prompt "Use auto-detected token? (Y/n)" -Default "Y"
        if ($useAuto -match "^[Yy]" -or [string]::IsNullOrWhiteSpace($useAuto)) {
            return $autoToken
        }
    } else {
        Write-Host "  ${YELLOW}!${NC} Could not auto-detect Codex token from auth.json"
    }

    $entryChoice = Read-Choice -Prompt "How would you like to provide the token?" -Options @(
        "Enter token directly",
        "Show help for retrieving token"
    )

    if ($entryChoice -eq 2) {
        Write-Host ""
        Write-Host "  ${BOLD}How to retrieve your Codex token:${NC}"
        Write-Host ""
        Write-Host "  ${CYAN}Windows (PowerShell):${NC}"
        Write-Host '    (Get-Content "$env:USERPROFILE\.codex\auth.json" | ConvertFrom-Json).tokens.access_token'
        Write-Host ""
        Write-Host "  ${DIM}Paste the access token below after running the command above.${NC}"
    }

    return Read-SecretPrompt -Prompt "Codex token" -Validation { param($val) return $true }
}

# ─── Stop Existing Instance ────────────────────────────────────────────

function Stop-ExistingInstance {
    $pidFile = Join-Path $INSTALL_DIR "onwatch.pid"
    if (Test-Path $pidFile) {
        try {
            $content = Get-Content $pidFile -Raw
            if ($content -match "^(\d+)") {
                $pid = [int]$Matches[1]
                $proc = Get-Process -Id $pid -ErrorAction SilentlyContinue
                if ($proc -and $proc.Name -like "*onwatch*") {
                    Stop-Process -Id $pid -Force -ErrorAction SilentlyContinue
                    Write-Info "Stopped previous instance (PID $pid)"
                    Start-Sleep -Seconds 1
                }
            }
        } catch {}
        Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
    }

    # Also try to stop any running onwatch processes
    Get-Process -Name "onwatch*" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
}

# ─── Download Binary ───────────────────────────────────────────────────

function Install-Binary {
    $url = "https://github.com/$REPO/releases/latest/download/$ASSET_NAME"
    $dest = Join-Path $BIN_DIR "onwatch.exe"
    $tempDest = Join-Path $env:TEMP "onwatch-download-$PID.exe"

    Write-Info "Downloading onwatch for ${BOLD}windows-amd64${NC}..."
    Write-Info "  URL:  $url"
    Write-Info "  Dest: $dest"

    try {
        # Use TLS 1.2
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

        # Download with progress
        $webClient = New-Object System.Net.WebClient
        $webClient.DownloadFile($url, $tempDest)
    } catch {
        if (Test-Path $tempDest) { Remove-Item $tempDest -Force }
        Write-Fail "Download failed: $_`n       URL: $url"
    }

    # Validate download
    if (-not (Test-Path $tempDest)) {
        Write-Fail "Downloaded file does not exist"
    }

    $fileInfo = Get-Item $tempDest
    Write-Info "Downloaded $($fileInfo.Length) bytes"

    # Sanity check: binary should be at least 1MB
    if ($fileInfo.Length -lt 1000000) {
        Remove-Item $tempDest -Force
        Write-Fail "Downloaded file too small ($($fileInfo.Length) bytes) - expected ~13MB binary"
    }

    # Move into place
    if (Test-Path $dest) { Remove-Item $dest -Force }
    Move-Item $tempDest $dest -Force

    # Get version
    try {
        $version = & $dest --version 2>$null | Select-Object -First 1
    } catch {
        $version = "unknown"
    }

    Write-Ok "Installed ${BOLD}onwatch $version${NC}"
}

# ─── Create Wrapper Script ─────────────────────────────────────────────

function New-WrapperScript {
    $wrapperPath = Join-Path $INSTALL_DIR "onwatch.cmd"
    $binaryPath = Join-Path $BIN_DIR "onwatch.exe"

    @"
@echo off
cd /d "%USERPROFILE%\.onwatch" 2>nul
"%USERPROFILE%\.onwatch\bin\onwatch.exe" %*
"@ | Set-Content -Path $wrapperPath -Encoding ASCII

    Write-Ok "Created wrapper script"
}

# ─── Interactive Setup ─────────────────────────────────────────────────

function Start-InteractiveSetup {
    $envFile = Join-Path $INSTALL_DIR ".env"

    # Check for existing .env
    if (Test-Path $envFile) {
        $envContent = Get-Content $envFile -Raw

        $hasSyn = $envContent -match "SYNTHETIC_API_KEY=\S+"
        $hasZai = $envContent -match "ZAI_API_KEY=\S+"
        $hasAnth = $envContent -match "ANTHROPIC_TOKEN=\S+"
        $hasCodex = $envContent -match "CODEX_TOKEN=\S+"
        $hasAnti = $envContent -match "ANTIGRAVITY_ENABLED=true"

        if ($hasSyn -or $hasZai -or $hasAnth -or $hasCodex -or $hasAnti) {
            $configured = @()
            if ($hasSyn) { $configured += "Synthetic" }
            if ($hasZai) { $configured += "Z.ai" }
            if ($hasAnth) { $configured += "Anthropic" }
            if ($hasCodex) { $configured += "Codex" }
            if ($hasAnti) { $configured += "Antigravity" }

            Write-Info "Existing .env found - configured: $($configured -join ', ')"

            # Check for port/username in existing config
            if ($envContent -match "ONWATCH_PORT=(\d+)") {
                $script:SetupPort = $Matches[1]
            } else {
                $script:SetupPort = "9211"
            }
            if ($envContent -match "ONWATCH_ADMIN_USER=(\S+)") {
                $script:SetupUsername = $Matches[1]
            } else {
                $script:SetupUsername = "admin"
            }
            $script:SetupPassword = ""

            return
        }
    }

    # Fresh setup
    Write-Host ""
    Write-Host "  ${BOLD}--- Configuration ---${NC}"

    # Provider Selection
    $providerChoice = Read-Choice -Prompt "Which providers do you want to track?" -Options @(
        "Synthetic only",
        "Z.ai only",
        "Anthropic (Claude Code) only",
        "Codex only",
        "Antigravity (Windsurf) only",
        "Multiple (choose one at a time)",
        "All available"
    )

    $syntheticKey = ""
    $zaiKey = ""
    $zaiBaseUrl = ""
    $anthropicToken = ""
    $codexToken = ""
    $antigravityEnabled = ""

    if ($providerChoice -eq 6) {
        # Multiple - ask for each provider individually
        $addIt = Read-PromptWithDefault -Prompt "Add Synthetic provider? (y/N)" -Default "N"
        if ($addIt -match "^[Yy]") {
            Write-Host ""
            Write-Host "  ${DIM}Get your key: https://synthetic.new/settings/api${NC}"
            $syntheticKey = Read-SecretPrompt -Prompt "Synthetic API key (syn_...)" -Validation { param($val) Test-SyntheticKey $val }
        }

        $addIt = Read-PromptWithDefault -Prompt "Add Z.ai provider? (y/N)" -Default "N"
        if ($addIt -match "^[Yy]") {
            $zaiConfig = Get-ZaiConfig
            $zaiKey = $zaiConfig.Key
            $zaiBaseUrl = $zaiConfig.BaseUrl
        }

        $addIt = Read-PromptWithDefault -Prompt "Add Anthropic (Claude Code) provider? (y/N)" -Default "N"
        if ($addIt -match "^[Yy]") {
            $anthropicToken = Get-AnthropicConfig
        }

        $addIt = Read-PromptWithDefault -Prompt "Add Codex provider? (y/N)" -Default "N"
        if ($addIt -match "^[Yy]") {
            $codexToken = Get-CodexConfig
        }

        $addIt = Read-PromptWithDefault -Prompt "Add Antigravity (Windsurf) provider? (y/N)" -Default "N"
        if ($addIt -match "^[Yy]") {
            $antigravityEnabled = "true"
            Write-Host "  ${DIM}Antigravity auto-detects the running Windsurf process${NC}"
        }

        # Validate at least one provider selected
        if (-not $syntheticKey -and -not $zaiKey -and -not $anthropicToken -and -not $codexToken -and -not $antigravityEnabled) {
            Write-Fail "At least one provider is required"
        }
    } else {
        # Single provider or All
        if ($providerChoice -eq 1 -or $providerChoice -eq 7) {
            Write-Host ""
            Write-Host "  ${DIM}Get your key: https://synthetic.new/settings/api${NC}"
            $syntheticKey = Read-SecretPrompt -Prompt "Synthetic API key (syn_...)" -Validation { param($val) Test-SyntheticKey $val }
        }

        if ($providerChoice -eq 2 -or $providerChoice -eq 7) {
            $zaiConfig = Get-ZaiConfig
            $zaiKey = $zaiConfig.Key
            $zaiBaseUrl = $zaiConfig.BaseUrl
        }

        if ($providerChoice -eq 3 -or $providerChoice -eq 7) {
            $anthropicToken = Get-AnthropicConfig
        }

        if ($providerChoice -eq 4 -or $providerChoice -eq 7) {
            $codexToken = Get-CodexConfig
        }

        if ($providerChoice -eq 5 -or $providerChoice -eq 7) {
            $antigravityEnabled = "true"
            Write-Host ""
            Write-Host "  ${GREEN}OK${NC} Antigravity enabled (auto-detects running Windsurf process)"
        }
    }

    # Dashboard Credentials
    Write-Host ""
    Write-Host "  ${BOLD}--- Dashboard Credentials ---${NC}"
    Write-Host ""

    $script:SetupUsername = Read-PromptWithDefault -Prompt "Dashboard username" -Default "admin"

    $generatedPass = Get-RandomPassword
    Write-Host "  Dashboard password ${DIM}[Enter = auto-generate]${NC}: " -NoNewline
    $passInput = Read-Host -AsSecureString
    $passPlain = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
        [Runtime.InteropServices.Marshal]::SecureStringToBSTR($passInput)
    )

    if ([string]::IsNullOrWhiteSpace($passPlain)) {
        $script:SetupPassword = $generatedPass
        Write-Host "  ${GREEN}OK${NC} Generated password: ${BOLD}$($script:SetupPassword)${NC}"
        Write-Host "  ${YELLOW}Save this password - it won't be shown again${NC}"
    } else {
        $script:SetupPassword = $passPlain
        Write-Host "  ${GREEN}OK${NC} Password set"
    }

    # Optional Settings
    Write-Host ""
    Write-Host "  ${BOLD}--- Optional Settings ---${NC}"
    Write-Host ""

    while ($true) {
        $script:SetupPort = Read-PromptWithDefault -Prompt "Dashboard port" -Default "9211"
        if (Test-Port $script:SetupPort) { break }
    }

    while ($true) {
        $pollInterval = Read-PromptWithDefault -Prompt "Polling interval in seconds" -Default "60"
        if (Test-Interval $pollInterval) { break }
    }

    # Write .env
    $envContent = @"
# ═══════════════════════════════════════════════════════════════
# onWatch Configuration
# Generated by installer on $(Get-Date -Format "yyyy-MM-dd HH:mm:ss UTC")
# ═══════════════════════════════════════════════════════════════

"@

    if ($syntheticKey) {
        $envContent += @"
# Synthetic API key (https://synthetic.new/settings/api)
SYNTHETIC_API_KEY=$syntheticKey

"@
    }

    if ($zaiKey) {
        $envContent += @"
# Z.ai API key (https://www.z.ai/api-keys)
ZAI_API_KEY=$zaiKey

# Z.ai base URL
ZAI_BASE_URL=$zaiBaseUrl

"@
    }

    if ($anthropicToken) {
        $envContent += @"
# Anthropic token (Claude Code)
ANTHROPIC_TOKEN=$anthropicToken

"@
    }

    if ($codexToken) {
        $envContent += @"
# Codex OAuth token
CODEX_TOKEN=$codexToken

"@
    }

    if ($antigravityEnabled) {
        $envContent += @"
# Antigravity (Windsurf) - auto-detected from local process
ANTIGRAVITY_ENABLED=true

"@
    }

    $envContent += @"
# Dashboard credentials
ONWATCH_ADMIN_USER=$($script:SetupUsername)
ONWATCH_ADMIN_PASS=$($script:SetupPassword)

# Polling interval in seconds (10-3600)
ONWATCH_POLL_INTERVAL=$pollInterval

# Dashboard port
ONWATCH_PORT=$($script:SetupPort)
"@

    # Write without BOM - PowerShell 5.1's -Encoding UTF8 adds BOM which breaks godotenv
    $utf8NoBom = New-Object System.Text.UTF8Encoding $false
    [System.IO.File]::WriteAllText($envFile, $envContent, $utf8NoBom)
    Write-Ok "Created $envFile"

    # Summary
    $providerLabel = switch ($providerChoice) {
        1 { "Synthetic" }
        2 { "Z.ai" }
        3 { "Anthropic" }
        4 { "Codex" }
        5 { "Antigravity" }
        6 {
            $parts = @()
            if ($syntheticKey) { $parts += "Synthetic" }
            if ($zaiKey) { $parts += "Z.ai" }
            if ($anthropicToken) { $parts += "Anthropic" }
            if ($codexToken) { $parts += "Codex" }
            if ($antigravityEnabled) { $parts += "Antigravity" }
            $parts -join ", "
        }
        7 { "All providers" }
    }

    $maskedPass = "*" * $script:SetupPassword.Length

    Write-Host ""
    Write-Host "  ${BOLD}+-- Configuration Summary ------------------+${NC}"
    Write-Host "  ${BOLD}|${NC}  Provider:  $($providerLabel.PadRight(29))${BOLD}|${NC}"
    Write-Host "  ${BOLD}|${NC}  Dashboard: $("http://localhost:$($script:SetupPort)".PadRight(29))${BOLD}|${NC}"
    Write-Host "  ${BOLD}|${NC}  Username:  $($script:SetupUsername.PadRight(29))${BOLD}|${NC}"
    Write-Host "  ${BOLD}|${NC}  Password:  $($maskedPass.PadRight(29))${BOLD}|${NC}"
    Write-Host "  ${BOLD}|${NC}  Interval:  $("${pollInterval}s".PadRight(29))${BOLD}|${NC}"
    Write-Host "  ${BOLD}+--------------------------------------------+${NC}"
}

# ─── PATH Setup ────────────────────────────────────────────────────────

function Add-ToPath {
    # Check if already in PATH
    $currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($currentPath -like "*$INSTALL_DIR*") {
        return
    }

    # Add to user PATH
    $newPath = "$INSTALL_DIR;$currentPath"
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")

    # Also update current session
    $env:Path = "$INSTALL_DIR;$env:Path"

    Write-Ok "Added to PATH"
}

# ─── Start Service ─────────────────────────────────────────────────────

function Start-OnWatch {
    $port = if ($script:SetupPort) { $script:SetupPort } else { "9211" }
    $username = if ($script:SetupUsername) { $script:SetupUsername } else { "admin" }
    $password = $script:SetupPassword

    Write-Info "Starting onWatch..."

    $binaryPath = Join-Path $BIN_DIR "onwatch.exe"

    # Change to install directory and start
    Push-Location $INSTALL_DIR
    try {
        # Start the process (it will daemonize itself on Windows)
        $process = Start-Process -FilePath $binaryPath -PassThru -WindowStyle Hidden
        Start-Sleep -Seconds 2

        # Check if it's running
        $running = Get-Process -Id $process.Id -ErrorAction SilentlyContinue
        if ($running) {
            Write-Ok "onWatch is running in background (PID: $($process.Id))"
        } else {
            # Try to start in debug mode to see the error
            Write-Host ""
            Write-Host "  ${RED}${BOLD}onWatch failed to start${NC}"
            Write-Host ""
            Write-Host "  Run in debug mode to see errors:"
            Write-Host "    ${CYAN}cd $INSTALL_DIR${NC}"
            Write-Host "    ${CYAN}.\bin\onwatch.exe --debug${NC}"
            Pop-Location
            return
        }
    } finally {
        Pop-Location
    }

    Write-Host ""
    Write-Host "  ${GREEN}${BOLD}Dashboard: http://localhost:$port${NC}"
    if ($password) {
        Write-Host "  ${DIM}Login with: $username / $password${NC}"
    } else {
        Write-Host "  ${DIM}Login with: $username / <your configured password>${NC}"
    }
}

# ─── Main ──────────────────────────────────────────────────────────────

function Main {
    Write-Host ""
    Write-Host "  ${BOLD}onWatch Installer${NC}"
    Write-Host "  ${DIM}https://github.com/$REPO${NC}"
    Write-Host ""

    Write-Info "Platform: ${BOLD}windows-amd64${NC}"

    # Create directories
    New-Item -ItemType Directory -Force -Path $INSTALL_DIR | Out-Null
    New-Item -ItemType Directory -Force -Path $BIN_DIR | Out-Null
    New-Item -ItemType Directory -Force -Path $DATA_DIR | Out-Null

    # Stop existing instance
    Stop-ExistingInstance

    # Download binary
    Install-Binary

    # Create wrapper script
    New-WrapperScript

    # Interactive setup
    Start-InteractiveSetup

    # Service management info
    Write-Host ""
    Write-Ok "Windows detected - onWatch runs as a background process"
    Write-Host "  ${DIM}Manage with:${NC}"
    Write-Host "    ${CYAN}onwatch${NC}           # Start (runs in background)"
    Write-Host "    ${CYAN}onwatch stop${NC}      # Stop"
    Write-Host "    ${CYAN}onwatch status${NC}    # Status"
    Write-Host "    ${CYAN}onwatch --debug${NC}   # Run in foreground (logs to stdout)"

    # Add to PATH
    Add-ToPath

    # Start the service
    Write-Host ""
    Start-OnWatch

    Write-Host ""
    Write-Host "  ${GREEN}${BOLD}Installation complete${NC}"
    Write-Host ""

    # Keep window open if double-clicked
    if ($host.Name -eq "ConsoleHost") {
        # Check if this is likely a double-click execution
        $parentProcess = Get-Process -Id $PID | Select-Object -ExpandProperty Parent -ErrorAction SilentlyContinue
        if ($parentProcess -and $parentProcess.ProcessName -eq "explorer") {
            Write-Host "  Press any key to close..."
            $null = $host.UI.RawUI.ReadKey("NoEcho,IncludeKeyDown")
        }
    }
}

# Run main function
Main
