#!/usr/bin/env bash
# Run RepeaterTastic (no radio, UDP multicast link) on the harness's docker network so it meshes
# with the meshtasticd containers started by run_meshtasticd.sh.
#
#   ./run_repeatertastic.sh up      build a static binary and start container repeatertastic-rt
#   ./run_repeatertastic.sh down    remove it (state kept in ./state/rt)
#   ./run_repeatertastic.sh logs    follow logs
#
# Ports on 127.0.0.1: 8080 web GUI/API, 4403 "Base Camp", 4404 "Ops Desk".
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
NET_NAME="${NET_NAME:-repeatertastic-mesh}"
NAME=repeatertastic-rt
STATE="$HERE/state/rt"

case "${1:-}" in
up)
  mkdir -p "$STATE/data"
  (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=interop" -o "$STATE/repeatertastic" ./cmd/repeatertastic)
  if [[ ! -f "$STATE/repeatertastic.yaml" ]]; then
    cat >"$STATE/repeatertastic.yaml" <<'EOF'
radio:
  driver: none
mesh:
  region: EU_868
  preset: LONG_FAST
relay:
  role: client
  long_name: RT Interop Relay
  short_name: RTIR
airtime:
  nodeinfo_interval: 15m
links:
  udp_multicast:
    enabled: true
web:
  enabled: true
  bind: 0.0.0.0
  port: 8080
state_dir: /rt/data
log_level: debug
identities:
  - long_name: Base Camp
    short_name: BASE
    api_port: 4403
  - long_name: Ops Desk
    short_name: OPS
    api_port: 4404
EOF
  fi
  docker network inspect "$NET_NAME" >/dev/null 2>&1 || docker network create "$NET_NAME" >/dev/null
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker run -d --name "$NAME" --network "$NET_NAME" --user "$(id -u):$(id -g)" \
    -p 127.0.0.1:8080:8080 -p 127.0.0.1:4403:4403 -p 127.0.0.1:4404:4404 \
    -v "$STATE:/rt" alpine:latest /rt/repeatertastic -config /rt/repeatertastic.yaml >/dev/null
  echo "started $NAME (web http://127.0.0.1:8080, API 127.0.0.1:4403 / :4404)"
  ;;
down)
  docker rm -f "$NAME" >/dev/null 2>&1 && echo "removed $NAME" || true
  ;;
logs)
  shift
  docker logs "$@" "$NAME"
  ;;
*)
  sed -n '2,9p' "$0"
  exit 1
  ;;
esac
