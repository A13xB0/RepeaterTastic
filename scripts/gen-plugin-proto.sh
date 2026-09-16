#!/usr/bin/env bash
# Regenerate the Plugin API v1 Go code next to its definition, api/plugin/v1/plugin.proto.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:$(go env GOPATH)/bin"
protoc -Iapi/plugin/v1 \
  --go_out=api/plugin/v1 --go_opt=paths=source_relative \
  --go-grpc_out=api/plugin/v1 --go-grpc_opt=paths=source_relative \
  api/plugin/v1/plugin.proto
