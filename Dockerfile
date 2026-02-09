# Build stage: Use Alpine Linux for building
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /build

# Copy go mod files first (for better caching)
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build arguments for version info and target platform
ARG VERSION=dev
ARG BUILD_TIME
ARG TARGETPLATFORM
ARG TARGETOS
ARG TARGETARCH

# Build the binary
# -ldflags="-s -w" strips debug info for smaller binary
# CGO_ENABLED=0 ensures static binary (required for distroless)
# TARGETOS and TARGETARCH are set by Docker buildx for multi-arch builds
RUN \
  TARGETOS=${TARGETOS:-linux} \
  TARGETARCH=${TARGETARCH:-amd64} \
  CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.buildTime=${BUILD_TIME}" \
    -trimpath \
    -o onwatch .

# Verify the binary works (only if native build)
RUN ./onwatch --version || echo "Cross-compiled binary, skipping version check"

# Runtime stage: Use distroless for minimal, secure image
FROM gcr.io/distroless/static-debian12:nonroot

# Build arguments (need to be redeclared for this stage)
ARG VERSION=dev

# Metadata labels
LABEL maintainer="onllm-dev"
LABEL description="onWatch - Lightweight API quota tracker for Anthropic, Synthetic, and Z.ai"
LABEL version="${VERSION:-dev}"

# Set working directory
WORKDIR /app

# Copy the binary from builder
COPY --from=builder /build/onwatch /app/onwatch

# Expose the web UI port
EXPOSE 9211

# Set default environment variables for Docker
ENV ONWATCH_DB_PATH=/data/onwatch.db \
    ONWATCH_PORT=9211 \
    ONWATCH_LOG_LEVEL=info

# Run the binary (distroless has no shell, use exec form)
ENTRYPOINT ["/app/onwatch"]
