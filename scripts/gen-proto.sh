#!/usr/bin/env bash
# Regenerate the Go code for the vendored Meshtastic protobufs in api/meshtastic, next to their
# sources (upstream commit in api/meshtastic/UPSTREAM_COMMIT).
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:$(go env GOPATH)/bin"
OUT=api/meshtastic
PKG=github.com/ScotMesh/RepeaterTastic/$OUT
rm -f "$OUT"/*.pb.go
args=()
for f in api/nanopb.proto api/meshtastic/*.proto; do
  args+=("--go_opt=M${f#api/}=$PKG;pb")
done
protoc -Iapi --go_out="$OUT" --go_opt=paths=import --go_opt=module="$PKG" "${args[@]}" \
  api/nanopb.proto api/meshtastic/*.proto
