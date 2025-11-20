#!/bin/bash
set -e

# Build script for Tenatrix Agent

VERSION=${1:-"dev"}
OUTPUT=${2:-"tenatrix-agent"}

echo "🔨 Building Tenatrix Agent v${VERSION}..."

# Build static binary
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.version=${VERSION} -s -w" \
  -o "${OUTPUT}" \
  cmd/agent/main.go

echo "✅ Build complete: ${OUTPUT}"
echo "📦 Binary size: $(du -h ${OUTPUT} | cut -f1)"

# Make executable
chmod +x "${OUTPUT}"

echo ""
echo "To test locally:"
echo "  ./${OUTPUT} --config config.example.json run"
