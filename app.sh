#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION=$(cat "$SCRIPT_DIR/VERSION")
BINARY="onwatch"
LDFLAGS="-ldflags=-s -w -X main.version=$VERSION"

# --- Colors ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info()    { echo -e "${CYAN}${BOLD}==> $1${NC}"; }
success() { echo -e "${GREEN}${BOLD}==> $1${NC}"; }
error()   { echo -e "${RED}${BOLD}==> ERROR: $1${NC}" >&2; }
warn()    { echo -e "${YELLOW}${BOLD}==> $1${NC}"; }

# --- Usage ---
usage() {
    cat <<EOF
${BOLD}onWatch v${VERSION} -- Project Management Script${NC}

${CYAN}USAGE:${NC}
    ./app.sh [FLAGS...]

${CYAN}FLAGS:${NC}
    --build,   -b                  Build production binary with version ldflags
    --test,    -t                  Run all tests with race detection and coverage
    --smoke,   -s                  Quick validation: vet + build check + short tests
    --run,     -r                  Build and run in debug mode (foreground)
    --release                      Run tests, then cross-compile for 5 platforms
    --clean,   -c                  Remove binary, coverage files, dist/, test cache
    --stop                         Stop a running instance (native or Docker)
    --docker                       Docker mode: --build/--run/--clean/--stop use Docker
    --deps,    -d, --install,
               --dependencies,
               --requirements      Install dependencies (Go, git) for your OS
    --help,    -h                  Show this help message

${CYAN}EXAMPLES:${NC}
    ./app.sh --build               # Build production binary
    ./app.sh --test                 # Run full test suite
    ./app.sh --smoke               # Quick pre-commit check
    ./app.sh --clean --build --run  # Clean, rebuild, and run
    ./app.sh --deps --build --test  # Install deps, build, test
    ./app.sh --release              # Full release build (5 platforms)
    ./app.sh --docker --build      # Build Docker image
    ./app.sh --docker --run        # Build image and start container
    ./app.sh --docker --stop       # Stop running container
    ./app.sh --docker --clean      # Remove container and image

${CYAN}NOTES:${NC}
    Flags can be combined. Execution order is always:
    deps -> clean -> build -> test -> smoke -> release -> run
    With --docker, build/run/clean/stop operate on Docker instead of native Go.
EOF
}

# --- Flag parsing ---
DO_DEPS=false
DO_CLEAN=false
DO_BUILD=false
DO_TEST=false
DO_SMOKE=false
DO_RELEASE=false
DO_RUN=false
DO_STOP=false
DO_DOCKER=false

if [[ $# -eq 0 ]]; then
    usage
    exit 0
fi

for arg in "$@"; do
    case "$arg" in
        --build|-b)
            DO_BUILD=true ;;
        --test|-t)
            DO_TEST=true ;;
        --smoke|-s)
            DO_SMOKE=true ;;
        --run|-r)
            DO_RUN=true ;;
        --release)
            DO_RELEASE=true ;;
        --clean|-c)
            DO_CLEAN=true ;;
        --stop)
            DO_STOP=true ;;
        --docker)
            DO_DOCKER=true ;;
        --deps|-d|--install|--dependencies|--requirements)
            DO_DEPS=true ;;
        --help|-h)
            usage
            exit 0 ;;
        *)
            error "Unknown flag: $arg"
            echo ""
            usage
            exit 1 ;;
    esac
done

# --- Step functions ---

do_deps() {
    info "Installing dependencies..."
    if [[ "$(uname)" == "Darwin" ]]; then
        info "Detected macOS -- using Homebrew"
        if ! command -v brew &>/dev/null; then
            error "Homebrew not found. Install from https://brew.sh"
            exit 1
        fi
        if ! command -v go &>/dev/null; then
            info "Installing Go..."
            brew install go
        else
            success "Go already installed: $(go version)"
        fi
        if ! command -v git &>/dev/null; then
            info "Installing git..."
            brew install git
        else
            success "git already installed: $(git --version)"
        fi
    elif [[ -f /etc/debian_version ]]; then
        info "Detected Debian/Ubuntu -- using apt"
        sudo apt-get update -qq
        if ! command -v go &>/dev/null; then
            info "Installing Go..."
            sudo apt-get install -y golang
        else
            success "Go already installed: $(go version)"
        fi
        if ! command -v git &>/dev/null; then
            info "Installing git..."
            sudo apt-get install -y git
        else
            success "git already installed: $(git --version)"
        fi
    elif [[ -f /etc/redhat-release ]] || [[ -f /etc/fedora-release ]]; then
        info "Detected Fedora/RHEL -- using dnf"
        if ! command -v go &>/dev/null; then
            info "Installing Go..."
            sudo dnf install -y golang
        else
            success "Go already installed: $(go version)"
        fi
        if ! command -v git &>/dev/null; then
            info "Installing git..."
            sudo dnf install -y git
        else
            success "git already installed: $(git --version)"
        fi
    else
        error "Unsupported OS. Please install Go and git manually."
        exit 1
    fi
    success "Dependencies ready."
}

do_clean() {
    info "Cleaning build artifacts..."
    rm -f "$SCRIPT_DIR/$BINARY"
    rm -f "$SCRIPT_DIR/coverage.out" "$SCRIPT_DIR/coverage.html"
    rm -rf "$SCRIPT_DIR/dist/"
    go clean -testcache
    success "Clean complete."
}

