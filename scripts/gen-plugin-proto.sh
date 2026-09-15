#!/usr/bin/env bash
# Regenerate the Plugin API v1 Go code (pluginapi/v1) from proto/plugin/v1/plugin.proto.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:$(go env GOPATH)/bin"
mkdir -p pluginapi/v1
protoc -Iproto/plugin/v1 \
  --go_out=pluginapi/v1 --go_opt=paths=source_relative \
  --go-grpc_out=pluginapi/v1 --go-grpc_opt=paths=source_relative \
  proto/plugin/v1/plugin.proto
