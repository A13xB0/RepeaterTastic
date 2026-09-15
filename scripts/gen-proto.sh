#!/usr/bin/env bash
# Regenerate Go code for the vendored Meshtastic protobufs (proto/, upstream commit in proto/UPSTREAM_COMMIT).
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:$(go env GOPATH)/bin"
OUT=pb
rm -rf "$OUT" && mkdir -p "$OUT"
args=()
for f in proto/nanopb.proto proto/meshtastic/*.proto; do
  args+=("--go_opt=M${f#proto/}=github.com/A13xB0/RepeaterTastic/pb;pb")
done
protoc -Iproto --go_out="$OUT" --go_opt=paths=import --go_opt=module=github.com/A13xB0/RepeaterTastic/pb "${args[@]}" \
  proto/nanopb.proto proto/meshtastic/*.proto