do_build() {
    info "Building onWatch v${VERSION}..."
    cd "$SCRIPT_DIR"
    go build -ldflags="-s -w -X main.version=$VERSION" -o "$BINARY" .
    success "Built ./$BINARY ($(du -h "$BINARY" | cut -f1 | xargs))"
}

do_test() {
    info "Running tests with race detection and coverage..."
    cd "$SCRIPT_DIR"
    go test -race -cover -count=1 ./...
    success "All tests passed."
}

do_smoke() {
    info "Running smoke checks..."
    cd "$SCRIPT_DIR"

    info "  go vet ./..."
    go vet ./...

    info "  Build check..."
    go build -ldflags="-s -w -X main.version=$VERSION" -o /dev/null .

    info "  Short tests..."
    go test -short -count=1 ./...

    success "Smoke checks passed."
}

do_release() {
    info "Running tests before release..."
    cd "$SCRIPT_DIR"
    go test -race -cover -count=1 ./...
    success "Tests passed."

    info "Cross-compiling onWatch v${VERSION} for 5 platforms..."
    mkdir -p "$SCRIPT_DIR/dist"

    local targets=(
        "darwin:arm64:"
        "darwin:amd64:"
        "linux:amd64:"
        "linux:arm64:"
        "windows:amd64:.exe"
    )

    for target in "${targets[@]}"; do
        IFS=':' read -r os arch ext <<< "$target"
        local output="dist/onwatch-${os}-${arch}${ext}"
        info "  Building ${output}..."
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
            -ldflags="-s -w -X main.version=$VERSION" \
            -o "$SCRIPT_DIR/$output" .
    done

    success "Release build complete. Binaries in dist/:"
    ls -lh "$SCRIPT_DIR/dist/"
}

do_run() {
    info "Building and running onWatch v${VERSION} in debug mode..."
    do_build
    info "Starting ./onwatch --debug"
    exec "$SCRIPT_DIR/$BINARY" --debug
}

do_stop() {
    info "Stopping onWatch..."
    if [[ -f "$SCRIPT_DIR/$BINARY" ]]; then
        "$SCRIPT_DIR/$BINARY" stop && success "Stopped." || warn "No running instance found."
    else
        warn "Binary not found. Build first with --build."
    fi
}

# --- Docker functions ---

docker_ensure() {
    if ! command -v docker &>/dev/null; then
        error "Docker not found. Install from https://docs.docker.com/get-docker/"
        exit 1
    fi
    if ! docker info &>/dev/null 2>&1; then
        error "Docker daemon is not running."
        exit 1
    fi
}

do_docker_build() {
    docker_ensure
    info "Building Docker image onwatch:${VERSION}..."
    docker build -t onwatch:latest \
        --build-arg VERSION="$VERSION" \
        --build-arg BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        "$SCRIPT_DIR"
    local size
    size=$(docker image ls onwatch:latest --format '{{.Size}}')
    success "Docker image built: onwatch:latest ($size)"
}

do_docker_run() {
    docker_ensure
    # Build if image doesn't exist
    if ! docker image inspect onwatch:latest &>/dev/null; then
        do_docker_build
    fi

    # Stop existing container if running
    docker rm -f onwatch 2>/dev/null || true

    # Create data dir
    mkdir -p "$SCRIPT_DIR/onwatch-data"

    # Build env-file arg
    local env_args=()
    if [[ -f "$SCRIPT_DIR/.env" ]]; then
        env_args=(--env-file "$SCRIPT_DIR/.env")
    else
        warn "No .env file found. Create one from .env.docker.example"
    fi

    info "Starting onWatch container..."
    docker run -d --name onwatch \
        -p "${ONWATCH_PORT:-9211}:9211" \
        -v "$SCRIPT_DIR/onwatch-data:/data" \
        "${env_args[@]}" \
        --restart unless-stopped \
        --memory 64m \
        onwatch:latest

    success "Container started. Dashboard: http://localhost:${ONWATCH_PORT:-9211}"
    info "  Logs:  docker logs -f onwatch"
    info "  Stop:  ./app.sh --docker --stop"
}

do_docker_stop() {
    docker_ensure
    info "Stopping onWatch container..."
    if docker stop onwatch 2>/dev/null; then
        docker rm onwatch 2>/dev/null || true
        success "Container stopped and removed."
    else
        warn "No running container named 'onwatch' found."
    fi
}

do_docker_clean() {
    docker_ensure
    info "Cleaning Docker resources..."
    docker rm -f onwatch 2>/dev/null && info "  Removed container: onwatch" || true
    docker rmi onwatch:latest 2>/dev/null && info "  Removed image: onwatch:latest" || true
    success "Docker clean complete."
}

# --- Execute in order: deps -> clean -> build -> test -> smoke -> release -> run/stop ---

if $DO_DOCKER; then
    $DO_DEPS    && do_deps
    $DO_CLEAN   && do_docker_clean
    $DO_BUILD   && do_docker_build
    $DO_TEST    && do_test
    $DO_SMOKE   && do_docker_build
    $DO_RELEASE && { warn "Release builds native binaries, not Docker images. Skipping."; }
    $DO_STOP    && do_docker_stop
    $DO_RUN     && do_docker_run
else
    $DO_DEPS    && do_deps
    $DO_CLEAN   && do_clean
    $DO_BUILD   && do_build
    $DO_TEST    && do_test
    $DO_SMOKE   && do_smoke
    $DO_RELEASE && do_release
    $DO_STOP    && do_stop
    $DO_RUN     && do_run
fi

exit 0
