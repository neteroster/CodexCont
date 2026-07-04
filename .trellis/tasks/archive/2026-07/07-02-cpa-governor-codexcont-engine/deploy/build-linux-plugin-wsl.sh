#!/usr/bin/env bash
set -euo pipefail
WORK="/tmp/codex-go-build-$(date +%s)"
mkdir -p "$WORK"
cd "$WORK"
curl -fsSL https://go.dev/dl/go1.22.6.linux-amd64.tar.gz -o go.tar.gz
tar -xzf go.tar.gz
export PATH="$WORK/go/bin:$PATH"
go version
cd /mnt/d/Dev/20_Software/23_Reference/llm-gateway/CodexCont/cpa_governor_plugin/go
mkdir -p ../dist/linux/amd64
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags cliproxy_plugin -buildmode=c-shared -o ../dist/linux/amd64/cpa-governor.so .
sha256sum ../dist/linux/amd64/cpa-governor.so
file ../dist/linux/amd64/cpa-governor.so
